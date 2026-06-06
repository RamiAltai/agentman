"use strict";

const COLS = [["todo", "Todo"], ["doing", "In Progress"], ["blocked", "Blocked"], ["done", "Done"]];
const ST = { todo: "var(--st-todo)", doing: "var(--st-doing)", blocked: "var(--st-blocked)", done: "var(--st-done)" };
const PRIO = ["#f4756b", "#f8b738", "#8b93a4", "#6e7681"]; // 0 urgent .. 3 low
const PRIO_LABEL = ["Urgent", "High", "", ""];
const PRIO_OPTS = ["0 — Urgent", "1 — High", "2 — Normal", "3 — Low"];

const FEED_W_KEY = "am.feedW", FEED_COLLAPSED_KEY = "am.feedCollapsed";

let projects = [];
let selected = new Set();    // selected project slugs; empty = "All"
let tasks = new Map();       // id -> task (terse)
let cursor = 0;              // highest event id seen (SSE since=)
let es = null, backoff = 1000, refreshTimer = null, openTaskId = null, lastFocus = null, dragId = null;

const $ = (id) => document.getElementById(id);

// Storage may be unavailable (private mode, sandboxed iframe) — never let it break the app.
function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } }

// el builds DOM safely: string children become text nodes (never innerHTML),
// so agent-supplied titles/comments can't inject markup.
function el(tag, props, ...kids) {
  const n = document.createElement(tag);
  props = props || {};
  for (const k in props) {
    const v = props[k];
    if (v == null) continue;
    if (k === "class") n.className = v;
    else if (k === "value") n.value = v;
    else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2).toLowerCase(), v);
    else n.setAttribute(k, v);
  }
  for (const kid of kids) {
    if (kid == null) continue;
    n.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return n;
}

async function api(method, path, body) {
  const opt = { method, headers: { "X-Agent": "human" } };
  if (body) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  const txt = await r.text();
  const data = txt ? JSON.parse(txt) : null;
  if (!r.ok) throw new Error((data && data.error) || ("HTTP " + r.status));
  return data;
}

function qstr(extra) {
  const p = new URLSearchParams(extra || {});
  if (selected.size === 1) p.set("project", [...selected][0]);
  const s = p.toString();
  return s ? "?" + s : "";
}

function setStatus(text, cls) {
  const e = $("status");
  e.className = "status " + (cls || "");
  e.querySelector(".status-text").textContent = text;
}

// ---------- load / render ----------

async function loadProjects() {
  projects = await api("GET", "/api/projects");
  renderTabs();
}

async function loadBoard() {
  const list = await api("GET", "/api/tasks" + qstr({ limit: 500 }));
  tasks = new Map(list.map((t) => [t.id, t]));
  renderBoard();
}

async function loadFeed() {
  const res = await api("GET", "/api/events" + qstr({ tail: 50 }));
  const list = $("feedList");
  list.replaceChildren();
  if (!res.events.length) list.append(el("li", { class: "feed-empty" }, "No activity yet"));
  else for (const ev of res.events) list.append(feedItem(ev)); // newest-first
  cursor = Math.max(cursor, res.last_id || 0);
}

function renderTabs() {
  const nav = $("tabs");
  const allOpen = projects.reduce((n, p) => n + openCount(p.counts), 0);
  nav.replaceChildren(tab("", "All", allOpen));
  for (const p of projects) nav.append(tab(p.slug, p.name, openCount(p.counts)));
  nav.append(el("button", { class: "tab add", onclick: openNewProject, title: "New project", "aria-label": "New project" }, "＋"));
}

function openCount(c) { c = c || {}; return (c.todo || 0) + (c.doing || 0) + (c.blocked || 0); }

function tab(slug, label, open) {
  const active = slug === "" ? selected.size === 0 : selected.has(slug);
  const b = el("button", {
    class: "tab" + (active ? " active" : ""),
    "aria-pressed": String(active),
    onclick: () => toggleProject(slug),
  }, label);
  if (open) b.append(el("span", { class: "badge" }, String(open)));
  return b;
}

function renderBoard() {
  const board = $("board");
  board.replaceChildren();
  const visible = selected.size > 0
    ? [...tasks.values()].filter(t => selected.has(t.project))
    : [...tasks.values()];
  if (visible.length === 0) { board.append(boardEmpty()); return; }

  const by = { todo: [], doing: [], blocked: [], done: [] };
  for (const t of visible) (by[t.status] || (by[t.status] = [])).push(t);

  for (const [key, label] of COLS) {
    let list = (by[key] || []).sort((a, b) => a.priority - b.priority || b.id - a.id);
    const total = list.length;
    let truncated = false;
    if (key === "done" && list.length > 50) { list = list.slice(0, 50); truncated = true; }

    const col = el("div", {
      class: "col", "data-status": key,
      ondragover: (e) => {
        if (dragId == null) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";
        e.currentTarget.classList.add("drag-over");
      },
      ondragleave: (e) => { if (!e.currentTarget.contains(e.relatedTarget)) e.currentTarget.classList.remove("drag-over"); },
      ondrop: (e) => {
        e.preventDefault();
        e.currentTarget.classList.remove("drag-over");
        const id = Number(e.dataTransfer.getData("text/plain")) || dragId;
        if (id) moveTask(id, key);
      },
    });
    col.append(el("div", { class: "colhead" },
      el("span", { class: "swatch", style: "background:" + ST[key] }),
      label,
      el("span", { class: "count" }, String(total))));
    const cards = el("div", { class: "cards" });
    if (!list.length) cards.append(el("div", { class: "empty-col" }, key === "done" ? "Nothing done yet" : "No tasks"));
    for (const t of list) cards.append(card(t));
    if (truncated) cards.append(el("div", { class: "more-note" }, "+" + (total - 50) + " more"));
    col.append(cards);
    board.append(col);
  }
}

function boardEmpty() {
  const w = el("div", { class: "board-empty" });
  if (!projects.length) {
    w.append(el("h3", {}, "No projects yet"),
      el("p", {}, "Create a project to start tracking work."),
      el("button", { class: "save", onclick: openNewProject }, "Create a project"));
  } else {
    w.append(el("h3", {}, selected.size > 0 ? "No tasks in selected projects" : "No tasks yet"),
      el("p", {}, "Add a task and your agents can pick it up."),
      el("button", { class: "save", onclick: openNew }, "+ New task"));
  }
  return w;
}

function card(t) {
  const c = el("div", {
    class: "card", role: "button", tabindex: "0", draggable: "true", "data-id": String(t.id),
    style: "--prio:" + (PRIO[t.priority] || PRIO[3]),
    "aria-label": "#" + t.id + " " + t.title + " — " + t.status + ". Press Enter to open, or [ and ] to change status.",
    onclick: () => openTask(t.id),
    onkeydown: (e) => onCardKey(e, t),
    ondragstart: (e) => {
      dragId = t.id;
      e.dataTransfer.setData("text/plain", String(t.id));
      e.dataTransfer.effectAllowed = "move";
      e.currentTarget.classList.add("dragging");
    },
    ondragend: (e) => {
      dragId = null;
      e.currentTarget.classList.remove("dragging");
      document.querySelectorAll(".col.drag-over").forEach((x) => x.classList.remove("drag-over"));
    },
  });
  const crow = el("div", { class: "crow" }, el("span", { class: "cid" }, "#" + t.id));
  if (PRIO_LABEL[t.priority]) crow.append(el("span", { class: "chip-prio" }, PRIO_LABEL[t.priority]));
  c.append(crow);
  c.append(el("div", { class: "ctitle" }, t.title));

  const foot = el("div", { class: "cfoot" });
  const who = el("span", { class: "who" + (t.assignee ? "" : " unassigned") });
  if (t.assignee) who.append(el("span", { class: "avatar" }, initials(t.assignee)), el("span", { class: "name" }, t.assignee));
  else who.append("Unassigned");
  foot.append(who);
  if (selected.size !== 1) foot.append(el("span", { class: "ptag" }, t.project));
  if (t.nc > 0) foot.append(el("span", { class: "cc" }, "💬 " + t.nc));
  c.append(foot);
  return c;
}

function initials(name) { const m = (name || "").replace(/[^a-zA-Z0-9]/g, ""); return (m.slice(0, 2) || "?").toUpperCase(); }

function onCardKey(e, t) {
  if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openTask(t.id); return; }
  if (e.key === "[" || e.key === "]") {           // keyboard equivalent of drag between columns
    e.preventDefault();
    const order = COLS.map((c) => c[0]);
    let i = order.indexOf(t.status) + (e.key === "]" ? 1 : -1);
    i = Math.max(0, Math.min(order.length - 1, i));
    moveTask(t.id, order[i]);
  }
}

// moveTask optimistically moves a card to a new status, then persists it.
// The SSE echo re-reconciles; on failure we revert.
function moveTask(id, status) {
  const t = tasks.get(id);
  if (!t || t.status === status) return;
  const prev = t.status;
  t.status = status;
  renderBoard();
  const moved = document.querySelector('.card[data-id="' + id + '"]');
  if (moved) moved.focus();
  api("PATCH", "/api/tasks/" + id, { status }).catch((e) => {
    alert(e.message);
    const tt = tasks.get(id);
    if (tt) { tt.status = prev; renderBoard(); }
  });
}

// ---------- project switching ----------

async function toggleProject(slug) {
  if (slug === "") {
    selected.clear();            // "All" clears selection
  } else if (selected.has(slug)) {
    selected.delete(slug);       // clicking an active tab deselects it
  } else {
    selected.add(slug);          // clicking an inactive tab adds it
  }
  renderTabs();
  try { await loadBoard(); await loadFeed(); } catch (e) { setStatus("error", "warn"); }
  connect();
}

function slugify(s) {
  return s.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
}

// ---------- modal infra ----------

function openModal() {
  lastFocus = document.activeElement;
  $("modal").classList.remove("hidden");
}

function closeModal() {
  $("modal").classList.add("hidden");
  openTaskId = null;
  if (lastFocus && lastFocus.focus) try { lastFocus.focus(); } catch (e) { /* ignore */ }
}

function trapFocus(e) {
  if (e.key !== "Tab" || $("modal").classList.contains("hidden")) return;
  const f = $("sheet").querySelectorAll('a[href],button,select,textarea,input,[tabindex]:not([tabindex="-1"])');
  const list = Array.from(f).filter((x) => !x.disabled && x.offsetParent !== null);
  if (!list.length) return;
  const first = list[0], last = list[list.length - 1];
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
}

function autoGrow(ta) { ta.style.height = "auto"; ta.style.height = ta.scrollHeight + "px"; }
function growTitle() { const ta = $("sheet").querySelector(".mtitle"); if (ta) autoGrow(ta); }

// ---------- detail modal ----------

async function openTask(id) {
  openTaskId = id;
  try {
    const t = await api("GET", "/api/tasks/" + id);
    renderModal(t);
    openModal();
    growTitle();
    $("sheet").focus();
  } catch (e) { alert(e.message); openTaskId = null; }
}

async function refreshModal() {
  if (!openTaskId) return;
  try { const t = await api("GET", "/api/tasks/" + openTaskId); if (openTaskId === t.id) { renderModal(t); growTitle(); } } catch (e) { /* ignore */ }
}

function patch(id, body) { return api("PATCH", "/api/tasks/" + id, body).catch((e) => alert(e.message)); }

function label(text, node) { return el("label", { class: "lbl" }, el("span", {}, text), node); }

function prioSelect(value, onchange) {
  const sel = el("select", { onchange, "aria-label": "Priority" });
  PRIO_OPTS.forEach((l, i) => {
    const o = el("option", { value: String(i) }, l);
    if (i === value) o.setAttribute("selected", "selected");
    sel.append(o);
  });
  return sel;
}

function renderModal(t) {
  $("modal").setAttribute("aria-label", "Task #" + t.id + ": " + t.title);
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal, "aria-label": "Close" }, "✕"));
  s.append(el("div", { class: "mhead" }, "#" + t.id + " · " + t.project + "-" + t.ref + " · created " + fmtDate(t.created_at)));

  const title = el("textarea", { class: "mtitle", rows: "1", "aria-label": "Task title", spellcheck: "false" });
  title.value = t.title;
  title.oninput = () => autoGrow(title);
  title.onchange = () => { if (title.value.trim()) patch(t.id, { title: title.value.trim() }); };
  s.append(title);

  const status = el("select", { "aria-label": "Status", onchange: (e) => patch(t.id, { status: e.target.value }) });
  for (const [k, l] of COLS) {
    const o = el("option", { value: k }, l);
    if (k === t.status) o.setAttribute("selected", "selected");
    status.append(o);
  }
  const asg = el("input", { class: "field", value: t.assignee || "", placeholder: "unassigned", "aria-label": "Assignee" });
  asg.onchange = () => patch(t.id, { assignee: asg.value.trim() });
  const pri = prioSelect(t.priority, (e) => patch(t.id, { priority: Number(e.target.value) }));
  s.append(el("div", { class: "mrow" }, label("Status", status), label("Assignee", asg), label("Priority", pri)));

  const body = el("textarea", { class: "mbody", placeholder: "Add a description…", "aria-label": "Description" });
  body.value = t.body || "";
  body.onchange = () => patch(t.id, { body: body.value });
  s.append(el("label", { class: "lbl" }, el("span", {}, "Description"), body));

  s.append(el("h3", {}, "Comments" + (t.comments && t.comments.length ? " (" + t.comments.length + ")" : "")));
  const cl = el("div", { class: "comments" });
  if (!t.comments || !t.comments.length) cl.append(el("div", { class: "feed-empty" }, "No comments yet"));
  for (const cm of t.comments || []) {
    cl.append(el("div", { class: "cm" },
      el("div", { class: "cm-head" }, el("b", {}, cm.author), el("span", { class: "t" }, fmtTime(cm.created_at))),
      el("div", { class: "cbody" }, cm.body)));
  }
  s.append(cl);

  const cbox = el("input", { class: "field cbox", placeholder: "Add a comment…", "aria-label": "Add a comment" });
  const submit = () => {
    const v = cbox.value.trim();
    if (!v) return;
    cbox.value = "";
    api("POST", "/api/tasks/" + t.id + "/comments", { body: v }).catch((err) => alert(err.message));
  };
  cbox.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); submit(); } };
  s.append(el("div", { class: "cm-row" }, cbox, el("button", { class: "btn-primary", onclick: submit }, "Send")));

  s.append(el("h3", {}, "History"));
  const hl = el("ul", { class: "hist" });
  for (const ev of t.events || []) hl.append(el("li", {}, el("span", { class: "t" }, fmtTime(ev.created_at)), el("span", {}, describeText(ev))));
  s.append(hl);
}

function openNew() {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal, "aria-label": "Close" }, "✕"));
  s.append(el("div", { class: "mhead" }, "New task"));
  const title = el("input", { class: "mtitle field", placeholder: "Task title", "aria-label": "Task title" });
  const psel = el("select", { "aria-label": "Project" });
  const defaultProj = selected.size === 1 ? [...selected][0] : (projects[0] ? projects[0].slug : "");
  for (const p of projects) {
    const o = el("option", { value: p.slug }, p.name);
    if (p.slug === defaultProj) o.setAttribute("selected", "selected");
    psel.append(o);
  }
  const pri = prioSelect(2, null);
  const body = el("textarea", { class: "mbody", placeholder: "Description (optional)", "aria-label": "Description" });
  const err = el("div", { class: "ferr" });
  const save = el("button", {
    class: "save", onclick: async () => {
      if (!title.value.trim()) { err.textContent = "enter a title"; title.focus(); return; }
      if (!psel.value) { err.textContent = "create a project first (＋ in the tab bar)"; return; }
      try {
        const t = await api("POST", "/api/tasks", { project: psel.value, title: title.value.trim(), body: body.value, priority: Number(pri.value) });
        closeModal();
        if (selected.size === 1 && t.project !== [...selected][0]) await toggleProject(t.project);
        else { await loadBoard().catch(() => {}); loadProjects().catch(() => {}); }
      } catch (e) { err.textContent = e.message; }
    }
  }, "Create task");
  s.append(title, el("div", { class: "mrow" }, label("Project", psel), label("Priority", pri)),
    el("label", { class: "lbl" }, el("span", {}, "Description"), body), err, save);
  openModal();
  title.focus();
}

function openNewProject() {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal, "aria-label": "Close" }, "✕"));
  s.append(el("div", { class: "mhead" }, "New project"));
  const name = el("input", { class: "mtitle field", placeholder: "Project name (e.g. Web App)", "aria-label": "Project name" });
  const slug = el("input", { class: "field", placeholder: "slug (auto)", "aria-label": "Slug" });
  const err = el("div", { class: "ferr" });
  let slugEdited = false;
  slug.oninput = () => { slugEdited = true; };
  name.oninput = () => { if (!slugEdited) slug.value = slugify(name.value); };
  const save = el("button", {
    class: "save", onclick: async () => {
      const sv = slugify(slug.value || name.value);
      if (!sv) { err.textContent = "enter a name"; name.focus(); return; }
      try {
        const p = await api("POST", "/api/projects", { slug: sv, name: name.value.trim() || sv });
        await loadProjects();
        closeModal();
        toggleProject(p.slug);
      } catch (e) {
        err.textContent = e.message === "conflict" ? "a project with slug “" + sv + "” already exists" : e.message;
      }
    }
  }, "Create project");
  s.append(name, el("div", { class: "mrow" }, label("Slug", slug)), err, save);
  openModal();
  name.focus();
}

// ---------- live stream ----------

function connect() {
  if (es) es.close();
  es = new EventSource("/api/stream" + qstr({ since: cursor }));
  es.onopen = () => { backoff = 1000; setStatus("live", "ok"); };
  es.onmessage = (m) => { let ev; try { ev = JSON.parse(m.data); } catch (e) { return; } onEvent(ev); };
  es.onerror = () => {
    es.close();
    setStatus("reconnecting…", "warn");
    setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 10000);
  };
}

function onEvent(ev) {
  if (ev.id <= cursor) return; // dedupe replay overlap
  cursor = ev.id;
  const fe = $("feedList").querySelector(".feed-empty");
  if (fe) fe.remove();
  $("feedList").prepend(feedItem(ev));
  trimFeed();
  if (ev.kind === "project.created" || ev.kind === "project.unarchived") loadProjects().catch(() => {});
  if (ev.kind === "project.archived") {
    const archivedSlug = (ev.data || {}).slug;
    if (selected.has(archivedSlug)) {
      selected.delete(archivedSlug);
      renderTabs();
      loadBoard().catch(() => {});
      loadFeed().catch(() => {});
      connect();
    }
    loadProjects().catch(() => {});
  }
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => loadBoard().catch(() => {}), 250); // debounced reconcile
  if (openTaskId && ev.task_id === openTaskId) refreshModal();
}

function feedItem(ev) {
  return el("li", { class: "ev k-" + evKind(ev) },
    el("span", { class: "ev-dot" }),
    evText(ev),
    el("span", { class: "ev-time", title: fullTime(ev.created_at) }, fmtTime(ev.created_at)));
}

function trimFeed() {
  const l = $("feedList");
  while (l.children.length > 200) l.removeChild(l.lastChild);
}

function evKind(ev) {
  if (ev.kind === "comment.added") return "comment";
  if (ev.kind === "task.claimed") return "claimed";
  if (ev.kind === "task.status") {
    const s = last((ev.data || {}).status);
    return s === "done" ? "done" : s === "blocked" ? "blocked" : "status";
  }
  return "other";
}

function refLink(id) {
  return el("span", { class: "ref", role: "link", tabindex: "0", onclick: (e) => { e.stopPropagation(); openTask(id); }, onkeydown: (e) => { if (e.key === "Enter") openTask(id); } }, "#" + id);
}

function evText(ev) {
  const span = el("span", { class: "ev-text" });
  const who = el("b", {}, ev.actor || "someone");
  const d = ev.data || {};
  const ref = ev.task_id ? refLink(ev.task_id) : null;
  switch (ev.kind) {
    case "task.created": span.append(who, " created ", ref); break;
    case "task.claimed": span.append(who, " claimed ", ref); break;
    case "task.status": span.append(who, " moved ", ref, " → ", String(last(d.status))); break;
    case "task.assign": span.append(who, " assigned ", ref, " → ", String(last(d.assignee) || "—")); break;
    case "task.patched": span.append(who, " edited ", ref); break;
    case "comment.added": span.append(who, " commented on ", ref); break;
    case "project.created": span.append(who, " created project ", el("b", {}, d.slug || "")); break;
    case "project.archived": span.append(who, " archived project ", el("b", {}, d.slug || "")); break;
    case "project.unarchived": span.append(who, " unarchived project ", el("b", {}, d.slug || "")); break;
    default: span.append(who, " " + ev.kind + " ", ref);
  }
  return span;
}

function describeText(ev) {
  const who = ev.actor || "someone";
  const t = ev.task_id ? "#" + ev.task_id : "";
  const d = ev.data || {};
  switch (ev.kind) {
    case "task.created": return `${who} created ${t}`;
    case "task.claimed": return `${who} claimed ${t}`;
    case "task.status": return `${who} moved ${t} → ${last(d.status)}`;
    case "task.assign": return `${who} assigned ${t} → ${last(d.assignee) || "—"}`;
    case "task.patched": return `${who} edited ${t}`;
    case "comment.added": return `${who} commented on ${t}`;
    case "project.created": return `${who} created project ${d.slug || ""}`;
    case "project.archived": return `${who} archived project ${d.slug || ""}`;
    case "project.unarchived": return `${who} unarchived project ${d.slug || ""}`;
    default: return `${who} ${ev.kind} ${t}`;
  }
}

function last(v) { return Array.isArray(v) ? v[v.length - 1] : v; }

function fmtTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d) ? iso.slice(11, 16) : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
function fullTime(iso) { if (!iso) return ""; const d = new Date(iso); return isNaN(d) ? iso : d.toLocaleString(); }
function fmtDate(iso) { if (!iso) return ""; const d = new Date(iso); return isNaN(d) ? iso.slice(0, 10) : d.toLocaleDateString(); }

// ---------- activity drawer: collapse + resize ----------

function setFeedCollapsed(collapsed) {
  document.body.classList.toggle("feed-collapsed", collapsed);
  lsSet(FEED_COLLAPSED_KEY, collapsed ? "1" : "0");
  $("feedToggle").setAttribute("aria-expanded", String(!collapsed));
}
function toggleFeed() { setFeedCollapsed(!document.body.classList.contains("feed-collapsed")); }

function setFeedW(w) {
  w = Math.min(720, Math.max(240, w));
  document.documentElement.style.setProperty("--feed-w", w + "px");
  lsSet(FEED_W_KEY, String(Math.round(w)));
}

function initFeed() {
  const w = parseInt(lsGet(FEED_W_KEY) || "", 10);
  if (w >= 240 && w <= 720) document.documentElement.style.setProperty("--feed-w", w + "px");
  let saved = lsGet(FEED_COLLAPSED_KEY);
  const collapsed = saved === null ? window.innerWidth <= 1024 : saved === "1";
  setFeedCollapsed(collapsed);

  $("feedToggle").onclick = toggleFeed;
  $("feedClose").onclick = () => setFeedCollapsed(true);
  $("feedBackdrop").onclick = () => setFeedCollapsed(true);

  const handle = $("feedResize");
  let startX = 0, startW = 0;
  const onMove = (e) => setFeedW(startW + (startX - e.clientX));
  const onUp = () => {
    document.body.classList.remove("resizing");
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
  };
  handle.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    startX = e.clientX;
    startW = $("feed").getBoundingClientRect().width;
    document.body.classList.add("resizing");
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  });
  handle.addEventListener("keydown", (e) => {
    const cur = $("feed").getBoundingClientRect().width;
    if (e.key === "ArrowLeft") { e.preventDefault(); setFeedW(cur + 24); }
    else if (e.key === "ArrowRight") { e.preventDefault(); setFeedW(cur - 24); }
  });
}

// ---------- keyboard ----------

function onKey(e) {
  if (e.key === "Escape") { if (!$("modal").classList.contains("hidden")) closeModal(); return; }
  trapFocus(e);
  const tag = (e.target.tagName || "").toLowerCase();
  const typing = tag === "input" || tag === "textarea" || tag === "select" || e.target.isContentEditable;
  if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
  const k = e.key.toLowerCase();
  if (k === "a") { e.preventDefault(); toggleFeed(); }
  else if (k === "n") { e.preventDefault(); openNew(); }
}

// ---------- init ----------

$("newBtn").onclick = openNew;
$("modal").onclick = (e) => { if (e.target.id === "modal") closeModal(); };
document.addEventListener("keydown", onKey);
initFeed();

(async function init() {
  try { await loadProjects(); await loadBoard(); await loadFeed(); }
  catch (e) { setStatus("error: " + e.message, "warn"); }
  connect();
})();
