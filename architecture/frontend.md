# Frontend Architecture

There **is** a frontend: a small single-page dashboard in `cmd/am/web/`
(`index.html` 87 lines, `app.css` 729 lines, `app.js` 2023 lines), embedded into the binary via
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
`body.view-overview`, sets the breadcrumb, loads the right data (overview cards vs. board), and
**re-opens the SSE stream with the new scope**. Within a board view, project tabs are still
**multi-select** — several projects can be active at once, and the "All" tab clears the within-view
selection (`toggleProject`); a view change resets that selection. Opening/closing the modal and the
graph overlay is not part of the hash route.

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
  drill in on click or Enter/Space (`navigate("#/cat/<slug>")` / `navigate("#/all")`). The
  overview keeps **one global, unfiltered** recent-activity feed (the existing `#feed` aside) — not
  per-card mini-feeds. `body.view-overview` (set by `applyView`) is what hides the project tabs and
  `#board` and shows `#overview`.
- **Header breadcrumb / back** — `setBreadcrumb`: fills the header `#breadcrumb` element on the
  board views with a **"← Categories"** back button (`navigate("#/")`) plus the current view's name
  (the category's `name` for `#/cat/<slug>`, "All" for `#/all`). Hidden on the overview
  (`body.view-overview .breadcrumb { display:none }`), which is the root.
- **Header / tabs** — `renderTabs`, `tab()`: project tabs with open-count badges + an "All" tab + a
  "＋" new-project button + a "⋯" **Manage projects** button (after the ＋). In a **category board**
  the tabs render only that category's projects (`projectsInView` filters `projects` by
  `p.category === activeCategory`) and the "All" tab spans the category's projects; in the `#/all`
  view it spans every project. Clicking "⋯" calls
  `openManageProjects`, which opens the reused `#sheet` modal (same focus-trap / Esc-to-close
  infrastructure as the task modal). Inside, `renderManageList` fetches all projects including
  archived ones via `GET /api/projects?archived=true` and builds a list with `el()` (no
  `innerHTML`): active projects show an **Archive** button; archived projects show an **Archived**
  badge and an **Unarchive** button. Both buttons call `POST /api/projects/{slug}/archive|unarchive`
  via `api()`, then refresh the list. If the just-archived project was selected, the tab bar and
  board reload automatically.
- **Search + label filter (header)** — a `#searchBox` input in the header `.actions` filters the
  board **server-side** (`?q=` on `GET /api/tasks` — substring match over title *and* body, which
  the client couldn't do since the list payload has no body). Input is **debounced 250 ms**
  (`searchTimer`) before `loadBoard()` re-fetches; the **`/`** keyboard shortcut focuses the box.
  Because the filter is applied in `loadBoard()` (state in `filterQ`/`filterLabel`, deliberately
  *not* in the shared `qstr()` used by the feed/SSE), the debounced SSE board reload keeps the
  filter across live refreshes. An active label filter shows a **`#labelFilterChip`** chip next to
  the search box (`setLabelFilter`) with a **✕** clear button (`clearLabelFilter`).
- **Board** — `renderBoard`, `card(t)`: four status columns (`COLS`), priority via card left-border
  + chip, avatar initials, project tag (shown when `selected.size !== 1`, i.e. only when the board
  isn't already scoped to a single project), comment count. Cards now also show a dependency tag in
  the card footer: **🔒 Blocked** (`.tag-blocked`, shown when `t.nopen > 0`) or **✓ Ready**
  (`.tag-ready`, shown when `t.nprereq > 0 && t.nopen === 0`). These are derived from server-side
  counts (`nprereq`/`nopen` on the task object); there is no stored "ready" status field. A card in
  `doing` with an assignee and no activity for 30+ minutes (`Date.now() - Date.parse(t.updated_at)
  > STALE_MS`, `STALE_MS = 30 * 60 * 1000`) additionally shows an amber **⏳ stale** chip
  (`.tag-stale`) — purely client-side, computed at render time from `updated_at`. Cards also show
  the task's **label chips** (`.tag-label`) in the footer — at most 3, then a **`+N`** overflow
  chip; clicking a label chip calls `setLabelFilter(l)` to filter the whole board by that label
  (the chips are `role="button"`/`tabindex=0` and stop click propagation so the card doesn't open).
- **Activity feed** — `feedItem`, `evText`, `evKind`: color-coded events with clickable `#refs`.
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
- **Detail modal** — `renderModal`, plus `openNew` (new task) and `openNewProject`: one reused
  `#sheet` element; auto-growing title `<textarea>`; status/assignee/priority controls; comments;
  history. `openNewProject` still POSTs `{slug,name}` only — the server defaults the category to
  `general` (Phase O kept the project-creation form category-unaware by design; the Phase R category
  UI is the read-side overview/drill-down, not a category picker on the create form). The modal includes a **Delete task** button (inline two-step confirm — see below) and
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
  After Labels comes a **read-only Meta section** (Phase P) — rendered only when the task carries
  meta: one `.meta-row` per pair (keys sorted; `.meta-key` muted, `.meta-val` monospace), built
  with `el()`/`textContent` only. Meta pairs are set via the CLI/API (`--meta k=v`), not the
  dashboard.
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
(`CategoryStat[]` for the overview — counts + active_agents), `overviewTimer` (debounce handle for
the live overview count refresh),
`projects` (array), `selected` (`Set<slug>` of active project filters within the current view,
empty=all), `tasks`
(`Map<id,task>`), `cursor` (highest seen `events.id` for SSE `since=`), `es` (EventSource),
`openTaskId`, `dragId`, `lastFocus`, `feedOldest` (lowest event id currently in `#feedList`; `0`
if none loaded), `feedPaginated` (`true` once the user has paginated; disables `trimFeed` cap),
`loadOlderBtn` (reference to the "Load older" button outside `#feedList`), `filterQ` /
`filterLabel` (active server-side search/label filters, applied by `loadBoard()`), `searchTimer`
(the search box's 250 ms input debounce). Graph overlay state:
`graphOpen` (bool), `graphSlug` (slug of the project currently shown), `graphData`
(`{nodes, edges}` from the last fetch), `graphViewState` / `graphInitialView` (current and
reset-target `viewBox`), `graphSelectedId` (currently highlighted node id), `graphDragState`,
`graphRefreshTimer`, `graphLastFocus`, `graphPanZoomInstalled`. Reconciliation is **snapshot-based**:
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
  the now-hidden `#overview`. `onEvent` also reloads the project strip on `category.created` (in
  addition to the existing `project.created`/`project.unarchived`/`category.archived`/
  `category.unarchived`) so a new category appears live. `onEvent` handles the three delete kinds:
  `task.deleted` removes the card from `tasks` map and closes the modal if it was open;
  `comment.deleted` refreshes the open modal; `project.deleted` drops the slug from `selected` and
  reloads the board/feed; `category.archived`/`category.unarchived` (like `project.created` and
  `project.unarchived`) trigger `loadProjects()` so the project strip reflects the archive
  cascade live. For `task.dep_added` and `task.dep_removed`, `onEvent` refreshes the
  open modal if either the task or the referenced prereq is currently open (so both sides of the
  edge see the update), then triggers the debounced board reload.
- Same-origin only; no CORS, no auth token (the API is unauthenticated).

## Styling and Design System

`app.css` defines a **CSS-variable design system** (`:root` tokens): background/surface levels,
`--line`, text `--fg`/`--muted`/`--faint`, `--accent`, status colors (`--st-todo/doing/blocked/
done`), radii, `--feed-w`, `--header-h`, plus **component color tokens** (backdrops, tag pill
backgrounds/borders, the status pulse ring, the `--on-accent` on-accent text, the scrollbar-hover
color, the graph legend background, the danger-confirm wash) that were factored out of inline
literals so a single override block can re-theme them. Thin custom scrollbars. Status and priority
each have a consistent color language across board, cards, and feed.

**Theming — dark default + light override.** The dashboard supports a **dark (default) and a light
theme**, selected by a single `data-theme` attribute on `<html>`. Dark is the bare `:root`; light is
**one `:root[data-theme="light"]{…}` block** that re-defines the color tokens — so an unknown or
absent `data-theme` renders dark, and dark keeps the exact prior literal values (no visual change in
dark mode). The `color-scheme` meta is `content="dark light"` so native form controls/scrollbars
render correctly in both. A header **`#themeToggle`** ghost icon button (in `.actions`, before the
Graph button, styled `.theme-toggle-icon`) flips the theme and shows the theme you'd switch TO (`☀`
in dark, `☾` in light; `aria-label`/`title`/`aria-pressed` track the action; **no keyboard
shortcut**). Theme selection is **default-to-system-then-persist** (ADR-030): an inline `<head>`
script in `index.html` sets `data-theme` from `localStorage["am.theme"]` (else `prefers-color-scheme`,
else `"dark"`) **before the stylesheet loads** (no flash); `app.js` (`THEME_KEY`, `applyTheme`/
`currentTheme`/`toggleTheme`/`initTheme`) wires the button and **live-follows the OS** via
`matchMedia` only while no explicit choice is stored (`lsGet(THEME_KEY) === null`). Clicking persists
`"light"`/`"dark"` to `localStorage["am.theme"]`. Both the inline script and `app.js` degrade
gracefully when `localStorage`/`matchMedia` are unavailable.

The board uses `justify-content: safe center` on `#board` so columns are centered on wide/ultrawide
screens. The `safe` keyword falls back to `flex-start` when columns overflow their container, so
horizontal scrolling on narrow screens never clips the leftmost column. New CSS classes support the
Manage-projects modal: `.proj-list`, `.proj-row`, `.badge-archived`, `.btn-archive` (and
`.btn-archive.unarchive`). The Phase R category overview adds `.cat-grid`, `.cat-card`
(`.cat-card-all` for the dashed "All" card), `.cat-name`/`.cat-sub`, `.count-chips`/`.count-chip`
(with a color `.swatch`), and `.active-agents`/`.active-agent-avatar`/`.no-agents`/`.more-agents`;
the header breadcrumb uses `.breadcrumb`/`.crumb-back`/`.crumb-current`. `#overview` is hidden by
default and shown via `body.view-overview #overview` (which also hides `#board` and `#tabs`). The card chips use `.tag-blocked` / `.tag-ready` / `.tag-stale`
(amber pill for the ⏳ stale badge). The modal's Meta section uses `.meta-row` / `.meta-key` /
`.meta-val` (tones match `.dep-status`; monospace value). The graph overlay is styled via `.graph-overlay`, `.graph-shell`,
`.graph-header`, `.graph-body`, `.graph-svg`, `.graph-detail`, `.graph-legend`, and assorted
`.gnode-*` / `.gedge-*` / `.gd-*` classes for nodes, edges, and the detail panel.

## Forms

Plain inputs/selects/textareas inside the modal; changes **auto-save** via `onchange` →
`PATCH`/`POST` (no submit button for edits). New task / new project use a "Create" button with
inline `.ferr` error text. Slug auto-derives from project name (`slugify`).

## UI States

- **Empty states** — `boardEmpty()` (no projects / no tasks, with a CTA), `.empty-col` per empty
  column, "No comments yet" / "No activity yet".
- **Connection state** — `setStatus()` shows `live` (green pulse) / `reconnecting…` / `connecting…`.
- **Loading** — minimal; localhost fetches are instant. No spinners.
- **Done column** capped at 50 rendered cards (`+N more`); feed capped at ~200 nodes (`trimFeed`) —
  cap is skipped once the user has paginated (`feedPaginated = true`) to preserve loaded history.

## Accessibility

Deliberately addressed in this codebase (see `decision-records.md` IADR / UX history):
- Skip link (`index.html`), global `:focus-visible` ring, `prefers-reduced-motion` reset.
- Modal: `role="dialog"`, `aria-modal`, dynamic `aria-label`, a **focus trap** (`trapFocus`) and
  **focus restore** to the trigger (`lastFocus`).
- Cards are `role="button"`, `tabindex=0`, openable with Enter/Space; status moves via `[` / `]`.
- Keyboard shortcuts (`onKey`): `n` new task, `a` toggle activity, `g` toggle graph overlay
  (open/close), `/` focus the search box, `Esc` close. The graph detail panel's "Open task" closes the overlay, then opens the
  task modal on the board (so the modal isn't hidden behind the overlay).
- `aria-pressed` on tabs, `aria-expanded` on the activity toggle, labels on all fields.
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
button.

**Remaining gap:** behavioral dashboard JS — the "Manage projects" modal, the delete confirm flows
(task/comment/project), the feed pagination button, the dependency section (prereq chips, add-prereq
dropdown, blocks list), the graph overlay (layout, pan/zoom, transitive highlight, detail panel,
live refresh), the **category overview + hash routing** (overview cards, drill-down, breadcrumb/back,
per-view stream re-open, live count refresh — Phase R), multi-select filter logic, and SSE
reconciliation paths — is not automatically tested. The **server** surface those views ride on is
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
  `describeText`. New board affordance → `renderBoard()`/`moveTask()`. Graph overlay changes →
  `computeGraphLayout`, `renderGraph`, `renderGraphDetail`; SVG elements via `svg()` helper.
- New **top-level view** → add a hash case to `route()` and a branch in `applyView`; new
  scope-aware data call → route it through `viewParams()` so `?project=`/`?category=` stay
  consistent across board/feed/stream.

## Risks and Gaps

- **Native HTML5 drag-and-drop doesn't fire on touch** → mobile relies on the status dropdown /
  `[ ]` keys (documented fallback in code comments).
- **Full board re-render per event batch** (debounced) — fine at small scale, O(n) at large scale.
- Single 2023-line `app.js`, no module split, no minification. Behavioral JS logic is not
  automatically tested (deliberate no-JS-runner decision); XSS-sink safety is enforced by the
  `TestDashboardNoXSSSinks` Go guard. The delete confirm flows, feed pagination, dependency UI,
  and the graph overlay are still untested at the behavioral level.
- **Graph overlay layout** uses a simplified layered longest-path algorithm (no crossing-minimization).
  Fine for modest projects; denser graphs may have edge crossings. Pan/zoom and the isolated-task
  lane mitigate readability for larger task sets.
- `localStorage` access is wrapped (`lsGet`/`lsSet`) so a sandboxed/Private-mode browser won't
  break the app — keep that pattern if you add persistence.
