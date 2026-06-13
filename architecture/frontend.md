# Frontend Architecture

There **is** a frontend: a small single-page dashboard in `cmd/am/web/`
(`index.html` 123 lines, `app.css` 1140 lines, `app.js` 2706 lines), embedded into the binary via
`//go:embed web` (`cmd/am/server.go`) and served at `/`. It is the human-facing view; agents do
not use it.

## Framework

**None — intentional.** Vanilla HTML/CSS/JavaScript, no framework, no bundler, no npm, no build
step. The only DOM construction helpers are `el(tag, props, ...kids)` (HTML) and `svg(tag, attrs)`
(SVG) in `app.js`. Both append string content as **text nodes** via `.textContent` (never
`innerHTML`) so agent-supplied text can't inject markup. `svg()` is parallel to `el()` and is
implemented as `document.createElementNS(SVG_NS, tag)` — the same technique used for the
dependency-graph overlay.

## Routing

**Hash routing** (Phase R). It's a single page, but top-level views are now driven by the URL hash
so they are **linkable and the browser back button works**. `route()` is the single hash→state
mapper (called on load and on `hashchange`):
- `#/` → **overview** (the category home, the landing view; also the empty-hash default)
- `#/all` → the **cross-category board** (the original board behavior)
- `#/cat/<slug>` → a **single category's board** (drill-down)

`navigate(hash)` sets `location.hash` (or calls `route()` directly when the hash is unchanged);
`applyView(next, cat)` updates the `view`/`activeCategory` module state, toggles
`body.view-overview`, sets the scope title (`setBreadcrumb`), re-renders the rail, loads the right
data (overview cards vs. board), and **re-opens the SSE stream with the new scope**. A view change
**resets** the within-view project selection (`selected`); the rail navigates to a single project via
`goProject`, which stashes the slug in `pendingProject` so `applyView` re-selects it after clearing
(see Left-rail navigation). Opening/closing the modal and the graph overlay is not part of the hash
route.

## Pages and Components

All built imperatively in `app.js` (no component framework):
- **Category overview (home)** — `loadOverview`, `renderOverview`, `catCard`, `allCard` (Phase R):
  the landing view (`#/`), rendered into the `#overview` section. `loadOverview` fetches
  `GET /api/categories` (now a `CategoryStat[]` carrying `counts` + `active_agents`) into the
  module-level `categories`, then renders a `.cat-grid` of cards. Each `catCard` shows the category
  name, four `.count-chip` count chips (one per status, reusing the `ST` status-swatch colors), and
  an `.active-agents` avatar row (up to 6 initials via `initials()`, then a `+N` overflow; "no
  active agents" when empty). An **"All" card** (`allCard`, `.cat-card-all`, dashed) shows the
  total open count and opens the cross-category board. Cards are `role="link"`/`tabindex=0` and
  drill in on click or Enter/Space (`navigate("#/cat/<slug>")` / `navigate("#/all")`). After the
  category cards a dashed **＋ New category** add-card (`newCatCard`, `.cat-card-add`,
  `role="button"`/`tabindex=0`) opens the create-category modal on click or Enter/Space
  (`openNewCategory`). The overview keeps **one global, unfiltered** recent-activity feed (the
  existing `#feed` aside) — not
  per-card mini-feeds. `body.view-overview` (set by `applyView`) is what hides `#board` and shows
  `#overview`.
- **Left-rail navigation** — `renderRail`, `railItem`, `goProject`: a collapsible left rail
  (`<aside id="rail">`) is the primary navigation; it **replaces the old header project-tab strip
  and the "← Categories" back button**. `renderRail` rebuilds `#railNav` from `categories` +
  `projects` + the current view/selection on every change. It lays out, in order: **Overview**
  (`#/`), **All tasks** (`#/all`, with a total open-count), then one **category section** per
  category (`.rail-section`: a clickable category label `.rail-cat` opening `#/cat/<slug>`, with its
  projects nested beneath as `.rail-project` rows carrying a status dot + open-count); uncategorized
  projects collect under a synthetic non-clickable **"Other"** header (`.rail-section-label`).
  After a divider come two **rail actions** (`.rail-action`): **＋ New project** (`openNewProject`)
  and **⋯ Manage** (`openManage`). Each `railItem` is a `<button>` with `aria-pressed`/`aria-current`
  reflecting the active scope. Clicking a project calls `goProject(p)`, which selects **only** that
  project: if already in the project's category/All view it swaps `selected` in place and reloads;
  otherwise it stashes the slug in `pendingProject` and navigates to the project's category board,
  where `applyView` re-selects it. `toggleProject` is retained only as a vestigial helper.
  Clicking **⋯ Manage** calls `openManage` (the former `openManageProjects`, kept as a back-compat
  alias), which opens the reused `#sheet` modal (same focus-trap / Esc-to-close infrastructure as the
  task modal) with two sections:
  - **Categories** (`renderManageCategories`) — fetches every category including archived ones via
    `GET /api/categories?archived=true` and builds a `.cat-manage-list` of `.cat-row`s (mirroring the
    project list, `el()` only): each row shows name, slug, open-task count, an **Archived** pill when
    archived, and an Archive/Unarchive toggle that calls
    `POST /api/categories/{slug}/archive|unarchive` then refreshes `loadProjects()`, the overview
    (when visible), and the category list in place. There is **no category delete** control (no
    category-delete API).
  - **Projects** (`renderManageList`) — fetches all projects including archived ones via
    `GET /api/projects?archived=true` and builds a list with `el()` (no
    `innerHTML`): active projects show **Edit** + **Archive**; archived ones show an **Archived**
    badge + **Edit** + **Unarchive** (plus the two-step **Delete** below). The archive buttons call
    `POST /api/projects/{slug}/archive|unarchive` via `api()`, then refresh the list. If the
    just-archived project was selected, the rail and board reload automatically. The **Edit**
    button (`btn-edit-proj`) opens the per-project edit sub-modal (`openEditProject`, see below).
- **Search + label filter (header)** — a `#searchBox` input in the top bar's Find segment filters the
  board **server-side** (`?q=` on `GET /api/tasks` — substring match over title *and* body, which
  the client couldn't do since the list payload has no body). Input is **debounced 250 ms**
  (`searchTimer`) before `loadBoard()` re-fetches; the **`/`** keyboard shortcut focuses the box.
  Because the filter is applied in `loadBoard()` (state in `filterQ`/`filterLabel`, deliberately
  *not* in the shared `qstr()` used by the feed/SSE), the debounced SSE board reload keeps the
  filter across live refreshes. An active label filter shows a **`#labelFilterChip`** chip next to
  the search box (`setLabelFilter`) with a **✕** clear button (`clearLabelFilter`).
- **Lean top bar** — the header is `<header>` inside `.appcol`, holding (left→right) a mobile-only
  hamburger (`#railOpen`), the scope **title** (`#breadcrumb`, an `<h1>`; see below), and the
  `.actions` cluster of three segments: a **Find** segment (`.actions-seg` "Find": the `#searchBox`,
  the `#labelFilterChip`, and the **Filter** popover), the **+ Task** primary button (`#newBtn`,
  `.btn-primary` gradient), an `.actions-sep` hairline, then a **utility cluster** (`.actions-seg
  .actions-util` "View": **Graph** `#graphBtn`, the **theme toggle** `#themeToggle`, the **Activity**
  toggle `#feedToggle`, and the live-status dot `#status`). `#breadcrumb` is just the scope label now
  (`setBreadcrumb` sets it — Overview / All tasks / a category name / the single selected project);
  navigation lives in the rail, so there is **no back button**.
- **Board filters (header popover)** — a single **Filter** button (`#filterBtn`, in the Find segment)
  toggles a popover panel (`#filterPanel`, `role="dialog"`) of server-side board filters
  (`renderFilterPanel`): **Ready** / **Blocked** / **Stale** checkboxes (→ `?ready=true`,
  `?blocked=true`, `?stale=30m` — `STALE_FILTER`, matching the `STALE_MS` stale-badge threshold), an
  **Assignee** text input (→ `?assignee=`) with a **Mine** fill-button that sets it to `human` (the
  fixed `X-Agent` the dashboard sends), and a **Meta key** input (→ `?meta_key=`), plus a **Clear
  all** reset. The flags + inputs all fold into `loadBoard()`'s query string (state in
  `filterReady`/`filterBlocked`/`filterStale`/`filterMine`/`filterMetaKey`, again deliberately not in
  the shared `qstr()`), so they **compose** with the project/category scope, search, and label filter
  and **survive SSE-driven live reloads** with no `onEvent`/`renderBoard` change. `applyFilters`
  persists the panel state to the board without closing the panel (so several toggles can be flipped
  in a row); `renderFilterBadge` keeps a count chip + a `has-filters` highlighted state on the button
  in sync. The panel closes on **outside click** (a lazily-wired `document` click listener,
  `filterOutsideClickWired`) and on **Escape** (which returns focus to the button); status is
  **intentionally not a filter** — the four board columns are the status axis.
- **Board** — `renderBoard`, `card(t)`: four status columns (`COLS`, `.col[data-status=<status>]`).
  Status is the dominant axis: the column header is **status-tinted**, the **Blocked column** carries
  a subtle warm tint (`.col[data-status="blocked"]`), and each card's **left edge is status-colored**
  (`.col[data-status=…] .card { border-left-color }`) — priority no longer drives the edge. A card is
  three rhythmic rows:
  - **Row 1 (head)** — `#id`, an **always-present priority rank chip** for all four levels
    (`.chip-prio.p0..p3`, **solid/filled** via `--prio-N` background + `--chip-prio-ink` text;
    `PRIO_RANK` = `P0..P3`, `PRIO_WORD` titles), and a labeled **status pill** (`.status-pill.st-…`:
    a dot + the status word over a soft fill — the redundant, non-color-only status cue).
  - **Row 2** — the title (`.ctitle`, line-clamped).
  - **Row 3** — three sub-rows in fixed order: `.cfoot` (assignee avatar + name · project `.ptag`,
    shown only when `selected.size !== 1`); a **reserved trouble sub-row** `.ctrouble` rendered
    *before* labels so critical signals never sink beneath decorative chips — **🔒 Blocked N**
    (`.tag-blocked`, when `t.nopen > 0`) or **✓ Ready** (`.tag-ready`, when `t.nprereq > 0`), plus an
    amber **⏳ Stale** chip (`.tag-stale`) for a `doing`+assigned card idle 30+ min
    (`Date.now() - Date.parse(t.updated_at) > STALE_MS`, `STALE_MS = 30 * 60 * 1000`, purely
    client-side); and `.ctags` (label chips `.tag-label`, at most 3 then a **`+N`** overflow, then
    the comment count). Clicking a label chip calls `setLabelFilter(l)` (the chips are
    `role="button"`/`tabindex=0` and stop click propagation so the card doesn't open). Both `.ctrouble`
    and `.ctags` collapse via `:empty { display:none }`. Blocked/Ready are derived from server counts
    (`nprereq`/`nopen`); there is no stored "ready" status. A reconciled card gets a one-shot
    `.is-updated` flash (`applyFlash`/`flashIds`, suppressed under reduced motion).
- **Activity feed** — `feedItem`, `evText`, `evKind`: a 3-column grid (`.ev`: glyph · text · time)
  of **typed events**. Each event kind gets a **glyph + the event stated in words**: `feedItem` adds
  an `.ev-icon` cell from `EV_GLYPH` (`claimed ▸`, `status ⇄`, `done ✓`, `blocked ⏸`, `comment 💬`,
  `other •`; decorative/`aria-hidden`, the meaning is in `.ev-text`) and the `.k-<kind>` class colors
  the glyph. Events carry clickable `#refs`.
  Event kinds include the project lifecycle: `project.created`, `project.archived`,
  `project.unarchived` (render via `evText`/`describeText`; `evKind` colors them as generic "other"),
  the category lifecycle: `category.created`, `category.archived`, `category.unarchived` —
  these carry **no project/task ref** (NULL `project_id`), so they have explicit cases in both
  `evText` and `describeText` ("who archived category slug"; the default branch would render a
  literal "null" ref). `project.patched` deliberately falls through to the default rendering (it
  has a project ref). The delete kinds: `task.deleted`, `comment.deleted`, `project.deleted`. A
  `task.reclaimed` event (stale-claim takeover) renders as *"X reclaimed #N from Y"* (the previous
  assignee comes from `data.assignee[0]`) and is colored like a claim (`evKind` maps it to
  `"claimed"`). `task.labeled` / `task.unlabeled` events render as *"X labeled #N +l"* /
  *"X unlabeled #N -l"* (new cases in both `evText` and `describeText`). `task.patched` lines
  append **`(meta: k1, k2)`** — the sorted changed keys — when the event delta contains a `meta`
  sub-object (Phase P; in both `evText` and `describeText`). The feed supports
  **backward pagination** via a "Load older activity" button appended **outside** `#feedList` (so
  `trimFeed` can't remove it); clicking it fetches `GET /api/events?before=<oldest-loaded-id>` and
  appends the results. `feedOldest` tracks the lowest event id currently in the feed; `feedPaginated`
  is set to `true` on the first paginated fetch, which causes `trimFeed` to skip its cap so the user's
  loaded history is not silently discarded. Trade-off: a long-running tab that paginates can grow
  the feed unbounded until the next page reload. When `?before=` returns no events, the button is
  replaced by a `"— start of activity —"` end-marker. All DOM via `el()` (no `innerHTML`).
- **Detail modal** — `renderModal`, plus `openNew` (new task), `openNewProject`, `openNewCategory`,
  and `openEditProject`: one reused
  `#sheet` element; auto-growing title `<textarea>`; status/assignee/priority controls; comments;
  history. **`openNewCategory`** mirrors `openNewProject` (name + auto-derived slug via `slugify`)
  but POSTs `/api/categories` `{slug,name}`, reloads the overview, and closes; a slug conflict
  surfaces as *a category with slug "<slug>" already exists*. **`openNewProject`** now has a
  required **Category** `<select>` populated from `GET /api/categories` (fetched lazily on the board
  views where the overview hasn't loaded it), defaulting to the current view's category on a
  category board, else `general` (falling back to the first known category if `general` is absent,
  or to a single `general` option if the list can't be fetched); the create POST carries
  `category: csel.value`. (This reverses the Phase O "category-unaware by design" decision —
  ADR-031, ADR-025; the server-side empty→`general` mapping stays as a compatibility fallback.)
  **`openEditProject`** edits a project's Name, Slug (a safe **uid-keyed rename**, NOT auto-derived),
  Vault project id, and Vault path; Save PATCHes `/api/projects/{slug}` with **only the changed
  fields** (a no-op edit just closes), and on a slug change the selection follows the new slug. Its
  errors surface as *slug "<slug>" is taken* (conflict) or *check name/slug (no spaces or /)*
  (validation). The modal includes a **Delete task** button (inline two-step confirm — see below) and
  each comment has a **× delete** button (also two-step). The modal also has a **Dependencies**
  section (`depsSection`):
  - **"Depends on"** — one chip per prerequisite showing a status dot, `project-ref` (clickable
    link to open that task), title, status text, and a **✕ remove** button
    (`DELETE /api/tasks/{id}/deps/{depId}`). Open prereqs get class `dep-open`; done ones get
    `dep-done`. If none, shows "None".
  - **"Add prerequisite…"** — a `<select>` dropdown lazily populated with same-project tasks
    (excludes self and already-linked tasks). Selecting a candidate calls
    `POST /api/tasks/{id}/deps {depends_on: id}` and refreshes the modal; an inline error element
    shows the rejection message (e.g. "would create a dependency cycle").
  - **"Blocks"** — a read-only list of tasks that depend on this one (`t.blocks`); each row shows
    the ref link, title, and status.
  Hard-block UX: if a claim or status-change to `doing`/`done` is rejected with a 409 `blocked`
  response, the dashboard surfaces the blocking prereq ids (e.g. "blocked by #1 #2 (prereq not
  done)") and reverts the card/modal to its previous state.
  The modal also has a **Labels** section (`.labels-row`): one chip per label with a **✕ remove**
  button (`DELETE /api/tasks/{id}/labels/{label}`), plus an **"Add label…" input** that submits on
  **Enter** (`POST /api/tasks/{id}/labels {label}`); a validation 400 shows an inline `.ferr` hint
  ("labels are 1-50 chars of a-z 0-9 . _ -").
  After Labels comes an **editable Meta section** (`.meta-section`; was read-only through Phase P,
  made editable in ADR-031): one `.meta-row` per existing pair (keys sorted; `.meta-key` muted,
  `.meta-val` monospace) each with a **✕ remove** button, plus a `.meta-add-row` of **key** + **value**
  inputs and an **Add** button (Enter in either input also adds). Both paths go through the
  `patchMeta(id, key, value)` helper — adding sends `PATCH /api/tasks/{id}` `{meta:{<key>:<value>}}`;
  removing sends an empty value (`{meta:{<key>:""}}`) which deletes the pair. `patchMeta` uses the
  **raw `api()`** call rather than the shared `patch()` helper, because `patch()`'s success path
  refreshes the modal and would wipe the inline error / in-progress add inputs — the SSE
  `task.patched` echo re-renders the section from server truth on its own. A validation error shows
  inline (`metaErrMsg`) and now names both cases: *meta key must be 1-50 chars of a-z 0-9 . _ - and
  value ≤500 chars*. All DOM via `el()`/`textContent`.
  The modal's delete row also has a **Release** button (`btn-release`, shown only when the task has
  an assignee or isn't in `todo`): one PATCH of `{assignee:"", status:"todo"}` returns the task to
  the unclaimed pool (the `am drop` equivalent), pushing **Delete** to the right edge of the row.
- **Dependency-graph overlay** — `openGraphOverlay` / `closeGraphOverlay` / `renderGraph` /
  `renderGraphDetail`: a full-screen overlay (`#graphOverlay`) that visualises the task dependency
  DAG for a project. Entry points: the **"Graph"** button in the header `.actions` (`#graphBtn`)
  and the **`g`** keyboard shortcut (suppressed while the user is typing in an input/textarea).
  Reuses the modal focus-trap + `Esc`-to-close infrastructure.
  - **Layout** — `computeGraphLayout` implements a **topological longest-path layering** using
    Kahn's algorithm: prerequisites are placed to the left, dependents to the right. Tasks that
    have no dependency edges at all are collected into a compact grid **"No dependencies" lane**
    rendered below the DAG so isolated tasks don't pile into one tall column. All tasks in the
    project are shown regardless of status.
  - **SVG renderer** — **pure vanilla SVG, no library, no npm**. Elements are created with the new
    `svg(tag, attrs)` helper (`createElementNS`), parallel to `el()`. Edges are cubic Bézier
    curves with `<marker>` arrowheads. The canvas supports **pan (drag)** and **zoom (wheel)**
    controlled via `viewBox` manipulation; a **"Reset view"** button restores the initial viewport.
  - **Encoding** — nodes are colored by task **priority** (the `PRIO` palette). Each node shows a
    status dot and a **Ready** (✓) or **Blocked** (🔒) indicator when applicable. Edges are
    colored by prereq-satisfied state: a `done` prerequisite → **green solid** ("cleared"); an
    open prerequisite → **amber dashed** ("blocking"). A **bottom-left legend** explains both axes.
  - **Interaction** — clicking a node applies **transitive highlight**: the node's full
    **upstream ancestor path** ("what leads to it") and **downstream subtree** ("what it unblocks")
    light up in distinct accent colors while all other nodes dim. Clicking the empty canvas clears
    the selection. The **right detail panel** (`#graphDetail`, built with `el()`) shows the clicked
    task's title, status, priority, assignee, Ready/Blocked state, a clickable **Prerequisites**
    list, a clickable **Unblocks** list, and an **"Open task"** button that opens the existing
    task-detail modal. Clicking entries in the Prerequisites or Unblocks lists navigates the
    selection within the graph.
  - **Project selector** — a `<select>` (`#graphProjectSel`) in the overlay header defaults to
    the currently selected project and lets the user switch to any project without closing the
    overlay.
  - **Live refresh** — while the overlay is open, `graphMaybeRefresh` is called from `onEvent`
    for events that affect the displayed project (`task.dep_added`, `task.dep_removed`,
    `task.status`, `task.created`, `task.deleted`, `task.assign`, `task.patched`,
    `task.reclaimed` — the `GRAPH_REFRESH_KINDS` set). It
    **debounces** re-fetches and **preserves the current pan/zoom state and selection**.
  - **XSS-safe** — SVG built via `svg()` + `.textContent`; the detail panel via `el()`. No
    `innerHTML` anywhere in the graph code (the `TestDashboardNoXSSSinks` guard passes).
- **Delete affordances** — three inline two-step confirms (no native `confirm()`/`prompt()` — they
  are blocked in webviews; all DOM built via `el()`, no `innerHTML`):
  1. **Delete task** (`btn-danger-task`) in the task modal — on first click shows "Confirm delete?";
     a 4-second timeout resets it; second click calls `DELETE /api/tasks/{id}`.
  2. **Per-comment ×** (`btn-del-cm`) on each comment row — same two-step flow; calls
     `DELETE /api/tasks/{id}/comments/{cid}`.
  3. **Delete project** (`btn-danger-proj`) in the Manage-projects modal — distinct from the Archive
     button; two-step with a 5-second timeout; calls `DELETE /api/projects/{slug}`.
  All three are irreversible hard deletes (cascade for projects/tasks).

## State Management

Module-level mutable variables in `app.js` (no store/framework):
`view` (`"overview"` | `"all"` | `"category"`, Phase R — the current top-level view, driven by the
URL hash), `activeCategory` (category slug when `view === "category"`), `categories`
(`CategoryStat[]` for the overview — counts + active_agents; the rail tree reads it too),
`overviewTimer` (debounce for the live overview count refresh), `railTimer` (debounce for the live
rail open-count refresh, `refreshRailCounts` re-fetching projects + categories), `pendingProject`
(a project slug stashed by `goProject` to auto-select after the next `applyView`),
`projects` (array), `selected` (`Set<slug>` of active project filters within the current view,
empty=all), `tasks`
(`Map<id,task>`), `cursor` (highest seen `events.id` for SSE `since=`), `es` (EventSource),
`openTaskId`, `dragId`, `lastFocus`, `feedOldest` (lowest event id currently in `#feedList`; `0`
if none loaded), `feedPaginated` (`true` once the user has paginated; disables `trimFeed` cap),
`loadOlderBtn` (reference to the "Load older" button outside `#feedList`), `filterQ` /
`filterLabel` (active server-side search/label filters, applied by `loadBoard()`), `searchTimer`
(the search box's 250 ms input debounce), and the board-filter popover state
`filterReady` / `filterBlocked` / `filterStale` (bool toggles) / `filterMine` (assignee string) /
`filterMetaKey` (meta-key string), all applied by `loadBoard()`, plus their per-input debounce
timers `filterMineTimer` / `filterMetaTimer` (separate from `searchTimer` so the two boxes don't
cancel each other) and `filterOutsideClickWired` (lazily wires the panel's outside-click closer
once). Graph overlay state:
`graphOpen` (bool), `graphSlug` (slug of the project currently shown), `graphData`
(`{nodes, edges}` from the last fetch), `graphViewState` / `graphInitialView` (current and
reset-target `viewBox`), `graphSelectedId` (currently highlighted node id), `graphDragState`,
`graphRefreshTimer`, `graphLastFocus`, `graphPanZoomInstalled`. **Persisted UI prefs** live in
`localStorage` (via `lsGet`/`lsSet`): `RAIL_COLLAPSED_KEY` (`am.railCollapsed`, the desktop rail
collapse state — `setRailCollapsed` toggles `body.rail-collapsed`; on mobile the rail is off-canvas
and `toggleRail`/`openMobileRail`/`closeMobileRail` slide it via `body.rail-open` behind
`#railBackdrop`), `FEED_W_KEY`/`FEED_COLLAPSED_KEY` (activity drawer), and `THEME_KEY` (`am.theme`).
Reconciliation is **snapshot-based**:
on each SSE event the feed updates immediately and a **debounced (250 ms) full `loadBoard()`**
re-fetches and re-renders (`onEvent`). The graph overlay uses its own debounced re-fetch
(`graphRefreshTimer`) when `graphMaybeRefresh` fires. Simple and correct over clever diffing.

## API Integration

- **`api(method, path, body)`** — `fetch` wrapper; always sends `X-Agent: human`; throws on non-2xx
  with the server's `error` field.
- **Scope params** — `viewParams()` (Phase R) is the single source of the scope query for board,
  feed, and stream calls: a single selected project always wins (`?project=`); otherwise a
  **category view** scopes to its category (`?category=<activeCategory>`); the `#/all` view and the
  overview's global feed are unscoped. `qstr()` and `loadOlderActivity` both apply it.
- **SSE** — `connect()` opens `EventSource('/api/stream?since=<cursor>')`, with `&project=<slug>` OR
  `&category=<slug>` appended by `qstr()` per `viewParams()` (single project → `project`; otherwise
  a category board → `category`; `#/all`/overview → unfiltered, filtered client-side via
  `selected.has(t.project)`). The stream is **re-opened on every view change** (`applyView` calls
  `connect()` after loading), so the live scope always matches the view. `onmessage` → `onEvent`;
  `onerror` → close + reconnect with exponential backoff (1s→10s) and a "reconnecting…" status.
  `loadFeed()` bootstraps from `/api/events?tail=50` (same `qstr` rule). On the **overview**,
  `onEvent` keeps the global feed live and, on any `task.*`/`project.*`/`category.*` event,
  **debounce-refreshes (250 ms) the category cards** via `loadOverview()` — the debounced callback
  re-checks `view === "overview"` at fire time so navigating away before it elapses never writes to
  the now-hidden `#overview`. `onEvent` rebuilds the **rail** (`loadProjects()` → `renderRail()`) on
  `category.created` (in addition to `project.created`/`project.unarchived`/`category.archived`/
  `category.unarchived`) so a new category/project appears live, and keeps the rail's open-counts
  live on the board views via `refreshRailCounts` (a debounced projects+categories re-fetch on any
  `task.*`/`project.*`/`category.*` event). `onEvent` handles the three delete kinds:
  `task.deleted` removes the card from `tasks` map and closes the modal if it was open;
  `comment.deleted` refreshes the open modal; `project.deleted` drops the slug from `selected`,
  re-renders the rail, and reloads the board/feed; `category.archived`/`category.unarchived` (like
  `project.created` and `project.unarchived`) trigger `loadProjects()` so the rail reflects the
  archive cascade live. For `task.dep_added` and `task.dep_removed`, `onEvent` refreshes the
  open modal if either the task or the referenced prereq is currently open (so both sides of the
  edge see the update), then triggers the debounced board reload.
- Same-origin only; no CORS, no auth token (the API is unauthenticated).

## Styling and Design System

`app.css` opens with a **token-based CSS custom-property design system** (`:root`). Tokens cover:
surface/line levels (`--bg`/`--surface`/`--surface-2`/`--col-bg`/`--card-bg`/`--card-hover`/`--line`/
`--line-strong`), text (`--fg`/`--muted`/`--faint`), and a **bold, vivid violet brand**: `--accent`
is violet (`#7d5cff` dark / `#6a40e0` light); all accent **text** uses `--accent-strong`
(`#b9a8ff` / `#5a31c8`) for WCAG-AA contrast; the **+ Task** primary is the `--accent-btn` gradient;
and a faint radial violet **`--app-glow`** sits behind the app background. Status tokens
(`--st-todo/doing/blocked/done`, their `*-soft` pill fills, and `--st-blocked-col` column tint) and
**priority** tokens (`--prio-0..3`, mirroring the JS `PRIO`/`PRIO_VAR` palette, painted as solid chip
fills with `--chip-prio-ink` for the text) each carry a consistent, AA-clearing language across board,
cards, feed, and graph in both themes. The system also defines reusable **scales** — type (`--fs-*`,
`--lh-*`), spacing (`--sp-1..8`, 4 px base), radii (`--r-xs..xl`, `--r-pill`), elevation (`--elev-*`,
`--shadow`), motion (`--dur-*`, `--ease`) — layout tokens (`--feed-w`, `--header-h`, `--col-min`,
`--rail-w`/`--rail-bg`), and **component color tokens** (backdrops, tag pill backgrounds/borders, the
status pulse ring, `--on-accent`, the scrollbar-hover color, the graph legend background, the
danger-confirm wash) factored out of inline literals so the light override block can re-theme them.
Thin custom scrollbars throughout.

**Theming — dark default + light override.** The dashboard supports a **dark (default) and a light
theme**, selected by a single `data-theme` attribute on `<html>`. Dark is the bare `:root`; light is
**one `:root[data-theme="light"]{…}` block** that re-defines the color tokens — so an unknown or
absent `data-theme` renders dark, and dark keeps the exact prior literal values (no visual change in
dark mode — only the violet token *values* changed in this redesign, not the mechanism). The
`color-scheme` meta is `content="dark light"` so native form controls/scrollbars render correctly in
both. A **`#themeToggle`** ghost icon button in the header's **utility cluster** (between Graph and
the Activity toggle, styled `.theme-toggle-icon`) flips the theme and shows the theme you'd switch TO
(`☀` in dark, `☾` in light; `aria-label`/`title`/`aria-pressed` track the action; **no keyboard
shortcut**). Theme selection is **default-to-system-then-persist** (ADR-030): an inline `<head>`
script in `index.html` sets `data-theme` from `localStorage["am.theme"]` (else `prefers-color-scheme`,
else `"dark"`) **before the stylesheet loads** (no flash); `app.js` (`THEME_KEY`, `applyTheme`/
`currentTheme`/`toggleTheme`/`initTheme`) wires the button and **live-follows the OS** via
`matchMedia` only while no explicit choice is stored (`lsGet(THEME_KEY) === null`). Clicking persists
`"light"`/`"dark"` to `localStorage["am.theme"]`. Both the inline script and `app.js` degrade
gracefully when `localStorage`/`matchMedia` are unavailable.

**Layout shell.** The page is `body > .shell > (aside#rail, .appcol > (header, main))`: `.shell` is
the horizontal split between the fixed-width rail and the flexible content column. `#modal`,
`#graphOverlay`, and `#railBackdrop` sit outside `.shell` as body-level overlays. The board uses
`justify-content: safe center` on `#board` so columns center on wide/ultrawide screens while `safe`
falls back to `flex-start` (so narrow-screen horizontal scrolling never clips the first column).

**Class inventory.** The **rail** uses `.rail-head`/`.brand`/`.rail-toggle`, `.rail-nav`,
`.rail-item` (`.active`, plus `.rail-cat`/`.rail-section`/`.rail-section-label`/`.rail-project`/
`.rail-dot`/`.rail-count`/`.rail-glyph`/`.rail-empty`/`.rail-divider`/`.rail-action`); collapse via
`body.rail-collapsed`, off-canvas via `body.rail-open` + `#railBackdrop`. The header actions use
`.actions` / `.actions-seg` (`.actions-util`) / `.actions-sep`; the scope title is `.breadcrumb`
(now a plain label — the dead `.crumb-back`/`.crumb-current` are gone). Cards use `.crow` + the solid
`.chip-prio.p0..p3`, the labeled `.status-pill.st-…`, `.cfoot`/`.who`/`.ptag`, the reserved
`.ctrouble` sub-row (`.tag-blocked`/`.tag-ready`/`.tag-stale`), and `.ctags` (`.tag-label`); the
feed uses the 3-col `.ev` grid + `.ev-icon`/`.ev-text`/`.ev-time` (kind-colored via `.k-<kind>`).
**Loading skeletons** (`.skeleton`/`.skel-card`/`.skel-line`/`.skel-feed`, shimmer; static under
reduced motion) and **non-blocking toasts** (`.toast-region`/`.toast`/`.toast-ok`/`.toast-x`,
`showToast`) are additive, `el()`-only. The Manage modal: `.proj-list`/`.proj-row`,
`.badge-archived`, `.btn-archive` (`.unarchive`), the per-project `.btn-edit-proj`, and the
Categories `.cat-manage-list`/`.cat-row`. The board-filter popover: `.filter-wrap` / `.filter-panel`
/ `.filter-section` / `.filter-check` / `.filter-mine-row` / `.filter-mine-btn` / `.filter-foot` /
`.filter-clear`, plus `.iconbtn.has-filters` + `.filter-count`. The modal's **Release** button is
`.btn-release` (`margin-right:auto` pushes Delete to the row's right edge). The category overview adds
`.cat-grid`, `.cat-card` (`.cat-card-all` dashed "All" card, `.cat-card-add` dashed "＋ New category"
add-card), `.cat-name`/`.cat-sub`, `.count-chips`/`.count-chip` (with a color `.swatch`), and
`.active-agents`/`.active-agent-avatar`/`.no-agents`/`.more-agents`. `#overview` is hidden by default
and shown via `body.view-overview #overview` (which also hides `#board`; `#tabs` is a hidden stub).
The modal's editable Meta section uses `.meta-section` / `.meta-row` / `.meta-key` / `.meta-val`
(monospace value) plus the `.meta-add-row` / `.meta-key-add` / `.meta-val-add` / `.meta-add-btn`
add-row (the per-row ✕ reuses `.dep-rm`). The graph overlay is styled via `.graph-overlay`,
`.graph-shell`, `.graph-header`, `.graph-body`, `.graph-svg`, `.graph-detail`, `.graph-legend`, and
assorted `.gnode-*` / `.gedge-*` / `.gd-*` classes for nodes, edges, and the detail panel.

## Forms

Plain inputs/selects/textareas inside the modal; changes **auto-save** via `onchange` →
`PATCH`/`POST` (no submit button for edits). New task / new project / new category use a "Create"
button with inline `.ferr` error text; the new-project modal adds a required Category `<select>`.
Slug auto-derives from name (`slugify`) for create modals; in **Edit project** the slug is an
explicit field (NOT auto-derived) and Save sends only the changed fields.

## UI States

- **Empty states** — `boardEmpty()` (no projects / no tasks, with a CTA), `.empty-col` per empty
  column, "No comments yet" / "No activity yet".
- **Connection state** — `setStatus()` (the `#status` dot) shows `live` (green pulse) /
  `connecting…` / `reconnecting…` (warn) / `offline — retrying…` (a loud, non-pulsing red `.status.err`
  once backoff climbs or `navigator.onLine === false`).
- **Loading** — minimal; localhost fetches are instant. No spinners.
- **Done column** capped at 50 rendered cards (`+N more`); feed capped at ~200 nodes (`trimFeed`) —
  cap is skipped once the user has paginated (`feedPaginated = true`) to preserve loaded history.

## Accessibility

Deliberately addressed in this codebase (see `decision-records.md` IADR / UX history):
- Skip link (`index.html`), global `:focus-visible` ring, `prefers-reduced-motion` reset (CSS kills
  transitions/animations; `prefersReducedMotion()` also skips scheduling the JS card-flash).
- Modal **and** graph overlay: `role="dialog"`, `aria-modal`, dynamic `aria-label`, a **focus trap**
  (`trapFocus`) and **focus restore** to the trigger (`lastFocus` / `graphLastFocus`); `Esc` closes.
- Cards are `role="button"`, `tabindex=0`, openable with Enter/Space; status moves via `[` / `]`.
- Status uses a redundant, non-color-only cue (the labeled `.status-pill` word + dot); priority shows
  the `P0..P3` rank text on every card; feed events carry a glyph **and** the event in words.
- Rail items are `<button>`s with `aria-pressed`/`aria-current` reflecting the active scope (the old
  tabs' `aria-pressed` model carried over); the rail is `<aside role="navigation">`, the toggle tracks
  `aria-expanded`, and `#status` is an `aria-live` region. Non-blocking **toasts** (`showToast`) and
  loading **skeletons** are `aria-live`/`aria-hidden` respectively.
- Keyboard shortcuts (`onKey`): `n` new task, `a` toggle activity, `g` toggle graph overlay
  (open/close), `/` focus the search box, `Esc` close (filter panel → modal → mobile rail, in order).
  The graph detail panel's "Open task" closes the overlay, then opens the task modal on the board (so
  the modal isn't hidden behind the overlay).
- `aria-expanded` on the activity toggle, labels on all fields.
- Drawer resize handle is a `role="separator"` with arrow-key support.

## Testing

**No JS test runner** — by deliberate decision (Phase E4 / ADR-018). Adding npm/jsdom would break
the no-npm/single-binary/no-build-step ethos that is a core project invariant.

**Source-level asset guards (Go level):** `cmd/am/web_test.go` reads the embedded assets via the
`webFS` embed.FS at Go test time (no JS runner). `TestDashboardNoXSSSinks` asserts that none of
`.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear in `web/app.js` +
`web/index.html` — this locks in the `el()`/`textContent` convention at `go test` time, so an
accidental sink assignment fails the build before it ships. `TestDashboardThemeAssets` (ADR-030)
asserts the dark/light theming stays wired: `app.css` ships the `:root[data-theme="light"]` override
block and `index.html` carries both the inline `am.theme` FOUC-guard script and the `#themeToggle`
button. `TestDashboardParityAffordances` (ADR-031) locks the CLI↔GUI parity affordances in at the
same level: it reads `app.js`/`index.html`/`app.css` and asserts the create/archive-category,
project-category-picker, project-edit, board-filter, editable-meta, and release wiring are present
(markers `openNewCategory`, `newCatCard`, `category: csel.value`, `renderManageCategories`,
`/api/categories?archived=true`, `openEditProject`, `btn-edit-proj`, `vault_project_id`,
`#filterBtn`/`#filterPanel`, `filterReady`/`filterBlocked`/`filterStale`/`filterMetaKey`,
`renderFilterPanel`, `patchMeta`, `meta-add-row`, `btn-release`, and the `.filter-panel`/
`.meta-add-row`/`.btn-release`/`.cat-card-add`/`.cat-row` CSS classes) — a regression that drops any
of them fails `go test` before it ships.

**Remaining gap:** behavioral dashboard JS — the "Manage" modal (category + project lists, project
edit), the board-filter popover, the editable Meta section, the Release button, the delete confirm flows
(task/comment/project), the feed pagination button, the dependency section (prereq chips, add-prereq
dropdown, blocks list), the graph overlay (layout, pan/zoom, transitive highlight, detail panel,
live refresh), the **left-rail navigation + hash routing** (rail tree, drill-down, `goProject`
single-select, collapse/off-canvas, per-view stream re-open, live count refresh), the category
overview cards, and SSE reconciliation paths — is not automatically tested. The **server** surface those views ride on is
covered by Go tests (the `?category=` feed/stream filtering and the augmented `/api/categories`
payload — `server_test.go`, `sse_test.go`, `hub_test.go`, `store_test.go`); the **rendering** relies
on preview/smoke + the XSS-sink guard. XSS safety of all these surfaces is guarded by
`TestDashboardNoXSSSinks`. All frontend behavior in these docs is from source reading and manual
verification. (Gap; see `known-risks-and-gaps.md`.)

## Where to Add New Features

- All UI changes go in `cmd/am/web/` (`index.html`/`app.css`/`app.js`). **Rebuild the binary**
  after editing (`go build -o am ./cmd/am`) — assets are embedded, so a running server serves the
  old UI until restarted.
- New card field → extend `card()` + `renderModal()`. New event type → `evKind`/`evText`/
  `describeText` (+ an `EV_GLYPH` entry for the feed glyph). New board affordance →
  `renderBoard()`/`moveTask()`. New rail entry → `renderRail()`/`railItem()`. Graph overlay changes →
  `computeGraphLayout`, `renderGraph`, `renderGraphDetail`; SVG elements via `svg()` helper.
- New **top-level view** → add a hash case to `route()` and a branch in `applyView`; new
  scope-aware data call → route it through `viewParams()` so `?project=`/`?category=` stay
  consistent across board/feed/stream.

## Risks and Gaps

- **Native HTML5 drag-and-drop doesn't fire on touch** → mobile relies on the status dropdown /
  `[ ]` keys (documented fallback in code comments).
- **Full board re-render per event batch** (debounced) — fine at small scale, O(n) at large scale.
- Single 2706-line `app.js`, no module split, no minification. Behavioral JS logic is not
  automatically tested (deliberate no-JS-runner decision); XSS-sink safety is enforced by the
  `TestDashboardNoXSSSinks` Go guard. The delete confirm flows, feed pagination, dependency UI,
  and the graph overlay are still untested at the behavioral level.
- **Graph overlay layout** uses a simplified layered longest-path algorithm (no crossing-minimization).
  Fine for modest projects; denser graphs may have edge crossings. Pan/zoom and the isolated-task
  lane mitigate readability for larger task sets.
- `localStorage` access is wrapped (`lsGet`/`lsSet`) so a sandboxed/Private-mode browser won't
  break the app — keep that pattern if you add persistence.
