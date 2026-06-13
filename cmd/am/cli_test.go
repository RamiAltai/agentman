package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exitCode is a sentinel panic value used by captureExit.
type exitCode int

// captureStdout redirects os.Stdout to a pipe, calls fn, restores Stdout, and
// returns everything fn wrote to stdout as a string. Safe to use in tests that
// also call t.Setenv, because each call captures its own pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStdout: Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	// Drain the pipe concurrently so large output can't deadlock on the buffer.
	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		outCh <- string(b)
	}()

	// Run fn in an inner scope so os.Stdout is restored and the writer is closed
	// even if fn panics — e.g. a fail()->osExit panic when nested under
	// captureExit. The reader goroutine then unblocks (EOF) and the panic
	// propagates past this helper without leaking the redirect.
	func() {
		defer func() {
			os.Stdout = orig
			w.Close()
		}()
		fn()
	}()

	return <-outCh
}

// captureStderr mirrors captureStdout for os.Stderr (fail() writes there).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStderr: Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w

	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		outCh <- string(b)
	}()

	func() {
		defer func() {
			os.Stderr = orig
			w.Close()
		}()
		fn()
	}()

	return <-outCh
}

// captureExit stubs osExit so that fail() panics instead of killing the
// process. It returns the exit code passed to osExit, or -1 if fn returned
// normally without calling osExit. Uses a named return so the deferred
// recover() can update the value that gets returned to the caller.
func captureExit(t *testing.T, fn func()) (code int) {
	t.Helper()
	origExit := osExit
	code = -1
	osExit = func(c int) { panic(exitCode(c)) }
	defer func() {
		osExit = origExit
		if v := recover(); v != nil {
			if c, ok := v.(exitCode); ok {
				code = int(c)
				return
			}
			// Re-panic for unexpected panics.
			panic(v)
		}
	}()
	fn()
	return code
}

// ---------- E1 verb output tests ----------

func TestCmdNewPrintsOnlyID(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "myproj")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	out := captureStdout(t, func() {
		cmdNew(c, parse([]string{"a title", "-p", "myproj"}))
	})
	out = strings.TrimSpace(out)
	if _, err := strconv.Atoi(out); err != nil {
		t.Fatalf("cmdNew output %q is not a plain numeric id: %v", out, err)
	}
}

func TestCmdLsTerse(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "lsproj")
	id1 := mustCreateTask(t, ts, "lsproj", "First Task")
	id2 := mustCreateTask(t, ts, "lsproj", "Second Task")

	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
	out := captureStdout(t, func() {
		cmdLs(c, parse([]string{"-p", "lsproj"}))
	})

	if !strings.Contains(out, id1) {
		t.Errorf("ls output missing task id %s\n%s", id1, out)
	}
	if !strings.Contains(out, id2) {
		t.Errorf("ls output missing task id %s\n%s", id2, out)
	}
	if !strings.Contains(out, "First Task") {
		t.Errorf("ls output missing title 'First Task'\n%s", out)
	}

	// Terse: each line should be one task — count non-empty lines.
	lines := 0
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 terse task lines, got %d\n%s", lines, out)
	}
}

func TestCmdMutationsSilent(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "mutproj")
	id := mustCreateTask(t, ts, "mutproj", "Mutation Task")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	// cmdStatus
	statusOut := captureStdout(t, func() {
		cmdStatus(c, parse([]string{id, "done"}))
	})
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("cmdStatus stdout = %q, want empty", statusOut)
	}

	// cmdNote
	noteOut := captureStdout(t, func() {
		cmdNote(c, parse([]string{id, "a comment"}))
	})
	if strings.TrimSpace(noteOut) != "" {
		t.Errorf("cmdNote stdout = %q, want empty", noteOut)
	}

	// cmdDrop (resets to todo)
	dropOut := captureStdout(t, func() {
		cmdDrop(c, parse([]string{id}))
	})
	if strings.TrimSpace(dropOut) != "" {
		t.Errorf("cmdDrop stdout = %q, want empty", dropOut)
	}
}

// ---------- E1 exit-code tests ----------

func TestExitNotFound(t *testing.T) {
	ts := newTestServer(t)
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	code := captureExit(t, func() {
		cmdShow(c, parse([]string{"99999"}))
	})
	if code != 3 {
		t.Fatalf("expected exit 3 (not found), got %d", code)
	}
}

func TestExitConflict(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "conflproj")
	id := mustCreateTask(t, ts, "conflproj", "Conflict Task")

	// First agent claims successfully via API.
	do(t, ts, http.MethodPost, "/api/tasks/"+id+"/claim", "", map[string]string{"X-Agent": "agent-a"})

	// Second agent tries to claim via cmdClaim — should exit 4.
	t.Setenv("AGENTMAN_AGENT", "agent-b")
	c := &Client{base: ts.URL, agent: "agent-b", http: ts.Client()}

	code := captureExit(t, func() {
		cmdClaim(c, parse([]string{id}))
	})
	if code != 4 {
		t.Fatalf("expected exit 4 (conflict/already claimed), got %d", code)
	}
}

func TestExitNotStale(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "staleproj")
	id := mustCreateTask(t, ts, "staleproj", "Held Task")

	// agent-a holds a fresh claim.
	do(t, ts, http.MethodPost, "/api/tasks/"+id+"/claim", "", map[string]string{"X-Agent": "agent-a"})

	// agent-b tries to steal a fresh claim → 409 not_stale → exit 4, naming the holder.
	t.Setenv("AGENTMAN_AGENT", "agent-b")
	c := &Client{base: ts.URL, agent: "agent-b", http: ts.Client()}

	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdClaim(c, parse([]string{id, "--steal-stale", "1h"}))
		})
	})
	if code != 4 {
		t.Fatalf("expected exit 4 (not stale), got %d", code)
	}
	if !strings.Contains(msg, "agent-a") || !strings.Contains(msg, "not stale") {
		t.Fatalf("stderr = %q, want holder agent-a and 'not stale'", msg)
	}
}

// TestStaleFlagsWireFormat asserts the exact wire encoding: `am ls --stale 2h`
// sends ?stale=2h and `am claim <id> --steal-stale 2h` posts {"steal_stale":"2h"}.
func TestStaleFlagsWireFormat(t *testing.T) {
	var lsQuery, claimBody string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			lsQuery = r.URL.RawQuery
			w.Write([]byte("[]"))
			return
		}
		b, _ := io.ReadAll(r.Body)
		claimBody = string(b)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	captureStdout(t, func() { cmdLs(c, parse([]string{"--stale", "2h"})) })
	if !strings.Contains(lsQuery, "stale=2h") {
		t.Fatalf("ls query = %q, want stale=2h", lsQuery)
	}

	captureStdout(t, func() { cmdClaim(c, parse([]string{"1", "--steal-stale", "2h"})) })
	if !strings.Contains(claimBody, `"steal_stale":"2h"`) {
		t.Fatalf("claim body = %q, want steal_stale 2h", claimBody)
	}
}

func TestExitValidation(t *testing.T) {
	ts := newTestServer(t)
	// Archive a project then try to create a task into it → 400 project_archived → exit 5.
	mustCreateProject(t, ts, "archvalproj")
	do(t, ts, http.MethodPost, "/api/projects/archvalproj/archive", "", nil)

	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
	code := captureExit(t, func() {
		cmdNew(c, parse([]string{"should fail", "-p", "archvalproj"}))
	})
	if code != 5 {
		t.Fatalf("expected exit 5 (validation/400), got %d", code)
	}
}

func TestExitServerDown(t *testing.T) {
	// Point at a port that has nothing listening.
	c := &Client{base: "http://127.0.0.1:1", agent: "tester", http: &http.Client{Timeout: time.Second}}

	code := captureExit(t, func() {
		cmdLs(c, parse([]string{}))
	})
	if code != 6 {
		t.Fatalf("expected exit 6 (server down), got %d", code)
	}
}

// ---------- E1 pure formatter / parse tests ----------

func TestParsePositionals(t *testing.T) {
	a := parse([]string{"hello", "world"})
	if a.at(0) != "hello" || a.at(1) != "world" {
		t.Fatalf("positionals: got %q %q", a.at(0), a.at(1))
	}
	if a.at(2) != "" {
		t.Fatalf("out-of-range at() = %q, want empty", a.at(2))
	}
}

func TestParseValueFlags(t *testing.T) {
	a := parse([]string{"-p", "myproj", "--body", "some body"})
	if a.flag("project") != "myproj" {
		t.Fatalf("project flag = %q", a.flag("project"))
	}
	if a.flag("body") != "some body" {
		t.Fatalf("body flag = %q", a.flag("body"))
	}
}

func TestParseBoolFlags(t *testing.T) {
	a := parse([]string{"--mine", "--all"})
	if !a.has("mine") {
		t.Fatal("mine flag not set")
	}
	if !a.has("all") {
		t.Fatal("all flag not set")
	}
	if a.has("json") {
		t.Fatal("json flag should not be set")
	}
}

func TestParseEqualsForm(t *testing.T) {
	a := parse([]string{"--status=done"})
	if a.flag("status") != "done" {
		t.Fatalf("--status=done flag = %q", a.flag("status"))
	}
}

func TestParseMixedOrder(t *testing.T) {
	// Positionals and flags can interleave.
	a := parse([]string{"--body", "b", "title", "-p", "proj"})
	if a.at(0) != "title" {
		t.Fatalf("positional = %q, want title", a.at(0))
	}
	if a.flag("project") != "proj" {
		t.Fatalf("project = %q", a.flag("project"))
	}
	if a.flag("body") != "b" {
		t.Fatalf("body = %q", a.flag("body"))
	}
}

func TestParseShortAlias(t *testing.T) {
	a := parse([]string{"-s", "todo", "-a", "bob"})
	if a.flag("status") != "todo" {
		t.Fatalf("short -s = %q, want todo", a.flag("status"))
	}
	if a.flag("assign") != "bob" {
		t.Fatalf("short -a = %q, want bob", a.flag("assign"))
	}
}

func TestTaskLine(t *testing.T) {
	tk := Task{ID: 7, Status: "doing", Title: "Fix the thing", Assignee: "alice", NComments: 2, Project: "web"}
	line := taskLine(tk, true)
	if !strings.Contains(line, "7") {
		t.Errorf("taskLine missing id: %q", line)
	}
	if !strings.Contains(line, "doing") {
		t.Errorf("taskLine missing status: %q", line)
	}
	if !strings.Contains(line, "Fix the thing") {
		t.Errorf("taskLine missing title: %q", line)
	}
	if !strings.Contains(line, "web") {
		t.Errorf("taskLine missing project: %q", line)
	}
	if !strings.Contains(line, "2c") {
		t.Errorf("taskLine missing comment count: %q", line)
	}
}

func TestStatusShort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"blocked", "block"},
		{"todo", "todo"},
		{"doing", "doing"},
		{"done", "done"},
	}
	for _, c := range cases {
		if got := statusShort(c.in); got != c.want {
			t.Errorf("statusShort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAssignee(t *testing.T) {
	// Unassigned.
	if got := assignee(""); got != "-" {
		t.Errorf("assignee(\"\") = %q, want -", got)
	}
	// Other agent.
	if got := assignee("bob"); got != "@bob" {
		t.Errorf("assignee(bob) = %q, want @bob", got)
	}
}

func TestTrunc(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello w…"},
		{"ab", 2, "ab"},
		{"abc", 2, "a…"},
	}
	for _, c := range cases {
		if got := trunc(c.s, c.n); got != c.want {
			t.Errorf("trunc(%q,%d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestApiErr(t *testing.T) {
	cases := []struct {
		data []byte
		def  string
		want string
	}{
		{[]byte(`{"error":"x","assignee":"a"}`), "def", "x by a"},
		{[]byte(`{"error":"oops"}`), "def", "oops"},
		{[]byte(`{}`), "def", "def"},
		{[]byte(`garbage`), "fallback", "fallback"},
	}
	for _, c := range cases {
		if got := apiErr(c.data, c.def); got != c.want {
			t.Errorf("apiErr(%s,%q) = %q, want %q", c.data, c.def, got, c.want)
		}
	}
}

// ---------- Phase L: am next + bulk status/assign ----------

func TestCmdNextPrintsOnlyID(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "nextproj")
	want := mustCreateTask(t, ts, "nextproj", "Pick Me")

	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
	out := captureStdout(t, func() {
		cmdNext(c, parse([]string{"-p", "nextproj"}))
	})
	if strings.TrimSpace(out) != want {
		t.Fatalf("cmdNext stdout = %q, want only id %q", out, want)
	}
}

func TestExitNextNoneReady(t *testing.T) {
	ts := newTestServer(t)
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	code := captureExit(t, func() {
		cmdNext(c, parse([]string{}))
	})
	if code != 3 {
		t.Fatalf("expected exit 3 (no ready task), got %d", code)
	}
}

func TestCmdStatusBulk(t *testing.T) {
	var patched []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = append(patched, strings.TrimPrefix(r.URL.Path, "/api/tasks/"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	out := captureStdout(t, func() {
		cmdStatus(c, parse([]string{"1", "2", "3", "done"}))
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("bulk cmdStatus stdout = %q, want empty", out)
	}
	if len(patched) != 3 || patched[0] != "1" || patched[1] != "2" || patched[2] != "3" {
		t.Fatalf("patched ids = %v, want [1 2 3]", patched)
	}
}

func TestCmdStatusBulkPartialFailure(t *testing.T) {
	var patched []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		w.Header().Set("Content-Type", "application/json")
		if id == "2" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not_found"}`))
			return
		}
		patched = append(patched, id)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdStatus(c, parse([]string{"1", "2", "3", "done"}))
		})
	})
	if code != 3 {
		t.Fatalf("expected exit 3 (first failure was 404), got %d", code)
	}
	if len(patched) != 2 || patched[0] != "1" || patched[1] != "3" {
		t.Fatalf("patched ids = %v, want [1 3] (loop continues past the failure)", patched)
	}
	if !strings.Contains(msg, "#2") || !strings.Contains(msg, "not_found") {
		t.Fatalf("stderr = %q, want a line naming #2 not_found", msg)
	}
}

func TestCmdAssignBulk(t *testing.T) {
	var bodies []string
	var ids []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		ids = append(ids, strings.TrimPrefix(r.URL.Path, "/api/tasks/"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	// Last positional is the assignee; the rest are ids.
	cmdAssign(c, parse([]string{"1", "2", "bob"}))
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Fatalf("assigned ids = %v, want [1 2]", ids)
	}
	for _, b := range bodies {
		if !strings.Contains(b, `"assignee":"bob"`) {
			t.Fatalf("assign body = %q, want assignee bob", b)
		}
	}

	// "me" resolves to the current agent.
	bodies, ids = nil, nil
	cmdAssign(c, parse([]string{"3", "me"}))
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"assignee":"tester"`) {
		t.Fatalf("assign me bodies = %v, want assignee tester", bodies)
	}

	// "-" unassigns.
	bodies, ids = nil, nil
	cmdAssign(c, parse([]string{"4", "-"}))
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"assignee":""`) {
		t.Fatalf("assign - bodies = %v, want empty assignee", bodies)
	}

	// Single-id regression: `am assign <id> <agent>` still works.
	bodies, ids = nil, nil
	cmdAssign(c, parse([]string{"5", "alice"}))
	if len(ids) != 1 || ids[0] != "5" || !strings.Contains(bodies[0], `"assignee":"alice"`) {
		t.Fatalf("single assign = ids %v bodies %v", ids, bodies)
	}
}

// ---------- Phase M: search + label CLI ----------

// TestCmdLsGrepWireFormat asserts the exact wire encoding: `am ls --grep "x y"`
// sends ?q=x+y and `-l bug` / `--label bug` send ?label=bug.
func TestCmdLsGrepWireFormat(t *testing.T) {
	var lsQuery string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lsQuery = r.URL.RawQuery
		w.Write([]byte("[]"))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	captureStdout(t, func() { cmdLs(c, parse([]string{"--grep", "x y"})) })
	if !strings.Contains(lsQuery, "q=x+y") {
		t.Fatalf("ls query = %q, want q=x+y", lsQuery)
	}

	captureStdout(t, func() { cmdLs(c, parse([]string{"-l", "bug"})) })
	if !strings.Contains(lsQuery, "label=bug") {
		t.Fatalf("ls query = %q, want label=bug (short -l)", lsQuery)
	}

	captureStdout(t, func() { cmdLs(c, parse([]string{"--label", "bug"})) })
	if !strings.Contains(lsQuery, "label=bug") {
		t.Fatalf("ls query = %q, want label=bug (long --label)", lsQuery)
	}
}

func TestCmdLabelAddRemove(t *testing.T) {
	type call struct{ method, path, body string }
	var calls []call
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, string(b)})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	var code int
	out := captureStdout(t, func() {
		code = captureExit(t, func() { cmdLabel(c, []string{"12", "+a", "-bug", "c"}) })
	})
	if code != -1 {
		t.Fatalf("cmdLabel exited %d, want normal return", code)
	}
	if out != "" {
		t.Fatalf("cmdLabel stdout = %q, want silent", out)
	}
	want := []call{
		{http.MethodPost, "/api/tasks/12/labels", `{"label":"a"}`},
		{http.MethodDelete, "/api/tasks/12/labels/bug", ""},
		{http.MethodPost, "/api/tasks/12/labels", `{"label":"c"}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("call %d = %+v, want %+v", i, calls[i], w)
		}
	}
}

func TestCmdLabelPrintsLabels(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":12,"labels":["a","b"]}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	out := captureStdout(t, func() { cmdLabel(c, []string{"12"}) })
	if out != "a b\n" {
		t.Fatalf("cmdLabel output = %q, want \"a b\\n\"", out)
	}
}

func TestCmdLabelUsage(t *testing.T) {
	c := &Client{base: "http://127.0.0.1:1", agent: "tester", http: &http.Client{Timeout: time.Second}}

	// Missing id → exit 1.
	code := captureExit(t, func() { cmdLabel(c, nil) })
	if code != 1 {
		t.Fatalf("missing id exit = %d, want 1", code)
	}

	// Empty token after prefix strip → exit 5 (before any request is made).
	code = captureExit(t, func() { cmdLabel(c, []string{"12", "+"}) })
	if code != 5 {
		t.Fatalf("empty +token exit = %d, want 5", code)
	}
	code = captureExit(t, func() { cmdLabel(c, []string{"12", "-"}) })
	if code != 5 {
		t.Fatalf("empty -token exit = %d, want 5", code)
	}

	// Double-dash tokens are flags, never labels → exit 5.
	code = captureExit(t, func() { cmdLabel(c, []string{"12", "--json"}) })
	if code != 5 {
		t.Fatalf("--json exit = %d, want 5", code)
	}

	// Known global value flags are rejected by name with a message.
	msg := captureStderr(t, func() {
		code = captureExit(t, func() { cmdLabel(c, []string{"12", "-p", "web"}) })
	})
	if code != 5 || !strings.Contains(msg, "-p is a global flag") {
		t.Fatalf("-p exit = %d, stderr = %q, want 5 with global flag message", code, msg)
	}
}

// ---------- Phase O: category CLI ----------

func TestCmdCategoryVerbs(t *testing.T) {
	ts := newTestServer(t)
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	// am category new prints only the slug.
	out := captureStdout(t, func() {
		cmdCategory(c, parse([]string{"new", "work", "Work"}))
	})
	if strings.TrimSpace(out) != "work" {
		t.Fatalf("category new stdout = %q, want work", out)
	}

	// am categories --json includes the uid.
	out = captureStdout(t, func() {
		cmdCategories(c, parse([]string{"--json"}))
	})
	if !strings.Contains(out, `"uid":"amc_`) {
		t.Fatalf("categories --json = %q, want amc_ uid", out)
	}

	// archive/unarchive are silent successes.
	out = captureStdout(t, func() {
		cmdCategory(c, parse([]string{"archive", "work"}))
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("category archive stdout = %q, want empty", out)
	}
	out = captureStdout(t, func() {
		cmdCategory(c, parse([]string{"unarchive", "work"}))
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("category unarchive stdout = %q, want empty", out)
	}

	// Usage errors exit 1.
	code := captureExit(t, func() { cmdCategory(c, parse([]string{"new"})) })
	if code != 1 {
		t.Fatalf("category new (no slug) exit = %d, want 1", code)
	}
	code = captureExit(t, func() { cmdCategory(c, parse([]string{"bogus"})) })
	if code != 1 {
		t.Fatalf("category bogus exit = %d, want 1", code)
	}
}

func TestCmdProjectNewRequiresCategory(t *testing.T) {
	ts := newTestServer(t)
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	// Without -c and without AGENTMAN_CATEGORY → exit 5.
	t.Setenv("AGENTMAN_CATEGORY", "")
	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdProject(c, parse([]string{"new", "pslug"}))
		})
	})
	if code != 5 {
		t.Fatalf("project new without category exit = %d, want 5", code)
	}
	if !strings.Contains(msg, "AGENTMAN_CATEGORY") {
		t.Fatalf("stderr = %q, want hint about AGENTMAN_CATEGORY", msg)
	}

	// With AGENTMAN_CATEGORY set it succeeds and prints the slug.
	t.Setenv("AGENTMAN_CATEGORY", "general")
	out := captureStdout(t, func() {
		cmdProject(c, parse([]string{"new", "pslug"}))
	})
	if strings.TrimSpace(out) != "pslug" {
		t.Fatalf("project new stdout = %q, want pslug", out)
	}

	// With an explicit -c flag.
	mustCreateCategory(t, ts, "work")
	out = captureStdout(t, func() {
		cmdProject(c, parse([]string{"new", "wproj", "-c", "work"}))
	})
	if strings.TrimSpace(out) != "wproj" {
		t.Fatalf("project new -c stdout = %q, want wproj", out)
	}
}

func TestCmdProjectEdit(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	// Silent success for rename + vault binding.
	out := captureStdout(t, func() {
		cmdProject(c, parse([]string{"edit", "web", "--slug", "frontend", "--vault-id", "p_7", "--vault-path", "/v/x"}))
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("project edit stdout = %q, want empty", out)
	}
	resp := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer resp.Body.Close()
	var ps []Project
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(ps) != 1 || ps[0].Slug != "frontend" || ps[0].VaultProjectID != "p_7" || ps[0].VaultPath != "/v/x" {
		t.Fatalf("project after edit = %+v", ps)
	}

	// Clearing a vault field with an explicit empty value works (ok-form flags).
	captureStdout(t, func() {
		cmdProject(c, parse([]string{"edit", "frontend", "--vault-path="}))
	})
	resp2 := do(t, ts, http.MethodGet, "/api/projects", "", nil)
	defer resp2.Body.Close()
	ps = nil
	json.NewDecoder(resp2.Body).Decode(&ps)
	if ps[0].VaultPath != "" {
		t.Fatalf("vault_path after clear = %q, want empty", ps[0].VaultPath)
	}

	// Nothing to change → exit 1.
	code := captureExit(t, func() { cmdProject(c, parse([]string{"edit", "frontend"})) })
	if code != 1 {
		t.Fatalf("project edit (no flags) exit = %d, want 1", code)
	}
}

func TestCmdLsCategoryWireFormat(t *testing.T) {
	var lsQuery string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lsQuery = r.URL.RawQuery
		w.Write([]byte("[]"))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	captureStdout(t, func() { cmdLs(c, parse([]string{"-c", "work"})) })
	if !strings.Contains(lsQuery, "category=work") {
		t.Fatalf("ls query = %q, want category=work (short -c)", lsQuery)
	}

	// AGENTMAN_CATEGORY is the fallback; --all suppresses the scope.
	t.Setenv("AGENTMAN_CATEGORY", "personal")
	captureStdout(t, func() { cmdLs(c, parse([]string{})) })
	if !strings.Contains(lsQuery, "category=personal") {
		t.Fatalf("ls query = %q, want category=personal from env", lsQuery)
	}
	captureStdout(t, func() { cmdLs(c, parse([]string{"--all"})) })
	if strings.Contains(lsQuery, "category=") {
		t.Fatalf("ls --all query = %q, want no category scope", lsQuery)
	}
}

func TestCmdNextCategory(t *testing.T) {
	ts := newTestServer(t)
	mustCreateCategory(t, ts, "work")
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}

	// No ready task in the category → exit 3.
	code := captureExit(t, func() {
		cmdNext(c, parse([]string{"-c", "work"}))
	})
	if code != 3 {
		t.Fatalf("next -c work (none ready) exit = %d, want 3", code)
	}

	// Bogus category slug → also exit 3 (the server can't tell them apart).
	code = captureExit(t, func() {
		cmdNext(c, parse([]string{"-c", "bogus"}))
	})
	if code != 3 {
		t.Fatalf("next -c bogus exit = %d, want 3", code)
	}

	// A ready task in scope is picked and its id printed.
	r := do(t, ts, http.MethodPost, "/api/projects", `{"slug":"wproj","category":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	want := mustCreateTask(t, ts, "wproj", "Pick Me")
	out := captureStdout(t, func() {
		cmdNext(c, parse([]string{"-c", "work"}))
	})
	if strings.TrimSpace(out) != want {
		t.Fatalf("next -c work stdout = %q, want %q", out, want)
	}
}

// Regression: `am show <id> -c` still means --comments after -c became the
// global category flag (main.go rewrites the token for show only).
func TestCmdShowDashCStillPrintsComments(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "showproj")
	id := mustCreateTask(t, ts, "showproj", "Has Comment")
	r := do(t, ts, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"the comment body"}`,
		map[string]string{"Content-Type": "application/json", "X-Agent": "alice"})
	r.Body.Close()

	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
	out := captureStdout(t, func() {
		cmdShow(c, parse(rewriteShowComments([]string{id, "-c"})))
	})
	if !strings.Contains(out, "the comment body") {
		t.Fatalf("show -c output missing comment:\n%s", out)
	}
}

func TestRewriteShowComments(t *testing.T) {
	got := rewriteShowComments([]string{"12", "-c", "--json"})
	if got[0] != "12" || got[1] != "--comments" || got[2] != "--json" {
		t.Fatalf("rewriteShowComments = %v", got)
	}
}

// ---------- Phase P: --meta flag tests ----------

func TestParseMultiFlag(t *testing.T) {
	// Repeated --meta collects every occurrence, in order, including the = form.
	a := parse([]string{"--meta", "a=1", "--meta=b=2", "--meta", "c=3"})
	got := a.all("meta")
	if len(got) != 3 || got[0] != "a=1" || got[1] != "b=2" || got[2] != "c=3" {
		t.Fatalf("all(meta) = %v, want [a=1 b=2 c=3]", got)
	}
	if a.flag("meta") != "" {
		t.Fatalf("flag(meta) = %q, want empty (meta is multi, not single)", a.flag("meta"))
	}
	// Single-value flags are still last-wins.
	a = parse([]string{"--body", "first", "--body", "second"})
	if a.flag("body") != "second" {
		t.Fatalf("repeated --body = %q, want last-wins second", a.flag("body"))
	}
	// No --meta → empty slice.
	if got := parse([]string{"x"}).all("meta"); len(got) != 0 {
		t.Fatalf("all(meta) with no flags = %v, want empty", got)
	}
}

func TestCmdNewMetaWireFormat(t *testing.T) {
	var posts int
	var body string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		posts++
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	captureStdout(t, func() {
		cmdNew(c, parse([]string{"title", "-p", "web", "--meta", "a=1", "--meta", "b=2"}))
	})
	if posts != 1 {
		t.Fatalf("POST count = %d, want 1 (all meta in one request)", posts)
	}
	var sent struct {
		Meta map[string]string `json:"meta"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("unmarshal body %q: %v", body, err)
	}
	if len(sent.Meta) != 2 || sent.Meta["a"] != "1" || sent.Meta["b"] != "2" {
		t.Fatalf("body meta = %v, want {a:1 b:2}", sent.Meta)
	}

	// Missing '=' and empty value are usage errors (exit 5) — before any request.
	posts = 0
	code := captureExit(t, func() {
		captureStderr(t, func() { cmdNew(c, parse([]string{"t", "-p", "web", "--meta", "bare"})) })
	})
	if code != 5 || posts != 0 {
		t.Fatalf("--meta bare exit = %d posts = %d, want 5 and 0", code, posts)
	}
	code = captureExit(t, func() {
		captureStderr(t, func() { cmdNew(c, parse([]string{"t", "-p", "web", "--meta", "k="})) })
	})
	if code != 5 || posts != 0 {
		t.Fatalf("--meta k= on new exit = %d posts = %d, want 5 and 0", code, posts)
	}
}

func TestCmdEditMetaSinglePatch(t *testing.T) {
	var patches int
	var body string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			patches++
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	// Two sets and one removal — the dispatcher's atomic auto+packet flip —
	// must ride in exactly ONE PATCH.
	cmdEdit(c, parse([]string{"1", "--meta", "auto=packet-8", "--meta", "stage=review", "--meta", "old="}))
	if patches != 1 {
		t.Fatalf("PATCH count = %d, want 1", patches)
	}
	var sent struct {
		Meta map[string]string `json:"meta"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("unmarshal body %q: %v", body, err)
	}
	if len(sent.Meta) != 3 || sent.Meta["auto"] != "packet-8" ||
		sent.Meta["stage"] != "review" || sent.Meta["old"] != "" {
		t.Fatalf("patch meta = %v, want auto/stage set and old:\"\" removal", sent.Meta)
	}

	// Values may contain '=' — split happens at the FIRST one.
	cmdEdit(c, parse([]string{"1", "--meta", "expr=a=b"}))
	if !strings.Contains(body, `"expr":"a=b"`) {
		t.Fatalf("body = %q, want expr=a=b (split at first =)", body)
	}

	// --meta alone is a change; no flags at all still errors.
	code := captureExit(t, func() {
		captureStderr(t, func() { cmdEdit(c, parse([]string{"1"})) })
	})
	if code != 1 {
		t.Fatalf("edit with nothing exit = %d, want 1", code)
	}
}

func TestCmdNextMetaWireFormat(t *testing.T) {
	var body string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"id":7}`))
	}))
	t.Cleanup(stub.Close)
	t.Setenv("AGENTMAN_AGENT", "tester")
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	out := captureStdout(t, func() { cmdNext(c, parse([]string{"--meta", "auto"})) })
	if !strings.Contains(body, `"meta_key":"auto"`) {
		t.Fatalf("next body = %q, want meta_key auto", body)
	}
	if strings.TrimSpace(out) != "7" {
		t.Fatalf("next stdout = %q, want 7", out)
	}

	// key=value and repeated keys are usage errors for the filter form.
	code := captureExit(t, func() {
		captureStderr(t, func() { cmdNext(c, parse([]string{"--meta", "auto=1"})) })
	})
	if code != 5 {
		t.Fatalf("next --meta k=v exit = %d, want 5", code)
	}
	code = captureExit(t, func() {
		captureStderr(t, func() { cmdNext(c, parse([]string{"--meta", "a", "--meta", "b"})) })
	})
	if code != 5 {
		t.Fatalf("next with two --meta exit = %d, want 5", code)
	}
}

func TestCmdLsMetaWireFormat(t *testing.T) {
	var lsQuery string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lsQuery = r.URL.RawQuery
		w.Write([]byte("[]"))
	}))
	t.Cleanup(stub.Close)
	c := &Client{base: stub.URL, agent: "tester", http: stub.Client()}

	captureStdout(t, func() { cmdLs(c, parse([]string{"--meta", "auto"})) })
	if !strings.Contains(lsQuery, "meta_key=auto") {
		t.Fatalf("ls query = %q, want meta_key=auto", lsQuery)
	}

	code := captureExit(t, func() {
		captureStderr(t, func() { cmdLs(c, parse([]string{"--meta", "auto=1"})) })
	})
	if code != 5 {
		t.Fatalf("ls --meta k=v exit = %d, want 5", code)
	}
	code = captureExit(t, func() {
		captureStderr(t, func() { cmdLs(c, parse([]string{"--meta", "a", "--meta", "b"})) })
	})
	if code != 5 {
		t.Fatalf("ls with two --meta exit = %d, want 5", code)
	}
}

func TestCmdShowPrintsMeta(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "web")
	r := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"web","title":"carrier","meta":{"owner":"alice","auto":"packet-7"}}`,
		map[string]string{"Content-Type": "application/json"})
	var tk Task
	if err := json.NewDecoder(r.Body).Decode(&tk); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	r.Body.Close()

	c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
	out := captureStdout(t, func() {
		cmdShow(c, parse([]string{strconv.FormatInt(tk.ID, 10)}))
	})
	// One line, keys sorted.
	if !strings.Contains(out, "meta: auto=packet-7 owner=alice") {
		t.Fatalf("show output missing sorted meta line:\n%s", out)
	}

	// No meta → no meta line.
	plain := mustCreateTask(t, ts, "web", "plain")
	out = captureStdout(t, func() { cmdShow(c, parse([]string{plain})) })
	if strings.Contains(out, "meta:") {
		t.Fatalf("show printed a meta line for a task without meta:\n%s", out)
	}
}
