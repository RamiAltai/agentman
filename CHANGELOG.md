# Changelog

All notable changes to **agentman** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

When cutting a release, rename the `[Unreleased]` heading to the version + date and start a
fresh `[Unreleased]` section.

## [Unreleased]

### Added

- **Input size limits** — task titles are capped at 500 bytes; task bodies and comment bodies at
  64 KiB; priority must be 0–3. Exceeding a limit returns `400 invalid` (CLI exit 5) instead of
  silently inserting megabyte payloads that render into every board card and SSE event. Enforced
  in the store (`CreateTask`, `PatchTask`, `AddComment`); boundary values accepted.
  Test: `TestInputLimits`.

### Fixed

- **Dashboard `api()` no longer crashes on non-JSON responses** — a proxy error page or truncated
  body now falls through to an `HTTP <status>` error message instead of throwing an uncaught
  `SyntaxError` from `JSON.parse`. (`cmd/am/web/app.js`)
- **SSE Flusher-unsupported error is now JSON** — `handleStream` returned plain text via
  `http.Error` while every other error path returns JSON; now `{"error":"streaming_unsupported"}`.
  (`cmd/am/server.go`)
- **`am db prune --before` validates its date** — a malformed date (e.g. `2026-13-99`) previously
  fed an ISO-8601 string comparison and silently pruned the wrong rows (usually none); it now
  fails with a clear error, both before the confirmation prompt and inside `pruneEvents`.
  Test: `TestPruneEventsRejectsBadDate`. (`cmd/am/db.go`)
- **Event-payload marshal errors are no longer discarded** — `insertEvent` returns the
  `json.Marshal` error instead of writing a corrupted/empty payload into the events table (the
  durable replay cursor). (`cmd/am/store.go`)
- **`am update` semver compare handles prereleases** — a stable tag now beats a prerelease of the
  same triple (`v0.5.0` > `v0.5.0-rc1`); prereleases order lexically. Previously a prerelease
  build never saw the stable release as an update. (`cmd/am/update.go`)
- **SSE reconnect backoff is jittered** — multiple open dashboard tabs no longer reconnect in
  lockstep. (`cmd/am/web/app.js`)

## [0.5.0] - 2026-06-07

### Added

- **Dependency-graph overlay** — a per-project interactive visualization of the task dependency DAG.
  - **Entry:** the **"Graph"** button in the dashboard header + the **`g`** keyboard shortcut (not
    while typing). Opens a full-screen overlay (`#graphOverlay`) reusing the modal focus-trap and
    `Esc`-to-close; a project `<select>` defaults to the selected project; **"Reset view"** + ✕
    close the overlay.
  - **Rendering:** pure vanilla SVG, no library, no npm. A new `svg(tag, attrs)` helper
    (`document.createElementNS`) is parallel to the existing `el()` helper and uses `.textContent`
    for all text — XSS-safe (`TestDashboardNoXSSSinks` passes). Edges are cubic Bézier curves
    with `<marker>` arrowheads.
  - **Layout:** topological longest-path / Kahn's algorithm — prerequisites left, dependents
    right. Dependency-free tasks are placed in a compact grid **"No dependencies" lane** below
    the DAG (so isolated tasks don't pile into one tall column). All tasks in the project appear.
  - **Encoding:** nodes colored by task **priority** (`PRIO` palette) with a status dot and
    Ready/🔒 Blocked indicators. Edges: `done` prerequisite → **green solid** ("cleared"); open
    prerequisite → **amber dashed** ("blocking"). A **bottom-left legend** explains both axes.
  - **Interaction:** click a task → **transitive highlight** — its full upstream prereq path and
    downstream subtree light up in distinct accents; everything else dims. Click the empty canvas
    to clear. The **right detail panel** (built with `el()`) shows title, status/priority/assignee,
    Ready/Blocked, a clickable **Prerequisites** list and **Unblocks** list, and an **"Open task"**
    button → the existing detail modal.
  - **Pan/zoom:** drag to pan, scroll to zoom, `viewBox` manipulation — no library. **"Reset view"**
    restores the initial viewport.
  - **Live:** while open, debounced re-fetch on SSE events affecting the project
    (`task.dep_added/removed`, `task.status`, `task.created/deleted`, `task.assign`,
    `task.patched`), preserving pan/zoom and selection.
  - **Backend:** `GET /api/projects/{slug}/graph` → `{nodes: []Task, edges: []GraphEdge{from,to}}`
    — all tasks as nodes, edges oriented prereq→dependent. Read-only: no writes, no events emitted.
    404 on a missing project. New store method `ProjectGraph`; new types `GraphEdge`,
    `ProjectGraphData`.
  - **Tests:** +4 backend (`TestProjectGraph`, `TestProjectGraphMissingProject` in `store_test.go`;
    `TestProjectGraphEndpoint`, `TestProjectGraphEndpoint404` in `server_test.go`) → **95 tests**
    total. Overlay JS is untested behaviorally (no JS runner — ADR-018); XSS-sink guard covers it.

- **Task dependencies (Phase H)** — tasks can now have prerequisites (other tasks that must be
  `done` first). Many-to-many, same-project only.
  - **CLI:** `am dep add <id> <prereq…>` / `am dep rm <id> <prereq>` — add/remove prerequisite
    edges. `am ls --ready` lists todo tasks with no open prereqs (the safe pick-up list for agents).
    `am ls --blocked` lists tasks with ≥1 open prereq. `am ls` rows show a `[blk:N]` or `[ready]`
    marker. `am show <id>` prints `depends on:` / `blocks:` lines when present.
  - **API:** `POST /api/tasks/{id}/deps {depends_on:<id-or-ref>}` — add edge (same project; rejects
    self-deps, cross-project, cycles). `DELETE /api/tasks/{id}/deps/{depId}` — remove edge.
    `GET /api/tasks?ready=true` / `?blocked=true` — server-side prereq filters.
    `GET /api/tasks/{id}` now returns `depends_on:[…]` and `blocks:[…]`.
  - **Hard-block:** claiming or PATCHing a task to `doing`/`done` while it has open prerequisites
    fails with `409 {"error":"blocked","open_prereqs":[…]}`. CLI maps this to exit 4 and prints
    e.g. `claim: #3 blocked — prereqs not done (#1 #2)`. Edit, comment, assign, and
    status→`todo`/`blocked` are unaffected.
  - **Cycle prevention:** self-deps and transitive cycles are rejected by a recursive CTE
    (`wouldCycle`) — validation error / HTTP 400.
  - **Dashboard:** task modal has a **Dependencies** section — "Depends on" chips (status dot +
    ref link + title + status + ✕ remove), an **"Add prerequisite…"** dropdown of same-project
    tasks (excludes self + existing edges), and a read-only **Blocks** list. Board cards show a
    **🔒 Blocked** tag (`nopen > 0`) or **✓ Ready** tag (`nprereq > 0 && nopen == 0`). Hard-block
    409s surface the blocking prereq ids and revert the card/modal.
  - **Storage:** new join table `task_deps(task_id, depends_on_id)` — composite PK, `ON DELETE
    CASCADE` on both FKs (deleting a task removes its edges in both directions), reverse index
    `idx_task_deps_prereq`. Propagated to existing DBs via `CREATE TABLE IF NOT EXISTS` in
    `schema.sql` — no migration-runner step, no version bump.
  - **Event kinds:** 2 new — `task.dep_added`, `task.dep_removed` (total now 14).
  - **Tests:** +24 (now 91 total) — cycle/self/cross-project rejection, idempotent add/remove,
    cascade, counts, filters, hard-block (claim + patch), HTTP endpoints, 409 blocked, fresh-DB
    table existence.

## [0.4.2] - 2026-06-07

### Changed

- **Minimum Go raised to `1.25.11`** (`go.mod`). Go 1.25.0–1.25.10 ship a standard library with 21
  known advisories (`crypto/tls`, `crypto/x509`, `net/url`, `net/http`, …). With this floor,
  `go install` always builds against a security-patched stdlib — even for installers on an older Go,
  whose toolchain auto-upgrades to ≥ 1.25.11. No source changes; agentman's own code was unaffected.
- **CI builds on the latest stable Go** (`go-version: 'stable'` in `.github/workflows/ci.yml`,
  replacing the exact `go.mod` pin), so `govulncheck` scans a current/patched stdlib instead of a
  frozen one that goes red as CVEs accrue.

## [0.4.1] - 2026-06-07

> Note: `v0.4.0` was accidentally tagged on a stale commit (and that tag was already cached by the
> Go module proxy, which is immutable), so this release ships as **v0.4.1**. Do not use `v0.4.0`.

### Added

- **CI via GitHub Actions (Phase F)** — `.github/workflows/ci.yml` is the project's first CI.
  Triggers on push to `main` and on pull requests. Single `ubuntu-latest` job runs, in order:
  `go build ./...`, `go vet ./...`, `gofmt -l` (fails if non-empty), `go test -race -count=1 ./...`,
  `node --check cmd/am/web/app.js` (JS syntax), and `govulncheck ./...` (blocks on reachable
  vulnerabilities; `@latest` keeps the advisory DB current). All checks pass; 0 reachable
  vulnerabilities. One known non-blocking module-level advisory (`GO-2026-5024`, Windows-only,
  unreachable) is documented in `architecture/known-risks-and-gaps.md`.

- **Expanded automated test coverage (Phase E)** — 9 test files, 71 tests, all green under
  `-race`. Four new test files close the previously-untested layers:
  - **`cli_test.go` (E1)** — CLI verb output (`cmdNew`/`cmdLs`/`cmdStatus`/`cmdNote`/`cmdDrop`)
    and the `doOrFail` exit-code mapping (3 not found · 4 conflict · 5 validation · 6 server
    down); plus table tests for the `parse`/`Args` helpers and the pure formatters
    (`taskLine`/`statusShort`/`assignee`/`trunc`/`apiErr`). Uses `captureStdout`/`captureExit`
    helpers against a real `httptest` server.
  - **`sse_test.go` (E2)** — `TestSSEDeliversLiveEvent` (live mutation arrives over SSE) and
    `TestSSEReplayOnReconnect` (reconnect with `Last-Event-ID` replays missed events; every
    replayed id is strictly greater than the resume cursor).
  - **`identity_test.go` (E3)** — `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` env
    override, `sanitizeType` table, `newIdentity` format. Isolated via the `AGENTMAN_AGENT_FILE`
    env seam (never writes to `~/.agentman`).
  - **`web_test.go` (E4)** — `TestDashboardNoXSSSinks`: source-level XSS-sink guard that reads
    the embedded `web/app.js` + `web/index.html` via `webFS` and asserts none of `.innerHTML`/
    `.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear. Locks in the `el()`/
    `textContent` convention at `go test` time. No JS runner added (ADR-018).
  - **Testability seam:** `var osExit = os.Exit` in `cli.go`; `fail()` now calls `osExit` so
    tests can intercept exit codes without killing the process. No production behavior change.
  (`cmd/am/cli_test.go`, `cmd/am/sse_test.go`, `cmd/am/identity_test.go`, `cmd/am/web_test.go`,
  `cmd/am/cli.go`)

- **Opt-in request logging (Phase D2)** — `am serve --log` or `AGENTMAN_LOG=1` installs a
  `requestLogger` middleware that logs one line per request after completion:
  `METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default `"human"`) to stderr via the
  standard `log` package. Off by default. The middleware is installed outermost so security-guard
  403s are also logged. `statusRecorder` proxies `http.Flusher` so SSE connections continue to
  work. A long-lived SSE connection logs once on disconnect with a large latency (inherent).
  `AGENTMAN_LOG` treats any non-empty value as on; `=1` is canonical.
  (`cmd/am/server.go`, `cmd/am/main.go`, `cmd/am/cli.go`)

- **Events pagination + retention (Phase C2)** — completes Phase C:
  - **`GET /api/events?before=<id>`** — backward cursor: returns events with `id < before`,
    newest-first (default 40, cap 200). Applies the same archived-project filter as `?since=`/`?tail=`
    when no `?project=` is given; an explicit `?project=<slug>` returns that project's events.
    Store method: `ListEventsBefore(before, project, limit)` (`cmd/am/store.go`).
  - **Dashboard "Load older activity"** button at the bottom of the activity feed fetches
    `?before=<oldest-loaded-id>` and appends results. Placed outside `#feedList` so `trimFeed`
    can't remove it. `feedPaginated` disables the feed cap once the user has paged (trade-off: the
    in-browser feed can grow unbounded until the next reload). An end-marker replaces the button
    when all history is loaded. All DOM via `el()` (no `innerHTML`).
    (`cmd/am/web/app.js`, `cmd/am/web/app.css`)
  - **`am db prune (--before <YYYY-MM-DD> | --keep <N>) [--db PATH] [--yes]`** — offline events
    retention (CLI-only, no HTTP route). Refuses while a server is running (same guard as
    `am db import`). Deletes rows from the **`events` table only** (not tasks/comments/projects),
    then runs `VACUUM` (best-effort) to reclaim disk space. Prints `pruned N events` to stderr;
    stdout stays clean. `--before <date>`: same-day events are kept (date-only string sorts before
    same-day ISO timestamps). `--keep N`: keeps the newest N events by id. (`cmd/am/db.go`)
  - Tests: `TestListEventsBefore` (store), `TestEventsBeforeEndpoint` (HTTP); `TestPruneEventsKeep`,
    `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary` (prune).

- **Hard delete (Phase C1)** — permanent removal for tasks, comments, and projects:
  - CLI: `am rm <id>` hard-deletes a task and all its comments (silent success; exit 3 if not found).
    `am project rm <slug> --yes` hard-deletes a project **and all its tasks/comments** (cascade);
    `--yes` is required or the command errors with a hint.
  - API: `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`,
    `DELETE /api/projects/{slug}` — all return `200 {"status":"deleted"}`; missing target → 404.
    Cascade is via existing FK constraints (`projects → tasks → comments`).
  - Dashboard: inline two-step delete confirms (no native `confirm()`/`prompt()`) in the task modal
    (**Delete task**), per-comment (**×**), and the Manage-projects modal (**Delete project**,
    distinct from Archive). All DOM via `el()`.
  - Three new event kinds: `task.deleted`, `comment.deleted`, `project.deleted` (total now 12).
    `onEvent` handles each: `task.deleted` removes the card and closes the open modal; `comment.deleted`
    refreshes the open modal; `project.deleted` drops the project from selection and reloads.
    Events are **never** deleted — the audit log (including `*.deleted` events) survives hard deletes.
  - Store: `DeleteTask`, `DeleteComment`, `DeleteProject` — each inserts its `*.deleted` event in
    the same tx before the `DELETE`, then commits; broadcast happens after commit.
  - Tests: `TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
    `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades` (store);
    `TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`, `TestDeleteCommentEndpoint` (HTTP).
    (`cmd/am/store.go`, `cmd/am/server.go`, `cmd/am/cli.go`, `cmd/am/main.go`, `cmd/am/web/app.js`,
    `cmd/am/web/app.css`)

- **DB export / import** — `am db export [path] [--db PATH]` writes a consistent `VACUUM INTO`
  snapshot (chmod `0o600`, prints the path); `am db import <path> [--db PATH] [--yes]` validates
  the candidate (integrity + foreign-key checks, required tables, schema version), **refuses to
  run while a server is live**, backs up the current DB, then atomically swaps it in. CLI-only —
  there is no HTTP route; it operates directly on the SQLite file. (`cmd/am/db.go`)
- **Project archive / hide** — `am project archive <slug>` / `am project unarchive <slug>`, plus
  `am projects --all` and `GET /api/projects?archived=true` / `POST /api/projects/{slug}/archive`
  and `…/unarchive`. Backed by the first real schema migration (**v2**, adding
  `projects.archived_at`), which exercises the Phase-0 forward-only migration runner end-to-end.
- **Multi-select project filter** on the dashboard — click several project tabs to view their
  boards together; the **All** tab clears the selection.
- **Dashboard archive / unarchive control** — a "⋯ Manage projects" button in the tab bar opens a
  modal listing all projects (active and archived). Active projects have an **Archive** button;
  archived projects show an "Archived" badge and an **Unarchive** button. Archive/unarchive calls
  the existing API endpoints; on success the tab bar refreshes in place and, if the just-archived
  project was selected, the board and feed reload automatically. All DOM is built via the existing
  `el()` helper (no `innerHTML`); the modal focus trap and Esc-to-close are preserved.
  (`cmd/am/web/app.js`, `cmd/am/web/app.css`)

### Fixed

- **500 responses leaked internal error detail (Phase D1).** `writeErr`'s default branch
  previously returned the raw Go error string (SQL messages, file paths, etc.) to the client.
  It now logs the real error server-side (`log.Printf("agentman: internal error: %v", err)` to
  stderr) and returns a generic `{"error":"internal"}` body. All sentinel mappings
  (`ErrNotFound`→404, `ErrValidation`→400, `ErrProjectArchived`→400, `ErrConflict`→409,
  `*ConflictError`→409) are unchanged. Tests: `TestWriteErrHidesInternalDetail`,
  `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`. 46 tests pass total.
  (`cmd/am/server.go`)

- **Archived projects' events appeared in the activity feed.** `ListEvents` and `RecentEvents`
  had no archived filter, so the "All"-view feed kept streaming events from archived projects.
  Both functions now LEFT JOIN `projects` and exclude events whose `project_id` belongs to an
  archived project when no explicit `project=` filter is given. An explicit `?project=<slug>`
  still returns all of that project's events for direct inspection. Regression test:
  `TestFeedHidesArchivedProjectEvents`. (`cmd/am/store.go`, `cmd/am/store_test.go`)
- **`am new -p <archived>` silently created a hidden ticket.** `CreateTask` looked up the project
  slug with no archived check, so tasks created into archived projects were immediately invisible
  everywhere. `CreateTask` now rejects with a new `ErrProjectArchived` sentinel (mapped to
  `400 {"error":"project_archived"}` by the HTTP layer). Regression tests:
  `TestCreateTaskRejectsArchivedProject` (store) and `TestCreateTaskIntoArchivedProject400` (HTTP).
  (`cmd/am/store.go`, `cmd/am/server.go`, `cmd/am/store_test.go`, `cmd/am/server_test.go`)

- **Archived projects' tasks were still shown on the board.** Archiving hid a project's tab and
  column header (`ListProjects` filters archived) but `ListTasks` had no archived filter, so the
  tickets kept rendering in the board's "All" view and in `am ls`. `ListTasks` now excludes tasks
  belonging to archived projects when **no explicit project is requested**; an explicit
  `?project=<slug>` / `am ls -p <slug>` still returns them for direct inspection. Regression test:
  `TestListTasksHidesArchivedProjectTasks`. (`cmd/am/store.go`, `cmd/am/store_test.go`)
- **The board clung to the left edge on wide / ultrawide screens.** The status columns cap at
  `max-width: 480px`, so beyond ~1990px of width the leftover space piled up on the right. The
  board now centers with `justify-content: safe center`; the `safe` keyword falls back to
  `flex-start` when columns overflow, so horizontal scrolling on narrow screens never clips the
  first column. The mobile (≤720px) vertical stack stays top-aligned. (`cmd/am/web/app.css`)
- **Review hardening for DB export/import** (caught during the Phase 1 tester pass): `exportDB`
  now fails fast on a missing source DB instead of silently writing an empty snapshot;
  `validateImportCandidate` checks `rows.Err()` after iterating; `copyFile` propagates the file
  close error via a named return rather than swallowing it on a double `Close`. (`cmd/am/db.go`)

### Changed

- **Documentation brought current** with the shipped features across `README.md`,
  `docs/agent-integration.md`, and the `architecture/` set — new commands, routes, event kinds
  (`project.archived` / `project.unarchived`), schema v2, and the now-exercised migration runner.

## [0.3.0] and earlier

Predate this changelog — see the git history (`v0.1.0` – `v0.3.0`). Highlights: the single-binary
CLI + HTTP/SSE server + embedded dashboard, atomic claim, per-directory agent identity,
`am update` + startup version check, the Phase-0 migration-runner foundation, and the localhost
HTTP guardrails (Host allowlist + write-CSRF guard + CSP).
