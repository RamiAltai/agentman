package main

import (
	"strings"
	"testing"
)

// TestDashboardNoXSSSinks is a SOURCE-LEVEL guardrail that enforces the el()/
// textContent DOM convention used throughout app.js.
//
// WHY THIS EXISTS:
//   - The project has no JS test runner (keeps the no-npm / single-binary ethos).
//   - The dashboard deliberately avoids innerHTML and related sinks; all dynamic
//     content is built via el() which uses document.createTextNode() for strings,
//     so agent-supplied titles and comments cannot inject HTML.
//   - This test locks in that XSS-safe convention at the Go build level. A future
//     accidental .innerHTML assignment will fail `go test` before it ships.
//
// The patterns checked are dot-prefixed (e.g. ".innerHTML") to avoid triggering
// on the intentional comment in app.js that says "never innerHTML" (no leading dot).
func TestDashboardNoXSSSinks(t *testing.T) {
	files := []string{
		"web/app.js",
		"web/index.html",
	}

	// Dangerous sink patterns. Each is a dot-prefixed property assignment form or
	// a function call that can execute arbitrary HTML/JS.
	sinks := []string{
		".innerHTML",
		".outerHTML",
		".insertAdjacentHTML",
		"document.write",
		"eval(",
	}

	for _, name := range files {
		data, err := webFS.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		content := string(data)
		for _, sink := range sinks {
			if strings.Contains(content, sink) {
				t.Errorf("%s contains dangerous XSS sink %q — use el()/textContent instead", name, sink)
			}
		}
	}
}

// TestDashboardThemeAssets locks in the dark/light theme wiring at the Go build
// level (same no-JS-runner rationale as TestDashboardNoXSSSinks): the CSS must
// ship the light-theme override block, and index.html must carry both the inline
// FOUC-guard script and the toggle button.
func TestDashboardThemeAssets(t *testing.T) {
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatalf("ReadFile app.css: %v", err)
	}
	if !strings.Contains(string(css), `:root[data-theme="light"]`) {
		t.Error("app.css missing light-theme override block")
	}

	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("ReadFile index.html: %v", err)
	}
	hs := string(html)
	if !strings.Contains(hs, `localStorage.getItem("am.theme")`) {
		t.Error("index.html missing inline theme-init script")
	}
	if !strings.Contains(hs, `id="themeToggle"`) {
		t.Error("index.html missing #themeToggle button")
	}
}
