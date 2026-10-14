package main

import (
	"strings"
	"testing"
)

// Source-level guards for the third group of dashboard behavior fixes (same
// no-JS-runner rationale as TestDashboardNoXSSSinks). Each fails on the pre-fix app.js.

// cwfWebSpan returns s from the first occurrence of from up to the next occurrence
// of to after it (inclusive of from, exclusive of to).
func cwfWebSpan(t *testing.T, what, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("%s: %q not found", what, from)
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		t.Fatalf("%s: %q not found after %q", what, to, from)
	}
	return s[i : i+j]
}

// FIND-0398: create buttons disable while the POST is in flight and re-enable on
// failure, so a double click cannot create two tasks (or 409 a created project).
func TestCwfWebCreateButtonsBlockDoubleSubmit(t *testing.T) {
	js := cwfWebAppJS(t)
	for _, fn := range []string{"openNew", "openNewCategory", "openNewProject"} {
		f := cwfWebFunc(t, js, fn)
		cwfWebBefore(t, fn, f, "save.disabled = true;", `await api("POST"`)
		catchBody := cwfWebSpan(t, fn, f, "} catch (e) {", "}, \"Create")
		cwfWebHas(t, fn+" catch", catchBody, "save.disabled = false;")
	}
}

// FIND-0402 is covered by TestCwfWebFeedTrimMovesOldest (feedSeq guard, g2).

// FIND-0403: the feed cap grows with each older page instead of being dropped, and
// the start marker is cleared when rows above it are trimmed or the feed reloads.
func TestCwfWebFeedCapSurvivesPagination(t *testing.T) {
	js := cwfWebAppJS(t)
	cwfWebLacks(t, "app.js", js, "feedPaginated")
	tf := cwfWebFunc(t, js, "trimFeed")
	cwfWebHas(t, "trimFeed", tf, "while (l.children.length > feedCap)")
	cwfWebHas(t, "trimFeed", tf, "if (!loadOlderBtn) setLoadOlder(true);")
	cwfWebHas(t, "loadOlderActivity", cwfWebFunc(t, js, "loadOlderActivity"), "feedCap += evs.length;")
	lf := cwfWebFunc(t, js, "loadFeed")
	cwfWebHas(t, "loadFeed", lf, "feedCap = 200;")
	cwfWebHas(t, "loadFeed", lf, "setLoadOlder(feedOldest > 0);")
	cwfWebHas(t, "setLoadOlder", cwfWebFunc(t, js, "setLoadOlder"), `feed.querySelector(".feed-start-marker")?.remove();`)
}

// FIND-0408: live refreshes coalesce with a bounded delay (no clear-and-rearm),
// the modal refresh is coalesced too, and the prerequisite candidates are cached.
func TestCwfWebLiveRefreshesCannotStarve(t *testing.T) {
	js := cwfWebAppJS(t)
	c := cwfWebFunc(t, js, "coalesce")
	cwfWebHas(t, "coalesce", c, "if (coalesceTimers[key]) return;")
	cwfWebLacks(t, "coalesce", c, "clearTimeout")
	oe := cwfWebFunc(t, js, "onEvent")
	cwfWebLacks(t, "onEvent", oe, "clearTimeout(")
	cwfWebHas(t, "onEvent", oe, `coalesce("board", () => loadBoard().catch(reconcileFailed), 250)`)
	cwfWebHas(t, "onEvent", oe, `coalesce("overview",`)
	rr := cwfWebFunc(t, js, "refreshRailCounts")
	cwfWebHas(t, "refreshRailCounts", rr, `coalesce("rail",`)
	cwfWebLacks(t, "refreshRailCounts", rr, "clearTimeout(")
	rm := cwfWebFunc(t, js, "reconcileModal")
	cwfWebHas(t, "reconcileModal", rm, `coalesce("modal", () => refreshModal(), 150)`)
	cwfWebBefore(t, "reconcileModal", rm, "depCandidates = null;", "if (!openTaskId) return;")
	md := cwfWebFunc(t, js, "renderTaskDeps")
	cwfWebBefore(t, "renderTaskDeps", md, "if (!depCandidates || depCandidates.project !== t.project) {", `api("GET", "/api/tasks?project="`)
	cwfWebHas(t, "renderTaskDeps", md, "await want.list;")
}

// FIND-0412: the dependency BFS indexes edges once and walks the queue by index.
func TestCwfWebGraphNeighborsLinear(t *testing.T) {
	g := cwfWebFunc(t, cwfWebAppJS(t), "graphNeighbors")
	cwfWebLacks(t, "graphNeighbors", g, ".shift()")
	cwfWebHas(t, "graphNeighbors", g, "adj.get(q[i])")
}

// FIND-0413: graph refreshes skip events of other projects by project_id and
// coalesce without re-arming.
func TestCwfWebGraphRefreshSkipsOtherProjects(t *testing.T) {
	g := cwfWebFunc(t, cwfWebAppJS(t), "graphMaybeRefresh")
	cwfWebHas(t, "graphMaybeRefresh", g, "if (shown && ev.project_id !== shown.id) return;")
	cwfWebLacks(t, "graphMaybeRefresh", g, "node.project !== graphSlug")
	cwfWebBefore(t, "graphMaybeRefresh", g, "if (graphRefreshTimer) return;", "graphRefreshTimer = setTimeout(")
	cwfWebLacks(t, "graphMaybeRefresh", g, "clearTimeout(")
}

// FIND-0414: edge elements are matched to edge objects through a from:to map.
func TestCwfWebGraphFocusEdgeLookupKeyed(t *testing.T) {
	r := cwfWebFunc(t, cwfWebAppJS(t), "selectGraphNode")
	cwfWebLacks(t, "selectGraphNode", r, "edges.find(")
	cwfWebHas(t, "selectGraphNode", r, `new Map(edges.map((x) => [x.from + ":" + x.to, x]))`)
}

// FIND-0422 (failed comment and task deletes tell the user why) is exercised by
// TestCwfWebG5HarnessArmedDeletes since the deletes share armButton (FIND-0421).

// FIND-0425: the "kind changes open-counts" rule is encoded once in onEvent.
func TestCwfWebOnEventCountKindsOnce(t *testing.T) {
	oe := cwfWebFunc(t, cwfWebAppJS(t), "onEvent")
	if n := strings.Count(oe, `/^(task|project|category)\./`); n != 1 {
		t.Errorf("onEvent: count-kind regex appears %d times, want 1", n)
	}
}

// FIND-0430: startup runs after every module-level declaration, graph state included.
func TestCwfWebStartupRunsLast(t *testing.T) {
	js := cwfWebAppJS(t)
	cwfWebBefore(t, "app.js", js, "let graphOpen = false;", "\ninitFeed();")
	cwfWebBefore(t, "app.js", js, "const GRAPH_REFRESH_KINDS", "(async function init() {")
	if !strings.HasSuffix(strings.TrimSpace(js), "})();") {
		t.Error("app.js: the init IIFE is not the last statement")
	}
}
