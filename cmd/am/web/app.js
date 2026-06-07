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
let feedOldest = 0;          // lowest event id currently in #feedList (0 = none loaded)
let feedPaginated = false;   // true once the user has clicked "Load older"; trimFeed is then skipped
                             // to avoid fighting pagination (raising the cap is the other option,
                             // but skipping trimming is simpler since the user explicitly asked for more)
let loadOlderBtn = null;     // reference to the "Load older" button element

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
  if (!r.ok) {
    let msg = (data && data.error) || ("HTTP " + r.status);
    // Surface the blocking prerequisites instead of a bare "blocked".
    if (data && data.error === "blocked" && Array.isArray(data.open_prereqs) && data.open_prereqs.length)
      msg = "blocked by " + data.open_prereqs.map((n) => "#" + n).join(" ") + " (prereq not done)";
    const err = new Error(msg);
    err.data = data;
    throw err;
  }
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
  feedPaginated = false; // reset pagination state on full reload
  if (!res.events.length) {
    list.append(el("li", { class: "feed-empty" }, "No activity yet"));
    feedOldest = 0;
  } else {
    for (const ev of res.events) list.append(feedItem(ev)); // newest-first
    // Track the oldest (minimum) event id currently shown.
    feedOldest = res.events.reduce((min, ev) => ev.id < min ? ev.id : min, res.events[0].id);
  }
  cursor = Math.max(cursor, res.last_id || 0);

  // Rebuild the "Load older" button outside #feedList so trimFeed can't remove it.
  const feed = $("feed");
  if (loadOlderBtn) { loadOlderBtn.remove(); loadOlderBtn = null; }
  if (feedOldest > 0) {
    loadOlderBtn = el("button", { class: "feed-load-older", onclick: loadOlderActivity }, "Load older activity");
    feed.append(loadOlderBtn);
  }
}

function renderTabs() {
  const nav = $("tabs");
  const allOpen = projects.reduce((n, p) => n + openCount(p.counts), 0);
  nav.replaceChildren(tab("", "All", allOpen));
  for (const p of projects) nav.append(tab(p.slug, p.name, openCount(p.counts)));
  nav.append(el("button", { class: "tab add", onclick: openNewProject, title: "New project", "aria-label": "New project" }, "＋"));
  nav.append(el("button", { class: "tab add", onclick: openManageProjects, title: "Manage projects", "aria-label": "Manage projects" }, "⋯"));
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
  if (t.nopen > 0) foot.append(el("span", { class: "tag-blocked" }, "🔒 " + t.nopen));
  else if (t.nprereq > 0) foot.append(el("span", { class: "tag-ready" }, "✓ Ready"));
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

function patch(id, body) {
  // On failure (e.g. a hard-block 409), re-render the modal from server truth so
  // the status <select> / fields revert to the real state instead of the rejected value.
  return api("PATCH", "/api/tasks/" + id, body).catch((e) => { alert(e.message); refreshModal(); });
}

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

  // ---- Dependencies section ----
  s.append(el("h3", {}, "Dependencies"));
  const depsSection = el("div", { class: "deps-section" });

  // "Depends on" chips
  const depsPrereqDiv = el("div", { class: "deps-prereq" });
  const prereqs = t.depends_on || [];
  if (prereqs.length) {
    prereqs.forEach((dep) => {
      const chip = el("div", { class: "dep-chip" + (dep.status !== "done" ? " dep-open" : " dep-done") });
      chip.append(el("span", { class: "dep-dot", style: "background:" + (dep.status === "done" ? "var(--st-done)" : dep.status === "doing" ? "var(--st-doing)" : dep.status === "blocked" ? "var(--st-blocked)" : "var(--st-todo)") }));
      chip.append(el("span", { class: "dep-ref", role: "link", tabindex: "0", onclick: (e) => { e.stopPropagation(); openTask(dep.id); } }, dep.project + "-" + dep.ref));
      chip.append(el("span", { class: "dep-title" }, dep.title));
      chip.append(el("span", { class: "dep-status" }, dep.status));
      const rmBtn = el("button", { class: "dep-rm", "aria-label": "Remove prerequisite", title: "Remove" }, "✕");
      rmBtn.onclick = async () => {
        rmBtn.disabled = true;
        try {
          await api("DELETE", "/api/tasks/" + t.id + "/deps/" + dep.id);
        } catch (e) {
          rmBtn.disabled = false;
          alert(e.message);
        }
      };
      chip.append(rmBtn);
      depsPrereqDiv.append(chip);
    });
  } else {
    depsPrereqDiv.append(el("span", { class: "deps-empty" }, "None"));
  }

  // Add prerequisite control
  const addDepErr = el("div", { class: "ferr" });
  const depSel = el("select", { "aria-label": "Add prerequisite" });
  depSel.append(el("option", { value: "" }, "Add prerequisite…"));
  // Fetch same-project candidates lazily (do it async, populate when ready)
  (async () => {
    try {
      const candidates = await api("GET", "/api/tasks?project=" + encodeURIComponent(t.project) + "&limit=500");
      const existingIds = new Set(prereqs.map((d) => d.id));
      existingIds.add(t.id); // exclude self
      for (const cand of candidates) {
        if (existingIds.has(cand.id)) continue;
        depSel.append(el("option", { value: String(cand.id) }, cand.project + "-" + cand.ref + " " + cand.title));
      }
    } catch (e) { /* ignore */ }
  })();
  depSel.onchange = async () => {
    const val = depSel.value;
    if (!val) return;
    depSel.value = "";
    addDepErr.textContent = "";
    try {
      await api("POST", "/api/tasks/" + t.id + "/deps", { depends_on: Number(val) });
    } catch (e) {
      addDepErr.textContent = e.message;
    }
  };
  depsPrereqDiv.append(el("div", { class: "dep-add-row" }, depSel, addDepErr));
  depsSection.append(el("div", { class: "deps-group" },
    el("span", { class: "deps-label" }, "Depends on"),
    depsPrereqDiv));

  // "Blocks" list (read-only)
  const blocks = t.blocks || [];
  if (blocks.length) {
    const blocksDiv = el("div", { class: "deps-blocks" });
    blocks.forEach((dep) => {
      const row = el("div", { class: "dep-block-row" });
      row.append(
        el("span", { class: "dep-ref", role: "link", tabindex: "0", onclick: (e) => { e.stopPropagation(); openTask(dep.id); } }, dep.project + "-" + dep.ref),
        el("span", { class: "dep-title" }, dep.title),
        el("span", { class: "dep-status" }, "(" + dep.status + ")")
      );
      blocksDiv.append(row);
    });
    depsSection.append(el("div", { class: "deps-group" }, el("span", { class: "deps-label" }, "Blocks"), blocksDiv));
  }
  s.append(depsSection);

  s.append(el("h3", {}, "Comments" + (t.comments && t.comments.length ? " (" + t.comments.length + ")" : "")));
  const cl = el("div", { class: "comments" });
  if (!t.comments || !t.comments.length) cl.append(el("div", { class: "feed-empty" }, "No comments yet"));
  for (const cm of t.comments || []) {
    const cmDiv = el("div", { class: "cm" });
    const cmHead = el("div", { class: "cm-head" },
      el("b", {}, cm.author),
      el("span", { class: "t" }, fmtTime(cm.created_at)));
    // Inline two-step delete for each comment.
    const delCm = el("button", { class: "btn-del-cm", "aria-label": "Delete comment" }, "×");
    let cmConfirming = false;
    delCm.onclick = async () => {
      if (!cmConfirming) {
        cmConfirming = true;
        delCm.textContent = "Confirm delete?";
        delCm.classList.add("confirming");
        setTimeout(() => { if (cmConfirming) { cmConfirming = false; delCm.textContent = "×"; delCm.classList.remove("confirming"); } }, 4000);
        return;
      }
      delCm.disabled = true;
      try {
        await api("DELETE", "/api/tasks/" + t.id + "/comments/" + cm.id);
      } catch (e) {
        delCm.disabled = false;
        cmConfirming = false;
        delCm.textContent = "×";
        delCm.classList.remove("confirming");
      }
    };
    cmHead.append(delCm);
    cmDiv.append(cmHead, el("div", { class: "cbody" }, cm.body));
    cl.append(cmDiv);
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

  // Inline two-step delete for the task itself.
  const delBtn = el("button", { class: "btn-danger-task" }, "Delete task");
  let delConfirming = false;
  delBtn.onclick = async () => {
    if (!delConfirming) {
      delConfirming = true;
      delBtn.textContent = "Confirm delete?";
      delBtn.classList.add("confirming");
      const cancelEl = el("button", { class: "btn-cancel-del" }, "Cancel");
      cancelEl.onclick = () => {
        delConfirming = false;
        delBtn.textContent = "Delete task";
        delBtn.classList.remove("confirming");
        cancelEl.remove();
      };
      delBtn.after(cancelEl);
      return;
    }
    delBtn.disabled = true;
    try {
      await api("DELETE", "/api/tasks/" + t.id);
      closeModal();
    } catch (e) {
      delBtn.disabled = false;
      delConfirming = false;
      delBtn.textContent = "Delete task";
      delBtn.classList.remove("confirming");
      const sibling = delBtn.nextSibling;
      if (sibling && sibling.classList && sibling.classList.contains("btn-cancel-del")) sibling.remove();
    }
  };
  s.append(el("div", { class: "del-task-row" }, delBtn));
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

// ---------- manage projects modal ----------

async function openManageProjects() {
  const s = $("sheet");
  s.replaceChildren();
  s.append(el("button", { class: "x", onclick: closeModal, "aria-label": "Close" }, "✕"));
  s.append(el("div", { class: "mhead" }, "Manage projects"));
  const err = el("div", { class: "ferr" });
  s.append(err);

  const list = el("ul", { class: "proj-list", "aria-label": "Projects" });
  s.append(list);

  openModal();
  s.focus();

  await renderManageList(list, err);
}

async function renderManageList(list, err) {
  list.replaceChildren();
  let all;
  try {
    all = await api("GET", "/api/projects?archived=true");
  } catch (e) {
    err.textContent = e.message;
    return;
  }
  if (!all.length) {
    list.append(el("li", { class: "feed-empty" }, "No projects yet"));
    return;
  }
  for (const p of all) {
    const isArchived = !!p.archived_at;
    const openTasks = openCount(p.counts);

    const row = el("li", { class: "proj-row" + (isArchived ? " archived" : "") });

    const nameSpan = el("span", { class: "proj-row-name" }, p.name);
    const slugSpan = el("span", { class: "proj-row-slug" }, p.slug);
    const countSpan = el("span", { class: "proj-row-count" }, openTasks + " open");

    const archBtn = el("button", {
      class: "btn-archive" + (isArchived ? " unarchive" : ""),
      onclick: async () => {
        archBtn.disabled = true;
        try {
          if (isArchived) {
            await api("POST", "/api/projects/" + p.slug + "/unarchive");
          } else {
            await api("POST", "/api/projects/" + p.slug + "/archive");
            // If this project was selected, remove it from selection.
            if (selected.has(p.slug)) {
              selected.delete(p.slug);
              loadBoard().catch(() => {});
              loadFeed().catch(() => {});
              connect();
            }
          }
          // Refresh tab bar.
          await loadProjects();
          // Refresh the manage list in place.
          await renderManageList(list, err);
        } catch (e) {
          err.textContent = e.message;
          archBtn.disabled = false;
        }
      },
    }, isArchived ? "Unarchive" : "Archive");

    // Inline two-step delete button for the project.
    const rmBtn = el("button", { class: "btn-danger-proj" }, "Delete");
    let rmConfirming = false;
    rmBtn.onclick = async () => {
      if (!rmConfirming) {
        rmConfirming = true;
        rmBtn.textContent = "Confirm delete?";
        rmBtn.classList.add("confirming");
        setTimeout(() => { if (rmConfirming) { rmConfirming = false; rmBtn.textContent = "Delete"; rmBtn.classList.remove("confirming"); } }, 5000);
        return;
      }
      rmBtn.disabled = true;
      try {
        await api("DELETE", "/api/projects/" + p.slug);
        if (selected.has(p.slug)) { selected.delete(p.slug); loadBoard().catch(() => {}); loadFeed().catch(() => {}); connect(); }
        await loadProjects();
        await renderManageList(list, err);
      } catch (e) {
        err.textContent = e.message;
        rmBtn.disabled = false;
        rmConfirming = false;
        rmBtn.textContent = "Delete";
        rmBtn.classList.remove("confirming");
      }
    };

    if (isArchived) row.append(nameSpan, slugSpan, el("span", { class: "badge-archived" }, "Archived"), countSpan, archBtn, rmBtn);
    else row.append(nameSpan, slugSpan, countSpan, archBtn, rmBtn);

    list.append(row);
  }
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
  graphMaybeRefresh(ev);
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
  if (ev.kind === "task.deleted") {
    tasks.delete(ev.task_id);
    renderBoard();
    if (openTaskId === ev.task_id) closeModal();
    loadProjects().catch(() => {});
  }
  if (ev.kind === "comment.deleted") {
    if (openTaskId === ev.task_id) refreshModal();
  }
  if (ev.kind === "project.deleted") {
    const deletedSlug = (ev.data || {}).slug;
    if (selected.has(deletedSlug)) {
      selected.delete(deletedSlug);
      renderTabs();
      connect();
    }
    loadProjects().catch(() => {});
    loadBoard().catch(() => {});
  }
  if (ev.kind === "task.dep_added" || ev.kind === "task.dep_removed") {
    // Refresh modal if it shows either the task or the prereq being linked/unlinked.
    const depId = (ev.data || {}).depends_on;
    if (openTaskId === ev.task_id || openTaskId === depId) refreshModal();
  }
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => loadBoard().catch(() => {}), 250); // debounced reconcile
  if (openTaskId && ev.task_id === openTaskId && ev.kind !== "task.deleted" && ev.kind !== "comment.deleted" && ev.kind !== "task.dep_added" && ev.kind !== "task.dep_removed") refreshModal();
}

function feedItem(ev) {
  return el("li", { class: "ev k-" + evKind(ev) },
    el("span", { class: "ev-dot" }),
    evText(ev),
    el("span", { class: "ev-time", title: fullTime(ev.created_at) }, fmtTime(ev.created_at)));
}

function trimFeed() {
  // Once the user has paginated, skip trimming so older entries they loaded are
  // preserved. Without this, each live event would pop the oldest paginated row.
  if (feedPaginated) return;
  const l = $("feedList");
  while (l.children.length > 200) l.removeChild(l.lastChild);
}

async function loadOlderActivity() {
  if (!feedOldest || !loadOlderBtn) return;
  loadOlderBtn.disabled = true;
  const limit = 50;
  try {
    const params = { before: String(feedOldest), limit: String(limit) };
    if (selected.size === 1) params.project = [...selected][0];
    const qs = new URLSearchParams(params);
    const res = await api("GET", "/api/events?" + qs.toString());
    const evs = res.events || [];
    const list = $("feedList");
    for (const ev of evs) list.append(feedItem(ev)); // already newest-first, append to bottom
    if (evs.length > 0) {
      feedOldest = evs.reduce((min, ev) => ev.id < min ? ev.id : min, evs[0].id);
      feedPaginated = true;
    }
    if (evs.length < limit) {
      // No more older events — replace button with an end-marker.
      loadOlderBtn.replaceWith(el("div", { class: "feed-start-marker" }, "— start of activity —"));
      loadOlderBtn = null;
    } else {
      loadOlderBtn.disabled = false;
    }
  } catch (e) {
    loadOlderBtn.disabled = false;
  }
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
    case "task.deleted": span.append(who, " deleted ", ref || document.createTextNode("#" + ev.task_id)); break;
    case "comment.deleted": span.append(who, " deleted a comment on ", ref || document.createTextNode("#" + ev.task_id)); break;
    case "project.deleted": span.append(who, " deleted project ", el("b", {}, d.slug || "")); break;
    case "task.dep_added": span.append(who, " linked ", ref, " → depends on #", String(d.depends_on || "")); break;
    case "task.dep_removed": span.append(who, " unlinked ", ref, " dep #", String(d.depends_on || "")); break;
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
    case "task.deleted": return `${who} deleted task`;
    case "comment.deleted": return `${who} deleted a comment on ${t}`;
    case "project.deleted": return `${who} deleted project ${d.slug || ""}`;
    case "task.dep_added": return `${who} linked ${t} depends on #${d.depends_on || ""}`;
    case "task.dep_removed": return `${who} unlinked ${t} dep #${d.depends_on || ""}`;
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
  else if (k === "g") { e.preventDefault(); if (graphOpen) closeGraphOverlay(); else openGraphOverlay(); }
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

// ===================== dependency graph overlay =====================

// svg() creates an SVG element (parallel to el() for HTML). Uses createElementNS
// to produce real SVG; never uses innerHTML.
function svg(tag, attrs) {
  const ns = "http://www.w3.org/2000/svg";
  const n = document.createElementNS(ns, tag);
  if (attrs) {
    for (const k in attrs) {
      if (attrs[k] != null) n.setAttribute(k, String(attrs[k]));
    }
  }
  return n;
}

// wrapTextLines splits text into lines of at most maxChars, up to maxLines lines.
function wrapTextLines(text, maxChars, maxLines) {
  if (!text) return [""];
  const words = text.split(" ");
  const lines = [];
  let cur = "";
  for (const word of words) {
    if (lines.length >= maxLines) break;
    if (!cur) { cur = word; continue; }
    if (cur.length + 1 + word.length <= maxChars) { cur += " " + word; }
    else { lines.push(cur); cur = word; }
  }
  if (cur && lines.length < maxLines) lines.push(cur);
  if (!lines.length) lines.push("");
  return lines;
}

// --- layout ---

// computeGraphLayout: topological longest-path layering (Kahn's algorithm).
// nodes: Task[], edges: GraphEdge[] {from, to}
// Returns { nodes, edges, positions: Map<id, {x,y,w,h}>, width, height }
function computeGraphLayout(nodes, edges) {
  const nodeW = 176;
  const nodeH = 60;
  const colGap = 248;
  const rowGap = 88;
  const margin = 72;

  const ids = nodes.map((n) => n.id);
  const idSet = new Set(ids);
  const validEdges = edges.filter((e) => idSet.has(e.from) && idSet.has(e.to));

  // Partition: nodes in >=1 edge get the layered DAG; dep-free tasks go into a
  // compact grid lane below, so isolated tasks don't pile into one tall column.
  // (We still show ALL tasks — decision: graph all tasks.)
  const connectedIds = new Set();
  for (const e of validEdges) {
    connectedIds.add(e.from);
    connectedIds.add(e.to);
  }
  const connected = nodes.filter((n) => connectedIds.has(n.id));
  const isolated = nodes
    .filter((n) => !connectedIds.has(n.id))
    .sort((a, b) => a.title.localeCompare(b.title));

  const positions = new Map();

  // ---- layered DAG (longest-path / Kahn's layering) over the connected nodes ----
  let dagWidth = 0;
  let dagHeight = 0;
  if (connected.length) {
    const cids = connected.map((n) => n.id);
    const indegree = new Map(cids.map((id) => [id, 0]));
    const adjacency = new Map(cids.map((id) => [id, []]));
    for (const e of validEdges) {
      indegree.set(e.to, (indegree.get(e.to) || 0) + 1);
      adjacency.get(e.from).push(e.to);
    }
    const levels = new Map(cids.map((id) => [id, 0]));
    const queue = cids.filter((id) => (indegree.get(id) || 0) === 0);
    while (queue.length > 0) {
      const cur = queue.shift();
      for (const next of adjacency.get(cur) || []) {
        levels.set(next, Math.max(levels.get(next) || 0, (levels.get(cur) || 0) + 1));
        indegree.set(next, (indegree.get(next) || 0) - 1);
        if ((indegree.get(next) || 0) === 0) queue.push(next);
      }
    }
    const grouped = new Map();
    for (const n of connected) {
      const lv = levels.get(n.id) || 0;
      if (!grouped.has(lv)) grouped.set(lv, []);
      grouped.get(lv).push(n);
    }
    for (const list of grouped.values()) {
      list.sort((a, b) => a.title.localeCompare(b.title));
    }
    const maxLevel = Math.max(0, ...Array.from(grouped.keys()));
    const maxRows = Math.max(1, ...Array.from(grouped.values()).map((l) => l.length));
    dagWidth = margin * 2 + nodeW + maxLevel * colGap;
    dagHeight = margin * 2 + (maxRows - 1) * rowGap + nodeH;
    for (const [lv, list] of grouped.entries()) {
      const groupH = nodeH + (list.length - 1) * rowGap;
      const startY = Math.max(margin, (dagHeight - groupH) / 2);
      list.forEach((n, i) => {
        positions.set(n.id, { x: margin + lv * colGap, y: startY + i * rowGap, w: nodeW, h: nodeH });
      });
    }
  }

  // ---- compact grid lane for isolated (dependency-free) tasks ----
  let laneY = 0;
  let gridWidth = 0;
  let gridHeight = 0;
  if (isolated.length) {
    const gapX = 26;
    const gapY = 24;
    const perRow = Math.min(isolated.length, 6);
    const top = connected.length ? dagHeight + 56 : margin;
    laneY = connected.length ? dagHeight + 8 : 0; // divider position (0 = no divider)
    gridWidth = margin * 2 + perRow * nodeW + (perRow - 1) * gapX;
    isolated.forEach((n, i) => {
      const row = Math.floor(i / perRow);
      const col = i % perRow;
      positions.set(n.id, {
        x: margin + col * (nodeW + gapX),
        y: top + row * (nodeH + gapY),
        w: nodeW,
        h: nodeH,
      });
    });
    const rows = Math.ceil(isolated.length / perRow);
    gridHeight = top + rows * nodeH + (rows - 1) * gapY + margin;
  }

  const width = Math.max(440, dagWidth, gridWidth);
  const height = Math.max(440, isolated.length ? gridHeight : dagHeight);

  return { nodes, edges: validEdges, positions, width, height, laneY };
}

// --- graph state ---

let graphOpen = false;
let graphSlug = "";         // slug currently displayed
let graphData = null;       // {nodes:[], edges:[]}  (last fetched)
let graphViewState = { x: 0, y: 0, width: 1000, height: 640 };
let graphInitialView = { x: 0, y: 0, width: 1000, height: 640 };
let graphSelectedId = null; // selected node id (number)
let graphDragState = null;
let graphRefreshTimer = null;
let graphLastFocus = null;
let graphPanZoomInstalled = false; // installed once per overlay open; guards listener leak

// BFS upstream (ancestors) and downstream (descendants) from a node.
function graphNeighbors(nodeId, edges) {
  const upstream = new Set();
  const downstream = new Set();
  const upEdges = new Set();
  const downEdges = new Set();

  // upstream: walk edges backward (to→from)
  let q = [nodeId];
  while (q.length) {
    const cur = q.shift();
    for (const e of edges) {
      if (e.to === cur && !upstream.has(e.from)) {
        upstream.add(e.from);
        upEdges.add(e);
        q.push(e.from);
      }
    }
  }
  // downstream: walk edges forward (from→to)
  q = [nodeId];
  while (q.length) {
    const cur = q.shift();
    for (const e of edges) {
      if (e.from === cur && !downstream.has(e.to)) {
        downstream.add(e.to);
        downEdges.add(e);
        q.push(e.to);
      }
    }
  }
  return { upstream, downstream, upEdges, downEdges };
}

function openGraphOverlay() {
  // Determine project to show.
  const slug = selected.size === 1 ? [...selected][0] : (projects[0] ? projects[0].slug : "");
  if (!slug) return;
  graphLastFocus = document.activeElement;
  graphSelectedId = null;
  $("graphOverlay").classList.remove("hidden");
  graphOpen = true;
  graphPanZoomInstalled = false;
  buildGraphProjectSelect(slug);
  fetchAndRenderGraph(slug);
  $("graphClose").focus();
}

function closeGraphOverlay() {
  $("graphOverlay").classList.add("hidden");
  graphOpen = false;
  graphData = null;
  graphSelectedId = null;
  if (graphRefreshTimer) { clearTimeout(graphRefreshTimer); graphRefreshTimer = null; }
  if (graphLastFocus && graphLastFocus.focus) try { graphLastFocus.focus(); } catch (e) { /* */ }
}

function buildGraphProjectSelect(activeSlug) {
  const sel = $("graphProjectSel");
  sel.replaceChildren();
  for (const p of projects) {
    const o = el("option", { value: p.slug }, p.name);
    if (p.slug === activeSlug) o.setAttribute("selected", "selected");
    sel.append(o);
  }
  sel.onchange = () => {
    graphSelectedId = null;
    fetchAndRenderGraph(sel.value);
  };
}

async function fetchAndRenderGraph(slug) {
  graphSlug = slug;
  $("graphTitle").textContent = "Dependencies — " + slug;
  // Update select to match slug.
  const sel = $("graphProjectSel");
  if (sel.value !== slug) sel.value = slug;

  let data;
  try {
    data = await api("GET", "/api/projects/" + encodeURIComponent(slug) + "/graph");
  } catch (e) {
    showGraphEmpty("Error loading graph: " + e.message);
    return;
  }
  graphData = data;
  if (!data.nodes || !data.nodes.length) {
    showGraphEmpty("No tasks in this project yet.");
    return;
  }
  renderGraph();
}

function showGraphEmpty(msg) {
  const svgEl = $("graphSvg");
  svgEl.replaceChildren();
  svgEl.setAttribute("viewBox", "0 0 600 200");
  const t = svg("text", { x: "300", y: "100", "text-anchor": "middle", class: "graph-empty-text" });
  t.textContent = msg;
  svgEl.append(t);
  $("graphDetail").replaceChildren(el("p", { class: "graph-detail-hint" }, msg));
  buildGraphLegend();
}

function renderGraph() {
  if (!graphData) return;

  const { nodes, edges, positions, width, height, laneY } = computeGraphLayout(graphData.nodes, graphData.edges);
  const nodesById = new Map(nodes.map((n) => [n.id, n]));

  // Update the initial (reset-target) viewBox from layout dims.
  graphInitialView = { x: 0, y: 0, width, height };
  // Preserve the user's current pan/zoom on live refresh; only reset on first render.
  if (!graphPanZoomInstalled) graphViewState = { ...graphInitialView };

  const svgEl = $("graphSvg");
  svgEl.replaceChildren();
  svgEl.setAttribute("viewBox", viewBoxStr(graphViewState));

  // Arrowhead markers (two: green for cleared, amber for blocking).
  const defs = svg("defs");

  function makeArrow(id, color) {
    const m = svg("marker", {
      id, viewBox: "0 0 10 10", refX: "9", refY: "5",
      markerWidth: "6", markerHeight: "6", orient: "auto-start-reverse",
    });
    const p = svg("path", { d: "M 0 0 L 10 5 L 0 10 z", fill: color });
    m.append(p);
    return m;
  }
  defs.append(makeArrow("gArrowCleared", "var(--st-done)"));
  defs.append(makeArrow("gArrowBlocking", "var(--warn)"));
  defs.append(makeArrow("gArrowUp", "var(--accent)"));
  defs.append(makeArrow("gArrowDown", "var(--violet)"));
  svgEl.append(defs);

  // Pan surface (transparent background for drag/click).
  const panSurface = svg("rect", {
    x: "0", y: "0", width: String(width), height: String(height), class: "graph-pan-surface",
  });
  svgEl.append(panSurface);

  // Divider + label between the dependency DAG and the dependency-free lane.
  if (laneY > 0) {
    const sep = svg("g", { class: "graph-lane-sep" });
    sep.append(svg("line", {
      x1: "24", y1: String(laneY), x2: String(width - 24), y2: String(laneY), class: "graph-lane-line",
    }));
    const lbl = svg("text", { x: "28", y: String(laneY + 22), class: "graph-lane-label" });
    lbl.textContent = "No dependencies";
    sep.append(lbl);
    svgEl.append(sep);
  }

  const edgeGroup = svg("g", { class: "gedge-group" });
  const nodeGroup = svg("g", { class: "gnode-group" });
  svgEl.append(edgeGroup);
  svgEl.append(nodeGroup);

  // --- draw edges ---
  const edgeEls = new Map();  // Map<GraphEdge, SVGElement>

  for (const e of edges) {
    const fp = positions.get(e.from);
    const tp = positions.get(e.to);
    if (!fp || !tp) continue;

    const sx = fp.x + fp.w;
    const sy = fp.y + fp.h / 2;
    const tx = tp.x;
    const ty = tp.y + tp.h / 2;
    const delta = tx - sx;
    const bend = Math.max(58, Math.abs(delta) * 0.42);
    const d = delta >= 0
      ? `M ${sx} ${sy} C ${sx + bend} ${sy}, ${tx - bend} ${ty}, ${tx} ${ty}`
      : `M ${sx} ${sy} C ${sx + bend} ${sy}, ${tx - bend} ${ty - 80}, ${tx} ${ty}`;

    const fromNode = nodesById.get(e.from);
    const cleared = fromNode && fromNode.status === "done";
    const marker = cleared ? "url(#gArrowCleared)" : "url(#gArrowBlocking)";
    const edgeCls = "gedge " + (cleared ? "gedge-cleared" : "gedge-blocking");

    const path = svg("path", { d, class: edgeCls, "marker-end": marker,
      "data-from": String(e.from), "data-to": String(e.to) });
    edgeGroup.append(path);
    edgeEls.set(e, path);
  }

  // --- draw nodes ---
  const nodeEls = new Map();  // Map<id, SVGElement>

  for (const n of nodes) {
    const pos = positions.get(n.id);
    if (!pos) continue;

    const prioColor = PRIO[n.priority] || PRIO[3];
    const statusColor = ST[n.status] || "var(--faint)";
    const isDone = n.status === "done";
    const isBlocked = n.nopen > 0;
    const isReady = n.nprereq > 0 && n.nopen === 0;

    const g = svg("g", {
      class: "gnode" + (isDone ? " gnode-done" : ""),
      transform: `translate(${pos.x}, ${pos.y})`,
      "data-id": String(n.id),
      role: "button",
      tabindex: "0",
      "aria-label": n.project + "-" + n.ref + " " + n.title + " (" + n.status + ")",
    });

    const rect = svg("rect", {
      x: "0", y: "0", width: String(pos.w), height: String(pos.h),
      rx: "9", class: "gnode-rect",
      stroke: prioColor,
    });
    g.append(rect);

    // Status dot.
    const dot = svg("circle", { cx: "14", cy: "16", r: "5", class: "gnode-dot", fill: statusColor });
    g.append(dot);

    // Ready/blocked indicator text.
    if (isBlocked) {
      const ind = svg("text", { x: String(pos.w - 10), y: "16", class: "gnode-ind gnode-ind-blocked",
        "text-anchor": "end", "dominant-baseline": "middle" });
      ind.textContent = "🔒";  // lock emoji
      g.append(ind);
    } else if (isReady) {
      const ind = svg("text", { x: String(pos.w - 10), y: "16", class: "gnode-ind gnode-ind-ready",
        "text-anchor": "end", "dominant-baseline": "middle" });
      ind.textContent = "✓";
      g.append(ind);
    }

    // Label: #ref + title wrapped.
    const refLine = n.project + "-" + n.ref;
    const refT = svg("text", { x: "26", y: "16", class: "gnode-ref", "dominant-baseline": "middle" });
    refT.textContent = refLine;
    g.append(refT);

    const titleLines = wrapTextLines(n.title, 22, 2);
    const titleGroup = svg("text", { x: "8", y: "36", class: "gnode-title" });
    titleLines.forEach((line, i) => {
      const span = svg("tspan", { x: "8", dy: i === 0 ? "0" : "14" });
      span.textContent = line;
      titleGroup.append(span);
    });
    g.append(titleGroup);

    nodeGroup.append(g);
    nodeEls.set(n.id, g);

    g.addEventListener("click", (ev) => {
      ev.stopPropagation();
      if (graphSelectedId === n.id) {
        graphSelectedId = null;
      } else {
        graphSelectedId = n.id;
      }
      applyGraphFocus(nodeEls, edgeEls, edges, nodesById);
      if (graphSelectedId != null) renderGraphDetail(n, edges, nodesById);
      else clearGraphDetail();
    });
    g.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" || ev.key === " ") {
        ev.preventDefault();
        g.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      }
    });
  }

  // Pan-surface click clears selection (panSurface is a new element each render; no leak).
  panSurface.addEventListener("click", () => {
    graphSelectedId = null;
    rebuildFocusAfterDetailNav(); // uses current DOM, not stale closure
    clearGraphDetail();
  });
  // svgEl is persistent — add this click-to-deselect listener only once (guarded by graphPanZoomInstalled).
  if (!graphPanZoomInstalled) {
    svgEl.addEventListener("click", (ev) => {
      if (ev.target === svgEl) {
        graphSelectedId = null;
        rebuildFocusAfterDetailNav();
        clearGraphDetail();
      }
    });
  }

  // Restore selection if the node still exists.
  if (graphSelectedId != null && nodesById.has(graphSelectedId)) {
    applyGraphFocus(nodeEls, edgeEls, edges, nodesById);
    renderGraphDetail(nodesById.get(graphSelectedId), edges, nodesById);
  } else {
    graphSelectedId = null;
    clearGraphDetail();
  }

  // Pan/zoom — install listeners only once per overlay open to avoid stacking them
  // on every live refresh (svgEl is the same DOM node across re-renders).
  if (!graphPanZoomInstalled) {
    installGraphPanZoom(svgEl, panSurface);
    graphPanZoomInstalled = true;
  }
  buildGraphLegend();
}

function viewBoxStr(vs) {
  return `${vs.x} ${vs.y} ${vs.width} ${vs.height}`;
}

function applyGraphFocus(nodeEls, edgeEls, edges, nodesById) {
  // Reset all.
  nodeEls.forEach((el) => el.classList.remove("gnode-sel", "gnode-up", "gnode-down", "gnode-dim"));
  edgeEls.forEach((el) => el.classList.remove("gedge-up", "gedge-down", "gedge-dim"));
  // Reset marker refs on edges to their original markers.
  edgeEls.forEach((pathEl, e) => {
    const fromNode = nodesById.get(e.from);
    const cleared = fromNode && fromNode.status === "done";
    pathEl.setAttribute("marker-end", cleared ? "url(#gArrowCleared)" : "url(#gArrowBlocking)");
  });

  if (graphSelectedId == null) return;

  const { upstream, downstream, upEdges, downEdges } = graphNeighbors(graphSelectedId, edges);

  // Dim everything first.
  nodeEls.forEach((el) => el.classList.add("gnode-dim"));
  edgeEls.forEach((el) => el.classList.add("gedge-dim"));

  // Selected node.
  nodeEls.get(graphSelectedId)?.classList.remove("gnode-dim");
  nodeEls.get(graphSelectedId)?.classList.add("gnode-sel");

  // Upstream nodes + edges.
  for (const id of upstream) {
    nodeEls.get(id)?.classList.remove("gnode-dim");
    nodeEls.get(id)?.classList.add("gnode-up");
  }
  for (const e of upEdges) {
    const pathEl = edgeEls.get(e);
    if (pathEl) {
      pathEl.classList.remove("gedge-dim");
      pathEl.classList.add("gedge-up");
      pathEl.setAttribute("marker-end", "url(#gArrowUp)");
    }
  }

  // Downstream nodes + edges.
  for (const id of downstream) {
    nodeEls.get(id)?.classList.remove("gnode-dim");
    nodeEls.get(id)?.classList.add("gnode-down");
  }
  for (const e of downEdges) {
    const pathEl = edgeEls.get(e);
    if (pathEl) {
      pathEl.classList.remove("gedge-dim");
      pathEl.classList.add("gedge-down");
      pathEl.setAttribute("marker-end", "url(#gArrowDown)");
    }
  }
}

function clearGraphDetail() {
  const d = $("graphDetail");
  d.replaceChildren(el("p", { class: "graph-detail-hint" }, "Click a node to inspect. Scroll to zoom, drag to pan."));
}

function renderGraphDetail(node, edges, nodesById) {
  const d = $("graphDetail");
  d.replaceChildren();

  const prioColor = PRIO[node.priority] || PRIO[3];
  const statusColor = ST[node.status] || "var(--faint)";

  const head = el("div", { class: "gd-head" });
  head.append(el("span", { class: "gd-ref" }, node.project + "-" + node.ref));
  head.append(el("span", { class: "gd-title" }, node.title));
  d.append(head);

  const chips = el("div", { class: "gd-chips" });
  chips.append(el("span", { class: "gd-chip", style: "background:" + statusColor + "22;color:" + statusColor }, node.status));
  chips.append(el("span", { class: "gd-chip", style: "border-color:" + prioColor + ";color:" + prioColor },
    PRIO_LABEL[node.priority] || ("P" + node.priority)));
  if (node.assignee) chips.append(el("span", { class: "gd-chip gd-assignee" }, node.assignee));
  d.append(chips);

  const blockedLine = el("div", { class: "gd-blocked-line" });
  if (node.nopen > 0) {
    blockedLine.append(el("span", { class: "gd-blocked" }, "🔒 Blocked — " + node.nopen + " open prereq" + (node.nopen > 1 ? "s" : "")));
  } else if (node.nprereq > 0) {
    blockedLine.append(el("span", { class: "gd-ready" }, "✓ Ready"));
  } else {
    blockedLine.append(el("span", { class: "gd-free" }, "No prerequisites"));
  }
  d.append(blockedLine);

  // Prerequisites (incoming edges from→this node).
  const prereqs = edges.filter((e) => e.to === node.id).map((e) => nodesById.get(e.from)).filter(Boolean);
  const prereqSec = el("div", { class: "gd-section" });
  prereqSec.append(el("div", { class: "gd-section-label" }, "Prerequisites (" + prereqs.length + ")"));
  if (!prereqs.length) {
    prereqSec.append(el("div", { class: "gd-none" }, "None"));
  } else {
    for (const p of prereqs) {
      const row = el("div", { class: "gd-dep-row", role: "link", tabindex: "0",
        onclick: () => { graphSelectedId = p.id; renderGraphDetail(p, edges, nodesById); rebuildFocusAfterDetailNav(); },
        onkeydown: (ev) => { if (ev.key === "Enter") { graphSelectedId = p.id; renderGraphDetail(p, edges, nodesById); rebuildFocusAfterDetailNav(); } },
      });
      const dot = el("span", { class: "gd-dep-dot", style: "background:" + (ST[p.status] || "var(--faint)") });
      row.append(dot, el("span", { class: "gd-dep-ref" }, p.project + "-" + p.ref),
        el("span", { class: "gd-dep-title" }, p.title),
        el("span", { class: "gd-dep-status" }, p.status === "done" ? "✓" : p.status));
      prereqSec.append(row);
    }
  }
  d.append(prereqSec);

  // Unblocks (outgoing edges this→to).
  const unblocks = edges.filter((e) => e.from === node.id).map((e) => nodesById.get(e.to)).filter(Boolean);
  const unblockSec = el("div", { class: "gd-section" });
  unblockSec.append(el("div", { class: "gd-section-label" }, "Unblocks (" + unblocks.length + ")"));
  if (!unblocks.length) {
    unblockSec.append(el("div", { class: "gd-none" }, "None"));
  } else {
    for (const u of unblocks) {
      const row = el("div", { class: "gd-dep-row", role: "link", tabindex: "0",
        onclick: () => { graphSelectedId = u.id; renderGraphDetail(u, edges, nodesById); rebuildFocusAfterDetailNav(); },
        onkeydown: (ev) => { if (ev.key === "Enter") { graphSelectedId = u.id; renderGraphDetail(u, edges, nodesById); rebuildFocusAfterDetailNav(); } },
      });
      const dot = el("span", { class: "gd-dep-dot", style: "background:" + (ST[u.status] || "var(--faint)") });
      row.append(dot, el("span", { class: "gd-dep-ref" }, u.project + "-" + u.ref),
        el("span", { class: "gd-dep-title" }, u.title),
        el("span", { class: "gd-dep-status" }, u.status === "done" ? "✓" : u.status));
      unblockSec.append(row);
    }
  }
  d.append(unblockSec);

  // Leave the graph and open the full task: close the overlay first so the task
  // modal isn't hidden behind it, then open the task on the board.
  const openBtn = el("button", { class: "gd-open-btn", onclick: () => { closeGraphOverlay(); openTask(node.id); } }, "Open task");
  d.append(openBtn);
}

function rebuildFocusAfterDetailNav() {
  if (!graphData) return;
  const { nodes, edges } = graphData;
  const nodesById = new Map(nodes.map((n) => [n.id, n]));
  // Re-collect SVG elements by data-id.
  const svgEl = $("graphSvg");
  const nodeEls = new Map();
  svgEl.querySelectorAll(".gnode[data-id]").forEach((el) => nodeEls.set(Number(el.dataset.id), el));
  const edgeEls = new Map();
  // For edges we need the original edge objects — iterate all path elements.
  svgEl.querySelectorAll(".gedge").forEach((pathEl) => {
    const from = Number(pathEl.getAttribute("data-from"));
    const to = Number(pathEl.getAttribute("data-to"));
    // Find matching edge.
    const e = edges.find((x) => x.from === from && x.to === to);
    if (e) edgeEls.set(e, pathEl);
  });
  applyGraphFocus(nodeEls, edgeEls, edges, nodesById);
}

function installGraphPanZoom(svgEl, panSurface) {
  const setViewBox = () => svgEl.setAttribute("viewBox", viewBoxStr(graphViewState));

  svgEl.addEventListener("wheel", (ev) => {
    ev.preventDefault();
    const rect = svgEl.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const px = (ev.clientX - rect.left) / rect.width;
    const py = (ev.clientY - rect.top) / rect.height;
    const anchorX = graphViewState.x + px * graphViewState.width;
    const anchorY = graphViewState.y + py * graphViewState.height;
    const factor = ev.deltaY < 0 ? 0.9 : 1.1;
    const nextW = Math.min(Math.max(260, graphViewState.width * factor), 6000);
    const nextH = Math.min(Math.max(180, graphViewState.height * factor), 4000);
    graphViewState.x = anchorX - px * nextW;
    graphViewState.y = anchorY - py * nextH;
    graphViewState.width = nextW;
    graphViewState.height = nextH;
    setViewBox();
  }, { passive: false });

  svgEl.addEventListener("mousedown", (ev) => {
    if (!(ev.target === svgEl || ev.target === panSurface || ev.target.classList?.contains("graph-pan-surface"))) return;
    graphDragState = { startX: ev.clientX, startY: ev.clientY, viewX: graphViewState.x, viewY: graphViewState.y };
  });
  svgEl.addEventListener("mousemove", (ev) => {
    if (!graphDragState) return;
    const rect = svgEl.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const dx = ((ev.clientX - graphDragState.startX) * graphViewState.width) / rect.width;
    const dy = ((ev.clientY - graphDragState.startY) * graphViewState.height) / rect.height;
    graphViewState.x = graphDragState.viewX - dx;
    graphViewState.y = graphDragState.viewY - dy;
    setViewBox();
  });
  svgEl.addEventListener("mouseup", () => { graphDragState = null; });
  svgEl.addEventListener("mouseleave", () => { graphDragState = null; });
}

function buildGraphLegend() {
  const leg = $("graphLegend");
  leg.replaceChildren();

  const items = [
    { color: PRIO[0], label: "Urgent" },
    { color: PRIO[1], label: "High" },
    { color: PRIO[2], label: "Normal" },
    { color: PRIO[3], label: "Low" },
    { color: "var(--st-done)", label: "Cleared edge", isLine: true },
    { color: "var(--warn)", label: "Blocking edge", isLine: true, dashed: true },
    { color: "var(--st-done)", label: "Done task", isDot: true },
    { color: "var(--warn)", label: "Blocked task", isDot: true },
  ];

  for (const item of items) {
    const row = el("div", { class: "graph-legend-row" });
    if (item.isDot) {
      row.append(el("span", { class: "graph-legend-dot", style: "background:" + item.color }));
    } else if (item.isLine) {
      const line = el("span", { class: "graph-legend-line" + (item.dashed ? " dashed" : ""), style: "background:" + item.color });
      row.append(line);
    } else {
      row.append(el("span", { class: "graph-legend-swatch", style: "border-color:" + item.color }));
    }
    row.append(el("span", { class: "graph-legend-label" }, item.label));
    leg.append(row);
  }
}

// graphMaybeRefresh is called from onEvent (below) when the graph overlay is open.
const GRAPH_REFRESH_KINDS = new Set([
  "task.dep_added", "task.dep_removed", "task.status", "task.created", "task.deleted",
  "task.assign", "task.patched",
]);

function graphMaybeRefresh(ev) {
  if (!graphOpen || !graphSlug || !graphData) return;
  if (!GRAPH_REFRESH_KINDS.has(ev.kind)) return;
  // Skip if the event clearly belongs to a different project.
  if (ev.task_id) {
    const node = graphData.nodes.find((n) => n.id === ev.task_id);
    if (node && node.project !== graphSlug) return;
  }
  if (graphRefreshTimer) clearTimeout(graphRefreshTimer);
  graphRefreshTimer = setTimeout(async () => {
    if (!graphOpen) return;
    const prevSel = graphSelectedId;
    await fetchAndRenderGraph(graphSlug);
    // Restore selection if the node still exists after refresh.
    if (prevSel != null && graphData && graphData.nodes.find((n) => n.id === prevSel)) {
      graphSelectedId = prevSel;
      rebuildFocusAfterDetailNav();
      const n = graphData.nodes.find((x) => x.id === prevSel);
      if (n) {
        const nodesById = new Map(graphData.nodes.map((x) => [x.id, x]));
        renderGraphDetail(n, graphData.edges, nodesById);
      }
    }
  }, 350);
}

// Focus trap for graph overlay.
function graphTrapFocus(ev) {
  if (ev.key !== "Tab" || $("graphOverlay").classList.contains("hidden")) return;
  const f = $("graphOverlay").querySelectorAll('a[href],button,select,textarea,input,[tabindex]:not([tabindex="-1"])');
  const list = Array.from(f).filter((x) => !x.disabled && x.offsetParent !== null);
  if (!list.length) return;
  const first = list[0], last = list[list.length - 1];
  if (ev.shiftKey && document.activeElement === first) { ev.preventDefault(); last.focus(); }
  else if (!ev.shiftKey && document.activeElement === last) { ev.preventDefault(); first.focus(); }
}

// Wire up graph overlay controls.
$("graphBtn").onclick = openGraphOverlay;
$("graphClose").onclick = closeGraphOverlay;
$("graphReset").onclick = () => {
  graphViewState = { ...graphInitialView };
  $("graphSvg").setAttribute("viewBox", viewBoxStr(graphViewState));
};
$("graphOverlay").addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") { ev.stopPropagation(); closeGraphOverlay(); }
  graphTrapFocus(ev);
});
