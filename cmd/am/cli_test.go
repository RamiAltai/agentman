package main

import (
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
