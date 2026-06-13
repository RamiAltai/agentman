package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newWaitClient builds the Client cmdWait needs (it only reads base + agent;
// the SSE connection uses its own un-timed http.Client).
func newWaitClient(base string) *Client {
	return &Client{base: base, agent: "tester", http: &http.Client{Timeout: time.Second}}
}

// patchTask issues a raw PATCH without test helpers, safe from goroutines
// (no t.Fatalf off the test goroutine).
func patchTask(base, id, body string) {
	req, err := http.NewRequest(http.MethodPatch, base+"/api/tasks/"+id, strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent", "patcher")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func TestWaitDoneAlreadySatisfied(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	id := mustCreateTask(t, ts, "waitproj", "Done Already")
	patchTask(ts.URL, id, `{"status":"done"}`)

	c := newWaitClient(ts.URL)
	var code int
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{id, "--done", "--timeout", "5s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("wait --done stdout = %q, want empty", out)
	}
}

func TestWaitDoneEventArrives(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	id := mustCreateTask(t, ts, "waitproj", "Done Soon")

	go func() {
		time.Sleep(200 * time.Millisecond)
		patchTask(ts.URL, id, `{"status":"done"}`)
	}()

	c := newWaitClient(ts.URL)
	var code int
	start := time.Now()
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{id, "--done", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("wait took %v, should have returned on the event", elapsed)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("wait --done stdout = %q, want empty", out)
	}
}

// Regression: AGENTMAN_PROJECT naming a different project than the watched
// task's must not scope the SSE stream under --done — a scoped stream drops
// the task's events and the wait runs to the full timeout.
func TestWaitDoneCrossProject(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "alpha")
	mustCreateProject(t, ts, "beta")
	id := mustCreateTask(t, ts, "beta", "Other Project")
	t.Setenv("AGENTMAN_PROJECT", "alpha")

	go func() {
		time.Sleep(200 * time.Millisecond)
		patchTask(ts.URL, id, `{"status":"done"}`)
	}()

	c := newWaitClient(ts.URL)
	start := time.Now()
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{id, "--done", "--timeout", "10s"}))
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("wait took %v, should have returned on the event, not the timeout", elapsed)
	}
}

func TestWaitReadyOnPrereqDone(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	prereq := mustCreateTask(t, ts, "waitproj", "prereq")
	dep := mustCreateTask(t, ts, "waitproj", "dependent")
	r := do(t, ts, http.MethodPost, "/api/tasks/"+dep+"/deps",
		`{"depends_on":`+prereq+`}`, map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	// Claim the prereq so it is doing (not ready); the dependent is blocked.
	r = do(t, ts, http.MethodPost, "/api/tasks/"+prereq+"/claim", "",
		map[string]string{"X-Agent": "agent-a"})
	r.Body.Close()

	go func() {
		time.Sleep(200 * time.Millisecond)
		patchTask(ts.URL, prereq, `{"status":"done"}`)
	}()

	c := newWaitClient(ts.URL)
	var code int
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "-p", "waitproj", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if strings.TrimSpace(out) != dep {
		t.Fatalf("wait --ready stdout = %q, want ready task id %q", out, dep)
	}
}

func TestWaitTimeout(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	id := mustCreateTask(t, ts, "waitproj", "Never Done")

	c := newWaitClient(ts.URL)
	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{id, "--done", "--timeout", "1s"}))
		})
	})
	if code != 7 {
		t.Fatalf("expected exit 7 (timeout), got %d", code)
	}
	if !strings.Contains(msg, "wait: timeout") {
		t.Fatalf("stderr = %q, want terse 'wait: timeout'", msg)
	}
}

func TestWaitTaskNotFound(t *testing.T) {
	ts := newTestServer(t)
	c := newWaitClient(ts.URL)
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"99999", "--done", "--timeout", "5s"}))
	})
	if code != 3 {
		t.Fatalf("expected exit 3 (not found), got %d", code)
	}
}

func TestWaitServerDown(t *testing.T) {
	c := newWaitClient("http://127.0.0.1:1")
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"1", "--done", "--timeout", "5s"}))
	})
	if code != 6 {
		t.Fatalf("expected exit 6 (server down), got %d", code)
	}
}

func TestWaitUsageErrors(t *testing.T) {
	c := newWaitClient("http://127.0.0.1:1") // never reached
	cases := [][]string{
		{},                         // no condition
		{"5"},                      // id but no condition
		{"--done"},                 // --done without id
		{"5", "--ready"},           // --ready with id
		{"5", "--done", "--ready"}, // both conditions
	}
	for _, argv := range cases {
		code := captureExit(t, func() { cmdWait(c, parse(argv)) })
		if code != 1 {
			t.Errorf("cmdWait(%v) exit = %d, want 1 (usage)", argv, code)
		}
	}
}

func TestParseWaitTimeout(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"300", 300 * time.Second, false}, // bare integer = seconds
		{"5m", 5 * time.Minute, false},    // Go duration
		{"1h30m", 90 * time.Minute, false},
		{"junk", 0, true},
		{"-5m", 0, true},
		{"0", 0, true},
		{"-3", 0, true},
		{"9223372036854775807", 0, true}, // bare-integer seconds that overflow a Duration
	}
	for _, c := range cases {
		got, err := parseWaitTimeout(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseWaitTimeout(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseWaitTimeout(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestWaitBadTimeoutExit5(t *testing.T) {
	c := newWaitClient("http://127.0.0.1:1") // validated before any request
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"1", "--done", "--timeout", "junk"}))
	})
	if code != 5 {
		t.Fatalf("expected exit 5 (bad --timeout), got %d", code)
	}
}

// ---------- Phase O: wait --ready -c ----------

// createTaskRaw creates a task without test helpers, safe from goroutines
// (no t.Fatalf off the test goroutine).
func createTaskRaw(base, project, title string) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/tasks",
		strings.NewReader(`{"project":"`+project+`","title":"`+title+`"}`))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// TestWaitReadyCategoryScoped: a ready task in another category must NOT
// release a category-scoped wait; a ready task inside the category must.
// The stream stays unscoped (no ?category= on /api/stream in Phase O) — the
// REST re-check carries the scope.
func TestWaitReadyCategoryScoped(t *testing.T) {
	ts := newTestServer(t)
	r := do(t, ts, http.MethodPost, "/api/categories", `{"slug":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	r = do(t, ts, http.MethodPost, "/api/projects", `{"slug":"wproj","category":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	mustCreateProject(t, ts, "gproj") // lands in general

	// Out-of-category ready task exists BEFORE the wait starts: must not satisfy it.
	mustCreateTask(t, ts, "gproj", "general ready")

	go func() {
		time.Sleep(300 * time.Millisecond)
		createTaskRaw(ts.URL, "wproj", "work ready")
	}()

	c := newWaitClient(ts.URL)
	var code int
	start := time.Now()
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "-c", "work", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	// Must have blocked until the in-category task appeared, not returned on the
	// pre-existing general one (which would be ~instant).
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("wait returned after %v — released by the out-of-category task?", elapsed)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("wait took %v, should have released on the in-category task", elapsed)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("wait --ready printed nothing, want the ready task id")
	}
}

func TestWaitReadyCategoryEnv(t *testing.T) {
	ts := newTestServer(t)
	r := do(t, ts, http.MethodPost, "/api/categories", `{"slug":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	r = do(t, ts, http.MethodPost, "/api/projects", `{"slug":"wproj","category":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	id := mustCreateTask(t, ts, "wproj", "already ready")

	t.Setenv("AGENTMAN_CATEGORY", "work")
	c := newWaitClient(ts.URL)
	var code int
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "--timeout", "5s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if strings.TrimSpace(out) != id {
		t.Fatalf("wait --ready stdout = %q, want %q", out, id)
	}
}

func TestWaitReadyCategoryTimeout(t *testing.T) {
	ts := newTestServer(t)
	r := do(t, ts, http.MethodPost, "/api/categories", `{"slug":"work"}`,
		map[string]string{"Content-Type": "application/json"})
	r.Body.Close()
	mustCreateProject(t, ts, "gproj")
	mustCreateTask(t, ts, "gproj", "general ready") // out of scope forever

	c := newWaitClient(ts.URL)
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"--ready", "-c", "work", "--timeout", "1s"}))
	})
	if code != 7 {
		t.Fatalf("expected exit 7 (timeout, scope never satisfied), got %d", code)
	}
}

// ---------- Phase P: wait --ready --meta ----------

// createMetaTaskRaw mirrors createTaskRaw but attaches a meta object;
// goroutine-safe (no t.Fatalf off the test goroutine).
func createMetaTaskRaw(base, project, title, metaJSON string) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/tasks",
		strings.NewReader(`{"project":"`+project+`","title":"`+title+`","meta":`+metaJSON+`}`))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// A pre-existing ready task WITHOUT the key must not satisfy the wait, and
// events from more keyless ready tasks must only trigger re-checks (that keep
// failing), never a release — the wait runs to its timeout (exit 7).
func TestWaitReadyMetaNoHotSpin(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	mustCreateTask(t, ts, "waitproj", "plain ready")

	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep ready-task events flowing during the wait
		for {
			select {
			case <-stop:
				return
			case <-time.After(150 * time.Millisecond):
				createTaskRaw(ts.URL, "waitproj", "more plain")
			}
		}
	}()

	c := newWaitClient(ts.URL)
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"--ready", "--meta", "auto", "-p", "waitproj", "--timeout", "1s"}))
	})
	if code != 7 {
		t.Fatalf("expected exit 7 (timeout; keyless ready tasks must not release), got %d", code)
	}
}

func TestWaitReadyMetaReleasedByCreate(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	mustCreateTask(t, ts, "waitproj", "plain decoy")

	go func() {
		time.Sleep(300 * time.Millisecond)
		createMetaTaskRaw(ts.URL, "waitproj", "carrier", `{"auto":"x"}`)
	}()

	c := newWaitClient(ts.URL)
	var code int
	start := time.Now()
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "--meta", "auto", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	// Must have blocked until the carrier appeared, not released on the decoy.
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("wait returned after %v — released by the keyless decoy?", elapsed)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("wait --ready --meta printed nothing, want the carrier id")
	}
}

func TestWaitReadyMetaReleasedByPrereqDone(t *testing.T) {
	ts := newTestServer(t)
	mustCreateProject(t, ts, "waitproj")
	prereq := mustCreateTask(t, ts, "waitproj", "prereq")
	// The carrier exists but is blocked — not ready until the prereq is done.
	r := do(t, ts, http.MethodPost, "/api/tasks",
		`{"project":"waitproj","title":"blocked carrier","meta":{"auto":"x"}}`,
		map[string]string{"Content-Type": "application/json"})
	var carrier Task
	if err := json.NewDecoder(r.Body).Decode(&carrier); err != nil {
		t.Fatalf("decode carrier: %v", err)
	}
	r.Body.Close()
	carrierID := strconv.FormatInt(carrier.ID, 10)
	r = do(t, ts, http.MethodPost, "/api/tasks/"+carrierID+"/deps",
		`{"depends_on":`+prereq+`}`, map[string]string{"Content-Type": "application/json"})
	r.Body.Close()

	go func() {
		time.Sleep(300 * time.Millisecond)
		patchTask(ts.URL, prereq, `{"status":"done"}`)
	}()

	c := newWaitClient(ts.URL)
	var code int
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "--meta", "auto", "-p", "waitproj", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	if strings.TrimSpace(out) != carrierID {
		t.Fatalf("wait stdout = %q, want unblocked carrier id %q", out, carrierID)
	}
}

func TestWaitMetaUsageErrors(t *testing.T) {
	c := newWaitClient("http://127.0.0.1:1") // never reached

	// --meta only scopes --ready; with --done it is a usage error (exit 1).
	code := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(c, parse([]string{"5", "--done", "--meta", "auto"})) })
	})
	if code != 1 {
		t.Fatalf("wait --done --meta exit = %d, want 1", code)
	}
	// Two --meta keys → exit 5.
	code = captureExit(t, func() {
		captureStderr(t, func() { cmdWait(c, parse([]string{"--ready", "--meta", "a", "--meta", "b"})) })
	})
	if code != 5 {
		t.Fatalf("wait with two --meta exit = %d, want 5", code)
	}
	// key=value form → exit 5.
	code = captureExit(t, func() {
		captureStderr(t, func() { cmdWait(c, parse([]string{"--ready", "--meta", "auto=1"})) })
	})
	if code != 5 {
		t.Fatalf("wait --meta k=v exit = %d, want 5", code)
	}
}

// ---------- Phase Q: scoped wait --ready ----------

// newScopedWaitClient mirrors newWaitClient with an X-Agent-Scope.
func newScopedWaitClient(base, scope string) *Client {
	return &Client{base: base, agent: "tester", scope: scope, http: &http.Client{Timeout: time.Second}}
}

// A ready task that exists only OUTSIDE the agent's scope must never release
// a scoped wait — the unfiltered re-check is narrowed server-side.
func TestWaitReadyScopedTimeout(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts) // work task ready in wproj; pproj task ready too

	// Drain the personal board so only out-of-scope ready work remains.
	r := do(t, ts, http.MethodPost, "/api/tasks/next", "", scoped("drainer", "personal"))
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("drain next = %d, want 200", r.StatusCode)
	}

	c := newScopedWaitClient(ts.URL, "personal")
	code := captureExit(t, func() {
		cmdWait(c, parse([]string{"--ready", "--timeout", "1s"}))
	})
	if code != 7 {
		t.Fatalf("scoped wait --ready exit = %d, want 7 (out-of-scope ready must not release)", code)
	}
}

// An in-scope ready task created mid-wait releases the scoped wait with its id.
func TestWaitReadyScopedReleased(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts)
	r := do(t, ts, http.MethodPost, "/api/tasks/next", "", scoped("drainer", "personal"))
	r.Body.Close()

	go func() {
		time.Sleep(300 * time.Millisecond)
		createTaskRaw(ts.URL, "pproj", "in-scope ready")
	}()

	c := newScopedWaitClient(ts.URL, "personal")
	var code int
	start := time.Now()
	out := captureStdout(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "--timeout", "10s"}))
		})
	})
	if code != -1 {
		t.Fatalf("expected normal return (exit 0), got exit %d", code)
	}
	// Must have blocked until the in-scope task appeared, not released on the
	// pre-existing out-of-scope work task.
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("wait returned after %v — released by an out-of-scope task?", elapsed)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("scoped wait --ready printed nothing, want the ready task id")
	}
}

// An explicit -c outside the agent's scope is a loud failure: exit 8.
func TestWaitReadyExplicitOutOfScopeExit8(t *testing.T) {
	ts := newTestServer(t)
	scopedBoard(t, ts)

	c := newScopedWaitClient(ts.URL, "personal")
	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() {
			cmdWait(c, parse([]string{"--ready", "-c", "work", "--timeout", "5s"}))
		})
	})
	if code != 8 {
		t.Fatalf("wait --ready -c work scoped personal exit = %d, want 8", code)
	}
	if !strings.Contains(msg, "out_of_scope") {
		t.Fatalf("stderr = %q, want out_of_scope", msg)
	}
}
