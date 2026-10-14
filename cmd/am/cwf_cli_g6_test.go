package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A 2xx body that is not JSON (a proxy page) must exit 1, not print task id 0.
func TestCwfCliNewRejectsNonJSONResponse(t *testing.T) {
	stub := cwfCliStub(t, http.StatusCreated, "<html>proxy login</html>")
	c := newTestClient(stub.URL, stub.Client())
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() {
			code = captureExit(t, func() { cmdNew(c, parse([]string{"title", "-p", "x"})) })
		})
	})
	if code != 1 || out != "" {
		t.Fatalf("cmdNew on non-JSON 201: exit %d stdout %q, want exit 1 and no output", code, out)
	}
}

// `am token new` must never overwrite a stored token with an empty one when
// the server's 2xx body is not the expected shape.
func TestCwfCliTokenNewKeepsStoredTokenOnBadResponse(t *testing.T) {
	for _, body := range []string{"{}", "not json"} {
		f := filepath.Join(t.TempDir(), "identity")
		t.Setenv("AGENTMAN_AGENT_FILE", f)
		const stored = `{"agent":"a1","token":"amt_good"}`
		if err := os.WriteFile(f, []byte(stored), 0o600); err != nil {
			t.Fatal(err)
		}
		stub := cwfCliStub(t, http.StatusCreated, body)
		c := &Client{base: stub.URL, http: stub.Client()} // unscoped admin identity
		var code int
		captureStdout(t, func() {
			captureStderr(t, func() {
				code = captureExit(t, func() { cmdToken(c, parse([]string{"new", "--scope", "work"})) })
			})
		})
		if code != 1 {
			t.Errorf("body %q: exit %d, want 1", body, code)
		}
		if got, _ := os.ReadFile(f); string(got) != stored {
			t.Errorf("body %q: identity file = %s, want it unchanged", body, got)
		}
	}
}

// Every AGENTMAN_* variable the binary reads is named in `am help`, so the
// hand-maintained Env block cannot silently drift from the code.
func TestCwfCliUsageListsEveryEnvVar(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	name := regexp.MustCompile(`"(AGENTMAN_[A-Z_]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range name.FindAllStringSubmatch(string(src), -1) {
			read[m[1]] = true
		}
	}
	if len(read) < 5 {
		t.Fatalf("found only %d AGENTMAN_* names in sources: %v", len(read), read)
	}
	help := captureStdout(t, usage)
	for v := range read {
		if !regexp.MustCompile(`\b` + v + `\b`).MatchString(help) {
			t.Errorf("usage() does not mention %s, which the binary reads", v)
		}
	}
}
