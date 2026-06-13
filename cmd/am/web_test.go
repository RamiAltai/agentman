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

// TestDashboardParityAffordances locks in the CLI↔GUI parity affordances at the Go
// build level (same no-JS-runner rationale as TestDashboardNoXSSSinks): the
// dashboard assets must carry the create/archive-category, project-category-picker,
// project-edit, board-filter, editable-meta, and release wiring. A regression that
// drops any of these fails `go test` before it ships.
func TestDashboardParityAffordances(t *testing.T) {
	js, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("ReadFile app.js: %v", err)
	}
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("ReadFile index.html: %v", err)
	}
	css, err := webFS.ReadFile("web/app.css")
	if err != nil {
		t.Fatalf("ReadFile app.css: %v", err)
	}
	appJS, indexHTML, appCSS := string(js), string(html), string(css)

	checks := []struct {
		src    string
		needle string
		why    string
	}{
		// GAP 1 — create category.
		{appJS, "openNewCategory", "create-category modal handler"},
		{appJS, "newCatCard", "dashed add-card in the overview grid"},
		{appJS, "/api/categories", "category create/list endpoint"},
		// GAP 2 — category picker on project create.
		{appJS, "category: csel.value", "new-project POST carries the chosen category"},
		// GAP 3 — archive/unarchive category in the Manage modal.
		{appJS, "renderManageCategories", "categories section of the Manage modal"},
		{appJS, "/api/categories?archived=true", "Manage modal lists archived categories"},
		{appJS, "/unarchive", "category unarchive action"},
		// GAP 4 — edit project.
		{appJS, "openEditProject", "project-edit sub-modal handler"},
		{appJS, "btn-edit-proj", "per-row Edit button in the Manage modal"},
		{appJS, "vault_project_id", "project-edit can set the vault project id"},
		// GAP 5 — board filters.
		{indexHTML, `id="filterBtn"`, "filter button in the header"},
		{indexHTML, `id="filterPanel"`, "filter popover panel in the header"},
		{appJS, "filterReady", "ready board-filter state"},
		{appJS, "filterBlocked", "blocked board-filter state"},
		{appJS, "filterStale", "stale board-filter state"},
		{appJS, "filterMetaKey", "meta-key board-filter state"},
		{appJS, "renderFilterPanel", "filter panel renderer"},
		// GAP 6 — editable meta.
		{appJS, "patchMeta", "inline meta set/delete helper"},
		{appJS, "meta-add-row", "add-meta input row in the task modal"},
		// GAP 7 — release.
		{appJS, "btn-release", "one-click release button in the task modal"},
		// CSS — the affordances must be themed.
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
