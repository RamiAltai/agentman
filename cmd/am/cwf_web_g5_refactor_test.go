package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// Behavior tests for the fifth group (refactors) of dashboard fixes, run in the
// app.js node harness of cwf_web_g4_harness_test.go (skipped without node).

// cwfWebG5Task is a task detail payload for renderModal.
const cwfWebG5Task = `const T = { id: 5, ref: 5, project: "alpha", title: "T", status: "doing", assignee: "bob", priority: 1,
  body: "", created_at: "", labels: [], meta: {}, events: [], blocks: [],
  comments: [{ id: 9, author: "amy", body: "hi", created_at: "" }],
  depends_on: [{ id: 4, ref: 4, project: "alpha", title: "dep", status: "todo" }] };
const alerts = [];
globalThis.alert = (m) => alerts.push(m);
const field = (l) => $("sheet").querySelector('[aria-label="' + l + '"]');
const lastWrite = (method) => H.fetchLog.filter((f) => f.method === method).pop();
`

// FIND-0435: the parity affordances the substring table used to name, exercised
// through the UI: create category, the project category picker, archive and
// unarchive category, project edit, the board filters, editable meta and release.
func TestCwfWebG5HarnessParityAffordances(t *testing.T) {
	cwfWebRunJS(t, cwfWebG5Task+`const { eq, assert } = H;
// Create category from the overview's add card.
view = "overview";
categories = [{ slug: "work", name: "Work", counts: {} }];
renderOverview();
H.fire($("overview").querySelector(".cat-card-add"), "click");
eq($("sheet").querySelector(".mhead").textContent, "New category", "add card opens the form");
field("Category name").value = "Platform Team";
H.fetchLog.length = 0;
H.fire($("sheet").querySelector(".save"), "click"); await H.settle();
eq(lastWrite("POST").path, "/api/categories", "category create endpoint");
eq(lastWrite("POST").body, { slug: "platform-team", name: "Platform Team" }, "category create body");

// New project: the POST carries the picked category.
view = "category"; activeCategory = "work"; selected.clear();
categories = [{ slug: "general", name: "General" }, { slug: "work", name: "Work" }];
H.route = (path, opt) => path === "/api/projects" && opt.method === "POST" ? { status: 200, body: { slug: "web-app" } } : undefined;
await openNewProject();
eq(field("Category").querySelectorAll("option").map((o) => o.textContent), ["General", "Work"], "category picker options");
field("Project name").value = "Web App";
field("Category").value = "work";
H.fire($("sheet").querySelector(".save"), "click"); await H.settle();
eq(lastWrite("POST").body, { slug: "web-app", name: "Web App", category: "work" }, "project create body");
H.route = null;

// Manage: archive and unarchive categories, edit a project's vault id.
H.route = (path) => path === "/api/categories?archived=true" ? { status: 200, body: [{ slug: "work", name: "Work" }, { slug: "old", name: "Old", archived_at: "2026-01-01" }] } : undefined;
await openManage();
eq($("sheet").querySelectorAll(".cat-row").length, 2, "every category listed, archived included");
H.fire($("sheet").querySelectorAll(".cat-row")[0].querySelector(".btn-archive"), "click"); await H.settle();
eq(lastWrite("POST").path, "/api/categories/work/archive", "archive category");
H.fire($("sheet").querySelectorAll(".cat-row")[1].querySelector(".btn-archive"), "click"); await H.settle();
eq(lastWrite("POST").path, "/api/categories/old/unarchive", "unarchive category");
H.route = null;
H.fire($("sheet").querySelector(".btn-edit-proj"), "click");
eq($("sheet").querySelector(".mhead").textContent, "Edit project · alpha", "edit form");
field("Vault project id").value = "v-1";
H.fire($("sheet").querySelector(".save"), "click"); await H.settle();
eq([lastWrite("PATCH").path, lastWrite("PATCH").body], ["/api/projects/alpha", { vault_project_id: "v-1" }], "project edit PATCH");

// Board filters: each control lands in the board query.
view = "all"; selected.clear();
renderFilterPanel();
const panel = $("filterPanel");
const boardQuery = () => H.fetchLog.filter((f) => f.path.startsWith("/api/tasks?") && f.path.includes("status=todo")).pop().path;
const boxes = panel.querySelectorAll('input[type="checkbox"]');
eq(boxes.length, 3, "ready/blocked/stale toggles");
for (const b of boxes) { b.checked = true; H.fire(b, "change"); }
await H.settle();
for (const want of ["ready=true", "blocked=true", "stale=" + STALE_FILTER]) assert(boardQuery().includes(want), "board query has " + want + ": " + boardQuery());
panel.querySelector('[aria-label="Filter by meta key"]').value = "sprint";
H.fire(panel.querySelector('[aria-label="Filter by meta key"]'), "input");
H.fire(panel.querySelector(".filter-mine-btn"), "click");
H.runTimers(); await H.settle();
assert(boardQuery().includes("meta_key=sprint") && boardQuery().includes("assignee=human"), "meta key and Mine filters: " + boardQuery());
eq($("filterBtn").querySelector(".filter-count").textContent, "5", "filter count badge");
clearFilters(); await H.settle();

// Task modal: editable meta and one-click release.
openTaskId = 5;
renderModal(T);
field("Meta key").value = "sprint";
field("Meta value").value = "42";
H.fetchLog.length = 0;
H.fire($("sheet").querySelector(".meta-add-btn"), "click"); await H.settle();
eq([lastWrite("PATCH").path, lastWrite("PATCH").body], ["/api/tasks/5", { meta: { sprint: "42" } }], "meta add PATCH");
H.fire($("sheet").querySelector(".btn-release"), "click"); await H.settle();
eq(lastWrite("PATCH").body, { assignee: "", status: "todo" }, "release PATCH");
renderModal(Object.assign({}, T, { assignee: "", status: "todo" }));
eq($("sheet").querySelector(".btn-release"), null, "nothing to release on an unclaimed todo");`)
}

// FIND-0421 (and FIND-0422): the comment, task and project deletes share one
// two-step control: one timeout, the label kept while the delete is in flight,
// and a failure re-enables, disarms and reports.
func TestCwfWebG5HarnessArmedDeletes(t *testing.T) {
	cwfWebRunJS(t, cwfWebG5Task+`const { eq, assert } = H;
openTaskId = 5;
renderModal(T);
openModal();
const delCm = $("sheet").querySelector(".btn-del-cm");
H.fire(delCm, "click");
eq(delCm.textContent, "Confirm delete?", "first click arms");
assert(delCm.classList.contains("confirming"), "armed class");
H.runTimers();
eq(delCm.textContent, "×", "the armed button disarms after the timeout");
assert(!delCm.classList.contains("confirming"), "armed class cleared");
// A failed delete re-enables, disarms and says why.
H.route = (path, opt) => opt.method === "DELETE" ? { status: 500, body: { error: "boom" } } : undefined;
H.fire(delCm, "click");
H.fire(delCm, "click");
assert(delCm.disabled, "disabled while in flight");
H.runTimers();
eq(delCm.textContent, "Confirm delete?", "the arm timeout does not revert the label mid-delete");
await H.settle();
eq(lastWrite("DELETE").path, "/api/tasks/5/comments/9", "comment delete endpoint");
eq(alerts, ["boom"], "failure reported");
eq([delCm.disabled, delCm.textContent, delCm.classList.contains("confirming")], [false, "×", false], "re-enabled and disarmed");
// The task delete arms the same way (timeout, no extra Cancel control) and closes the modal on success.
H.route = null;
const delT = $("sheet").querySelector(".btn-danger-task");
H.fire(delT, "click");
eq(delT.textContent, "Confirm delete?", "task delete arms");
eq(delT.parentNode.children.length, 2, "row is still Release + Delete");
H.runTimers();
eq(delT.textContent, "Delete task", "task delete disarms after the timeout too");
H.fire(delT, "click");
H.fire(delT, "click");
await H.settle();
eq(lastWrite("DELETE").path, "/api/tasks/5", "task delete endpoint");
eq(openTaskId, null, "modal closed");
assert($("modal").classList.contains("hidden"), "modal hidden");
// The project delete: a failure is shown in the Manage error line; success drops the selection.
view = "all"; selected.clear(); selected.add("alpha");
await openManage();
const rm = () => $("sheet").querySelector(".btn-danger-proj");
H.route = (path, opt) => opt.method === "DELETE" ? { status: 409, body: { error: "project has tasks" } } : undefined;
H.fire(rm(), "click"); H.fire(rm(), "click"); await H.settle();
eq($("sheet").querySelector(".ferr").textContent, "project has tasks", "project delete failure shown");
eq([rm().disabled, rm().textContent], [false, "Delete"], "project delete re-armable");
H.route = null;
const n = H.streams.length;
H.fire(rm(), "click"); H.fire(rm(), "click"); await H.settle();
eq(lastWrite("DELETE").path, "/api/projects/alpha", "project delete endpoint");
assert(!selected.has("alpha"), "deleted project deselected");
eq(H.streams.length, n + 1, "stream reopened with the wider scope");`)
}

// FIND-0418, FIND-0423: selection changes reload through one path with one error
// policy, and every way a selected project disappears reloads feed and board.
func TestCwfWebG5HarnessSelectionReloads(t *testing.T) {
	cwfWebRunJS(t, cwfWebG5Task+`const { eq, assert } = H;
// A live project.deleted of the selected project reloads the feed too (it used to reload only the board).
view = "all"; selected.clear(); selected.add("alpha");
cursor = 100;
H.fetchLog.length = 0;
let n = H.streams.length;
onEvent({ id: 101, kind: "project.deleted", data: { slug: "alpha" } });
assert(!selected.has("alpha"), "deleted project deselected");
eq(H.streams.length, n + 1, "stream reopened");
eq($("breadcrumb").textContent, "All tasks", "title follows the wider scope");
await H.settle();
assert(H.fetchLog.some((f) => f.path === "/api/events?tail=50"), "feed reloaded unscoped");
assert(H.fetchLog.some((f) => f.path.startsWith("/api/tasks?") && !f.path.includes("project=")), "board reloaded unscoped");
// Archiving the selected project from the Manage modal takes the same path.
selected.add("beta");
await openManage();
n = H.streams.length;
H.fetchLog.length = 0;
H.fire($("sheet").querySelectorAll(".proj-row")[1].querySelectorAll(".btn-archive")[1], "click"); await H.settle(); // [0] is Edit
eq(lastWrite("POST").path, "/api/projects/beta/archive", "archive endpoint");
assert(!selected.has("beta"), "archived project deselected");
eq(H.streams.length, n + 1, "stream reopened");
assert(H.fetchLog.some((f) => f.path === "/api/events?tail=50"), "feed reloaded");
// An in-place project switch whose load fails says why and still reopens the stream.
view = "category"; activeCategory = "work"; selected.clear();
H.route = (path) => path.startsWith("/api/events?tail") ? { status: 500, body: { error: "db down" } } : undefined;
n = H.streams.length;
await goProject({ slug: "beta", category: "work" });
H.route = null;
eq([...selected], ["beta"], "project selected");
eq(H.streams.length, n + 1, "stream reopened after the failed load");
assert(document.querySelectorAll(".toast-msg").some((x) => x.textContent === "Couldn't load the view: db down"), "failure reason shown");
// toggleProject reloads through the same path.
H.fetchLog.length = 0;
await toggleProject("alpha");
eq([...selected].sort(), ["alpha", "beta"], "toggle adds");
const i = H.fetchLog.findIndex((f) => f.path.startsWith("/api/events?tail")), j = H.fetchLog.findIndex((f) => f.path.startsWith("/api/tasks?"));
assert(i >= 0 && j > i, "feed read before the board");`)
}

// FIND-0427: the feed and the task history word every event kind identically,
// and a kind without a task ref renders no "null".
func TestCwfWebG5HarnessEventWordingShared(t *testing.T) {
	store, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("ReadFile store.go: %v", err)
	}
	seen := map[string]bool{}
	var kinds []string
	for _, k := range regexp.MustCompile(`"((?:task|project|category|comment)\.[a-z_]+)"`).FindAllStringSubmatch(string(store), -1) {
		if !seen[k[1]] {
			seen[k[1]] = true
			kinds = append(kinds, k[1])
		}
	}
	if len(kinds) < 20 {
		t.Fatalf("found %d event kinds in store.go, want the full set", len(kinds))
	}
	js, _ := json.Marshal(kinds)
	cwfWebRunJS(t, "const KINDS = "+string(js)+";\n"+`const { eq, assert } = H;
for (const kind of KINDS) {
  const e = { id: 1, kind, task_id: 42, actor: "amy", project_id: 1,
    data: { slug: "alpha", status: ["todo", "done"], assignee: ["bob", "amy"], depends_on: 7, label: "bug", meta: { k: "v" } } };
  eq(evText(e).textContent, describeText(e), kind + ": feed and history wording");
}
const linked = evText({ kind: "task.dep_added", task_id: 3, actor: "x", data: { depends_on: 2 } });
eq(linked.textContent, "x linked #3 → depends on #2", "dep_added wording");
assert(linked.querySelector(".ref"), "task ref is a link");
eq(evText({ kind: "project.created", actor: "x", data: { slug: "web" } }).querySelectorAll("b").map((b) => b.textContent), ["x", "web"], "actor and slug bold");
for (const e of [{ kind: "widget.spun", actor: "amy" }, { kind: "category.created", actor: "amy", data: {} }]) {
  assert(!evText(e).textContent.includes("null") && !describeText(e).includes("null"), e.kind + " renders no null");
}
eq(describeText({ kind: "widget.spun", actor: "amy" }), "amy widget.spun", "unknown kind without a ref");
eq(evText({ kind: "widget.spun", actor: "amy", task_id: 4 }).textContent, "amy widget.spun #4", "unknown kind with a ref");`)
}

// FIND-0416, FIND-0420, FIND-0417, FIND-0433: the stale threshold, the priority
// table, Enter/Space activation everywhere it is offered, and graph selection.
func TestCwfWebG5HarnessSharedRules(t *testing.T) {
	cwfWebRunJS(t, cwfWebG5Task+`const { eq, assert } = H;
// Stale: the badge, its tooltip and the filter all follow STALE_MINUTES.
const ago = (min) => new Date(Date.now() - min * 60000).toISOString();
const C = (extra) => card(Object.assign({ id: 1, title: "t", status: "doing", assignee: "bob", priority: 1, project: "alpha", updated_at: ago(0) }, extra));
eq(C({ updated_at: ago(STALE_MINUTES + 1) }).querySelector(".tag-stale").getAttribute("title"), "no activity for " + STALE_MINUTES + "+ min", "stale tooltip");
eq(C({ updated_at: ago(STALE_MINUTES - 1) }).querySelector(".tag-stale"), null, "not stale yet");
eq(STALE_FILTER, STALE_MINUTES + "m", "filter duration");

// Priorities: card chip, card stripe, select options, graph chip and legend come from one table.
const chip = C({ priority: 3 }).querySelector(".chip-prio");
eq([chip.textContent, chip.getAttribute("title"), chip.className], ["P3", "Priority: Low", "chip-prio p3"], "card chip");
eq(C({ priority: 0 }).getAttribute("style"), "--prio:var(--prio-0)", "card stripe uses the themed color");
eq(C({ priority: 9 }).querySelector(".chip-prio").textContent, "P3", "unknown priority reads as Low");
eq(prioSelect(2, null).querySelectorAll("option").map((o) => o.textContent), ["0 — Urgent", "1 — High", "2 — Normal", "3 — Low"], "priority options");
renderGraphDetail({ id: 1, ref: 1, project: "alpha", title: "x", status: "todo", priority: 2, nopen: 0, nprereq: 0 }, [], new Map());
eq($("graphDetail").querySelectorAll(".gd-chip")[1].textContent, "Normal", "graph chip names every level");
buildGraphLegend();
eq($("graphLegend").querySelectorAll(".graph-legend-label").slice(0, 4).map((x) => x.textContent), ["Urgent", "High", "Normal", "Low"], "legend");

// Enter/Space activation.
categories = [{ slug: "work", name: "Work", counts: {} }];
renderOverview();
const cards = $("overview").querySelectorAll(".cat-card");
H.fire(cards[1], "keydown", { key: "Enter" });
eq(location.hash, "#/cat/work", "Enter on a category card");
let e = H.fire(cards[0], "keydown", { key: " " });
eq(location.hash, "#/all", "Space on the All card");
assert(e.defaultPrevented, "Space does not scroll");
H.fire(cards[0], "keydown", { key: "x" });
eq(location.hash, "#/all", "other keys ignored");
H.fire(cards[2], "keydown", { key: " " });
eq($("sheet").querySelector(".mhead").textContent, "New category", "Space on the add card");
H.route = (path) => /^\/api\/tasks\/\d+$/.test(path) ? { status: 200, body: T } : undefined;
const labelled = C({ labels: ["bug"] });
H.fetchLog.length = 0;
H.fire(labelled.querySelector(".tag-label"), "keydown", { key: " " });
await H.settle();
eq(filterLabel, "bug", "Space on a label chip filters");
assert(!H.fetchLog.some((f) => f.path === "/api/tasks/1"), "and does not open the card");
H.fire(labelled, "keydown", { key: " " }); await H.settle();
assert(H.fetchLog.some((f) => f.path === "/api/tasks/1"), "Space on the card opens it");
clearLabelFilter();
H.fetchLog.length = 0;
H.fire(refLink(7), "keydown", { key: " " }); await H.settle();
assert(H.fetchLog.some((f) => f.path === "/api/tasks/7"), "Space on a feed ref opens the task");
H.fetchLog.length = 0;
openTaskId = 5; renderModal(T);
H.fire($("sheet").querySelector(".dep-ref"), "keydown", { key: "Enter" }); await H.settle();
assert(H.fetchLog.some((f) => f.path === "/api/tasks/4"), "Enter on a prerequisite ref opens it");
closeModal();
H.route = null;

// Graph: Space on a node and a detail row selects; a refresh restores the
// selection once (in renderGraph) or drops it when the node is gone.
const N = (id, extra) => Object.assign({ id, ref: id, project: "alpha", title: "n" + id, status: "todo", priority: 2, nopen: 0, nprereq: 0 }, extra);
let graph = { nodes: [N(1), N(2, { nprereq: 1, nopen: 1 })], edges: [{ from: 1, to: 2 }] };
H.route = (path) => path === "/api/projects/alpha/graph" ? { status: 200, body: graph } : undefined;
view = "all"; selected.clear(); selected.add("alpha");
openGraphOverlay(); await H.settle();
const gnode = (id) => $("graphSvg").querySelector('.gnode[data-id="' + id + '"]');
H.fire(gnode(2), "keydown", { key: " " });
eq(graphSelectedId, 2, "Space selects a node");
H.fire($("graphDetail").querySelector(".gd-dep-row"), "keydown", { key: " " });
eq(graphSelectedId, 1, "Space on a detail row selects that task");
assert(gnode(1).classList.contains("gnode-sel"), "highlight follows");
let details = 0;
const realDetail = renderGraphDetail;
renderGraphDetail = (...a) => { details++; return realDetail(...a); };
graphMaybeRefresh({ kind: "task.status", project_id: 1, task_id: 1 });
H.runTimers(); await H.settle();
eq([graphSelectedId, details], [1, 1], "refresh restores the selection once");
graph = { nodes: [N(2)], edges: [] };
graphMaybeRefresh({ kind: "task.deleted", project_id: 1, task_id: 1 });
H.runTimers(); await H.settle();
renderGraphDetail = realDetail;
eq(graphSelectedId, null, "a removed node is deselected");
assert($("graphDetail").querySelector(".graph-detail-hint"), "detail panel cleared");
closeGraphOverlay();`)
}
