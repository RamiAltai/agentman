# Engineering Conventions

Derived from the existing code in `cmd/am/`. Follow these so changes stay consistent; where a
convention is loose, it's called out.

## Code Organization

- **One flat `main` package** in `cmd/am/`; modules are separated **by file, not by Go package**:
  `server.go` (HTTP), `hub.go` (SSE), `store.go` (data + domain), `client.go`/`cli.go` (CLI),
  `db.go` (offline DB export/import), `identity.go`, `version.go`, `update.go`. The web UI is in `cmd/am/web/`.
- Keep that split: HTTP handling in `server.go`, all SQL in `store.go`, CLI presentation in `cli.go`.
  Do **not** put SQL in handlers or HTTP in the store.
- There is no `internal/`/`pkg/`; because it's one package, every symbol is mutually visible —
  discipline is by convention. Don't reach across concerns just because you can.

## Naming Conventions

- HTTP handlers: `handleX` (e.g. `handleClaim`). Routes registered in `Server.Handler()`.
- Store methods: exported PascalCase domain verbs (`CreateTask`, `ClaimTask`, `ListEvents`).
- CLI verb implementations: `cmdX` (`cmdClaim`, `cmdNew`); dispatched in `main.go`.
- Sentinel errors: `ErrNotFound`, `ErrConflict`, `ErrValidation`; typed `*ConflictError`.
- Event kinds: dotted `noun.verb` strings — `task.created`, `task.claimed`, `task.status`,
  `task.assign`, `task.patched`, `comment.added`, `project.created`, `project.archived`,
  `project.unarchived`.
- Env vars: `AGENTMAN_*` (`AGENTMAN_URL/PROJECT/AGENT/AGENT_FILE/DB/PORT/NO_UPDATE_CHECK`).

## API Conventions

- JSON in/out; write via `writeJSON`, errors via `writeErr` (`{"error": "..."}`).
- Status codes: `200/201` success, `400` validation, `404` not found, `409` conflict
  (`{"error":"already_claimed","assignee":…}` for lost claims), `500` otherwise.
- Writes read the actor from the **`X-Agent`** header (`actorOf`, default `"human"`).
- **List endpoints return a terse projection** (no body/comments); the full object is only on
  `GET /api/tasks/{id}`. Keep list payloads lean — agents hit them most.
- `{id}` path params accept a global id or a `slug-ref` (`resolveTaskID`).

## Data Access Conventions

- **All DB access goes through `store.go`.** No direct SQLite access from handlers or the CLI.
- **Always parameterize** (`?`); never concatenate caller input into SQL.
- A mutation and its `events` row must be in **one `*sql.Tx`**; **broadcast only after commit**
  (`hub.Broadcast(ev)` in the handler, not the store).
- Timestamps via SQL `strftime('%Y-%m-%dT%H:%M:%fZ','now')` (UTC ISO-8601 TEXT); set `updated_at`
  explicitly in every `UPDATE`.
- Represent SQL NULLs with the `nullStr`/`nullable`/`nullableID` helpers.

## Error Handling

- Store returns sentinel errors; handlers translate via `writeErr`; the CLI translates HTTP status →
  **exit code** in `client.go doOrFail` (`0` ok · `1` generic · `3` not found · `4` conflict ·
  `5` validation · `6` server down).
- CLI: errors go to **stderr** via `fail(code, fmt, …)`; **stdout stays clean** (only ids on
  create/claim) so command substitution works.

## Validation

- Status: `validStatus` map + SQL `CHECK`. Reject empty title/slug/body with `ErrValidation`.
- `PatchTask` applies only known keys (`status/assignee/title/body/priority`) and ignores the rest.

## Logging

- `log.Printf`/`log.Fatalf` to stderr, sparingly (startup line, shutdown, update banner). No
  structured logging or request logs. If you add logging, don't log task/comment contents or tokens.

## Configuration

- Flags on `am serve`: `--port`, `--db`. Everything else is env (`AGENTMAN_*`) with sensible
  defaults (`defaultDBPath` → `~/.agentman/agentman.db`, port `8787`). Flags override env where both
  exist. Add new config as an `AGENTMAN_*` env var and/or a flag, default-off / backward-compatible.

## Testing

- `go test ./...` (`go test -race ./cmd/am/` for the race detector); table-driven tests (see
  `cmd/am/update_test.go`). Coverage spans pure logic plus the store, HTTP, migrations, and the
  offline DB tooling — `store_test.go`, `server_test.go`, `migrate_test.go`, `db_test.go`.

## Commands

```sh
go build -o am ./cmd/am     # build (rebuild after ANY cmd/am/web change — assets are embedded)
go vet ./...                # static checks
go test ./...               # tests
gofmt -l cmd/am             # list unformatted files (should be empty)
gofmt -w cmd/am             # auto-format
```

**Before submitting:** `gofmt -w`, `go vet ./...`, `go test ./...` all clean, and update the
matching `architecture/` doc. (`gofmt -l cmd/am` is currently empty — no outstanding format drift.)

## Anti-Patterns

- ❌ `innerHTML` / `insertAdjacentHTML` in `web/` — always use `el()` / `textContent` (XSS).
- ❌ SQL built by string concatenation with user input.
- ❌ Broadcasting an SSE event before the DB transaction commits.
- ❌ A second DB writer / opening the SQLite file from the CLI (breaks the single-writer model).
- ❌ Binding beyond `127.0.0.1` without adding authentication first.
- ❌ Printing chatter to **stdout** in the CLI (breaks `id=$(am new …)` and token budgets).
- ❌ Editing `cmd/am/web/*` and forgetting to rebuild — the running server serves stale embedded assets.
- ❌ Adding a column to `schema.sql` and assuming existing DBs get it. A forward-only runner
  exists (`store.go runMigrations`): for a column change, append a `{version, apply}` step to
  `schemaMigrations` and bump `currentSchemaVersion`; `schema.sql` still seeds fresh DBs.

## Unknowns

- **Commit-message convention** is not documented; observed history is short, lowercase, imperative
  (`"add 'am update' command and startup update check"`, `"docs update"`). Confidence: Low.
- No `.editorconfig`/linter config beyond `gofmt`/`go vet`; style is "standard Go".
