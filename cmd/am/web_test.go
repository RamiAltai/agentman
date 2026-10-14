package main

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestDashboardNoXSSSinks is a SOURCE-LEVEL guardrail that enforces the el()/
// textContent DOM convention used throughout app.js.
//
// WHY THIS EXISTS:
//   - The project has no npm toolchain (single-binary ethos); app.js behavior is
//     exercised by the node harness in cwf_web_g4_harness_test.go when node is on PATH.
//   - The dashboard deliberately avoids innerHTML and related sinks; all dynamic
//     content is built via el() which uses document.createTextNode() for strings,
//     so agent-supplied titles and comments cannot inject HTML.
//   - It scans every file embedded under web/ (a new script is covered without
//     editing this list) for the known markup/script sinks below, in dot and
//     bracket form. It is a pattern scan, not a parser: a sink reached through an
//     alias or computed name passes. el()'s rule that string on* props are dropped
//     is checked at runtime by TestCwfWebHarnessElDropsStringHandlers.
//
// The property patterns are dot- or bracket-prefixed (e.g. ".innerHTML") to avoid
// triggering on the intentional comment in app.js that says "never innerHTML".
func TestDashboardNoXSSSinks(t *testing.T) {
	sinks := []struct{ re, what string }{
		{`\.(innerHTML|outerHTML|insertAdjacentHTML)\b`, "HTML property/method"},
		{`\[\s*["'\x60](innerHTML|outerHTML|insertAdjacentHTML|srcdoc)["'\x60]\s*\]`, "bracket-notation HTML sink"},
		{`document\.write`, "document.write"},
		{`\beval\(`, "eval("},
		{`\bFunction\(`, "Function( / new Function("},
		{`\bset(Timeout|Interval)\(\s*["'\x60]`, "string-evaluating timer"},
		{`\bsrcdoc\b`, "iframe srcdoc"},
		{`createContextualFragment`, "Range.createContextualFragment"},
		{`(?i)javascript:`, "javascript: URL"},
	}
	n := 0
	err := fs.WalkDir(webFS, "web", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		data, err := webFS.ReadFile(name)
		if err != nil {
			return err
		}
		for _, s := range sinks {
			if loc := regexp.MustCompile(s.re).FindIndex(data); loc != nil {
				t.Errorf("%s contains XSS sink %q (%s) — use el()/textContent instead", name, data[loc[0]:loc[1]], s.what)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web/: %v", err)
	}
	if n < 3 {
		t.Fatalf("walked %d files under web/, want app.js, app.css and index.html at least", n)
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

// TestDashboardParityAffordances is a coarse presence test for the CLI↔GUI parity
// affordances: index.html must carry the filter controls' element ids and app.css
// the selectors that style each affordance. It matches raw text, so it proves the
// hooks exist, not that they work. The affordances' behavior (create/archive
// category, the project category picker, project edit, board filters, editable
// meta, release) is exercised by TestCwfWebG5HarnessParityAffordances.
func TestDashboardParityAffordances(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("ReadFile index.html: %v", err)
	}
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatalf("ReadFile app.css: %v", err)
	}
	indexHTML, appCSS := string(html), string(css)

	checks := []struct {
		src    string
		needle string
		why    string
	}{
		// Board filters: the header button and its popover.
		{indexHTML, `id="filterBtn"`, "filter button in the header"},
		{indexHTML, `id="filterPanel"`, "filter popover panel in the header"},
		// Styling: each affordance must be themed.
		{appCSS, ".filter-panel", "filter popover styling"},
		{appCSS, ".meta-add-row", "editable-meta row styling"},
		{appCSS, ".btn-release", "release button styling"},
		{appCSS, ".cat-card-add", "dashed add-category card styling"},
		{appCSS, ".cat-row", "manage-categories row styling"},
	}
	for _, c := range checks {
		if !strings.Contains(c.src, c.needle) {
			t.Errorf("missing %q — %s", c.needle, c.why)
		}
	}
}
