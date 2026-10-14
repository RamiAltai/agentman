package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Behavior tests for app.js (FIND-0387, -0388, -0389, -0390, -0434). Unlike the
// source-substring guards, these load the embedded app.js under node against a
// minimal DOM built from index.html, with fake timers and stubbed fetch and
// EventSource, and assert on what the code does. No npm: node's built-in vm is
// enough. The tests skip when node is not on PATH (CI runners ship it).

// cwfWebHarnessJS boots app.js and then runs a test body in the same vm context,
// so the body sees app.js's top-level bindings (state, onEvent, ...). Helpers live
// on one global, H, so an app.js function cannot shadow them.
// argv: app.js index.html body.js. Prints "ok" on success; exits 1 on a failed
// assertion, a thrown error or an unhandled rejection.
const cwfWebHarnessJS = `"use strict";
// Loads web/app.js into a vm context with a minimal DOM built from web/index.html,
// fake timers, and stubbed fetch/EventSource, then runs a test body in the same
// context (it sees app.js's top-level bindings). argv: app.js index.html test.js
const vm = require("vm");
const fs = require("fs");
const [appPath, htmlPath, testPath] = process.argv.slice(2);

const camel = (s) => s.replace(/-([a-z])/g, (_, c) => c.toUpperCase());

class FakeText {
  constructor(t) { this.nodeType = 3; this.data = String(t); this.parentNode = null; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
}

class FakeClassList {
  constructor(n) { this.n = n; }
  list() { return this.n.className.split(/\s+/).filter(Boolean); }
  add(...c) { const a = this.list(); for (const x of c) if (!a.includes(x)) a.push(x); this.n.className = a.join(" "); }
  remove(...c) { this.n.className = this.list().filter((x) => !c.includes(x)).join(" "); }
  contains(c) { return this.list().includes(c); }
  toggle(c, force) { const on = force === undefined ? !this.contains(c) : !!force; if (on) this.add(c); else this.remove(c); return on; }
}

const SEL = /([a-zA-Z][\w-]*)|\.([\w-]+)|\[([\w-]+)(?:="([^"]*)")?\]|:not\(\[([\w-]+)="([^"]*)"\]\)/y;
function matchOne(n, s) {
  for (let i = 0; i < s.length;) {
    SEL.lastIndex = i;
    const m = SEL.exec(s);
    if (!m) throw new Error("harness: unsupported selector " + JSON.stringify(s));
    i = SEL.lastIndex;
    if (m[1] && n.tagName !== m[1].toUpperCase()) return false;
    if (m[2] && !n.classList.contains(m[2])) return false;
    if (m[3] && (n.getAttribute(m[3]) === null || (m[4] !== undefined && n.getAttribute(m[3]) !== m[4]))) return false;
    if (m[5] && n.getAttribute(m[5]) === m[6]) return false;
  }
  return true;
}
const matches = (n, sel) => sel.split(",").some((s) => matchOne(n, s.trim()));

class FakeEl {
  constructor(tag) {
    this.nodeType = 1;
    this.tagName = String(tag).toUpperCase();
    this.childNodes = [];
    this.parentNode = null;
    this.attrs = {};
    this.dataset = {};
    this.listeners = {};
    this.className = "";
    this.value = "";
    this.disabled = false;
    this.style = { setProperty() {} };
    this.classList = new FakeClassList(this);
  }
  get id() { return this.attrs.id || ""; }
  get children() { return this.childNodes.filter((c) => c.nodeType === 1); }
  get childElementCount() { return this.children.length; }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; }
  get nextSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) + 1] || null : null; }
  get offsetParent() {
    for (let n = this; n; n = n.parentNode) if (n.classList.contains("hidden") || "hidden" in n.attrs) return null;
    return {};
  }
  get scrollHeight() { return 0; }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) { this.replaceChildren(String(v)); }
  adopt(k) {
    const c = k !== null && typeof k === "object" && k.nodeType ? k : new FakeText(k);
    if (c.parentNode) c.parentNode.removeChild(c);
    c.parentNode = this;
    return c;
  }
  append(...ks) { for (const k of ks) this.childNodes.push(this.adopt(k)); }
  appendChild(k) { this.append(k); return k; }
  prepend(...ks) { this.childNodes.unshift(...ks.map((k) => this.adopt(k))); }
  removeChild(c) { const i = this.childNodes.indexOf(c); if (i >= 0) this.childNodes.splice(i, 1); c.parentNode = null; return c; }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
  replaceChildren(...ks) { for (const c of this.childNodes) c.parentNode = null; this.childNodes = []; this.append(...ks); }
  replaceWith(k) { const p = this.parentNode; if (!p) return; const c = p.adopt(k); p.childNodes.splice(p.childNodes.indexOf(this), 1, c); this.parentNode = null; }
  after(k) { const p = this.parentNode; const c = p.adopt(k); p.childNodes.splice(p.childNodes.indexOf(this) + 1, 0, c); }
  setAttribute(k, v) {
    v = String(v);
    if (k === "class") { this.className = v; return; }
    this.attrs[k] = v;
    if (k.startsWith("data-")) this.dataset[camel(k.slice(5))] = v;
  }
  getAttribute(k) { return k === "class" ? (this.className || null) : (k in this.attrs ? this.attrs[k] : null); }
  hasAttribute(k) { return this.getAttribute(k) !== null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, fn, opt) {
    (this.listeners[t] = this.listeners[t] || []).push({ fn, capture: opt === true || !!(opt && opt.capture) });
  }
  removeEventListener(t, fn) { this.listeners[t] = (this.listeners[t] || []).filter((l) => l.fn !== fn); }
  // dispatchEvent follows the DOM order: capture listeners root-to-target, then
  // bubble listeners target-to-root (at the target, capture ones run first).
  // stopPropagation lets the current node finish; stopImmediatePropagation does not.
  dispatchEvent(ev) {
    ev.target = ev.target || this;
    const path = [];
    for (let n = this; n; n = n.parentNode) path.push(n);
    const steps = path.slice().reverse().map((n) => [n, true]).concat(path.map((n) => [n, false]));
    for (const [n, capture] of steps) {
      if (ev.stopped && ev.stoppedAt !== n) break;
      ev.currentTarget = n;
      for (const l of ((n.listeners && n.listeners[ev.type]) || []).slice()) {
        if (ev.immediateStopped) break;
        if (l.capture === capture) l.fn.call(n, ev);
      }
      if (!capture && !ev.immediateStopped && typeof n["on" + ev.type] === "function") n["on" + ev.type](ev);
    }
    return !ev.defaultPrevented;
  }
  focus() { doc.activeElement = this; }
  blur() {}
  getBoundingClientRect() { return { left: 0, top: 0, width: 1000, height: 640 }; }
  descendants() { const out = []; const walk = (n) => { for (const c of n.children) { out.push(c); walk(c); } }; walk(this); return out; }
  querySelectorAll(sel) { return this.descendants().filter((n) => matches(n, sel)); }
  querySelector(sel) { return this.descendants().find((n) => matches(n, sel)) || null; }
  closest(sel) { for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (matches(n, sel)) return n; return null; }
}

class FakeEvent {
  constructor(type, init) { this.type = type; Object.assign(this, init || {}); }
  preventDefault() { this.defaultPrevented = true; }
  stopPropagation() { this.stopped = true; this.stoppedAt = this.currentTarget; }
  stopImmediatePropagation() { this.stopPropagation(); this.immediateStopped = true; }
}

// parseHTML builds the <body> tree of index.html (no comments, no head).
function parseHTML(src) {
  const body = src.slice(src.indexOf("<body"), src.indexOf("</body>")).replace(/<!--[\s\S]*?-->/g, "");
  const VOID = new Set(["input", "meta", "link", "br", "img", "hr"]);
  const root = new FakeEl("html");
  const stack = [root];
  const tok = /<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)([^>]*)>|([^<]+)/g;
  let m;
  while ((m = tok.exec(body))) {
    const top = stack[stack.length - 1];
    if (m[1]) { stack.pop(); continue; }
    if (m[4]) { if (m[4].trim()) top.append(m[4].trim()); continue; }
    const n = new FakeEl(m[2]);
    const at = /([\w:-]+)(?:\s*=\s*"([^"]*)")?/g;
    let a;
    while ((a = at.exec(m[3]))) n.setAttribute(a[1], a[2] === undefined ? "" : a[2]);
    if (n.tagName === "INPUT") n.value = n.getAttribute("value") || "";
    top.append(n);
    if (!VOID.has(m[2].toLowerCase()) && !m[3].trim().endsWith("/")) stack.push(n);
  }
  return root;
}

const html = parseHTML(fs.readFileSync(htmlPath, "utf8"));
const doc = {
  documentElement: html,
  body: html.children[0],
  activeElement: null,
  listeners: {},
  getElementById(id) { return html.descendants().find((n) => n.attrs.id === id) || null; },
  createElement: (t) => new FakeEl(t),
  createElementNS: (ns, t) => new FakeEl(t),
  createTextNode: (t) => new FakeText(t),
  querySelector: (s) => html.querySelector(s),
  querySelectorAll: (s) => html.querySelectorAll(s),
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); },
  removeEventListener() {},
};
doc.activeElement = doc.body;

// Fake timers: nothing fires until the test calls runTimers().
const timers = new Map();
let timerSeq = 0;
function runTimers() {
  for (let n = 0; timers.size && n < 1000; n++) {
    const [id, fn] = timers.entries().next().value;
    timers.delete(id);
    fn();
  }
}

// fetch: route(path, opt) may return {status, body}; undefined falls back to defaults.
const fetchLog = [];
function defaultRoute(path) {
  if (path.startsWith("/api/events")) return { events: [], last_id: 0 };
  if (/^\/api\/projects\/[^/]+\/graph/.test(path)) return { nodes: [], edges: [] };
  if (path.startsWith("/api/projects")) return [{ id: 1, slug: "alpha", name: "Alpha", category: "work" }, { id: 2, slug: "beta", name: "Beta", category: "work" }];
  if (path.startsWith("/api/categories")) return [{ slug: "work", name: "Work" }];
  return [];
}
const streams = [];
class FakeEventSource {
  constructor(url) { this.url = url; this.readyState = 1; streams.push(this); }
  close() { this.readyState = 2; }
}
const store = new Map();

const sandbox = {
  console,
  URLSearchParams,
  document: doc,
  window: { innerWidth: 1280, matchMedia: () => ({ matches: false, addEventListener() {} }), addEventListener() {}, removeEventListener() {} },
  navigator: { onLine: true },
  location: { hash: "" },
  localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) },
  setTimeout: (fn, ms) => { timers.set(++timerSeq, fn); return timerSeq; },
  clearTimeout: (id) => { timers.delete(id); },
  EventSource: FakeEventSource,
  MouseEvent: FakeEvent,
  fetch: async (path, opt) => {
    fetchLog.push({ path, method: opt.method, body: opt.body ? JSON.parse(opt.body) : undefined });
    const r = (H.route && H.route(path, opt)) || { status: 200, body: defaultRoute(path) };
    return { ok: r.status < 400, status: r.status, text: async () => JSON.stringify(r.body) };
  },
  H: null, // test helpers, set below (one name, so app.js globals cannot shadow them)
};

const H = sandbox.H = {
  route: null,
  fetchLog,
  streams,
  timers,
  runTimers,
  settle: async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); },
  fire: (n, type, init) => { const ev = new FakeEvent(type, init); n.dispatchEvent(ev); return ev; },
  assert: (cond, msg) => { if (!cond) throw new Error("assert: " + msg); },
  eq: (got, want, msg) => {
    if (got === want) return;
    const show = (v) => v instanceof FakeEl ? v.tagName + "#" + v.id + "." + v.className : JSON.stringify(v);
    const g = show(got), w = show(want);
    if (g !== w) throw new Error("eq: " + msg + ": got " + g + ", want " + w);
  },
};

process.on("unhandledRejection", (e) => { console.error("unhandled rejection:", e); process.exitCode = 1; });
const ctx = vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(appPath, "utf8"), ctx, { filename: "app.js" });
const body = fs.readFileSync(testPath, "utf8");
vm.runInContext("(async () => { await H.settle(); H.runTimers(); await H.settle();\n" + body + "\n})()", ctx, { filename: "test.js", lineOffset: -1 })
  .then(() => { if (!process.exitCode) console.log("ok"); })
  .catch((e) => { console.error(e && e.stack || e); process.exitCode = 1; });
`

// cwfWebRunJS runs body in the harness and fails the test with node's output.
func cwfWebRunJS(t *testing.T, body string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; the app.js behavior harness needs it")
	}
	dir := t.TempDir()
	paths := map[string]string{}
	for name, src := range map[string]string{"harness.js": cwfWebHarnessJS, "body.js": body, "app.js": "web/app.js", "index.html": "web/index.html"} {
		data := []byte(src)
		if strings.HasPrefix(src, "web/") {
			if data, err = webFS.ReadFile(src); err != nil {
				t.Fatalf("ReadFile %s: %v", src, err)
			}
		}
		paths[name] = filepath.Join(dir, name)
		if err := os.WriteFile(paths[name], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, paths["harness.js"], paths["app.js"], paths["index.html"], paths["body.js"]).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("app.js harness failed (%v):\n%s", err, out)
	}
}

// FIND-0387: live-update routing (dedupe, feed, coalesced board reconcile,
// selection follow on rename/archive/delete, modal reconcile, overview path) and
// the stream's backoff/resync.
func TestCwfWebHarnessLiveEvents(t *testing.T) {
	cwfWebRunJS(t, `const { eq, assert } = H;
const feed = $("feedList");
// Stream lifecycle: an error backs off and reconnects; onopen after a drop resyncs from REST.
const es0 = H.streams[H.streams.length - 1];
es0.onopen();
eq($("status").textContent, "live", "status after open");
es0.onerror();
eq(es0.readyState, 2, "errored stream closed");
eq($("status").textContent, "reconnecting…", "status after first error");
eq(backoff, 2000, "backoff doubles");
H.runTimers();
const es1 = H.streams[H.streams.length - 1];
assert(es1 !== es0, "reconnect opens a new stream");
for (let i = 0; i < 3; i++) { es1.onerror(); }
eq($("status").textContent, "offline — retrying…", "status after repeated errors");
eq(backoff, 10000, "backoff capped");
H.fetchLog.length = 0;
es1.onopen();
eq(backoff, 1000, "open resets backoff");
await H.settle();
assert(H.fetchLog.some((f) => f.path.startsWith("/api/events")), "onopen after a drop reloads the feed");
eq(streamDropped, false, "drop flag cleared");

// Board view with project alpha selected.
view = "all";
selected.clear(); selected.add("alpha");
cursor = 10;
feed.replaceChildren();
// Replay overlap is dropped.
onEvent({ id: 10, kind: "task.created", task_id: 3, actor: "bob" });
eq(feed.children.length, 0, "event id <= cursor is ignored");
// A new event is prepended to the feed and advances the cursor.
onEvent({ id: 11, kind: "task.created", task_id: 3, actor: "bob" });
eq(cursor, 11, "cursor advances");
eq(feed.children[0].dataset.id, "11", "newest event first");
eq(feed.children[0].querySelector(".ev-text").textContent, "bob created #3", "feed text");
assert(flashIds.has(3), "task event flashes its card");
// Messages arrive as JSON through the stream.
H.streams[H.streams.length - 1].onmessage({ data: JSON.stringify({ id: 12, kind: "comment.added", task_id: 3, actor: "amy" }) });
eq(cursor, 12, "stream message routed to onEvent");
H.streams[H.streams.length - 1].onmessage({ data: "not json" });
eq(cursor, 12, "malformed message ignored");
// The board reconcile is coalesced: one load for a burst.
H.fetchLog.length = 0;
H.runTimers();
await H.settle();
eq(H.fetchLog.filter((f) => f.path.startsWith("/api/tasks") && f.path.includes("status=todo")).length, 1, "one coalesced board load");
assert(H.fetchLog.some((f) => f.path.startsWith("/api/tasks") && f.path.includes("project=alpha")), "board load scoped to the selection");

// task.deleted drops the card.
tasks.set(7, { id: 7, ref: 7, project: "alpha", status: "todo", title: "x", priority: 2 });
onEvent({ id: 13, kind: "task.deleted", task_id: 7 });
assert(!tasks.has(7), "task.deleted removes the task");
assert(!flashIds.has(7), "deleted task is not flashed");

// A rename of the selected project follows the selection and reopens the stream.
let n = H.streams.length;
onEvent({ id: 14, kind: "project.patched", data: { slug: ["alpha", "alpha2"] } });
assert(selected.has("alpha2") && !selected.has("alpha"), "rename follows selection");
eq(H.streams.length, n + 1, "rename reconnects");
assert(H.streams[n].url.includes("project=alpha2"), "stream scoped to the new slug: " + H.streams[n].url);
// Archiving / deleting the selected project drops it from the selection.
n = H.streams.length;
onEvent({ id: 15, kind: "project.archived", data: { slug: "alpha2" } });
assert(!selected.has("alpha2"), "archived project deselected");
eq(H.streams.length, n + 1, "archive reconnects");
selected.add("beta");
onEvent({ id: 16, kind: "project.deleted", data: { slug: "beta" } });
assert(!selected.has("beta"), "deleted project deselected");
// An archive of an unselected project does not reconnect.
n = H.streams.length;
onEvent({ id: 17, kind: "project.archived", data: { slug: "zzz" } });
eq(H.streams.length, n, "unrelated archive keeps the stream");

// Modal reconcile: a dep link to the open task refreshes it; deleting it closes the modal.
H.runTimers(); await H.settle();
openTaskId = 9;
$("modal").classList.remove("hidden");
onEvent({ id: 18, kind: "task.dep_added", task_id: 4, data: { depends_on: 9 } });
assert(coalesceTimers.modal, "dep link to the open task schedules a modal refresh");
onEvent({ id: 19, kind: "task.status", task_id: 5, data: { status: ["todo", "doing"] } });
H.runTimers(); await H.settle();
onEvent({ id: 20, kind: "task.status", task_id: 5, data: { status: ["todo", "doing"] } });
assert(!coalesceTimers.modal, "unrelated task does not refresh the modal");
onEvent({ id: 21, kind: "task.deleted", task_id: 9 });
eq(openTaskId, null, "deleting the open task closes the modal");
assert($("modal").classList.contains("hidden"), "modal hidden");

// Overview: task events refresh the cards (coalesced), not the board.
view = "overview";
selected.clear();
H.runTimers(); await H.settle();
onEvent({ id: 22, kind: "task.status", task_id: 5, data: { status: ["doing", "done"] } });
assert(coalesceTimers.overview, "overview refresh scheduled");
assert(!coalesceTimers.board, "no board reconcile on the overview");
H.fetchLog.length = 0;
H.runTimers(); await H.settle();
assert(H.fetchLog.some((f) => f.path === "/api/categories"), "overview reloads categories");`)
}

// FIND-0387: the edit-project form PATCHes only the changed fields, follows a
// slug change in the selection and reports a conflict; slugify.
func TestCwfWebHarnessEditProject(t *testing.T) {
	cwfWebRunJS(t, `const { eq, assert } = H;
eq(slugify("  My New Project! "), "my-new-project", "slugify");
eq(slugify("a/b..c_d"), "a-b..c_d", "slugify keeps . and _");
// Edit project: the PATCH carries only the changed fields.
view = "all"; selected.clear(); selected.add("alpha");
const p = { id: 1, slug: "alpha", name: "Alpha", vault_project_id: "", vault_path: "" };
const field = (label) => $("sheet").querySelector('[aria-label="' + label + '"]');
const save = () => $("sheet").querySelector(".save");
openEditProject(p);
H.fetchLog.length = 0;
H.fire(save(), "click"); await H.settle();
eq(H.fetchLog.filter((f) => f.method === "PATCH").length, 0, "unchanged form sends no PATCH");
openEditProject(p);
field("Project name").value = "Alpha Two ";
field("Slug").value = "alpha2";
H.fetchLog.length = 0;
H.fire(save(), "click"); await H.settle();
const patch = H.fetchLog.find((f) => f.method === "PATCH");
assert(patch, "PATCH sent");
eq(patch.path, "/api/projects/alpha", "PATCH path uses the old slug");
eq(patch.body, { slug: "alpha2", name: "Alpha Two" }, "PATCH body is the diff");
assert(selected.has("alpha2") && !selected.has("alpha"), "slug change follows the selection");
// A conflict is shown, not swallowed.
openEditProject(p);
field("Slug").value = "beta";
H.route = (path, opt) => opt.method === "PATCH" ? { status: 409, body: { error: "conflict" } } : undefined;
H.fire(save(), "click"); await H.settle();
H.route = null;
eq($("sheet").querySelector(".ferr").textContent, "slug “beta” is taken", "conflict message");`)
}

// FIND-0388: computeGraphLayout (Kahn layering, isolated-task grid, edge filter,
// canvas size), wrapTextLines and graphNeighbors.
func TestCwfWebHarnessGraphLayout(t *testing.T) {
	cwfWebRunJS(t, `const { eq, assert } = H;
const T = (id, title) => ({ id, title: title || "t" + id });
const pos = (L, id) => L.positions.get(id);
// wrapTextLines.
eq(wrapTextLines("", 22, 2), [""], "empty title");
eq(wrapTextLines("fix the login page now", 10, 2), ["fix the", "login page"], "wraps and caps at maxLines");
eq(wrapTextLines("supercalifragilistic word", 5, 3), ["supercalifragilistic", "word"], "long word kept on its own line");
eq(wrapTextLines("a b", 22, 2), ["a b"], "short title on one line");
// No edges: every task goes to the grid lane, no divider.
let L = computeGraphLayout([T(1, "b"), T(2, "a")], []);
eq(L.laneY, 0, "no divider without a DAG");
eq([pos(L, 2).x, pos(L, 2).y], [72, 72], "grid sorted by title, starts at the margin");
eq(pos(L, 1).x, 72 + 176 + 26, "second grid cell");
eq([L.width, L.height], [72 * 2 + 2 * 176 + 26, 440], "grid width, minimum height");
// Chain 1 -> 2 -> 3: one column per level, same row.
L = computeGraphLayout([T(3), T(1), T(2)], [{ from: 1, to: 2 }, { from: 2, to: 3 }]);
eq([pos(L, 1).x, pos(L, 2).x, pos(L, 3).x], [72, 72 + 248, 72 + 496], "chain levels");
eq(pos(L, 1).y, pos(L, 3).y, "chain on one row");
eq(L.width, 72 * 2 + 176 + 2 * 248, "chain width");
// Diamond 1 -> {2,3} -> 4: 4 is at level 2 (longest path), 2 and 3 share level 1.
L = computeGraphLayout([T(1), T(2), T(3), T(4)], [{ from: 1, to: 2 }, { from: 1, to: 3 }, { from: 2, to: 4 }, { from: 3, to: 4 }]);
eq([pos(L, 2).x, pos(L, 3).x, pos(L, 4).x], [320, 320, 568], "diamond levels");
eq(pos(L, 3).y - pos(L, 2).y, 88, "level siblings one row apart");
eq(pos(L, 1).y, pos(L, 4).y, "single-node levels centered alike");
// Longest path wins over a shortcut edge: 1->2->3 plus 1->3 puts 3 at level 2.
L = computeGraphLayout([T(1), T(2), T(3)], [{ from: 1, to: 2 }, { from: 2, to: 3 }, { from: 1, to: 3 }]);
eq(pos(L, 3).x, 72 + 496, "longest-path layering");
// Edges to unknown ids are dropped; their endpoint task becomes isolated.
L = computeGraphLayout([T(1), T(2)], [{ from: 1, to: 99 }, { from: 98, to: 2 }]);
eq(L.edges.length, 0, "dangling edges filtered");
eq(L.laneY, 0, "no DAG left");
// DAG plus more than 6 isolated tasks: a divider, 6 per row, rows below the DAG.
const nodes = [T(1, "a1"), T(2, "a2")];
for (let i = 0; i < 7; i++) nodes.push(T(10 + i, "iso" + i));
L = computeGraphLayout(nodes, [{ from: 1, to: 2 }]);
const dagH = 72 * 2 + 60;
eq(L.laneY, dagH + 8, "divider under the DAG");
eq([pos(L, 10).x, pos(L, 10).y], [72, dagH + 56], "first isolated task");
eq([pos(L, 15).x, pos(L, 15).y], [72 + 5 * 202, dagH + 56], "sixth on the first row");
eq([pos(L, 16).x, pos(L, 16).y], [72, dagH + 56 + 84], "seventh wraps");
eq(L.width, 72 * 2 + 6 * 176 + 5 * 26, "grid width");
eq(L.height, dagH + 56 + 2 * 60 + 24 + 72, "grid height");
eq(new Set([...L.positions.values()].map((p) => p.x + "," + p.y)).size, 9, "no overlapping nodes");
// graphNeighbors on the diamond.
const D = [{ from: 1, to: 2 }, { from: 1, to: 3 }, { from: 2, to: 4 }, { from: 3, to: 4 }];
let nb = graphNeighbors(2, D);
eq([[...nb.upstream], [...nb.downstream]], [[1], [4]], "neighbors of a side node");
eq([[...nb.upEdges], [...nb.downEdges]], [[D[0]], [D[2]]], "edges on the path");
nb = graphNeighbors(1, D);
eq([[...nb.upstream].sort(), [...nb.downstream].sort()], [[], [2, 3, 4]], "root reaches all descendants");
nb = graphNeighbors(4, D);
eq([...nb.upstream].sort(), [1, 2, 3], "sink reaches all ancestors");
eq(nb.upEdges.size, 4, "each ancestor edge once");
eq(graphNeighbors(7, D).downstream.size, 0, "unknown node has no neighbors");`)
}

// FIND-0389: feed pagination and the raised cap, and both feed renderers against
// every event kind store.go emits, so a kind added in Go without a JS case fails.
func TestCwfWebHarnessFeed(t *testing.T) {
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
const feed = $("feedList");
const ev = (id) => ({ id, kind: "task.created", task_id: id, actor: "a" });
const evs = (hi, n) => Array.from({ length: n }, (_, i) => ev(hi - i));
// Initial feed: 50 newest events, oldest id 51, "Load older" offered.
view = "all"; selected.clear(); selected.add("alpha");
H.route = (path) => path.startsWith("/api/events?tail") ? { status: 200, body: { events: evs(100, 50), last_id: 100 } } : undefined;
await loadFeed();
eq([feed.children.length, feedOldest, cursor, feedCap], [50, 51, 100, 200], "loaded feed state");
assert(loadOlderBtn && loadOlderBtn.parentNode === $("feed"), "load-older button outside the list");
// A full page appends below, moves the cursor to the page's minimum, and raises the cap.
let page = evs(50, 50);
H.route = (path) => path.startsWith("/api/events?before") ? { status: 200, body: { events: page } } : undefined;
H.fetchLog.length = 0;
await loadOlderActivity();
eq(H.fetchLog[0].path, "/api/events?before=51&limit=50&project=alpha", "page request");
eq([feed.children.length, feedOldest, feedCap], [100, 1, 250], "after a full page");
eq(feed.lastChild.dataset.id, "1", "older rows appended at the bottom");
assert(loadOlderBtn && !loadOlderBtn.disabled, "more pages may exist");
// A short page ends the feed with a marker.
page = [];
await loadOlderActivity();
eq(loadOlderBtn, null, "no button after the last page");
assert($("feed").querySelector(".feed-start-marker"), "start-of-activity marker shown");
// Live events do not evict paginated rows until the raised cap is reached.
cursor = 100;
for (let id = 101; id <= 250; id++) onEvent(ev(id));
eq(feed.children.length, 250, "cap raised by the pages");
eq(feed.lastChild.dataset.id, "1", "paginated history kept");
onEvent(ev(251));
eq(feed.children.length, 250, "list stays bounded");
eq(feedOldest, 2, "cursor follows the oldest row still shown");
assert(loadOlderBtn && !$("feed").querySelector(".feed-start-marker"), "trimmed rows can be loaded again");
// A page that resolves after a feed reload is dropped.
H.route = (path) => path.startsWith("/api/events?before") ? { status: 200, body: { events: evs(1, 1) } } : undefined;
const pending = loadOlderActivity();
feedSeq++;
await pending;
eq(feed.children.length, 250, "stale page ignored");
H.route = null;

// Both feed renderers name every event kind the server emits (no generic fallback),
// carry the task ref for task-scoped kinds, and agree on the actor.
for (const kind of KINDS) {
  const e = { id: 1, kind, task_id: 42, actor: "amy", project_id: 1,
    data: { slug: "alpha", status: ["todo", "done"], assignee: ["bob"], depends_on: 7, label: "bug", meta: { k: "v" } } };
  const a = evText(e).textContent, b = describeText(e);
  for (const [name, s] of [["evText", a], ["describeText", b]]) {
    assert(s.startsWith("amy "), name + "(" + kind + ") actor: " + s);
    assert(!s.includes(kind), name + " has no case for " + kind + ": " + s);
    if (/^(task|comment)\./.test(kind)) assert(s.includes("#42"), name + "(" + kind + ") lacks the task ref: " + s);
    if (/^(project|category)\./.test(kind)) assert(s.endsWith(" alpha"), name + "(" + kind + ") lacks the slug: " + s);
  }
}
eq(evKind({ kind: "task.status", data: { status: ["doing", "done"] } }), "done", "evKind done");
eq(evKind({ kind: "task.status", data: { status: ["todo", "blocked"] } }), "blocked", "evKind blocked");
eq(evKind({ kind: "task.status", data: { status: ["todo", "doing"] } }), "status", "evKind status");
eq(evKind({ kind: "task.reclaimed" }), "claimed", "evKind reclaimed");
eq(evKind({ kind: "comment.added" }), "comment", "evKind comment");
eq(evKind({ kind: "project.created" }), "other", "evKind other");
eq(evText({ kind: "task.status", task_id: 3, actor: "x", data: { status: ["todo", "done"] } }).textContent, "x moved #3 → done", "status text");
eq(describeText({ kind: "task.reclaimed", task_id: 3, actor: "x", data: { assignee: ["old", "x"] } }), "x reclaimed #3 from old", "reclaim text");`)
}

// FIND-0390: the graph overlay end to end: render, focus highlighting, detail
// panel navigation, pan/zoom, live refresh filtering and coalescing, focus trap,
// Escape and the empty state.
func TestCwfWebHarnessGraphOverlay(t *testing.T) {
	cwfWebRunJS(t, `const { eq, assert } = H;
const N = (id, title, status, extra) => Object.assign({ id, ref: id, project: "alpha", title, status, priority: 2, nopen: 0, nprereq: 0 }, extra);
let graph = { nodes: [N(1, "root", "done"), N(2, "left", "todo", { nprereq: 1 }), N(3, "right", "todo", { nprereq: 1 }), N(4, "sink", "todo", { nopen: 2, nprereq: 2 })],
  edges: [{ from: 1, to: 2 }, { from: 1, to: 3 }, { from: 2, to: 4 }, { from: 3, to: 4 }] };
H.route = (path) => path === "/api/projects/alpha/graph" ? { status: 200, body: graph } : undefined;
view = "all"; selected.clear(); selected.add("alpha");
const opener = $("graphBtn");
opener.focus();
openGraphOverlay();
await H.settle();
const svgEl = $("graphSvg"), ov = $("graphOverlay");
assert(graphOpen && !ov.classList.contains("hidden"), "overlay open");
eq(document.activeElement, $("graphClose"), "focus moves into the overlay");
eq($("graphTitle").textContent, "Dependencies — alpha", "title");
const gnode = (id) => svgEl.querySelector('.gnode[data-id="' + id + '"]');
const gedge = (f, t) => svgEl.querySelector('.gedge[data-from="' + f + '"][data-to="' + t + '"]');
eq(svgEl.querySelectorAll(".gnode").length, 4, "one node per task");
eq(svgEl.querySelectorAll(".gedge").length, 4, "one path per edge");
eq(gedge(1, 2).getAttribute("marker-end"), "url(#gArrowCleared)", "edge from a done task is cleared");
eq(gedge(2, 4).getAttribute("marker-end"), "url(#gArrowBlocking)", "edge from an open task blocks");
eq(svgEl.getAttribute("viewBox"), viewBoxStr(graphInitialView), "first render fits the view");
assert($("graphLegend").children.length > 0, "legend built");
// Selecting a node highlights its ancestors and descendants and dims the rest.
H.fire(gnode(2), "click");
eq(graphSelectedId, 2, "selected");
assert(gnode(2).classList.contains("gnode-sel"), "selected class");
assert(gnode(1).classList.contains("gnode-up") && gnode(4).classList.contains("gnode-down"), "path highlighted");
assert(gnode(3).classList.contains("gnode-dim"), "off-path node dimmed");
assert(gedge(3, 4).classList.contains("gedge-dim") && gedge(2, 4).classList.contains("gedge-down"), "edge classes");
eq($("graphDetail").querySelector(".gd-title").textContent, "left", "detail panel shows the node");
eq($("graphDetail").querySelectorAll(".gd-section-label").map((x) => x.textContent), ["Prerequisites (1)", "Unblocks (1)"], "detail sections");
// The sink's ancestors include both branches and all four edges.
H.fire(gnode(4), "keydown", { key: "Enter" });
eq(graphSelectedId, 4, "Enter selects");
assert(svgEl.querySelectorAll(".gedge").every((p) => p.classList.contains("gedge-up")), "every ancestor edge highlighted");
eq($("graphDetail").querySelector(".gd-blocked").textContent, "🔒 Blocked — 2 open prereqs", "blocked line");
// Navigating from the detail panel moves the selection.
H.fire($("graphDetail").querySelector(".gd-dep-row"), "click");
eq(graphSelectedId, 2, "detail navigation selects the prerequisite");
assert(gnode(2).classList.contains("gnode-sel") && !gnode(4).classList.contains("gnode-sel"), "focus rebuilt");
// Pan: a drag on the pan surface moves the view and its trailing click keeps the selection.
const drag = (target, dx, dy) => {
  H.fire(target, "mousedown", { clientX: 100, clientY: 100 });
  H.fire(target, "mousemove", { clientX: 100 + dx, clientY: 100 + dy });
  H.fire(target, "mouseup", {});
  H.fire(target, "click", {});
};
const v0 = { ...graphViewState };
drag(svgEl.querySelector(".graph-pan-surface"), 100, 50);
const u = 1 / Math.min(1000 / v0.width, 640 / v0.height);
eq([graphViewState.x, graphViewState.y], [v0.x - 100 * u, v0.y - 50 * u], "pan moves the view by the uniform scale");
eq(graphSelectedId, 2, "pan keeps the selection");
// A plain click on the canvas clears it.
H.fire(svgEl, "click", {});
eq(graphSelectedId, null, "background click deselects");
assert(!gnode(2).classList.contains("gnode-sel") && !gnode(3).classList.contains("gnode-dim"), "focus cleared");
// Zoom keeps the aspect ratio and is clamped.
const w0 = graphViewState.width, r0 = graphViewState.width / graphViewState.height;
H.fire(svgEl, "wheel", { deltaY: -1, clientX: 500, clientY: 320 });
eq(Math.round(graphViewState.width * 1000), Math.round(w0 * 900), "zoom in");
eq(Math.round(graphViewState.width / graphViewState.height * 1e6), Math.round(r0 * 1e6), "aspect kept");
for (let i = 0; i < 100; i++) H.fire(svgEl, "wheel", { deltaY: 1, clientX: 500, clientY: 320 });
eq(Math.round(graphViewState.width), graphInitialView.width * 4, "zoom out clamped");
$("graphReset").onclick();
eq(graphViewState, graphInitialView, "reset view");
// Live refresh: other projects' events are ignored; this project's task.claimed refetches once, keeping view and selection.
H.fire(gnode(3), "click");
H.fire(svgEl, "wheel", { deltaY: -1, clientX: 500, clientY: 320 });
const vz = { ...graphViewState };
H.fetchLog.length = 0;
graphMaybeRefresh({ kind: "task.claimed", project_id: 2, task_id: 9 });
eq(graphRefreshTimer, null, "other project ignored");
graphMaybeRefresh({ kind: "task.labeled", project_id: 1, task_id: 3 });
eq(graphRefreshTimer, null, "label kinds ignored");
graph = { nodes: graph.nodes.concat([N(5, "new", "todo")]), edges: graph.edges };
graphMaybeRefresh({ kind: "task.claimed", project_id: 1, task_id: 3 });
assert(graphRefreshTimer, "task.claimed schedules a refresh");
graphMaybeRefresh({ kind: "task.status", project_id: 1, task_id: 3 });
H.runTimers(); await H.settle();
eq(H.fetchLog.filter((f) => f.path.endsWith("/graph")).length, 1, "burst coalesced into one refetch");
eq(svgEl.querySelectorAll(".gnode").length, 5, "refetched graph rendered");
eq(graphViewState, vz, "refresh keeps pan/zoom");
eq(graphSelectedId, 3, "refresh keeps the selection");
assert(gnode(3).classList.contains("gnode-sel"), "selection re-highlighted");
// Focus trap: Tab wraps inside the overlay.
const focusables = ov.querySelectorAll("button,select,[tabindex]").filter((x) => x.getAttribute("tabindex") !== "-1");
const lastF = focusables[focusables.length - 1];
lastF.focus();
let e = H.fire(ov, "keydown", { key: "Tab", shiftKey: false });
eq(document.activeElement, focusables[0], "Tab from the last control wraps to the first");
assert(e.defaultPrevented, "Tab default prevented");
e = H.fire(ov, "keydown", { key: "Tab", shiftKey: true });
eq(document.activeElement, lastF, "Shift+Tab from the first wraps to the last");
// Escape closes and restores focus; a late refresh does nothing.
graphMaybeRefresh({ kind: "task.status", project_id: 1, task_id: 3 });
H.fire(ov, "keydown", { key: "Escape" });
assert(!graphOpen && ov.classList.contains("hidden"), "Escape closes");
eq(graphRefreshTimer, null, "pending refresh cancelled");
eq(document.activeElement, opener, "focus restored");
graphMaybeRefresh({ kind: "task.status", project_id: 1, task_id: 3 });
eq(graphRefreshTimer, null, "closed overlay ignores events");
// An empty project shows the empty state.
graph = { nodes: [], edges: [] };
openGraphOverlay(); await H.settle();
eq(svgEl.querySelector(".graph-empty-text").textContent, "No tasks in this project yet.", "empty state");
closeGraphOverlay();`)
}

// FIND-0434: el() drops string on* props (they would be inline script) and keeps
// string children as text.
func TestCwfWebHarnessElDropsStringHandlers(t *testing.T) {
	cwfWebRunJS(t, `const { eq, assert } = H;
const a = el("a", { onclick: "alert(1)", onmouseover: "steal()", title: "t", href: "#x" }, "<img src=x onerror=alert(1)>");
eq(a.getAttribute("onclick"), null, "string onclick dropped");
eq(a.getAttribute("onmouseover"), null, "string onmouseover dropped");
eq([a.getAttribute("title"), a.getAttribute("href")], ["t", "#x"], "other attributes kept");
eq(a.childNodes.length, 1, "one child");
eq(a.childNodes[0].nodeType, 3, "string child is a text node");
eq(a.textContent, "<img src=x onerror=alert(1)>", "markup stays text");
let hits = 0;
const b = el("button", { onclick: () => hits++ });
H.fire(b, "click");
eq(hits, 1, "function handlers are wired");
eq(b.getAttribute("onclick"), null, "function handler is not an attribute");
// Agent-supplied fields reach the feed as text.
const row = feedItem({ id: 1, kind: "task.created", task_id: 2, actor: "<script>x</script>" });
eq(row.querySelector("b").childNodes[0].nodeType, 3, "actor rendered as text");`)
}
