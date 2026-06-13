# Phase R — category home + drill-down dashboard (R6)

Draft notes for the docs-sync stage. Covers every externally visible change, the
design decisions and their rationale, deviations from the implementation map, and
the tests added per file.

## Externally visible changes

### HTTP API

- **`GET /api/categories` payload augmented.** Each element is now a
  `CategoryStat`: the existing `Category` fields plus two always-present rollups:
  - `counts`: `{todo, doing, blocked, done}` — task counts summed over the
    category's **non-archived** projects (an archived project's tasks are excluded
    even when `?archived=true` lists the category itself).
  - `active_agents`: distinct non-human actors whose recent events touched a task
    in the category within the last **30 minutes**, sorted. `comment.added` counts
    as activity (the predicate is `task_id IS NOT NULL AND actor != 'human'`);
    category/project admin events (no `task_id`) and the literal actor `human` do
    not. Stats are **always** included — there is no opt-in flag.
  - `?archived=true` still toggles whether archived categories appear. **No scope
    enforcement** on this endpoint (the dashboard is unscoped).

- **`?category=<slug>` added to `GET /api/events`** (all three access modes:
  default `?since=`, `?tail=`, `?before=`). Scopes the feed to the events of the
  category's projects. It **intentionally EXCLUDES the category's own
  category-level events** (those have a NULL `project_id`) — they belong to the
  All/overview feed, not a single category's drill-down. Combines with `?project=`
  (ANDed). An **unknown category returns 404** (`not_found`) — it flows through the
  `categoryID` `ErrNotFound` sentinel like an unknown project on `/api/tasks`.

- **`?category=<slug>` added to `GET /api/stream`** (the SSE endpoint). Scopes the
  live stream and the gap-replay to the category's projects, with the same NULL-
  project exclusion. **Exception:** `project.created` is delivered to every
  subscriber regardless of scope (the existing carve-out, so a new project's tab
  can appear live). An **unknown category is ignored silently** (the subscriber
  falls back to the unfiltered stream) — this matches the endpoint's existing
  unknown-`project` swallow, and differs deliberately from `/api/events` (a stream
  is long-lived and best-effort; a REST query is a one-shot lookup).

### Dashboard (web/)

- **New category-home overview is the landing view.** Cards per category showing
  the name, four count chips (reusing the status swatch colors), and an
  active-agents avatar row (initials). An **"All" card** opens the cross-category
  board. Card click (or Enter/Space) drills into the category board.
- **Hash routing** (linkable, browser-back works):
  - `#/` → overview (category home, the landing view; also the empty-hash default)
  - `#/all` → cross-category board (the original board behavior)
  - `#/cat/<slug>` → a single category's board (drill-down)
- **Category board (drill-down):** the project tabs render only that category's
  projects; the "All" tab spans the category's projects; the board, feed, and live
  stream are all scoped via `?category=` (or `?project=` when one project is
  selected within the category).
- **Header breadcrumb / back:** a "← Categories" back button plus the current
  view's name appears in the header on the board views (hidden on the overview).
- **One global recent-activity feed on the overview** (the existing `#feed` aside,
  unfiltered) — not per-card mini-feeds.
- **Live updates:** the overview's category counts refresh (debounced 250 ms) on
  any `task.*` / `project.* `/ `category.*` event; the SSE stream is re-opened with
  the new `?category=` scope on every view change.
- **The dashboard sends no scope header** — a human sees everything; the category
  view is a query-param choice, not an identity scope.
- Existing board features (drag-drop, modal, dependency graph, keyboard shortcuts,
  stale badges, search, label chips, meta section) are unchanged; they still mount
  inside `#board`.

No new error codes, exit codes, event kinds, schema, or migrations. `am wait` and
the `am` CLI surface are unchanged.

## Design decisions (rationale)

1. **Hash routing** (`#/`, `#/all`, `#/cat/<slug>`): views are linkable and the
   browser back button works, with `route()` as the single hash→state mapper.
2. **Counts + active_agents folded into `GET /api/categories`** (always present,
   no flag): the overview needs them on first paint; a separate endpoint would add
   a round-trip and a partial-render flash.
3. **Active-agents predicate `task_id IS NOT NULL AND actor != 'human'`, 30-min
   window:** commenting counts as activity (`comment.added` carries a `task_id`),
   while category/project admin churn and the human operator do not inflate the
   "who's working here" signal.
4. **`?category=` feeds EXCLUDE category-level (NULL-project) events:** those are
   instance-wide admin events that belong to the All/overview feed; a category
   drill-down should show only work happening *inside* the category.
5. **Hub category fan-out resolved at Subscribe time into a project-ID set:** the
   `ProjectIDsInCategory` lookup happens once when the stream opens, so `Broadcast`
   stays a pure in-memory membership check with no per-event DB hits (R9 SSE
   contract: the hub stays non-blocking and in-memory).
6. **`project.created` carve-out preserved through the category branch:** a brand-
   new project's tab appears live even on a category-scoped dashboard.
7. **Dashboard sends no scope header:** the human operator is unscoped; "category
   view" is a query-param lens over the same global data, not an identity scope.

### Risks / accepted limitations

- **Post-open project staleness window (hub):** a project created *after* a
  category stream subscribes is not in that subscriber's resolved project-ID set,
  so its task events won't stream until the dashboard re-opens the stream (which it
  does on view change) — and the REST snapshot remains the source of truth. The
  `project.created` carve-out still surfaces the new project itself. Documented in
  `hub.go`.
- **Unknown-category divergence:** `/api/events` 404s on an unknown category while
  `/api/stream` silently falls back to unfiltered — intentional, per decision on
  one-shot vs. long-lived semantics (see above).

## Deviations from the implementation map

- **None of substance.** Two small notes:
  - `RecentEvents` was refactored to build its WHERE clause from a `[]string`
    slice (like `ListProjects`) rather than the original single-branch `if/else`,
    because it now composes up to three independent conditions (project, category,
    archived-cascade). `ListEvents`/`ListEventsBefore` kept their incremental
    `q +=` style since their archived-cascade branch is mutually exclusive with the
    project/category branches.
  - **`wait.go` left unchanged** (as the map's primary recommendation): the wait
    stream's existing comment already documents that a category scope deliberately
    does not narrow the stream (the REST re-check is the authority, ADR-023). Adding
    `?category=` to the wait stream was judged non-trivial/risky enough to skip per
    the map's "only if trivial" condition. `wait_test.go` passes untouched.

## Tests added / extended (per file)

- **`cmd/am/store_test.go`** — added `TestListCategoriesCounts`: counts sum only
  non-archived projects' tasks (archives a project mid-test and re-asserts);
  `active_agents` lists distinct non-human actors in the 30-min window, counts a
  commenter, excludes `human`, and omits an agent whose only event is backdated
  outside the window. Existing `ListEvents`/`ListEventsBefore`/`RecentEvents`
  callers updated for the new `category` parameter (passing `""`).
- **`cmd/am/server_test.go`** — added `TestEventsCategoryFilter` (one category's
  task events only; excludes `category.*` and the other category; covers default
  `?since=`, `?tail=`, `?before=`; unknown category → 404). Added helpers
  `mustCreateProjectIn` (project under a named category) and `eventKinds`.
- **`cmd/am/sse_test.go`** — added `TestSSECategoryScopedStream` (task in B does
  not reach an acat subscriber; task in A does; `project.created` in B still
  reaches the acat subscriber via the carve-out; a bcat subscriber sees B not A)
  and `TestSSECategoryReconnectReplay` (a category-scoped reconnect replays only
  that category's gap events). Added helper `openStream`.
- **`cmd/am/hub_test.go`** (new) — direct hub unit tests:
  `TestHubCategoryScopedBroadcast` (in-category delivered, out-of-category dropped,
  category-level NULL-project dropped, `project.created` delivered regardless),
  `TestHubProjectScopedBroadcast`, `TestHubUnscopedBroadcast`,
  `TestHubBroadcastNilNoPanic`.
- **`cmd/am/web_test.go`** — `TestDashboardNoXSSSinks` runs automatically over the
  new `app.js`/`index.html`; all new DOM uses `el()`/`textContent` (no `innerHTML`).
- **`cmd/am/wait_test.go`** — unchanged; confirmed passing (no wait code changes).
