package main

import (
	"strings"
	"testing"
)

// Source-level guards for the dashboard behavior fixes (same no-JS-runner
// rationale as TestDashboardNoXSSSinks). Each check fails on the pre-fix app.js.

// cwfWebAppJS returns the embedded app.js source.
func cwfWebAppJS(t *testing.T) string {
	t.Helper()
	b, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("ReadFile app.js: %v", err)
	}
	return string(b)
}

// cwfWebFunc returns the source of the top-level JS function `name`, from its
// declaration to the matching closing brace. Braces inside strings/regexes in
// app.js are balanced, so plain counting is enough here.
func cwfWebFunc(t *testing.T, src, name string) string {
	t.Helper()
	start := -1
	for _, decl := range []string{"\nfunction " + name + "(", "\nasync function " + name + "("} {
		if i := strings.Index(src, decl); i >= 0 {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("function %s not found in app.js", name)
	}
	open := strings.Index(src[start:], "{")
	depth := 0
	for i := start + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("function %s: unbalanced braces", name)
	return ""
}

// cwfWebBefore fails unless a occurs in s, b occurs in s, and a comes first.
func cwfWebBefore(t *testing.T, what, s, a, b string) {
	t.Helper()
	ia, ib := strings.Index(s, a), strings.Index(s, b)
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("%s: want %q before %q (got %d, %d)", what, a, b, ia, ib)
	}
}

func cwfWebHas(t *testing.T, what, s, needle string) {
	t.Helper()
	if !strings.Contains(s, needle) {
		t.Errorf("%s: missing %q", what, needle)
	}
}

func cwfWebLacks(t *testing.T, what, s, needle string) {
	t.Helper()
	if strings.Contains(s, needle) {
		t.Errorf("%s: unexpected %q", what, needle)
	}
}

// FIND-0352: open statuses are fetched uncapped; only done is limited, and a
// capped done column says so.
func TestCwfWebBoardNotTruncatedAt500(t *testing.T) {
	js := cwfWebAppJS(t)
	lb := cwfWebFunc(t, js, "loadBoard")
	cwfWebLacks(t, "loadBoard", lb, "limit: 500")
	cwfWebHas(t, "loadBoard", lb, `status: "todo,doing,blocked"`)
	cwfWebHas(t, "loadBoard", lb, `status: "done", limit: DONE_FETCH_LIMIT`)
	cwfWebHas(t, "renderBoard", cwfWebFunc(t, js, "renderBoard"), "doneCapped")
}

// FIND-0354: modal reconciliation runs before the overview early return.
func TestCwfWebModalReconciledOnOverview(t *testing.T) {
	js := cwfWebAppJS(t)
	oe := cwfWebFunc(t, js, "onEvent")
	cwfWebBefore(t, "onEvent", oe, "reconcileModal(ev)", `if (view === "overview")`)
	rm := cwfWebFunc(t, js, "reconcileModal")
	cwfWebHas(t, "reconcileModal", rm, "closeModal()")
	cwfWebHas(t, "reconcileModal", rm, "refreshModal()")
	cwfWebLacks(t, "onEvent", oe, "refreshModal()")
	cwfWebLacks(t, "onEvent", oe, "closeModal()")
}

// FIND-0355: the feed (which sets the stream cursor) is read before the board,
// and before the stream reopens. FIND-0418: the view/selection changes all
// reload through reloadView.
func TestCwfWebCursorReadBeforeBoard(t *testing.T) {
	js := cwfWebAppJS(t)
	rs := cwfWebFunc(t, js, "resync")
	cwfWebBefore(t, "resync board branch", rs[strings.Index(rs, "else {"):], "await loadFeed()", "await loadBoard()")
	cwfWebBefore(t, "reloadView", cwfWebFunc(t, js, "reloadView"), "await resync()", "connect()")
	for _, fn := range []string{"applyView", "goProject", "toggleProject"} {
		cwfWebHas(t, fn, cwfWebFunc(t, js, fn), "await reloadView()")
	}
}

// FIND-0358: project.patched has feed/history text (both renderers word events
// through evParts since FIND-0427; the null-ref default branch is exercised by
// TestCwfWebG5HarnessEventWordingShared).
func TestCwfWebProjectPatchedFeedText(t *testing.T) {
	js := cwfWebAppJS(t)
	cwfWebHas(t, "evParts", cwfWebFunc(t, js, "evParts"), `case "project.patched":`)
	cwfWebHas(t, "evText", cwfWebFunc(t, js, "evText"), "evParts(ev)")
	cwfWebHas(t, "describeText", cwfWebFunc(t, js, "describeText"), "evParts(ev)")
	top := cwfWebFunc(t, js, "onEvent")
	cwfWebBefore(t, "onEvent", top, `ev.kind === "project.patched") loadProjects()`, "reconcileModal(ev)")
}

// FIND-0359: pan/zoom listeners are installed once at load, not per overlay
// open, and the view-reset flag is separate and reset on project switch.
func TestCwfWebGraphListenersInstalledOnce(t *testing.T) {
	js := cwfWebAppJS(t)
	cwfWebLacks(t, "app.js", js, "graphPanZoomInstalled")
	if n := strings.Count(js, "installGraphPanZoom("); n != 2 { // declaration + one call
		t.Errorf("installGraphPanZoom( appears %d times, want 2", n)
	}
	cwfWebHas(t, "app.js", js, "\ninstallGraphPanZoom($(\"graphSvg\"));")
	cwfWebLacks(t, "renderGraph", cwfWebFunc(t, js, "renderGraph"), "svgEl.addEventListener")
	cwfWebHas(t, "installGraphPanZoom", cwfWebFunc(t, js, "installGraphPanZoom"), `svgEl.addEventListener("click"`)
	cwfWebHas(t, "openGraphOverlay", cwfWebFunc(t, js, "openGraphOverlay"), "graphKeepView = false")
	cwfWebHas(t, "buildGraphProjectSelect", cwfWebFunc(t, js, "buildGraphProjectSelect"), "graphKeepView = false")
}

// FIND-0362: in-place project selection updates the top-bar title.
func TestCwfWebInPlaceSelectionUpdatesTitle(t *testing.T) {
	js := cwfWebAppJS(t)
	gp := cwfWebFunc(t, js, "goProject")
	cwfWebBefore(t, "goProject", gp, "setBreadcrumb()", "pendingProject = p.slug")
	cwfWebHas(t, "toggleProject", cwfWebFunc(t, js, "toggleProject"), "setBreadcrumb()")
}

// FIND-0363: label chips handle Enter/Space themselves; card keys ignore keys
// from nested controls.
func TestCwfWebLabelChipKeyboard(t *testing.T) {
	js := cwfWebAppJS(t)
	ck := cwfWebFunc(t, js, "onCardKey")
	cwfWebBefore(t, "onCardKey", ck, "e.target !== e.currentTarget", "onActivate(")
	c := cwfWebFunc(t, js, "card")
	chip := c[strings.Index(c, `class: "tag-label", role: "button"`):]
	cwfWebBefore(t, "label chip", chip, "onkeydown:", "}, l));")
}

// FIND-0366: Clear all and Mine cancel pending debounced box edits.
func TestCwfWebClearFiltersCancelsTimers(t *testing.T) {
	js := cwfWebAppJS(t)
	cf := cwfWebFunc(t, js, "clearFilters")
	cwfWebBefore(t, "clearFilters", cf, "clearTimeout(filterMineTimer)", "renderFilterPanel()")
	cwfWebBefore(t, "clearFilters", cf, "clearTimeout(filterMetaTimer)", "renderFilterPanel()")
	cwfWebHas(t, "renderFilterPanel", cwfWebFunc(t, js, "renderFilterPanel"), `onclick: () => { clearTimeout(filterMineTimer); filterMine = "human"`)
}

// FIND-0367: the overview's refresh path re-fetches projects too.
func TestCwfWebOverviewRefreshesProjects(t *testing.T) {
	cwfWebHas(t, "loadOverview", cwfWebFunc(t, cwfWebAppJS(t), "loadOverview"), `api("GET", "/api/projects")`)
}

// FIND-0368: a project.patched slug rename follows the selection.
func TestCwfWebProjectRenameFollowsSelection(t *testing.T) {
	js := cwfWebAppJS(t)
	oe := cwfWebFunc(t, js, "onEvent")
	i := strings.Index(oe, `if (ev.kind === "project.patched") {`)
	if i < 0 {
		t.Fatal("onEvent: no project.patched handler")
	}
	h := oe[i:]
	cwfWebBefore(t, "project.patched handler", h, "selected.delete(sl[0])", "selected.add(sl[1])")
	cwfWebBefore(t, "project.patched handler", h, "selected.add(sl[1])", "connect()")
	cwfWebHas(t, "loadProjects", cwfWebFunc(t, js, "loadProjects"), "setBreadcrumb()")
}

// FIND-0369: the New task picker is scoped to the current view.
func TestCwfWebNewTaskPickerScopedToView(t *testing.T) {
	on := cwfWebFunc(t, cwfWebAppJS(t), "openNew")
	cwfWebHas(t, "openNew", on, "projectsInView()")
	cwfWebLacks(t, "openNew", on, "for (const p of projects)")
	cwfWebLacks(t, "openNew", on, "projects[0]")
}

// FIND-0371: live refreshes are deferred while the sheet holds uncommitted text.
func TestCwfWebModalRefreshKeepsUnsavedEdits(t *testing.T) {
	js := cwfWebAppJS(t)
	rm := cwfWebFunc(t, js, "refreshModal")
	cwfWebBefore(t, "refreshModal", rm, "if (modalDirty())", `api("GET"`)
	cwfWebBefore(t, "refreshModal", rm, `api("GET"`, "renderModal(t)")
	md := cwfWebFunc(t, js, "modalDirty")
	cwfWebHas(t, "modalDirty", md, "x.value !== x.defaultValue")
	r := cwfWebFunc(t, js, "renderModal")
	cwfWebHas(t, "renderModal", r, "x.defaultValue = x.value")
	for _, f := range []string{"title.defaultValue = title.value", "asg.defaultValue = asg.value", "body.defaultValue = body.value"} {
		cwfWebHas(t, "renderModal", r, f)
	}
	cwfWebHas(t, "app.js", js, `$("sheet").addEventListener("change", runDeferredRefresh)`)
}
