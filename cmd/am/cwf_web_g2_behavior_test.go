package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Source-level guards for the second group of dashboard behavior fixes (same
// no-JS-runner rationale as TestDashboardNoXSSSinks). Each fails on the pre-fix app.js.

// FIND-0370: every non-task sheet renderer clears openTaskId, so a live event for
// the previously open task cannot re-render its detail over the form.
func TestCwfWebSheetFormsClearOpenTask(t *testing.T) {
	js := cwfWebAppJS(t)
	for _, fn := range []string{"openNew", "openNewCategory", "openNewProject", "openManage", "openEditProject"} {
		cwfWebBefore(t, fn, cwfWebFunc(t, js, fn), "openTaskId = null;", "s.replaceChildren()")
	}
}

// FIND-0372: archiving a category from Manage refreshes the rail's categories in
// the board views too (category events never reach a scoped stream).
func TestCwfWebManageCategoryArchiveRefreshesRail(t *testing.T) {
	r := cwfWebFunc(t, cwfWebAppJS(t), "renderManageCategories")
	cwfWebHas(t, "renderManageCategories", r, `if (view === "overview") await loadOverview();
          else await loadCategories();`)
}

// FIND-0376: a graph response that is no longer the latest request, or arrives
// after the overlay closed, is dropped before touching graphData.
func TestCwfWebGraphFetchDropsStaleResponse(t *testing.T) {
	f := cwfWebFunc(t, cwfWebAppJS(t), "fetchAndRenderGraph")
	cwfWebBefore(t, "fetchAndRenderGraph", f, "const seq = ++graphFetchSeq;", "await api(")
	cwfWebBefore(t, "fetchAndRenderGraph", f, "if (seq !== graphFetchSeq || !graphOpen) return;\n  graphData = data;", "renderGraph()")
	catchBody := f[strings.Index(f, "} catch (e) {"):strings.Index(f, "graphData = data;")]
	cwfWebBefore(t, "fetchAndRenderGraph catch", catchBody, "if (seq !== graphFetchSeq || !graphOpen) return;", "showGraphEmpty(")
}

// FIND-0377: trimming moves feedOldest to the oldest row left, rows carry their
// event id, and an older page that resolves after a feed reload is dropped.
func TestCwfWebFeedTrimMovesOldest(t *testing.T) {
	js := cwfWebAppJS(t)
	cwfWebHas(t, "feedItem", cwfWebFunc(t, js, "feedItem"), `"data-id": ev.id`)
	tf := cwfWebFunc(t, js, "trimFeed")
	cwfWebBefore(t, "trimFeed", tf, "l.removeChild(l.lastChild)", "feedOldest = Number(l.lastChild.dataset.id)")
	lo := cwfWebFunc(t, js, "loadOlderActivity")
	cwfWebBefore(t, "loadOlderActivity", lo, "const seq = feedSeq;", "await api(")
	cwfWebBefore(t, "loadOlderActivity", lo, "if (seq !== feedSeq) return;", "list.append(feedItem(ev))")
}

// FIND-0378: with the graph open, Escape closes it from anywhere and the other
// letter shortcuts are ignored.
func TestCwfWebGraphOverlayKeyboard(t *testing.T) {
	k := cwfWebFunc(t, cwfWebAppJS(t), "onKey")
	esc := k[:strings.Index(k, "trapFocus(e)")]
	cwfWebBefore(t, "onKey Escape", esc, "if (graphOpen) { closeGraphOverlay(); return; }", `$("filterPanel")`)
	cwfWebBefore(t, "onKey", k, `if (graphOpen && k !== "g") return;`, `if (k === "a")`)
}

// FIND-0381: pan/zoom convert pixels through the uniform "meet" scale and zoom by
// one factor clamped relative to the fitted view, not per-axis absolute bounds.
func TestCwfWebGraphPanZoomUniformScale(t *testing.T) {
	p := cwfWebFunc(t, cwfWebAppJS(t), "installGraphPanZoom")
	cwfWebHas(t, "installGraphPanZoom", p, "Math.min(rect.width / graphViewState.width, rect.height / graphViewState.height)")
	cwfWebHas(t, "installGraphPanZoom", p, "graphInitialView.width * 4")
	cwfWebHas(t, "installGraphPanZoom", p, "graphViewState.height *= f;")
	cwfWebLacks(t, "installGraphPanZoom", p, "/ rect.width;")
	cwfWebLacks(t, "installGraphPanZoom", p, "/ rect.height;")
	cwfWebLacks(t, "installGraphPanZoom", p, "4000")
}

// FIND-0382: a drag that moved the view swallows its trailing click (capture
// phase) so panning keeps the selection.
func TestCwfWebGraphPanKeepsSelection(t *testing.T) {
	p := cwfWebFunc(t, cwfWebAppJS(t), "installGraphPanZoom")
	cwfWebHas(t, "installGraphPanZoom mousemove", p, "graphDragMoved = true")
	cwfWebHas(t, "installGraphPanZoom", p, `if (graphDragMoved) { graphDragMoved = false; ev.stopImmediatePropagation(); }
  }, true);`)
}

// FIND-0392: every task.* kind the store emits refreshes the open graph, except
// the label kinds (labels are not drawn in the graph).
func TestCwfWebGraphRefreshKindsCoverStore(t *testing.T) {
	js := cwfWebAppJS(t)
	i := strings.Index(js, "const GRAPH_REFRESH_KINDS = new Set([")
	if i < 0 {
		t.Fatal("GRAPH_REFRESH_KINDS not found")
	}
	set := js[i : i+strings.Index(js[i:], "]);")]
	store, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("ReadFile store.go: %v", err)
	}
	skip := map[string]bool{`"task.labeled"`: true, `"task.unlabeled"`: true}
	kinds := regexp.MustCompile(`"task\.[a-z_]+"`).FindAllString(string(store), -1)
	if len(kinds) == 0 {
		t.Fatal("no task.* kinds found in store.go")
	}
	for _, k := range kinds {
		if !skip[k] && !strings.Contains(set, k) {
			t.Errorf("GRAPH_REFRESH_KINDS lacks %s, which store.go emits", k)
		}
	}
}

// FIND-0393: board and feed loads drop responses superseded by a newer load.
func TestCwfWebLoadsDropSupersededResponses(t *testing.T) {
	js := cwfWebAppJS(t)
	lb := cwfWebFunc(t, js, "loadBoard")
	cwfWebBefore(t, "loadBoard", lb, "const seq = ++boardSeq;", "await Promise.all(")
	cwfWebBefore(t, "loadBoard", lb, "if (seq !== boardSeq) return; // overlapping", "tasks = new Map(")
	lf := cwfWebFunc(t, js, "loadFeed")
	cwfWebBefore(t, "loadFeed", lf, "const seq = ++feedSeq;", "await api(")
	cwfWebBefore(t, "loadFeed", lf, "if (seq !== feedSeq) return; // a newer", "list.replaceChildren()")
}

// FIND-0394: filter and search reloads report failures instead of discarding them.
func TestCwfWebFilterLoadFailuresReported(t *testing.T) {
	js := cwfWebAppJS(t)
	for _, fn := range []string{"setLabelFilter", "clearLabelFilter", "applyFilters", "clearFilters"} {
		f := cwfWebFunc(t, js, fn)
		cwfWebHas(t, fn, f, "loadBoard().catch(reportBoardError)")
		cwfWebLacks(t, fn, f, ".catch(() => {})")
	}
	cwfWebHas(t, "reportBoardError", cwfWebFunc(t, js, "reportBoardError"), "showToast(")
	cwfWebHas(t, "searchBox", js, "filterQ = e.target.value.trim();\n    loadBoard().catch(reportBoardError);")
}

// FIND-0395: a malformed %-escape in #/cat/ falls back to the overview instead of
// throwing before applyView, and hashchange errors are reported.
func TestCwfWebRouteSurvivesMalformedHash(t *testing.T) {
	js := cwfWebAppJS(t)
	r := cwfWebFunc(t, js, "route")
	cwfWebBefore(t, "route", r, "try {", "decodeURIComponent(")
	cwfWebBefore(t, "route", r, "decodeURIComponent(", `catch (e) { next = "overview";`)
	cwfWebBefore(t, "route", r, `catch (e) { next = "overview";`, "await applyView(next, cat)")
	cwfWebLacks(t, "app.js", js, "route().catch(() => {})")
}

// FIND-0397: failed live reconciles flag the header and retry with capped
// backoff, and a reconnect after a drop reloads the view from REST.
func TestCwfWebReconcileFailuresRetry(t *testing.T) {
	js := cwfWebAppJS(t)
	oe := cwfWebFunc(t, js, "onEvent")
	cwfWebHas(t, "onEvent", oe, "loadBoard().catch(reconcileFailed), 250)")
	cwfWebHas(t, "onEvent", oe, "loadOverview().catch(reconcileFailed)")
	cwfWebHas(t, "refreshRailCounts", cwfWebFunc(t, js, "refreshRailCounts"), "loadRail().catch(reconcileFailed)")
	rf := cwfWebFunc(t, js, "reconcileFailed")
	cwfWebBefore(t, "reconcileFailed", rf, `setStatus(`, "setTimeout(resyncNow, resyncDelay)")
	cwfWebHas(t, "reconcileFailed", rf, "Math.min(resyncDelay * 2, 10000)")
	rn := cwfWebFunc(t, js, "resyncNow")
	cwfWebBefore(t, "resyncNow", rn, "await resync();", "resyncDelay = 1000;")
	cwfWebHas(t, "resyncNow", rn, "catch (e) { reconcileFailed(); }")
	c := cwfWebFunc(t, js, "connect")
	cwfWebHas(t, "connect onerror", c[strings.Index(c, "es.onerror"):], "streamDropped = true;")
	cwfWebHas(t, "connect onopen", c, "if (streamDropped) { streamDropped = false; resyncNow(); }")
}
