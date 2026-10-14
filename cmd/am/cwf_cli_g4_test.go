package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// FIND-0064: the CLI reads its identity and default scope from the
// environment and from ~/.agentman. Start every test in this package from a
// clean slate so a developer's exported AGENTMAN_* or real identity file cannot
// change results; tests that need a value set it with t.Setenv. (init, not
// TestMain, so other files can never collide with it.)
func init() {
	for _, k := range []string{"AGENTMAN_AGENT", "AGENTMAN_SCOPE", "AGENTMAN_TOKEN", "AGENTMAN_PROJECT", "AGENTMAN_CATEGORY", "AGENTMAN_URL"} {
		os.Unsetenv(k)
	}
	// A path that does not exist: reads see no identity, and a test that
	// writes one without isolating lands in the temp dir, never in $HOME.
	os.Setenv("AGENTMAN_AGENT_FILE", filepath.Join(os.TempDir(), "agentman-test-no-identity", "id"))
}

// cwfCliGetTask reads a task straight from the server (no CLI involved).
func cwfCliGetTask(t *testing.T, ts *httptest.Server, id string) Task {
	t.Helper()
	r := do(t, ts, http.MethodGet, "/api/tasks/"+id, "", nil)
	defer r.Body.Close()
	var tk Task
	if err := json.NewDecoder(r.Body).Decode(&tk); err != nil {
		t.Fatalf("decode task %s: %v", id, err)
	}
	return tk
}

// cwfCliProjects lists every project, archived included.
func cwfCliProjects(t *testing.T, ts *httptest.Server) map[string]Project {
	t.Helper()
	r := do(t, ts, http.MethodGet, "/api/projects?archived=true", "", nil)
	defer r.Body.Close()
	var ps []Project
	if err := json.NewDecoder(r.Body).Decode(&ps); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	m := map[string]Project{}
	for _, p := range ps {
		m[p.Slug] = p
	}
	return m
}

// FIND-0062: --json=true sets the boolean (it used to land in flags, leaving
// has("json") false); a non-boolean value is a usage error.
func TestCwfCliParseBoolEqualsForm(t *testing.T) {
	if a := parse([]string{"--json=true", "--all=false"}); !a.has("json") || a.has("all") || a.flag("json") != "" {
		t.Fatalf("bools = %v flags = %v, want json true, all false, nothing in flags", a.bools, a.flags)
	}
	code := captureExit(t, func() { captureStderr(t, func() { parse([]string{"--json=yes please"}) }) })
	if code != 5 {
		t.Fatalf("--json=<junk> exit = %d, want 5", code)
	}
}

// FIND-0043: the ls filter flags reach the server under the right names.
func TestCwfCliLsFilterWireFormat(t *testing.T) {
	var q url.Values
	n := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		tasks := make([]Task, n)
		for i := range tasks {
			tasks[i] = Task{ID: int64(i + 1), Title: "t", Status: "todo"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tasks)
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "bob", http: stub.Client()}
	ls := func(args ...string) string {
		return captureStdout(t, func() { cmdLs(c, parse(args)) })
	}

	ls()
	if q.Get("status") != "todo,doing,blocked" || q.Get("limit") != "50" || q.Has("assignee") || q.Has("ready") || q.Has("blocked") {
		t.Fatalf("default query = %v, want status=todo,doing,blocked limit=50 and no other filters", q)
	}
	ls("--status", "done")
	if q.Get("status") != "done" {
		t.Fatalf("--status done query = %v", q)
	}
	ls("-p", "web", "--all")
	if q.Has("status") || q.Has("project") {
		t.Fatalf("--all query = %v, want no status and no project", q)
	}
	ls("--ready")
	if q.Get("ready") != "true" || q.Has("blocked") {
		t.Fatalf("--ready query = %v", q)
	}
	ls("--blocked")
	if q.Get("blocked") != "true" || q.Has("ready") {
		t.Fatalf("--blocked query = %v", q)
	}
	t.Setenv("AGENTMAN_AGENT", "bob")
	ls("--mine")
	if q.Get("assignee") != "bob" {
		t.Fatalf("--mine query = %v, want assignee=bob", q)
	}
	t.Setenv("AGENTMAN_AGENT", "")
	code := captureExit(t, func() { captureStderr(t, func() { ls("--mine") }) })
	if code != 5 {
		t.Fatalf("--mine without an identity exit = %d, want 5", code)
	}

	// A full page in text mode ends with the truncation notice on stdout.
	n = 50
	out := ls()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 51 || !strings.Contains(lines[50], "50 shown") {
		t.Fatalf("50-row ls: %d lines, last %q, want 50 rows + a '50 shown' notice", len(lines), lines[len(lines)-1])
	}
	n = 49
	if out := ls(); strings.Contains(out, "shown") {
		t.Fatalf("49-row ls printed a truncation notice:\n%s", out)
	}
}

// FIND-0043: am next --json prints the claimed task as JSON.
func TestCwfCliNextJSON(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "nextproj")
	want := mustCreateTask(t, ts, "nextproj", "Pick Me")
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := newTestClient(ts.URL, ts.Client())
	out := captureStdout(t, func() { cmdNext(c, parse([]string{"-p", "nextproj", "--json"})) })
	var tk Task
	if err := json.Unmarshal([]byte(out), &tk); err != nil {
		t.Fatalf("next --json stdout %q is not JSON: %v", out, err)
	}
	if strconv.FormatInt(tk.ID, 10) != want || tk.Assignee != "tester" || tk.Status != "doing" {
		t.Fatalf("next --json = %+v, want #%s doing by tester", tk, want)
	}
}

// FIND-0043, FIND-0044: dep add with several prereqs, show's body/labels/
// depends-on/blocks lines, a blocked claim (exit 4 naming the open prereqs),
// and dep rm.
func TestCwfCliDepShowAndBlockedClaim(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "dp")
	r := do(t, ts, http.MethodPost, "/api/tasks", `{"project":"dp","title":"main","body":"the body"}`,
		map[string]string{"Content-Type": "application/json"})
	var a Task
	json.NewDecoder(r.Body).Decode(&a)
	r.Body.Close()
	aID := strconv.FormatInt(a.ID, 10)
	bID := mustCreateTask(t, ts, "dp", "prereq b")
	cID := mustCreateTask(t, ts, "dp", "prereq c")
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := newTestClient(ts.URL, ts.Client())

	if code := captureExit(t, func() { cmdDep(c, parse([]string{"add", aID, bID, cID})) }); code != -1 {
		t.Fatalf("dep add exit = %d", code)
	}
	cmdLabel(c, []string{aID, "+bug"})
	if deps := cwfCliGetTask(t, ts, aID).DependsOn; len(deps) != 2 {
		t.Fatalf("depends_on after dep add with two prereqs = %+v, want 2", deps)
	}

	b := cwfCliGetTask(t, ts, bID)
	out := captureStdout(t, func() { cmdShow(c, parse([]string{aID})) })
	for _, want := range []string{"main\nthe body\n", "labels: bug\n", fmt.Sprintf("depends on: dp-%d todo · ", b.Ref)} {
		if !strings.Contains(out, want) {
			t.Fatalf("show #%s missing %q:\n%s", aID, want, out)
		}
	}
	out = captureStdout(t, func() { cmdShow(c, parse([]string{bID})) })
	if !strings.Contains(out, fmt.Sprintf("blocks: dp-%d\n", a.Ref)) {
		t.Fatalf("show #%s missing blocks line:\n%s", bID, out)
	}

	var code int
	msg := captureStderr(t, func() { code = captureExit(t, func() { cmdClaim(c, parse([]string{aID})) }) })
	if code != 4 || !strings.Contains(msg, "blocked") || !strings.Contains(msg, "#"+bID) || !strings.Contains(msg, "#"+cID) {
		t.Fatalf("blocked claim: exit = %d stderr = %q, want 4 naming #%s and #%s", code, msg, bID, cID)
	}
	if tk := cwfCliGetTask(t, ts, aID); tk.Assignee != "" {
		t.Fatalf("blocked claim assigned the task to %q", tk.Assignee)
	}

	cmdDep(c, parse([]string{"rm", aID, cID}))
	if deps := cwfCliGetTask(t, ts, aID).DependsOn; len(deps) != 1 || strconv.FormatInt(deps[0].ID, 10) != bID {
		t.Fatalf("depends_on after dep rm = %+v, want only #%s", deps, bID)
	}
}

// FIND-0044: claim's 400 and fallback branches.
func TestCwfCliClaimBadRequestAndOther(t *testing.T) {
	t.Setenv("AGENTMAN_AGENT", "tester")
	for _, tc := range []struct {
		status int
		body   string
		code   int
	}{
		{400, `{"error":"bad_steal_stale"}`, 5},
		{500, `{"error":"db_locked"}`, 1},
	} {
		stub := cwfCliStub(t, tc.status, tc.body)
		c := newTestClient(stub.URL, stub.Client())
		var code int
		msg := captureStderr(t, func() { code = captureExit(t, func() { cmdClaim(c, parse([]string{"7"})) }) })
		var e struct{ Error string }
		json.Unmarshal([]byte(tc.body), &e)
		if code != tc.code || !strings.Contains(msg, e.Error) {
			t.Errorf("claim on %d: exit = %d stderr = %q, want %d with %s", tc.status, code, msg, tc.code, e.Error)
		}
	}
}

// FIND-0066: project archive/unarchive, and rm refuses without --yes.
func TestCwfCliProjectArchiveAndRm(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	id := mustCreateTask(t, ts, "web", "doomed")
	c := newTestClient(ts.URL, ts.Client())

	cmdProject(c, parse([]string{"archive", "web"}))
	if p := cwfCliProjects(t, ts)["web"]; p.ArchivedAt == "" {
		t.Fatalf("project after archive = %+v, want archived_at set", p)
	}
	cmdProject(c, parse([]string{"unarchive", "web"}))
	if p := cwfCliProjects(t, ts)["web"]; p.ArchivedAt != "" {
		t.Fatalf("project after unarchive = %+v, want archived_at cleared", p)
	}

	code := captureExit(t, func() { captureStderr(t, func() { cmdProject(c, parse([]string{"rm", "web"})) }) })
	if _, ok := cwfCliProjects(t, ts)["web"]; code != 5 || !ok {
		t.Fatalf("project rm without --yes: exit = %d, still exists = %v; want 5 and kept", code, ok)
	}
	if code := captureExit(t, func() { cmdProject(c, parse([]string{"rm", "web", "--yes"})) }); code != -1 {
		t.Fatalf("project rm --yes exit = %d", code)
	}
	if _, ok := cwfCliProjects(t, ts)["web"]; ok {
		t.Fatal("project still listed after rm --yes")
	}
	if r := do(t, ts, http.MethodGet, "/api/tasks/"+id, "", nil); r.StatusCode != http.StatusNotFound {
		r.Body.Close()
		t.Fatalf("task of a removed project: GET = %d, want 404", r.StatusCode)
	}
}

// FIND-0044, FIND-0066: token ls lists a minted token, revoke marks it, and
// the revoked token stops working (exit 9).
func TestCwfCliTokenLsAndRevoke(t *testing.T) {
	ts := newTestServer(t)
	mustCreateCategory(t, ts, "work")
	isolateIdentity(t)
	human := &Client{base: ts.URL, http: ts.Client()}
	tok := strings.TrimSpace(captureStdout(t, func() {
		captureStderr(t, func() { cmdToken(human, parse([]string{"new", "--scope", "work"})) })
	}))
	var toks []Token
	json.Unmarshal([]byte(captureStdout(t, func() { cmdToken(human, parse([]string{"ls", "--json"})) })), &toks)
	if len(toks) != 1 || toks[0].Category != "work" || toks[0].RevokedAt != "" {
		t.Fatalf("token ls --json = %+v, want one live work token", toks)
	}
	agent := &Client{base: ts.URL, token: tok, http: ts.Client()}
	if code := captureExit(t, func() { captureStdout(t, func() { cmdLs(agent, parse(nil)) }) }); code != -1 {
		t.Fatalf("ls with a live token exit = %d", code)
	}

	out := captureStdout(t, func() { cmdToken(human, parse([]string{"revoke", toks[0].ID})) })
	if out != "" {
		t.Fatalf("token revoke stdout = %q, want silent", out)
	}
	out = captureStdout(t, func() { cmdToken(human, parse([]string{"ls"})) })
	if !strings.Contains(out, toks[0].ID) || !strings.Contains(out, "work") || !strings.Contains(out, "(revoked)") {
		t.Fatalf("token ls after revoke = %q, want the id, its scope and (revoked)", out)
	}
	code := captureExit(t, func() { captureStderr(t, func() { cmdLs(agent, parse(nil)) }) })
	if code != 9 {
		t.Fatalf("ls with a revoked token exit = %d, want 9", code)
	}
}

// FIND-0072: the shared status → exit-code table, and doOrFail through it.
func TestCwfCliExitCodeTableAndDoOrFail(t *testing.T) {
	for st, want := range map[int]int{0: 1, 200: 0, 204: 0, 400: 5, 401: 9, 403: 8, 404: 3, 409: 4, 500: 1} {
		if got := exitCodeFor(st); got != want {
			t.Errorf("exitCodeFor(%d) = %d, want %d", st, got, want)
		}
	}
	for _, tc := range []struct {
		status    int
		body, msg string
		code      int
	}{
		{409, `{"error":"claimed","assignee":"bob"}`, "claimed by bob", 4},
		{403, `{"error":"out_of_scope"}`, "out_of_scope", 8},
		{404, ``, "not found", 3},
		{500, ``, "error 500", 1},
	} {
		stub := cwfCliStub(t, tc.status, tc.body)
		c := newTestClient(stub.URL, stub.Client())
		var code int
		msg := captureStderr(t, func() { code = captureExit(t, func() { c.doOrFail("POST", "/api/tasks/1/claim", nil) }) })
		if code != tc.code || !strings.Contains(msg, tc.msg) {
			t.Errorf("doOrFail on %d: exit = %d stderr = %q, want %d with %q", tc.status, code, msg, tc.code, tc.msg)
		}
	}
}

// FIND-0075: NewClient wires URL, identity, and the request timeout.
func TestCwfCliNewClient(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:9999/")
	t.Setenv("AGENTMAN_AGENT", "a1")
	t.Setenv("AGENTMAN_SCOPE", "work/api")
	t.Setenv("AGENTMAN_TOKEN", "amt_x")
	c := NewClient()
	if c.base != "http://127.0.0.1:9999" || c.agent != "a1" || c.scope != "work/api" || c.token != "amt_x" {
		t.Fatalf("NewClient = base %q agent %q scope %q token %q", c.base, c.agent, c.scope, c.token)
	}
	if c.http == nil || c.http.Timeout != 10*time.Second {
		t.Fatalf("NewClient http client = %+v, want a 10s timeout", c.http)
	}
	t.Setenv("AGENTMAN_URL", "")
	if c := NewClient(); c.base != "http://127.0.0.1:8787" {
		t.Fatalf("default base = %q, want http://127.0.0.1:8787", c.base)
	}
}

// FIND-0137: without AGENTMAN_AGENT_FILE, init writes
// ~/.agentman/agents/<sha1(cwd)[:12]> (creating the directories), and each
// working directory gets its own identity.
func TestCwfCliIdentityFileDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTMAN_AGENT_FILE", "")
	agents := filepath.Join(home, ".agentman", "agents")
	initIn := func(dir string) (id, file string) {
		t.Chdir(dir)
		wd, _ := os.Getwd()
		sum := sha1.Sum([]byte(wd))
		file = filepath.Join(agents, hex.EncodeToString(sum[:])[:12])
		id = strings.TrimSpace(captureStdout(t, func() { cmdInit(parse([]string{"bugfix"})) }))
		return id, file
	}
	dir1, dir2 := t.TempDir(), t.TempDir()
	id1, f1 := initIn(dir1)
	id2, f2 := initIn(dir2)
	if f1 == f2 || id1 == id2 {
		t.Fatalf("two directories share an identity: %s %s", f1, f2)
	}
	entries, err := os.ReadDir(agents)
	if err != nil || len(entries) != 2 {
		t.Fatalf("agents dir: %v entries, err %v; want 2 files", entries, err)
	}
	for _, e := range entries {
		if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(e.Name()) {
			t.Fatalf("identity file name %q, want 12 hex chars", e.Name())
		}
	}
	for file, id := range map[string]string{f1: id1, f2: id2} {
		if b, err := os.ReadFile(file); err != nil || string(b) != `{"agent":"`+id+`"}` {
			t.Fatalf("%s = %q (err %v), want %q", file, b, err, id)
		}
	}
	if got := resolveAgent(); got != id2 { // cwd is dir2
		t.Fatalf("resolveAgent in dir2 = %q, want %q", got, id2)
	}
	t.Chdir(dir1)
	if got := resolveAgent(); got != id1 {
		t.Fatalf("resolveAgent in dir1 = %q, want %q", got, id1)
	}
}

// FIND-0347: readSSEFrame skips the retry preamble and ping comments, takes
// CRLF and extra fields, and returns the read error for a frame cut off by EOF.
func TestCwfCliReadSSEFrame(t *testing.T) {
	stream := "retry: 3000\n\n: ping\n\nid: 5\r\ndata: {\"a\":1}\r\n\r\n: ping\nid: 6\nevent: x\ndata: {\"b\":2}\n\nid: 7\ndata: {\"c\""
	br := bufio.NewReader(strings.NewReader(stream))
	for _, want := range []struct {
		id   int64
		data string
	}{{5, `{"a":1}`}, {6, `{"b":2}`}} {
		id, data, err := readSSEFrame(br)
		if err != nil || id != want.id || string(data) != want.data {
			t.Fatalf("frame = (%d, %q, %v), want (%d, %q, nil)", id, data, err, want.id, want.data)
		}
	}
	if id, data, err := readSSEFrame(br); err == nil {
		t.Fatalf("partial frame = (%d, %q, nil), want an error", id, data)
	}
}

// FIND-0347: after the server ends a stream, the reconnect resumes from the
// last event id seen (?since=7), not from the original cursor.
func TestCwfCliWaitReconnectResumesFromLastEvent(t *testing.T) {
	old := waitReconnectDelay
	waitReconnectDelay = 20 * time.Millisecond
	defer func() { waitReconnectDelay = old }()
	var mu sync.Mutex
	var since []string
	ts := cwfCliWaitStub(t, func(int64) string {
		mu.Lock()
		defer mu.Unlock()
		if len(since) >= 2 {
			return `{"id":1,"status":"done"}`
		}
		return `{"id":1,"status":"todo"}`
	}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		since = append(since, r.URL.Query().Get("since"))
		first := len(since) == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if first { // one unrelated event, then the server hangs up
			fmt.Fprint(w, "id: 7\ndata: {\"task_id\":99}\n\n")
			return
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	exit := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "3s"})) })
	})
	mu.Lock()
	defer mu.Unlock()
	if exit != -1 || len(since) != 2 || since[0] != "0" || since[1] != "7" {
		t.Fatalf("exit %d, stream since values %q; want normal return after since=[0 7]", exit, since)
	}
}

// FIND-0347: an event that lands between the cursor read and the first check
// is replayed, because the cursor is taken BEFORE the check. The stub's first
// task read "creates" event 4; only a stream opened with since=3 replays it,
// and only that replay makes the task done.
func TestCwfCliWaitCursorBeforeCheck(t *testing.T) {
	var taskReads, replayed atomic.Int64
	var gotSince atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/events":
			last := 3
			if taskReads.Load() > 0 {
				last = 4
			}
			fmt.Fprintf(w, `{"last_id":%d}`, last)
		case "/api/tasks/1":
			taskReads.Add(1)
			if replayed.Load() > 0 {
				w.Write([]byte(`{"id":1,"status":"done"}`))
				return
			}
			w.Write([]byte(`{"id":1,"status":"todo"}`))
		case "/api/stream":
			s := r.URL.Query().Get("since")
			gotSince.Store(s)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if s == "3" {
				replayed.Store(1)
				fmt.Fprint(w, "id: 4\ndata: {\"task_id\":1}\n\n")
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	exit := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "2s"})) })
	})
	if exit != -1 || gotSince.Load() != "3" {
		t.Fatalf("exit %d, stream since=%v; want normal return with since=3 (cursor before check)", exit, gotSince.Load())
	}
}
