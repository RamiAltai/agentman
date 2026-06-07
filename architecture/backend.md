# Backend Architecture

## Framework and Runtime

- **Go** (module `go 1.25.0`; `go.mod`). Standard-library HTTP — `net/http` with the Go 1.22+
  **method+pattern ServeMux** (e.g. `mux.HandleFunc("GET /api/tasks/{id}", …)` in
  `cmd/am/server.go`). No web framework.
- **SQLite** via `modernc.org/sqlite` v1.51.0 (pure Go, no cgo) — the only direct dependency.
- The server is started by `runServe()` in `cmd/am/main.go`:
  `http.Server{Addr: "127.0.0.1:" + port, ReadHeaderTimeout: 10s, BaseContext: …}`.

## API Structure

Routes are registered in one place — `Server.Handler()` (`cmd/am/server.go`):

```
GET   /api/projects                 handleListProjects      ?archived=true includes archived
POST  /api/projects                 handleCreateProject     {slug,name}
POST  /api/projects/{slug}/archive    handleArchiveProject
POST  /api/projects/{slug}/unarchive  handleUnarchiveProject
GET   /api/tasks                    handleListTasks         ?project=&status=&assignee=&limit=  (no project ⇒ hides archived-project tasks)
POST  /api/tasks                    handleCreateTask        {project,title,body?,priority?,assignee?}
GET   /api/tasks/{id}               handleGetTask           (task + comments + recent events)
PATCH /api/tasks/{id}               handlePatchTask         {status?,assignee?,title?,body?,priority?}
POST  /api/tasks/{id}/claim         handleClaim             (atomic; X-Agent = claimant)
POST  /api/tasks/{id}/comments      handleComment           {body}
GET   /api/events                   handleEvents            ?since=  | ?tail=  | ?before=  | ?project=  (no project ⇒ hides archived-project events)
GET   /api/stream                   handleStream            text/event-stream (SSE)
DELETE /api/tasks/{id}              handleDeleteTask        hard-delete task + comments (cascade); 200 {"status":"deleted"}
DELETE /api/tasks/{id}/comments/{cid} handleDeleteComment  hard-delete one comment; 200 {"status":"deleted"}
DELETE /api/projects/{slug}         handleDeleteProject     hard-delete project + tasks + comments (cascade); 200 {"status":"deleted"}
/                                   http.FileServer(embed)  serves cmd/am/web/
```

`{id}` accepts a global id (`13`) or a project ref (`web-3`), resolved by `store.resolveTaskID`.
Responses are JSON via `writeJSON`; errors via `writeErr`.

`am db export`/`am db import`/`am db prune` have **no HTTP route** — they are CLI-only local-file
operations. The `db` command is dispatched in `cmd/am/main.go` *before* the HTTP client is built
(`cmdDB`, ahead of `NewClient()`), so it works directly on the SQLite file (`cmd/am/db.go`).

## Request Flow

When `--log` / `AGENTMAN_LOG` is enabled, the chain is wrapped OUTERMOST by `requestLogger`
(so guard 403s are also logged): `requestLogger(securityHeaders(hostGuard(csrfGuard(mux))))`.
Otherwise the chain is `securityHeaders(hostGuard(csrfGuard(mux)))` (Phase 0/ADR-011). Then:
`mux` → `handleX(w, r)` → decode JSON body (`decode`, capped at 1 MiB via `io.LimitReader`) →
call a `store.*` method → on success `hub.Broadcast(event)` **after** the store commits →
`writeJSON`. The actor for writes comes from the `X-Agent` header (`actorOf`, default `"human"`).
The security middleware rejects non-loopback `Host` (403) and cross-origin browser writes (403)
without affecting the CLI, reads, or SSE.

`requestLogger` wraps the response writer in a `statusRecorder` (captures the status code,
defaulting to 200; also implements `http.Flusher` so SSE connections continue to work when
logging is enabled). It logs one line per request after completion:
`METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default `"human"`), via the standard
`log` package to stderr. Note: a long-lived SSE connection logs once on disconnect with a
large latency (inherent).

## Business Logic

Lives in `cmd/am/store.go` (there is no separate "service" layer — the store *is* the domain
logic). Each mutating method returns `(result, *Event, error)`; the handler broadcasts the event.
Key methods: `CreateProject`, `ListProjects(includeArchived bool)`, `ArchiveProject`,
`UnarchiveProject`, `DeleteProject`, `ListTasks`, `GetTask`, `CreateTask`, `PatchTask`,
`ClaimTask`, `AddComment`, `DeleteComment`, `ListEvents`, `RecentEvents`, `ListEventsBefore`,
`DeleteTask`. `ArchiveProject`/`UnarchiveProject` are
transactional and idempotent (no event when already in the target state).
`CreateTask` checks the target project's `archived_at` before the insert and returns
`ErrProjectArchived` if archived — creation into an archived project is rejected.
`ListEvents`/`RecentEvents` use `LEFT JOIN projects p ON p.id = events.project_id` and,
when no explicit `project=` filter is supplied, exclude events whose project is archived via
`(events.project_id IS NULL OR p.archived_at IS NULL)` — mirroring `ListTasks`. An explicit
`?project=<slug>` filter still returns that project's events. `ListEventsBefore(before, project,
limit)` applies the same archived-project filter and returns events with `id < before`,
newest-first (default limit 40, cap 200) — used by the `?before=` cursor branch in `handleEvents`
for backward pagination.

**Atomic claim** (`ClaimTask`) is the most important invariant — a single conditional statement:

```sql
UPDATE tasks SET assignee=?, status=CASE WHEN status='todo' THEN 'doing' ELSE status END, updated_at=…
 WHERE id=? AND assignee IS NULL AND status!='done'
RETURNING project_id, status;
```

Zero rows ⇒ loser; the code then distinguishes idempotent re-claim by the same agent (returns the
task, no event) from `*ConflictError` (owned by someone else) and `ErrNotFound`.

## Data Access

- One `*sql.DB` with **`SetMaxOpenConns(1)`** → single writer, so writes serialize and
  `SQLITE_BUSY` is effectively impossible (`cmd/am/store.go OpenStore`).
- Pragmas set on the DSN (applied per-connection at open): `busy_timeout(5000)`,
  `journal_mode(WAL)`, `foreign_keys(1)`, `synchronous(1)`.
- **All queries are parameterized** (`?` placeholders) — no string-concatenated SQL with user input.
- Mutations + their `events` row run in one `*sql.Tx`; broadcast happens only after commit so SSE
  never announces uncommitted state.

See `data-model.md` for the schema.

## Models and Schemas

Go structs in `cmd/am/store.go`: `Project`, `Task`, `Comment`, `Event`, `TaskFilter`,
`CreateTaskInput`. SQL schema in `cmd/am/schema.sql` (embedded via `//go:embed schema.sql`).

## Authentication and Authorization

**No authentication.** The `X-Agent` header is an *actor label* for attribution, not a credential —
any caller can claim any identity. Access control is the `127.0.0.1` bind, now hardened by the
Phase 0 guardrails (Host allowlist + write-CSRF guard, `server.go`, ADR-011) which block
browser-driven cross-origin/DNS-rebinding attacks but are **not** auth (any local process is still
trusted). No per-resource authorization. See `security.md` (ADR-002/ADR-011 in `decision-records.md`).

## Validation

- Status validated against `validStatus` map and a SQL `CHECK (status IN (...))` constraint
  (`store.go`, `schema.sql`).
- Empty title / slug / comment body rejected with `ErrValidation`; slug must not contain spaces
  (`CreateProject`).
- Priority coerced via `toInt`. Unknown PATCH keys are ignored (only known fields applied in
  `PatchTask`).
- Handlers map `ErrValidation` → HTTP 400.
- Creating a task into an archived project is rejected: `CreateTask` returns `ErrProjectArchived`
  → HTTP 400 `{"error":"project_archived"}`.

## Background Jobs

No job queue. Long-lived goroutines only:
- **SSE connections** — one goroutine per `handleStream` request, with a 15s heartbeat ticker;
  cleaned up on `r.Context().Done()` (`cmd/am/server.go`, `cmd/am/hub.go`).
- **Startup update check** — `checkForUpdate()` fires a single background goroutine (4s timeout,
  silent on error) (`cmd/am/update.go`).
- **Graceful shutdown** — SIGINT/SIGTERM → cancel base context (unblocks SSE) → `Shutdown(3s)` →
  `PRAGMA wal_checkpoint(TRUNCATE)` → close (`runServe`, `store.Close`).

## External Integrations

- `proxy.golang.org` — version check (`checkForUpdate`); opt out with `AGENTMAN_NO_UPDATE_CHECK=1`.
- `go install …@<ver>` shelled out via `os/exec` in `am update` (`cmdUpdate`).

## Error Handling

Sentinel errors in `store.go`: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrProjectArchived`,
and a typed `*ConflictError{Assignee}`. `writeErr` (`server.go`) maps them: 404 / 409 / 400, with
`ConflictError` → `409 {"error":"already_claimed","assignee":…}`,
`ErrProjectArchived` → `400 {"error":"project_archived"}`,
`ErrValidation` → `400`; anything else → **HTTP 500 with a generic `{"error":"internal"}` body**
(the real error is logged server-side via `log.Printf("agentman: internal error: %v", err)` to
stderr — it is never sent to the client). Delete handlers (`handleDeleteTask`,
`handleDeleteComment`, `handleDeleteProject`) return `404` via `writeErr` when the target is
missing (`ErrNotFound`). The CLI re-maps HTTP status to **exit codes** in `client.go doOrFail`
(`3` not found · `4` conflict · `5` validation/project_archived · `6` server down · `1` other).

## Observability

Minimal: `log.Printf` to stderr for startup, shutdown, the update banner, and `log.Fatalf` on a
fatal listen error. **No structured logging, metrics, or tracing.**

**Opt-in request logging** is available via `am serve --log` or the `AGENTMAN_LOG` env var (any
non-empty value; use `AGENTMAN_LOG=1`). When enabled, `runServe` logs `request logging enabled`
at startup and installs the `requestLogger` middleware outermost in the chain. It logs one line
per request after completion: `METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default
`"human"`). Plain `log.Printf` lines to stderr — not structured logging. Off by default.

## Testing

There are nine test files (run `go test -race ./cmd/am/`; 71 tests, all green):
- `cmd/am/update_test.go` — version-comparison logic.
- `cmd/am/store_test.go` — atomic-claim race (concurrent, `-race`-clean), events-cursor monotonicity,
  store CRUD + validation (`ErrValidation`), project archive/unarchive round-trip + idempotency,
  and hard-delete cascade/not-found (`TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
  `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades`).
- `cmd/am/server_test.go` — validation→status mapping (400/404/409), `hostGuard`, `csrfGuard`,
  `securityHeaders`, `listenAddr` loopback regression, archive/unarchive endpoints + 404,
  hard-delete HTTP endpoints (`TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`,
  `TestDeleteCommentEndpoint`), and Phase D: `TestWriteErrHidesInternalDetail` (500 returns
  generic body, not raw error), `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`
  (via `net/http/httptest`).
- `cmd/am/migrate_test.go` — migration runner (apply/skip/idempotent/rollback), incl. the v2 step
  that adds `projects.archived_at`.
- `cmd/am/db_test.go` — `db export`/`import` roundtrip + file perms (0o600), backup creation + perms,
  garbage rejection, server-liveness check; `TestPruneEventsKeep`, `TestPruneEventsBefore`,
  `TestPruneEventsBeforeSameDayBoundary` (prune).
- `cmd/am/cli_test.go` — CLI command-path + exit-code tests (Phase E1). Exercises verbs against a
  real `httptest` server via a directly-constructed `Client`, using `captureStdout`/`captureExit`
  helpers. Covers: `cmdNew` prints only the numeric id; `cmdLs` produces terse output; mutations
  (`cmdStatus`/`cmdNote`/`cmdDrop`) are silent on success; and the exit-code mapping in
  `client.go doOrFail` — 3 (not found), 4 (conflict), 5 (validation/`project_archived`), 6 (server
  down). Also table-tests for `parse`/`Args` and the pure formatters
  (`taskLine`/`statusShort`/`assignee`/`trunc`/`apiErr`).
- `cmd/am/sse_test.go` — SSE streaming + reconnect (Phase E2). `TestSSEDeliversLiveEvent`
  subscribes to `/api/stream`, creates a task, and asserts the `task.created` event arrives live.
  `TestSSEReplayOnReconnect` reconnects with `Last-Event-ID` and asserts that events created while
  disconnected are replayed and deduplicated (every replayed id is strictly greater than the resume
  cursor).
- `cmd/am/identity_test.go` — identity (Phase E3). `cmdInit`→`resolveAgent` roundtrip,
  `AGENTMAN_AGENT` env override wins, `sanitizeType` table, `newIdentity` format. Isolates via the
  `AGENTMAN_AGENT_FILE` env seam so the real `~/.agentman` is never written.
- `cmd/am/web_test.go` — dashboard XSS-sink guard (Phase E4). `TestDashboardNoXSSSinks` reads the
  embedded `web/app.js` + `web/index.html` via the `webFS` embed.FS and asserts that none of
  `.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear — a source-level
  regression guard that locks in the `el()`/`textContent` XSS-safe DOM convention.

So SSE streaming/reconnect, CLI verbs, exit-code mapping, and identity are now covered. The
dashboard has a source-level XSS-sink guard but **no behavioral JS tests** — the project
deliberately adopts no JS test runner (preserves the single-binary/no-npm ethos). (See
`known-risks-and-gaps.md`.)

## Where to Add New Features

- **New endpoint:** register it in `Server.Handler()` (`server.go`), add a `handleX`, add the
  backing `store.*` method, and (if it mutates) insert an `events` row in the same tx + broadcast.
- **New task field:** add the column in `schema.sql`, the struct field in `store.go`, thread it
  through `CreateTask`/`PatchTask`/`getTaskTx`, the API, and the dashboard (`web/`).
- **New event kind:** emit via `insertEvent(...)` and handle it in `web/app.js` `evText`/`describeText`.
  Current kinds (12 total): `task.created`, `task.claimed`, `task.status`, `task.assign`,
  `task.patched`, `task.deleted`, `comment.added`, `comment.deleted`, `project.created`,
  `project.archived`, `project.unarchived`, `project.deleted`.

## Risks and Gaps

- **Migration runner is now exercised** — Phase 0 added `runMigrations` (ADR-010); Phase 2's first
  step (`ALTER TABLE projects ADD COLUMN archived_at TEXT`, `currentSchemaVersion = 2`) proves the
  additive-column path end-to-end. A DB *newer* than the binary is still accepted silently today.
- **Single-writer** caps write throughput; fine for a personal board, unproven at scale.
- ~~**500s leak raw error strings**~~ — **fixed (Phase D1)**; 500s now return a generic `{"error":"internal"}` body; detail is logged server-side only.
- **No request size/time limits** beyond a 1 MiB body cap and `ReadHeaderTimeout`.
