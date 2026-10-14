package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cwfCliStub serves a fixed status/body for every request.
func cwfCliStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// FIND-0073: a 403 from the Host/Origin guards is a misconfigured URL (exit 1),
// not an out-of-scope task (exit 8) — also in the bulk loop, which must not
// treat it as a per-id skip.
func TestCwfCliGuard403IsNotOutOfScope(t *testing.T) {
	for _, code := range []string{"forbidden_host", "cross_origin"} {
		ts := cwfCliStub(t, http.StatusForbidden, `{"error":"`+code+`"}`)
		c := &Client{base: ts.URL, http: ts.Client()}
		var exit int
		errOut := captureStderr(t, func() {
			exit = captureExit(t, func() { c.doOrFail("GET", "/api/tasks/1", nil) })
		})
		if exit != 1 || !strings.Contains(errOut, "AGENTMAN_URL") {
			t.Fatalf("%s: exit %d, stderr %q; want exit 1 naming AGENTMAN_URL", code, exit, errOut)
		}
		exit = captureExit(t, func() {
			captureStderr(t, func() { bulkPatch(c, "status", []string{"1", "2"}, map[string]any{"status": "done"}) })
		})
		if exit != 1 {
			t.Fatalf("%s: bulkPatch exit %d, want 1", code, exit)
		}
	}
	// The real scope denial keeps exit 8.
	ts := cwfCliStub(t, http.StatusForbidden, `{"error":"out_of_scope"}`)
	c := &Client{base: ts.URL, http: ts.Client()}
	if exit := captureExit(t, func() {
		captureStderr(t, func() { c.doOrFail("GET", "/api/tasks/1", nil) })
	}); exit != 8 {
		t.Fatalf("out_of_scope exit %d, want 8", exit)
	}
}

// FIND-0076: a transport error keeps its text, and a mutation that failed
// after it was sent says the outcome is unknown instead of "is am serve running".
func TestCwfCliTransportErrorMessages(t *testing.T) {
	// Connection dropped after the request reached the server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer ts.Close()
	c := &Client{base: ts.URL, http: ts.Client()}
	var exit int
	errOut := captureStderr(t, func() {
		exit = captureExit(t, func() { c.doOrFail("POST", "/api/tasks", map[string]any{"title": "x"}) })
	})
	if exit != 6 || !strings.Contains(errOut, "outcome is unknown") || !strings.Contains(errOut, "EOF") {
		t.Fatalf("dropped POST: exit %d, stderr %q; want exit 6, outcome unknown, underlying error", exit, errOut)
	}

	// Nothing listening: a dial failure.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c = &Client{base: dead.URL, http: &http.Client{Timeout: time.Second}}
	errOut = captureStderr(t, func() {
		exit = captureExit(t, func() { c.doOrFail("POST", "/api/tasks", nil) })
	})
	if exit != 6 || !strings.Contains(errOut, "cannot reach server") || !strings.Contains(errOut, "refused") {
		t.Fatalf("dial failure: exit %d, stderr %q; want exit 6, cannot reach, underlying error", exit, errOut)
	}
}

// FIND-0130: am init replaces the identity file via temp file + rename (0600,
// no leftover temp), not an in-place truncate-and-write.
func TestCwfCliInitWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "id")
	t.Setenv("AGENTMAN_AGENT_FILE", f)
	captureStdout(t, func() { cmdInit(parse([]string{"bugfix"})) })
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file perm = %o, want 600 (written via CreateTemp + rename)", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files after init: %v", entries)
	}
}

// FIND-0132: with no home directory, init fails instead of writing the
// identity under ./.agentman in the current (project) directory.
func TestCwfCliInitWithoutHomeFails(t *testing.T) {
	t.Setenv("AGENTMAN_AGENT_FILE", "")
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	t.Chdir(cwd)
	var exit int
	errOut := captureStderr(t, func() {
		exit = captureExit(t, func() { captureStdout(t, func() { cmdInit(parse([]string{"bugfix"})) }) })
	})
	if exit != 1 || !strings.Contains(errOut, "AGENTMAN_AGENT_FILE") {
		t.Fatalf("init without HOME: exit %d, stderr %q; want exit 1 naming AGENTMAN_AGENT_FILE", exit, errOut)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".agentman")); !os.IsNotExist(err) {
		t.Fatalf("init wrote ./.agentman into the working directory (stat err %v)", err)
	}
	if rec := readIdentityFile(); rec != (identityRecord{}) {
		t.Fatalf("readIdentityFile without HOME = %+v, want empty", rec)
	}
}

// FIND-0143: a busy port fails before the DB is opened (no schema/migrations
// run against a file another server may be using).
func TestCwfCliServeBusyPortFailsBeforeOpeningDB(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	db := filepath.Join(t.TempDir(), "sub", "am.db")
	var exit int
	captureStderr(t, func() {
		exit = captureExit(t, func() { runServe([]string{"--port", port, "--db", db}) })
	})
	if exit != 1 {
		t.Fatalf("runServe on busy port: exit %d, want 1", exit)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("DB was created before the bind failed (stat err %v)", err)
	}
}

// FIND-0320: sameFile drives the "this am was NOT updated" warning.
func TestCwfCliSameFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("x"), 0o755)
	os.WriteFile(b, []byte("x"), 0o755)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	if !sameFile(a, link) {
		t.Fatal("sameFile(a, symlink to a) = false")
	}
	if sameFile(a, b) || sameFile(a, filepath.Join(dir, "missing")) {
		t.Fatal("sameFile reported distinct or missing files as the same")
	}
}

// FIND-0327: a checkout build (vcs settings stamped) counts as local, so the
// update check is skipped; a proxy install (no vcs settings) does not.
func TestCwfCliHasVCSInfo(t *testing.T) {
	checkout := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.modified", Value: "true"}}}
	proxy := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "GOOS", Value: "darwin"}}}
	if !hasVCSInfo(checkout) || hasVCSInfo(proxy) {
		t.Fatal("hasVCSInfo misclassified checkout/proxy build info")
	}
}

// cwfCliWaitStub fakes the endpoints cmdWait uses. task returns the body for
// GET /api/tasks/1 given the 1-based call count; stream handles /api/stream.
func cwfCliWaitStub(t *testing.T, task func(n int64) string, stream http.HandlerFunc) *httptest.Server {
	t.Helper()
	var taskCalls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/events":
			w.Write([]byte(`{"last_id":0}`))
		case "/api/tasks/1":
			w.Write([]byte(task(atomic.AddInt64(&taskCalls, 1))))
		case "/api/stream":
			stream(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// cwfCliSilentStream opens an SSE stream that never sends an event.
func cwfCliSilentStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

// FIND-0337: a non-2xx stream response fails once with the server's message
// instead of reconnecting in a tight loop until the timeout.
func TestCwfCliWaitStreamErrorFailsFast(t *testing.T) {
	var streams int64
	ts := cwfCliWaitStub(t, func(int64) string { return `{"id":1,"status":"todo"}` },
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&streams, 1)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"streaming_unsupported"}`))
		})
	var exit int
	errOut := captureStderr(t, func() {
		exit = captureExit(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "3s"})) })
	})
	if exit != 1 || !strings.Contains(errOut, "streaming_unsupported") {
		t.Fatalf("exit %d, stderr %q; want exit 1 with the server error", exit, errOut)
	}
	if n := atomic.LoadInt64(&streams); n != 1 {
		t.Fatalf("stream requested %d times, want 1", n)
	}
}

// FIND-0337: a stream the server closes right away is retried after a pause,
// not back-to-back.
func TestCwfCliWaitReconnectBacksOff(t *testing.T) {
	old := waitReconnectDelay
	waitReconnectDelay = 300 * time.Millisecond
	defer func() { waitReconnectDelay = old }()
	var streams int64
	ts := cwfCliWaitStub(t, func(int64) string { return `{"id":1,"status":"todo"}` },
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&streams, 1)
			w.Header().Set("Content-Type", "text/event-stream")
		})
	exit := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "1s"})) })
	})
	if exit != 7 {
		t.Fatalf("exit %d, want 7", exit)
	}
	if n := atomic.LoadInt64(&streams); n > 5 {
		t.Fatalf("stream requested %d times in 1s with a 300ms backoff — reconnect is spinning", n)
	}
}

// FIND-0343: a 2xx body that does not decode fails with exit 1 instead of
// waiting on zero values until the timeout.
func TestCwfCliWaitBadBodyFails(t *testing.T) {
	ts := cwfCliWaitStub(t, func(int64) string { return `<html>proxy</html>` }, cwfCliSilentStream)
	var exit int
	errOut := captureStderr(t, func() {
		exit = captureExit(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "3s"})) })
	})
	if exit != 1 || !strings.Contains(errOut, "bad response") {
		t.Fatalf("exit %d, stderr %q; want exit 1 bad response", exit, errOut)
	}
}

// FIND-0469: the wait re-checks via REST on its own schedule, so it returns
// even when the event that made the condition true never reaches the stream.
// The task reads done only from the third check on: pre-loop (1), after the
// first stream open (2), after the first periodic reconnect (3).
func TestCwfCliWaitRechecksWithoutEvents(t *testing.T) {
	old := waitRecheckEvery
	waitRecheckEvery = 200 * time.Millisecond
	defer func() { waitRecheckEvery = old }()
	ts := cwfCliWaitStub(t, func(n int64) string {
		if n >= 3 {
			return `{"id":1,"status":"done"}`
		}
		return `{"id":1,"status":"todo"}`
	}, cwfCliSilentStream)
	start := time.Now()
	exit := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"1", "--done", "--timeout", "5s"})) })
	})
	if exit != -1 {
		t.Fatalf("exit %d, want normal return: the dropped event left the wait to time out", exit)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("wait took %v; re-check should fire within ~200ms", d)
	}
}
