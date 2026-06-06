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
GET   /api/projects                 handleListProjects
POST  /api/projects                 handleCreateProject     {slug,name}
GET   /api/tasks                    handleListTasks         ?project=&status=&assignee=&limit=
POST  /api/tasks                    handleCreateTask        {project,title,body?,priority?,assignee?}
GET   /api/tasks/{id}               handleGetTask           (task + comments + recent events)
PATCH /api/tasks/{id}               handlePatchTask         {status?,assignee?,title?,body?,priority?}
POST  /api/tasks/{id}/claim         handleClaim             (atomic; X-Agent = claimant)
POST  /api/tasks/{id}/comments      handleComment           {body}
GET   /api/events                   handleEvents            ?since=  | ?tail=  (poll / feed bootstrap)
GET   /api/stream                   handleStream            text/event-stream (SSE)
/                                   http.FileServer(embed)  serves cmd/am/web/
```

`{id}` accepts a global id (`13`) or a project ref (`web-3`), resolved by `store.resolveTaskID`.
Responses are JSON via `writeJSON`; errors via `writeErr`.

## Request Flow

`mux` → `handleX(w, r)` → decode JSON body (`decode`, capped at 1 MiB via `io.LimitReader`) →
call a `store.*` method → on success `hub.Broadcast(event)` **after** the store commits →
`writeJSON`. The actor for writes comes from the `X-Agent` header (`actorOf`, default `"human"`).

## Business Logic

Lives in `cmd/am/store.go` (there is no separate "service" layer — the store *is* the domain
logic). Each mutating method returns `(result, *Event, error)`; the handler broadcasts the event.
Key methods: `CreateProject`, `ListTasks`, `GetTask`, `CreateTask`, `PatchTask`, `ClaimTask`,
`AddComment`, `ListEvents`, `RecentEvents`.

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

**None.** There is no auth layer and no authorization checks. The `X-Agent` header is an *actor
label* for attribution, not a credential — any caller can claim any identity. Access control is
entirely the `127.0.0.1` bind. See `security.md`. This is a deliberate decision (ADR-002 in
`decision-records.md`).

## Validation

- Status validated against `validStatus` map and a SQL `CHECK (status IN (...))` constraint
  (`store.go`, `schema.sql`).
- Empty title / slug / comment body rejected with `ErrValidation`; slug must not contain spaces
  (`CreateProject`).
- Priority coerced via `toInt`. Unknown PATCH keys are ignored (only known fields applied in
  `PatchTask`).
- Handlers map `ErrValidation` → HTTP 400.

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

Sentinel errors in `store.go`: `ErrNotFound`, `ErrConflict`, `ErrValidation`, and a typed
`*ConflictError{Assignee}`. `writeErr` (`server.go`) maps them: 404 / 409 / 400, with
`ConflictError` → `409 {"error":"already_claimed","assignee":…}`; anything else → 500 with the raw
message. The CLI re-maps HTTP status to **exit codes** in `client.go doOrFail`
(`3` not found · `4` conflict · `5` validation · `6` server down · `1` other).

## Observability

Minimal: `log.Printf` to stderr for startup, shutdown, the update banner, and `log.Fatalf` on a
fatal listen error. **No structured logging, request logging, metrics, or tracing.** (Gap.)

## Testing

Only `cmd/am/update_test.go` (3 tests: `TestEscapeModule`, `TestPseudoStamp`,
`TestUpdateAvailable`) — pure version-comparison logic. **No tests for handlers, the store, the
atomic claim, SSE, or the CLI.** Run with `go test ./...`. (Gap; see `known-risks-and-gaps.md`.)

## Where to Add New Features

- **New endpoint:** register it in `Server.Handler()` (`server.go`), add a `handleX`, add the
  backing `store.*` method, and (if it mutates) insert an `events` row in the same tx + broadcast.
- **New task field:** add the column in `schema.sql`, the struct field in `store.go`, thread it
  through `CreateTask`/`PatchTask`/`getTaskTx`, the API, and the dashboard (`web/`).
- **New event kind:** emit via `insertEvent(...)` and handle it in `web/app.js` `evText`/`describeText`.

## Risks and Gaps

- **No migration runner** — `OpenStore` only runs `CREATE TABLE IF NOT EXISTS`; `meta.schema_version`
  exists but nothing reads it. Adding/altering columns on existing DBs is unhandled. (See ADR/IADR.)
- **Single-writer** caps write throughput; fine for a personal board, unproven at scale.
- **500s leak raw error strings** to clients (`writeErr` default branch) — minor info exposure.
- **No request size/time limits** beyond a 1 MiB body cap and `ReadHeaderTimeout`.
