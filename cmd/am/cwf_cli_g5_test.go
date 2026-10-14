package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// ---------- FIND-0144: verb dispatch and serve settings ----------

// Every verb usage() documents is dispatched (none falls through to "unknown
// command").
func TestCwfCliDispatchCoversUsage(t *testing.T) {
	out := captureStdout(t, usage)
	verbs := regexp.MustCompile(`(?m)^\s+am ([a-z]+)`).FindAllStringSubmatch(out, -1)
	if len(verbs) < 20 {
		t.Fatalf("parsed only %d verbs from usage()", len(verbs))
	}
	for _, m := range verbs {
		v := m[1]
		_, local := localCmds[v]
		_, client := clientCmds[v]
		if !local && !client && v != "serve" && v != "label" {
			t.Errorf("usage() documents %q but dispatch does not handle it", v)
		}
	}
}

func TestCwfCliDispatchUnknownExit5(t *testing.T) {
	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() { dispatch("bogus", nil) })
	})
	if code != 5 || !strings.Contains(msg, "unknown command: bogus") {
		t.Fatalf("exit %d, stderr %q; want 5 unknown command", code, msg)
	}
}

func TestCwfCliServeConfig(t *testing.T) {
	t.Setenv("AGENTMAN_PORT", "")
	t.Setenv("AGENTMAN_DB", "")
	t.Setenv("AGENTMAN_PROPOSALS", "")
	port, db, prop := serveConfig(parse(nil))
	if port != "8787" || db != defaultDBPath() || prop != "meta/proposals" {
		t.Fatalf("defaults = %q %q %q", port, db, prop)
	}
	t.Setenv("AGENTMAN_PORT", "9000")
	t.Setenv("AGENTMAN_DB", "/env.db")
	t.Setenv("AGENTMAN_PROPOSALS", "env/props")
	if port, db, prop = serveConfig(parse(nil)); port != "9000" || db != "/env.db" || prop != "env/props" {
		t.Fatalf("env = %q %q %q", port, db, prop)
	}
	port, db, prop = serveConfig(parse([]string{"--port", "9100", "--db", "/flag.db", "--proposals", "flag/props"}))
	if port != "9100" || db != "/flag.db" || prop != "flag/props" {
		t.Fatalf("flags over env = %q %q %q", port, db, prop)
	}
}

func TestCwfCliProposalsScope(t *testing.T) {
	for _, bad := range []string{"", "meta", "meta/", "/proposals", "a/b/c"} {
		if sc, err := proposalsScope(bad); err == nil {
			t.Errorf("proposalsScope(%q) = %+v, want error (a bare category would open the whole category)", bad, sc)
		}
	}
	if sc, err := proposalsScope("Meta/Proposals"); err != nil || sc != (Scope{Category: "meta", Project: "proposals"}) {
		t.Fatalf("proposalsScope(Meta/Proposals) = %+v, %v", sc, err)
	}
}

// ---------- FIND-0328: version precedence ----------

func cwfCliSetVersion(t *testing.T, v string) {
	old := injectedVersion
	injectedVersion = v
	t.Cleanup(func() { injectedVersion = old })
}

func TestCwfCliVersion(t *testing.T) {
	cwfCliSetVersion(t, "v9.9.9")
	if v := version(); v != "v9.9.9" {
		t.Fatalf("version() = %q, want the injected v9.9.9", v)
	}
	if localBuild() {
		t.Fatal("an injected (release) version must not count as a local build")
	}
	for _, verb := range []string{"version", "--version", "-v"} {
		if out := captureStdout(t, func() { dispatch(verb, nil) }); out != "am v9.9.9\n" {
			t.Errorf("am %s printed %q", verb, out)
		}
	}
	injectedVersion = ""
	if v := version(); v == "" || v == "(devel)" {
		t.Fatalf("version() = %q; want a module version or exactly \"devel\"", v)
	}
}

// ---------- FIND-0321: update check and self-update ----------

func TestCwfCliUpdateCheckWanted(t *testing.T) {
	t.Setenv("AGENTMAN_NO_UPDATE_CHECK", "")
	cwfCliSetVersion(t, "v1.0.0")
	if !updateCheckWanted() {
		t.Fatal("release build: update check should run")
	}
	t.Setenv("AGENTMAN_NO_UPDATE_CHECK", "1")
	if updateCheckWanted() {
		t.Fatal("AGENTMAN_NO_UPDATE_CHECK=1 must skip the check")
	}
	t.Setenv("AGENTMAN_NO_UPDATE_CHECK", "")
	injectedVersion = "devel"
	if updateCheckWanted() {
		t.Fatal("a devel build must skip the check")
	}
}

func TestCwfCliReportUpdate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantLog string // "" = nothing logged
	}{
		{"newer", 200, `{"Version":"v2.0.0"}`, "update available — v2.0.0 (you have v1.0.0)"},
		{"same", 200, `{"Version":"v1.0.0"}`, ""},
		{"not found", 404, `{"Version":"v2.0.0"}`, ""},
		{"bad json", 200, `<html>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var path string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.EscapedPath()
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer ts.Close()
			var buf bytes.Buffer
			old := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(old)

			reportUpdate(ts.URL, "v1.0.0")
			if path != "/github.com/%21rami%21altai/agentman/@latest" && path != "/github.com/!rami!altai/agentman/@latest" {
				t.Fatalf("proxy path = %q", path)
			}
			got := buf.String()
			if c.wantLog == "" && got != "" || c.wantLog != "" && !strings.Contains(got, c.wantLog) {
				t.Fatalf("log = %q, want %q", got, c.wantLog)
			}
		})
	}
}

// cwfCliFakeGo puts a stub `go` first (and only) on PATH. It appends its argv
// to dir/argv, answers `go env GOBIN|GOPATH` with gobin/gopath, and exits
// installExit for `go install`. It returns dir.
func cwfCliFakeGo(t *testing.T, gobin, gopath string, installExit int) string {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub")
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1 $2" in
"env GOBIN") echo %q ;;
"env GOPATH") echo %q ;;
install*) exit %d ;;
esac
`, filepath.Join(dir, "argv"), gobin, gopath, installExit)
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestCwfCliCmdUpdate(t *testing.T) {
	gobin := t.TempDir()
	dir := cwfCliFakeGo(t, gobin, "", 0)
	if err := os.WriteFile(filepath.Join(gobin, "am"), []byte("#!/bin/sh\necho 'am v9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { cmdUpdate(parse(nil)) })
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if !strings.Contains(string(argv), "install github.com/RamiAltai/agentman/cmd/am@latest\n") {
		t.Fatalf("go argv = %q, want install …@latest (the default ref)", argv)
	}
	bin := filepath.Join(gobin, "am")
	for _, want := range []string{
		"installing github.com/RamiAltai/agentman/cmd/am@latest",
		"updated " + bin + " — now v9.9.9.",
		"was NOT updated", // the test binary is not the installed am
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}

	// An explicit ref is passed through.
	captureStdout(t, func() { cmdUpdate(parse([]string{"v1.2.3"})) })
	if argv, _ = os.ReadFile(filepath.Join(dir, "argv")); !strings.Contains(string(argv), "install github.com/RamiAltai/agentman/cmd/am@v1.2.3\n") {
		t.Fatalf("go argv = %q, want install …@v1.2.3", argv)
	}
}

func TestCwfCliCmdUpdateNoVersionReport(t *testing.T) {
	cwfCliFakeGo(t, filepath.Join(t.TempDir(), "missing"), "", 0) // no am there to ask
	out := captureStdout(t, func() { cmdUpdate(parse(nil)) })
	if !strings.Contains(out, "agentman: updated.\n") {
		t.Fatalf("output %q, want the plain 'updated.' line", out)
	}
}

func TestCwfCliCmdUpdateFailures(t *testing.T) {
	cwfCliFakeGo(t, "", "", 1)
	var code int
	msg := captureStderr(t, func() {
		code = captureExit(t, func() { captureStdout(t, func() { cmdUpdate(parse(nil)) }) })
	})
	if code != 1 || !strings.Contains(msg, "update failed") {
		t.Fatalf("install failure: exit %d, stderr %q", code, msg)
	}

	t.Setenv("PATH", t.TempDir()) // no go at all
	msg = captureStderr(t, func() {
		code = captureExit(t, func() { cmdUpdate(parse(nil)) })
	})
	if code != 1 || !strings.Contains(msg, "Go toolchain not found") {
		t.Fatalf("missing toolchain: exit %d, stderr %q", code, msg)
	}
}

func TestCwfCliInstalledBinary(t *testing.T) {
	goBin := filepath.Join(cwfCliFakeGo(t, "", "/gp", 0), "go")
	if got := installedBinary(goBin); got != filepath.Join("/gp", "bin", "am") {
		t.Fatalf("installedBinary with GOBIN unset = %q, want GOPATH/bin/am", got)
	}
	goBin = filepath.Join(cwfCliFakeGo(t, "/gb", "/gp", 0), "go")
	if got := installedBinary(goBin); got != filepath.Join("/gb", "am") {
		t.Fatalf("installedBinary = %q, want GOBIN/am", got)
	}
	goBin = filepath.Join(cwfCliFakeGo(t, "", "", 0), "go")
	if got := installedBinary(goBin); got != "" {
		t.Fatalf("installedBinary with neither = %q, want empty", got)
	}
	if installedVersion("") != "" || installedVersion(filepath.Join(t.TempDir(), "nope")) != "" {
		t.Fatal("installedVersion of a missing binary should be empty")
	}
}

// ---------- FIND-0346: wait's non-happy paths ----------

// cwfCliWaitRoutes fakes cmdWait's endpoints. routes maps a path to
// "STATUS BODY"; /api/events defaults to cursor 0 and /api/stream to a silent
// stream.
func cwfCliWaitRoutes(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := routes[r.URL.Path]
		switch {
		case ok:
			st, body, _ := strings.Cut(route, " ")
			code, _ := strconv.Atoi(st)
			w.WriteHeader(code)
			w.Write([]byte(body))
		case r.URL.Path == "/api/events":
			w.Write([]byte(`{"last_id":0}`))
		case r.URL.Path == "/api/stream":
			cwfCliSilentStream(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCwfCliWaitErrorStatuses(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		routes map[string]string
		code   int
		msg    string
	}{
		{"done 403", []string{"1", "--done"}, map[string]string{"/api/tasks/1": `403 {"error":"out_of_scope"}`}, 8, "out_of_scope"},
		{"done 500", []string{"1", "--done"}, map[string]string{"/api/tasks/1": `500 {"error":"boom"}`}, 1, "boom"},
		{"ready 500", []string{"--ready"}, map[string]string{"/api/tasks": `500 {"error":"boom"}`}, 1, "boom"},
		{"cursor 500", []string{"--ready"}, map[string]string{"/api/events": `500 {"error":"no events"}`}, 1, "no events"},
		{"cursor bad body", []string{"--ready"}, map[string]string{"/api/events": `200 <html>`}, 1, "bad response"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := cwfCliWaitRoutes(t, c.routes)
			var code int
			msg := captureStderr(t, func() {
				code = captureExit(t, func() {
					cmdWait(newWaitClient(ts.URL), parse(append(c.args, "--timeout", "3s")))
				})
			})
			if code != c.code || !strings.Contains(msg, c.msg) {
				t.Fatalf("exit %d, stderr %q; want exit %d with %q", code, msg, c.code, c.msg)
			}
		})
	}
}

func TestCwfCliWaitJSON(t *testing.T) {
	ts := cwfCliWaitRoutes(t, map[string]string{
		"/api/tasks/1": `200 {"id":1,"title":"a","status":"done"}`,
		"/api/tasks":   `200 [{"id":4,"title":"b","status":"todo"}]`,
	})
	for _, c := range []struct {
		args []string
		id   int64
	}{{[]string{"1", "--done", "--json"}, 1}, {[]string{"--ready", "--json"}, 4}} {
		out := captureStdout(t, func() { cmdWait(newWaitClient(ts.URL), parse(c.args)) })
		var tk Task
		if err := json.Unmarshal([]byte(out), &tk); err != nil || tk.ID != c.id {
			t.Fatalf("wait %v printed %q, want the task %d as JSON", c.args, out, c.id)
		}
	}
}

// Under --done, events for other tasks never trigger a re-check: only the
// initial and post-open checks run before the timeout.
func TestCwfCliWaitDoneIgnoresOtherTasks(t *testing.T) {
	var gets atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/events":
			w.Write([]byte(`{"last_id":0}`))
		case "/api/tasks/5":
			gets.Add(1)
			w.Write([]byte(`{"id":5,"status":"todo"}`))
		case "/api/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 1; i <= 3; i++ {
				fmt.Fprintf(w, "id: %d\ndata: {\"id\":%d,\"task_id\":9}\n\n", i, i)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}
	}))
	t.Cleanup(ts.Close)
	code := captureExit(t, func() {
		captureStderr(t, func() { cmdWait(newWaitClient(ts.URL), parse([]string{"5", "--done", "--timeout", "1s"})) })
	})
	if code != 7 {
		t.Fatalf("exit %d, want 7 (timeout)", code)
	}
	if n := gets.Load(); n != 2 {
		t.Fatalf("task GETs = %d, want 2: events for task 9 must not re-check task 5", n)
	}
}
