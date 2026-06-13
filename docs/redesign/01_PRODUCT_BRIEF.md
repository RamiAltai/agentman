# 01 — Product Brief: agentman Dashboard Redesign

**Product:** agentman — a real-time technical operations dashboard for monitoring
agent/task activity across projects and categories.
**Stack reality:** no-build static SPA (vanilla HTML/CSS/JS), embedded in a Go binary
via `embed`, served as static files. REST under `/api/*`, live updates over SSE at
`/api/stream?since=…`. Theme via `data-theme` + CSS custom properties.
**Author:** Product Manager (lifecycle subagent 1)
**Status:** Direction-setting. No code changes in this stage.

---

## 1. Current-state diagnosis

### What the UI does today

The dashboard is a three-region app shell: a fixed **header** (brand, breadcrumb,
project tabs, search, label-filter chip, filter popover, theme toggle, graph button,
"+ Task", live-status pill, activity toggle), a flexible **main** area, and a
collapsible/resizable **activity feed** drawer on the right.

Routing is hash-based with three views:

- `#/` — **Overview** (the landing view): a grid of category cards, each showing
  per-status counts (todo/doing/blocked/done) and up to six active-agent avatars,
  plus an "All" card and a dashed "+ New category" card.
- `#/all` — **cross-category Kanban board**.
- `#/cat/<slug>` — a **single category's board**.

The **board** is four fixed columns (Todo, In Progress, Blocked, Done) rendered as
cards. Cards carry: id, priority chip + left border color, title (clamped to 3
lines), assignee avatar/name (or "Unassigned"), project tag, up to 3 label chips
(click to filter), comment count, a lock badge (`🔒 N` open prereqs) or a `✓ Ready`
tag, and a `⏳ stale` badge for `doing`+assigned cards idle >30 min. Cards are
**drag-and-drop** between columns and keyboard-movable with `[` / `]`. The Done
column truncates at 50 with a "+N more" note.

The **task modal** (`.sheet`, user-resizable) is a full editor: inline-editable
title/status/assignee/priority/body, a **Dependencies** section (depends-on chips
with add/remove + read-only "Blocks" list), **Labels**, editable **Meta** key=value
pairs, **Comments** (add + two-step delete), **History**, and a footer with
**Release** and two-step **Delete**. There are also create/edit/archive/delete
modals for projects and categories ("Manage").

The **dependency graph overlay** is a hand-rolled SVG: Kahn's-algorithm longest-path
layering for connected tasks, a compact grid lane for dependency-free tasks,
pan/zoom, click-to-focus upstream/downstream highlighting, a detail side-panel, and
a legend. It live-refreshes on relevant SSE events.

**Live behavior:** an `EventSource` reconnects with exponential backoff + jitter; a
status pill shows live/reconnecting/error. Incoming events prepend to the feed
(trimmed at 200, unless the user paginated "older"), debounce-reconcile the board,
and refresh the open modal/graph/overview as applicable.

### User jobs it supports today

- Monitor board health at a glance (overview counts + active agents).
- Triage and move work (drag/keyboard status changes).
- Inspect a task in depth (modal with full history + dependencies).
- Track who is doing what, live (feed + assignee avatars + status pill).
- Reason about blocking relationships (badges + graph).
- Administer the taxonomy (projects, categories, archive/delete).
- Filter/search to find specific work (search, label, ready/blocked/stale/assignee/meta).

### Where usability likely suffers

- **Density vs. legibility tension.** The card foot can hold 6+ chips (assignee,
  project, 3 labels, comments, lock/ready, stale) that wrap unpredictably; visual
  priority among them is flat — the operationally critical signals (blocked, stale)
  don't reliably win attention.
- **Status discoverability.** Status lives in column position + a tiny swatch; there
  is no per-card status text. Priority is a thin left border + an uppercase chip only
  for Urgent/High (Normal/Low show nothing), so priority reads as near-invisible for
  most cards.
- **Scanning cost.** Columns are uniform; nothing guides the eye to the rows that
  need action. Stale/blocked items sink into the sort order (priority, then id).
- **Filter affordances are scattered:** search box, a separate label chip, and a
  filter popover with overlapping concepts (assignee "Mine" vs. the CLI's own
  identity) — discoverability and "what is currently filtered" are weak.
- **Feed legibility.** Event rows are terse and monochrome-ish; distinguishing a
  `done` from a `blocked` from a `comment` relies on a small colored dot.
- **Graph readability** degrades with node count; labels truncate hard at 22 chars ×
  2 lines, and the emoji indicators (🔒/✓) carry meaning that should not be
  color/emoji-only.
- **Light theme is a single override block** and is less battle-tested than dark;
  some accents (warn/stale ambers) risk weak contrast on light surfaces.

### What is RISKY to change

- **The `el()`/`svg()` no-`innerHTML` convention.** `web_test.go` fails the Go build
  if `.innerHTML`, `.outerHTML`, `.insertAdjacentHTML`, `document.write`, or `eval(`
  appears in `app.js`/`index.html`. All DOM must stay builder-based.
- **Every id/selector/data-attribute `app.js` depends on** (see §6 inventory). The
  parity test also greps for specific strings (`openNewCategory`, `newCatCard`,
  `renderManageCategories`, `openEditProject`, `btn-edit-proj`, `vault_project_id`,
  `filterReady/Blocked/Stale/MetaKey`, `renderFilterPanel`, `patchMeta`,
  `meta-add-row`, `btn-release`, and CSS classes `.filter-panel`, `.meta-add-row`,
  `.btn-release`, `.cat-card-add`, `.cat-row`).
- **Theme wiring:** the inline FOUC-guard script reading `localStorage.getItem("am.theme")`,
  `:root[data-theme="light"]` block, and `id="themeToggle"` are all test-locked.
- **Optimistic move + SSE reconcile** and the debounce timers — easy to regress into
  flicker or lost updates.
- **The graph layout/pan-zoom math** — subtle and stateful (selection restore across
  live refresh, listener-leak guards).

---

## 2. Primary personas

1. **Operator (monitoring progress).** Watches the board/overview to know if work is
   flowing. Wants instant board-health read, live activity, and to spot anything
   stuck — without reading every card. Often on a wide monitor, board left open all
   day. Low tolerance for noisy motion.

2. **Engineer (debugging agent behavior).** Drills into a single task: reads history,
   comments, meta, dependency chain; checks why an agent is blocked or stalled.
   Wants depth, exact timestamps, and traceable cause-and-effect. Keyboard-heavy.

3. **Reviewer (inspecting completed/blocked work).** Audits the Done and Blocked
   columns, verifies dependencies cleared in the right order, reads outcomes. Wants
   to scan completed/blocked sets quickly and confirm correctness, not to edit.

4. **Coordinator / triage lead (inferred).** Creates and shapes work: new tasks,
   priorities, dependencies, labels; manages projects/categories; releases stuck
   claims back to the pool. Cares about taxonomy hygiene and unblocking the queue.
   The Release button, dependency editor, and Manage modal exist for this person.

5. **Glanceable bystander / "wallboard" viewer (inferred).** May have the overview on
   a shared screen. Needs the highest-level signal (counts, who's active, anything
   red) to be readable from across a room.

---

## 3. User goals

- **Understand board health** in one glance — totals per status, momentum, where the
  pile-ups are, who is active.
- **Find blocked / stale / failed work fast** — the items that need a human are the
  hardest to lose.
- **Track live activity** — see claims, status moves, comments, completions as they
  happen, with enough context to know if action is needed.
- **Inspect task detail** — full history, dependencies, comments, meta, with exact
  times and clear edit affordances.
- **Understand dependencies** — what blocks what, what's ready now, what a change
  would unblock — both inline (badges) and in the graph.
- **Act without losing context** — move/claim/release/comment/relabel without
  navigating away or losing scroll/selection/filter state; keep real-time updates
  flowing while acting.

---

## 4. Dashboard success criteria

- **Clear hierarchy.** A deliberate type/space/color scale so the eye lands on
  status → priority → blockers → metadata in that order. Critical operational state
  (blocked, stale) visually outranks decorative metadata.
- **Fast scanning.** A user can sweep a column and identify "needs attention" rows in
  under a couple of seconds; counts and statuses are legible without hovering.
- **Reduced cognitive load.** Consistent chip vocabulary, predictable card layout
  (no unpredictable wrap reshuffles), one obvious place to see "what is filtered."
- **Obvious status & priority.** Status is unambiguous beyond column position;
  priority is encoded redundantly (not color-alone) and readable for all four levels.
- **Smooth real-time updates.** Live changes apply without layout jank, without
  yanking the user's scroll/focus/selection, and with motion that informs rather than
  distracts (and fully respects `prefers-reduced-motion`).
- **Accessible interactions.** Keyboard parity for all primary actions, visible focus,
  correct roles/labels/live-regions, status/priority not conveyed by color or emoji
  alone, WCAG AA contrast in **both** themes.
- **Responsive.** Usable from narrow (single-column board, off-canvas feed) to
  ultrawide (centered, capped columns) without horizontal-scroll traps.
- **No backend regressions.** Identical REST/SSE contract, static-embed model, and
  every selector/id/data-attribute `app.js` relies on preserved.

---

## 5. Prioritized scope

### Must-have
- **Visual-hierarchy overhaul of the card** — redundant, legible status + priority;
  promote blocked/stale to first-class signals; tame chip wrap; stable layout.
- **Refined design-token system** (spacing, type scale, elevation, semantic status
  colors) expressed entirely in existing CSS custom properties, fully themed for
  light **and** dark with AA contrast.
- **Column / board hierarchy pass** — clearer column headers, counts, and visual
  separation; calmer surfaces; "needs attention" items easier to find.
- **Activity feed legibility** — clearer event typing (icon + text, not color-only),
  better timestamp/scan rhythm, readable empty/paginated states.
- **Consistent, discoverable filter/search surface** — one clear "currently filtered"
  state combining search + label + board filters.
- **Accessibility hardening** — non-color status/priority cues, focus visibility,
  ARIA correctness, motion-reduction compliance, keyboard parity preserved.
- **Real-time polish** — updates that don't jank or steal focus/scroll.
- **Preserve 100% of test-locked selectors/ids/data-attributes and theme wiring.**

### Should-have
- **Overview cards** sharper as a wallboard read (bigger numbers, clearer agent
  presence, stronger "anything red" cue).
- **Task modal** information architecture pass — group/sequence sections, clearer
  section headers, better dependency/meta legibility.
- **Graph readability** — better node labels, non-emoji-only ready/blocked encoding,
  clearer focus dimming, legend contrast in both themes.
- **Density toggle or sensible defaults** for dense-but-not-cramped on big screens.

### Could-have
- Subtle, opt-in "new since you looked" affordance on the feed/board.
- Per-column collapse, or sticky column headers on long lists.
- Saved/quick filters.

### Non-goals (explicit)
- **No framework, no build step, no npm, no bundler, no external CDN / web fonts.**
- **No new backend endpoints, payload changes, or auth changes.** Front-end-only.
- **No change to the REST/SSE/static-embed architecture** or the hash-route model.
- **No removal/renaming** of existing ids, selectors, data-attributes, event kinds,
  or the parity affordances the Go tests assert.
- **No drag-and-drop library, charting library, or graph library** — the SVG graph
  stays hand-rolled.
- **No multi-user identity/auth, no persistence schema changes, no real avatars/images.**
- Not a feature-expansion stage: this is a visual/UX modernization, not new
  capabilities.

---

## 6. UX risks

- **Over-polishing vs. density.** Generous whitespace and large radii can cut the
  number of cards visible per column, hurting the operator's at-a-glance job. Target
  *dense-but-not-cramped*; validate card counts per column at common heights.
- **Hiding operational state.** "Calm" must not bury blocked/stale/failed signals.
  Risk: muting everything equally makes the 5% that matters invisible. Keep a loud,
  reserved channel for trouble.
- **Keyboard regressions.** `[`/`]` card moves, Enter/Space to open, `/` `n` `g` `a`
  global keys, modal/graph focus traps, and the resize-handle arrow keys must all
  survive. Easy to break by re-structuring DOM or focus order.
- **Breaking existing selectors / JS assumptions.** `app.js` queries many ids,
  classes, and `data-*` (and `web_test.go` greps strings). Any rename is a
  build-breaking or runtime-breaking regression. Treat the inventory below as frozen.
- **Dependency-graph legibility.** Re-styling nodes/edges risks the layout math,
  selection-restore-on-refresh, and pan/zoom. Color-only edge/node semantics
  (cleared/blocking, done/blocked) must gain a non-color cue.
- **Over-animating live updates.** Flashy enter/exit animations on a feed that fires
  often, or card-reflow transitions on every debounced reconcile, become distracting
  and can fight scroll position. Motion must be subtle, purposeful, and reduced-motion
  safe.
- **Light-theme contrast.** The amber warn/stale and accent-on-light combinations are
  the likeliest AA failures; verify every semantic token in both themes.
- **Focus/scroll theft on reconcile.** Board re-renders (`renderBoard`) replace all
  children; preserving the user's scroll and not stealing focus during live reloads
  must be respected.

### Frozen selector/id/data inventory (do not rename)

- **IDs:** `board`, `overview`, `tabs`, `breadcrumb`, `searchBox`, `labelFilterChip`,
  `filterBtn`, `filterPanel`, `themeToggle`, `graphBtn`, `newBtn`, `status`,
  `feedToggle`, `feed`, `feedResize`, `feedClose`, `feedList`, `feedBackdrop`,
  `modal`, `sheet`, `graphOverlay`, `graphTitle`, `graphProjectSel`, `graphReset`,
  `graphClose`, `graphSvg`, `graphDetail`, `graphLegend`.
- **Body classes:** `view-overview`, `feed-collapsed`, `resizing`.
- **Data attributes:** `data-theme` (html), `data-status` (col), `data-id` (card),
  `data-from`/`data-to` (graph edges).
- **Structural classes app.js queries:** `.card`, `.col`, `.col.drag-over`,
  `.cards`, `.status-text`, `.theme-toggle-icon`, `.filter-count`, `.filter-wrap`,
  `.feed-empty`, `.mtitle`, `.gnode[data-id]`, `.gedge`, `.btn-cancel-del`,
  `.graph-pan-surface`, plus the parity-test classes `.filter-panel`, `.meta-add-row`,
  `.btn-release`, `.cat-card-add`, `.cat-row`.
- **Theme contract:** inline `localStorage.getItem("am.theme")` FOUC script,
  `:root[data-theme="light"]` block, `#themeToggle`.

---

## 7. Acceptance criteria

**Functional**
- All existing behaviors work unchanged: hash routing (overview/all/category),
  project tab toggling, drag + `[`/`]` status moves with optimistic update + revert,
  search/label/board filters (composed server-side), modal CRUD (title, status,
  assignee, priority, body, deps add/remove, labels, meta, comments, release, delete),
  project/category create/edit/archive/delete, graph open/focus/pan/zoom/reset, feed
  collapse/resize/pagination, theme toggle + OS-follow.
- Live SSE flow intact: connect/backoff/jitter, dedupe by event id, feed prepend +
  trim, debounced board reconcile, modal/graph/overview refresh on relevant kinds.

**Visual**
- Coherent token-driven hierarchy; status and priority unambiguous on every card;
  blocked/stale/ready signals clearly outrank decorative metadata; predictable,
  non-reshuffling card layout; calm surfaces with intentional elevation/spacing;
  dense-but-not-cramped at common viewport heights.

**Accessibility**
- WCAG 2.1 AA contrast for text and meaningful UI in **both** themes.
- Status/priority/dependency state conveyed by more than color or emoji alone.
- Visible `:focus-visible` on all interactive elements; correct roles, `aria-*`
  labels, and `aria-live` regions (status pill, feed, graph detail) preserved.
- Full keyboard parity (all global keys, card keys, focus traps, resize keys).
- `prefers-reduced-motion: reduce` disables all transitions/animations.

**Performance**
- No build artifacts; ships as static `index.html` + `app.css` + `app.js`.
- Board with ~500 tasks and the feed under live event load remain smooth (no jank,
  no scroll/focus theft, no per-event full reflow churn beyond today's behavior).
- Graph remains interactive (pan/zoom/focus) at realistic project sizes.

**Backend compatibility**
- Zero changes to `/api/*` requests/responses, SSE stream format, or the embed model.
- No new network dependencies (no CDN, no remote fonts/scripts).
- Every frozen id/selector/data-attribute (§6) and theme wiring preserved.

**Maintainability**
- DOM stays builder-based (`el()`/`svg()`); **no** `innerHTML`/`outerHTML`/
  `insertAdjacentHTML`/`document.write`/`eval(` — `go test ./cmd/am/...` passes
  (`TestDashboardNoXSSSinks`, `TestDashboardThemeAssets`, `TestDashboardParityAffordances`).
- Theming driven by CSS custom properties; dark is the `:root` default, light is the
  single `[data-theme="light"]` override; an unknown/absent theme renders dark.
- CSS organized and commented; no dead selectors introduced; tokens documented.
