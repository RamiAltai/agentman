# Engineering Conventions

Derived from the existing code in `cmd/am/`. Follow these so changes stay consistent; where a
convention is loose, it's called out.

## Code Organization

- **One flat `main` package** in `cmd/am/`; modules are separated **by file, not by Go package**:
  `server.go` (HTTP), `hub.go` (SSE), `store.go` (data + domain), `client.go`/`cli.go` (CLI),
  `wait.go` (the `am wait` SSE-consuming verb), `db.go` (offline DB export/import/prune),
  `identity.go`, `version.go`, `update.go`. The web UI is in `cmd/am/web/`.
- Keep that split: HTTP handling in `server.go`, all SQL in `store.go`, CLI presentation in `cli.go`.
  Do **not** put SQL in handlers or HTTP in the store.
- There is no `internal/`/`pkg/`; because it's one package, every symbol is mutually visible —
  discipline is by convention. Don't reach across concerns just because you can.

## Naming Conventions

- HTTP handlers: `handleX` (e.g. `handleClaim`). Routes registered in `Server.Handler()`.
- Store methods: exported PascalCase domain verbs (`CreateTask`, `ClaimTask`, `ListEvents`).
- CLI verb implementations: `cmdX` (`cmdClaim`, `cmdNew`); dispatched in `main.go`.
- Short flags canonicalize in `canonFlag` (`cli.go`): `-p` → project, `-c` → **category**, `-s` →
  status, `-a` → assign, `-l` → label. One carve-out: `am show <id> -c` keeps meaning
  `--comments` — `main.go` rewrites `-c → --comments` for the `show` verb only
  (`rewriteShowComments`, before `parse()`).
- Sentinel errors: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrProjectArchived` (→ HTTP 400 `project_archived`), `ErrCategoryArchived` (→ HTTP 400 `category_archived`); typed `*ConflictError{Assignee}`; typed `*BlockedError{OpenPrereqs []int64}` (→ HTTP 409 `{"error":"blocked","open_prereqs":[…]}`); typed `*NotStaleError{Assignee}` (→ HTTP 409 `{"error":"not_stale","assignee":…}`).
- Event kinds: dotted `noun.verb` strings — `task.created`, `task.claimed`, `task.reclaimed`,
  `task.status`, `task.assign`, `task.patched`, `task.deleted`, `task.dep_added`,
  `task.dep_removed`, `task.labeled`, `task.unlabeled`, `comment.added`, `comment.deleted`,
  `project.created`, `project.archived`, `project.unarchived`, `project.patched`,
  `project.deleted`, `category.created`, `category.archived`, `category.unarchived` (21 total).
- Stable IDs: `newUID(prefix)` — prefix + 16 lowercase hex chars from `crypto/rand`; `amc_` for
  categories, `amp_` for projects. Immutable after creation; insert paths retry on a uid UNIQUE
  collision via `isUniqueErr`.
- Env vars: `AGENTMAN_*` (`AGENTMAN_URL/PROJECT/CATEGORY/AGENT/AGENT_FILE/DB/PORT/NO_UPDATE_CHECK/LOG`).

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
  **exit code** via `client.go exitCodeFor` (single source, used by `doOrFail` and the bulk
  `status`/`assign` loop): `0` ok · `1` generic · `3` not found · `4` conflict · `5` validation ·
  `6` server down; plus `7` = `am wait` timeout (CLI-side, no HTTP status).
- CLI: errors go to **stderr** via `fail(code, fmt, …)`; **stdout stays clean** (only ids on
  create/claim) so command substitution works.

## Validation

- Status: `validStatus` map + SQL `CHECK`. Reject empty title/slug/body with `ErrValidation`.
- `PatchTask` applies only known keys (`status/assignee/title/body/priority`) and ignores the rest.

## Logging

- `log.Printf`/`log.Fatalf` to stderr, sparingly (startup line, shutdown, update banner, and
  internal server errors). No structured logging, metrics, or tracing. If you add logging, don't
  log task/comment contents or tokens.
- **Opt-in request logging** — the `requestLogger` middleware (Phase D2) logs
  `METHOD PATH STATUS LATENCY ACTOR` per request when `am serve --log` is passed or
  `AGENTMAN_LOG` is set (any non-empty value). Off by default.

## Configuration

- Flags on `am serve`: `--port`, `--db`, `--log`. Everything else is env (`AGENTMAN_*`) with
  sensible defaults (`defaultDBPath` → `~/.agentman/agentman.db`, port `8787`). Flags override
  env where both exist. Add new config as an `AGENTMAN_*` env var and/or a flag, default-off /
  backward-compatible.
- Env vars: `AGENTMAN_URL`, `AGENTMAN_PROJECT`, `AGENTMAN_CATEGORY` (default category scope for
  `ls`/`next`/`wait --ready`/`project new`), `AGENTMAN_AGENT`, `AGENTMAN_AGENT_FILE`,
  `AGENTMAN_DB`, `AGENTMAN_PORT`, `AGENTMAN_NO_UPDATE_CHECK`, `AGENTMAN_LOG`.

## Testing

- `go test -race ./cmd/am/` (or `go test ./...`); table-driven tests (see `cmd/am/update_test.go`).
  Coverage spans pure logic, the store, HTTP, migrations, offline DB tooling, CLI verbs + exit codes,
  SSE streaming/reconnect, `am wait`, identity, and the dashboard XSS-sink guard — 10 test files,
  174 tests.
- **`osExit` testability var** — `cli.go` declares `var osExit = os.Exit`; `fail()` calls `osExit`
  rather than `os.Exit` directly. Tests in `cli_test.go` replace it via `captureExit(t, fn)`,
  which substitutes a panic-based stub so exit codes can be asserted without terminating the process.
  Follow this pattern for any new CLI path that needs exit-code testing.
- **`captureStdout(t, fn)`** in `cli_test.go` — redirects `os.Stdout` to a pipe for the duration
  of `fn` and returns the captured output. Use this to assert CLI stdout is clean (no chatter).
- **`AGENTMAN_AGENT_FILE` seam** in `identity.go` — set via `t.Setenv` in identity tests to
  redirect the per-dir identity file to `t.TempDir()` so the real `~/.agentman` is never written.
- **No JS test runner** — the project deliberately avoids npm/jsdom (preserves the
  single-binary/no-build-step ethos; ADR-018). Dashboard XSS safety is enforced by the
  `el()`/`textContent` convention plus the `TestDashboardNoXSSSinks` Go source-level sink guard
  in `web_test.go`. Do not add a JS runner; extend `web_test.go` instead if you add new patterns.

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
CI (`.github/workflows/ci.yml`) enforces the same checks — build, vet, gofmt, test(-race),
JS syntax (`node --check`), and `govulncheck` — on every push to `main` and on every PR.

## SVG Convention

SVG elements in `web/app.js` are created with the `svg(tag, attrs)` helper
(`document.createElementNS(SVG_NS, tag)`), parallel to `el()` for HTML. All text is set via
`.textContent`, never by attribute concatenation or `innerHTML`. This keeps the XSS-safe DOM
convention consistent across HTML and SVG. **Never** use `innerHTML` to construct SVG either —
the `TestDashboardNoXSSSinks` guard will catch it. When adding new SVG-based UI, use `svg()` for
SVG elements and `el()` for any HTML wrapper elements (e.g., the detail panel next to the canvas).

## Anti-Patterns

- ❌ `innerHTML` / `insertAdjacentHTML` in `web/` — always use `el()` / `textContent` for HTML
  and `svg()` / `textContent` for SVG (XSS).
- ❌ SQL built by string concatenation with user input.
- ❌ Broadcasting an SSE event before the DB transaction commits.
- ❌ A second DB writer / opening the SQLite file from the CLI (breaks the single-writer model).
- ❌ Binding beyond `127.0.0.1` without adding authentication first.
- ❌ Printing chatter to **stdout** in the CLI (breaks `id=$(am new …)` and token budgets).
- ❌ Editing `cmd/am/web/*` and forgetting to rebuild — the running server serves stale embedded assets.
- ❌ Adding a **column** to an existing table in `schema.sql` and assuming existing DBs get it —
  `ALTER TABLE` is needed. Use the forward-only runner: append a `{version, apply}` step to
  `schemaMigrations` and bump `currentSchemaVersion`; `schema.sql` still seeds fresh DBs.
  Exception: adding a **new table** with `CREATE TABLE IF NOT EXISTS` propagates automatically on
  every `OpenStore` without a runner step or version bump (see `task_deps` as the worked example).
  Never use `CREATE TABLE IF NOT EXISTS` to add a column to an existing table.

## Unknowns

- **Commit-message convention** is not documented; observed history is short, lowercase, imperative
  (`"add 'am update' command and startup update check"`, `"docs update"`). Confidence: Low.
- No `.editorconfig`/linter config beyond `gofmt`/`go vet`; style is "standard Go".
