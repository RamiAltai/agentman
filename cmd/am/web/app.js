"use strict";

const COLS = [["todo", "Todo"], ["doing", "In Progress"], ["blocked", "Blocked"], ["done", "Done"]];
const PRIO = ["#e5484d", "#f5a524", "#8b8d98", "#6e7681"]; // 0=urgent .. 3=low

let projects = [];
let current = "";            // selected project slug, "" = all
let tasks = new Map();       // id -> task (terse)
let cursor = 0;              // highest event id seen (SSE since=)
let es = null, backoff = 1000, refreshTimer = null, openTaskId = null;

const $ = (id) => document.getElementById(id);

// el builds DOM safely: string children become text nodes (never innerHTML),
// so agent-supplied titles/comments can't inject markup.
function el(tag, props, ...kids) {
  const n = document.createElement(tag);
  props = props || {};
  for (const k in props) {
    if (k === "class") n.className = props[k];
    else if (k === "onclick") n.onclick = props[k];
    else if (k === "onchange") n.onchange = props[k];
    else if (k === "onkeydown") n.onkeydown = props[k];
    else if (k === "value") n.value = props[k];
    else n.setAttribute(k, props[k]);
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
  if (current) p.set("project", current);
  const s = p.toString();
  return s ? "?" + s : "";
}

function setStatus(text, cls) { const e = $("status"); e.textContent = text; e.className = "status " + (cls || ""); }

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
  const res = await api("GET", "/api/events" + qstr({ tail: 40 }));
  $("feedList").replaceChildren();
  for (const ev of res.events) $("feedList").append(feedItem(ev)); // newest-first
  cursor = Math.max(cursor, res.last_id || 0);
}

function renderTabs() {
  const nav = $("tabs");
  nav.replaceChildren(tab("", "All", null));
  for (const p of projects) {
    const c = p.counts || {};
    nav.append(tab(p.slug, p.name, (c.todo || 0) + (c.doing || 0) + (c.blocked || 0)));
  }
  nav.append(el("button", { class: "tab add", onclick: openNewProject, title: "new project" }, "＋"));
}

function tab(slug, label, open) {
  const b = el("button", { class: "tab" + (slug === current ? " active" : ""), onclick: () => selectProject(slug) }, label);
  if (open) b.append(el("span", { class: "badge" }, String(open)));
  return b;
}

function renderBoard() {
  const board = $("board");
  board.replaceChildren();
  const by = { todo: [], doing: [], blocked: [], done: [] };
  for (const t of tasks.values()) (by[t.status] || (by[t.status] = [])).push(t);
  for (const [key, label] of COLS) {
    let list = (by[key] || []).sort((a, b) => a.priority - b.priority || b.id - a.id);
    const total = list.length;
    if (key === "done" && list.length > 50) list = list.slice(0, 50);
    const col = el("div", { class: "col" });
    col.append(el("div", { class: "colhead" }, label + " ", el("span", { class: "count" }, String(total))));
    const cards = el("div", { class: "cards" });
    for (const t of list) cards.append(card(t));
    col.append(cards);
    board.append(col);
  }
}

function card(t) {
  const c = el("div", { class: "card", onclick: () => openTask(t.id) });
  c.append(el("div", { class: "crow" },
    el("span", { class: "dot", style: "background:" + (PRIO[t.priority] || PRIO[3]) }),
    el("span", { class: "cid" }, "#" + t.id)));
  c.append(el("div", { class: "ctitle" }, t.title));
  const foot = el("div", { class: "cfoot" });
  foot.append(el("span", { class: "who" }, t.assignee ? "@" + t.assignee : "—"));
  if (!current) foot.append(el("span", { class: "ptag" }, t.project));
  if (t.nc > 0) foot.append(el("span", { class: "cc" }, "💬" + t.nc));
  c.append(foot);
  return c;
}

// ---------- project switching ----------

async function selectProject(slug) {
  current = slug;
  renderTabs();
  try { await loadBoard(); await loadFeed(); } catch (e) { setStatus("error", "warn"); }
  connect();
}

function slugify(s) {
  return s.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
}

function openNewProject() {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal }, "✕"));
  s.append(el("div", { class: "mhead" }, "New project"));
  const name = el("input", { class: "mtitle", placeholder: "project name (e.g. Web App)" });
  const slug = el("input", { class: "masg", placeholder: "slug (auto)" });
  const err = el("div", { class: "ferr" });
  // keep slug in sync with the name until the user edits the slug directly
  let slugEdited = false;
  slug.oninput = () => { slugEdited = true; };
  name.oninput = () => { if (!slugEdited) slug.value = slugify(name.value); };
  const save = el("button", {
    class: "save", onclick: async () => {
      const sv = slugify(slug.value || name.value);
      if (!sv) { err.textContent = "enter a name"; return; }
      try {
        const p = await api("POST", "/api/projects", { slug: sv, name: name.value.trim() || sv });
        await loadProjects();        // refresh tabs immediately, no SSE dependency
        closeModal();
        selectProject(p.slug);       // jump to the new project
      } catch (e) {
        err.textContent = e.message === "conflict" ? "a project with slug “" + sv + "” already exists" : e.message;
      }
    }
  }, "Create project");
  s.append(name, el("div", { class: "mrow" }, label("Slug", slug)), err, save);
  $("modal").classList.remove("hidden");
  name.focus();
}

// ---------- detail modal ----------

async function openTask(id) {
  openTaskId = id;
  try { renderModal(await api("GET", "/api/tasks/" + id)); $("modal").classList.remove("hidden"); }
  catch (e) { alert(e.message); openTaskId = null; }
}

function closeModal() { $("modal").classList.add("hidden"); openTaskId = null; }

async function refreshModal() {
  if (!openTaskId) return;
  try { const t = await api("GET", "/api/tasks/" + openTaskId); if (openTaskId === t.id) renderModal(t); } catch (e) { /* ignore */ }
}

function patch(id, body) { return api("PATCH", "/api/tasks/" + id, body).catch((e) => alert(e.message)); }

function label(text, node) { return el("label", { class: "lbl" }, el("span", {}, text), node); }

function prioSelect(value, onchange) {
  const sel = el("select", { onchange });
  ["0 urgent", "1 high", "2 normal", "3 low"].forEach((l, i) => {
    const o = el("option", { value: String(i) }, l);
    if (i === value) o.setAttribute("selected", "selected");
    sel.append(o);
  });
  return sel;
}

function renderModal(t) {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal }, "✕"));
  s.append(el("div", { class: "mhead" }, "#" + t.id + " · " + t.project + "-" + t.ref + " · created " + fmtDate(t.created_at)));

  const title = el("input", { class: "mtitle", value: t.title });
  title.onchange = () => patch(t.id, { title: title.value });
  s.append(title);

  const status = el("select", { onchange: (e) => patch(t.id, { status: e.target.value }) });
  for (const [k, l] of COLS) {
    const o = el("option", { value: k }, l);
    if (k === t.status) o.setAttribute("selected", "selected");
    status.append(o);
  }
  const asg = el("input", { class: "masg", value: t.assignee || "", placeholder: "unassigned" });
  asg.onchange = () => patch(t.id, { assignee: asg.value.trim() });
  const pri = prioSelect(t.priority, (e) => patch(t.id, { priority: Number(e.target.value) }));
  s.append(el("div", { class: "mrow" }, label("Status", status), label("Assignee", asg), label("Priority", pri)));

  const body = el("textarea", { class: "mbody", placeholder: "(no description)" });
  body.value = t.body || "";
  body.onchange = () => patch(t.id, { body: body.value });
  s.append(body);

  s.append(el("h3", {}, "Comments"));
  const cl = el("div", { class: "comments" });
  for (const cm of t.comments || []) {
    cl.append(el("div", { class: "cm" },
      el("b", {}, cm.author), el("span", { class: "t" }, fmtTime(cm.created_at)),
      el("div", {}, cm.body)));
  }
  s.append(cl);
  const cbox = el("input", { class: "cbox", placeholder: "add a comment, press ⏎ to send" });
  cbox.onkeydown = (e) => {
    if (e.key === "Enter" && cbox.value.trim()) {
      api("POST", "/api/tasks/" + t.id + "/comments", { body: cbox.value.trim() }).catch((err) => alert(err.message));
      cbox.value = "";
    }
  };
  s.append(cbox);

  s.append(el("h3", {}, "History"));
  const hl = el("ul", { class: "hist" });
  for (const ev of t.events || []) hl.append(el("li", {}, fmtTime(ev.created_at) + " — " + describe(ev)));
  s.append(hl);
}

function openNew() {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal }, "✕"));
  s.append(el("div", { class: "mhead" }, "New task"));
  const title = el("input", { class: "mtitle", placeholder: "title" });
  const psel = el("select", {});
  for (const p of projects) {
    const o = el("option", { value: p.slug }, p.slug);
    if (p.slug === current) o.setAttribute("selected", "selected");
    psel.append(o);
  }
  const pri = prioSelect(2, null);
  const body = el("textarea", { class: "mbody", placeholder: "description (optional)" });
  const save = el("button", {
    class: "save", onclick: async () => {
      if (!title.value.trim()) return;
      if (!psel.value) { alert("create a project first (＋ in the tab bar)"); return; }
      try {
        const t = await api("POST", "/api/tasks", { project: psel.value, title: title.value.trim(), body: body.value, priority: Number(pri.value) });
        closeModal();
        if (current && t.project !== current) await selectProject(t.project); // jump to where it landed
        else { await loadBoard().catch(() => {}); loadProjects().catch(() => {}); }
      } catch (e) { alert(e.message); }
    }
  }, "Create");
  s.append(title, el("div", { class: "mrow" }, label("Project", psel), label("Priority", pri)), body, save);
  $("modal").classList.remove("hidden");
}

// ---------- live stream ----------

function connect() {
  if (es) es.close();
  es = new EventSource("/api/stream" + qstr({ since: cursor }));
  es.onopen = () => { backoff = 1000; setStatus("● live", "ok"); };
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
  $("feedList").prepend(feedItem(ev));
  trimFeed();
  if (ev.kind === "project.created") loadProjects().catch(() => {});
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => loadBoard().catch(() => {}), 250); // debounced reconcile
  if (openTaskId && ev.task_id === openTaskId) refreshModal();
}

function feedItem(ev) {
  return el("li", {}, el("span", { class: "ft" }, fmtTime(ev.created_at)), describe(ev));
}

function trimFeed() {
  const l = $("feedList");
  while (l.children.length > 200) l.removeChild(l.lastChild);
}

function describe(ev) {
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
    default: return `${who} ${ev.kind} ${t}`;
  }
}

function last(v) { return Array.isArray(v) ? v[v.length - 1] : v; }

function fmtTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d) ? iso.slice(11, 16) : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function fmtDate(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d) ? iso.slice(0, 10) : d.toLocaleDateString();
}

// ---------- init ----------

$("newBtn").onclick = openNew;
$("modal").onclick = (e) => { if (e.target.id === "modal") closeModal(); };
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeModal(); });

(async function init() {
  try { await loadProjects(); await loadBoard(); await loadFeed(); }
  catch (e) { setStatus("error: " + e.message, "warn"); }
  connect();
})();
