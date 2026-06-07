package main

import (
	"io"
	"net/http"
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
