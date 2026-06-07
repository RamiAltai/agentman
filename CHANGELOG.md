# Changelog

All notable changes to **agentman** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

When cutting a release, rename the `[Unreleased]` heading to the version + date and start a
fresh `[Unreleased]` section.

## [Unreleased]

_Target: **v0.4.0** — not yet tagged._

### Added

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
