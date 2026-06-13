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
- **Repeatable value flags** register in the `multiFlags` map (`cli.go`, like `boolFlags` —
  registration is global across verbs); every occurrence is collected in order into `Args.multi`
  and read with `a.all(k)`. A flag is multi OR single (last-wins `Args.flags`), never both.
  Prefer repetition over comma-splitting one value when values are opaque (may contain commas).
  `--meta` is the first multi flag; it has no short alias.
- Sentinel errors: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrProjectArchived` (→ HTTP 400 `project_archived`), `ErrCategoryArchived` (→ HTTP 400 `category_archived`), `ErrOutOfScope` (→ HTTP 403 `out_of_scope` → CLI exit 8), `ErrInvalidToken` (Phase S; message `unauthorized` → HTTP 401 `unauthorized` → CLI exit 9); typed `*ConflictError{Assignee}`; typed `*BlockedError{OpenPrereqs []int64}` (→ HTTP 409 `{"error":"blocked","open_prereqs":[…]}`); typed `*NotStaleError{Assignee}` (→ HTTP 409 `{"error":"not_stale","assignee":…}`).
- Event kinds: dotted `noun.verb` strings — `task.created`, `task.claimed`, `task.reclaimed`,
  `task.status`, `task.assign`, `task.patched`, `task.deleted`, `task.dep_added`,
  `task.dep_removed`, `task.labeled`, `task.unlabeled`, `comment.added`, `comment.deleted`,
  `project.created`, `project.archived`, `project.unarchived`, `project.patched`,
  `project.deleted`, `category.created`, `category.archived`, `category.unarchived` (21 total).
- Stable IDs: `newUID(prefix)` — prefix + 16 lowercase hex chars from `crypto/rand`; `amc_` for
  categories, `amp_` for projects, `tk_` for token ids. Immutable after creation; insert paths retry
  on a uid UNIQUE collision via `isUniqueErr`. The bearer-token plaintext is a separate primitive:
  `newToken()` → `amt_` + 32 lowercase hex (16 bytes of `crypto/rand`), and only its `hashToken()`
  sha256 is stored (Phase S).
- Env vars: `AGENTMAN_*` (`AGENTMAN_URL/PROJECT/CATEGORY/SCOPE/TOKEN/AGENT/AGENT_FILE/DB/PORT/PROPOSALS/NO_UPDATE_CHECK/LOG`).

## Scope Enforcement (Phase Q; tokens Phase S)

- **One scope-resolution point.** `(s *Server) scopeOf(r)` in `server.go` is the **sole** reader of
  request scope — no handler reads `Authorization` or the `X-Agent-Scope` header directly. Every
  handler that mutates or names a resource calls it and then a `check*`/`narrowScope` helper.
  **Precedence (Phase S):** a bearer token (`Authorization: Bearer`, via `bearerToken`) WINS — it
  resolves to the token's server-bound scope (`store.ResolveToken`) and ignores any header; an
  unknown/revoked token is `ErrInvalidToken` (→ 401 → exit 9), never a silent fallthrough. With no
  token, `X-Agent-Scope` (`category[/project]`, parsed by `parseScope`) is the scope; the zero `Scope`
  (absent both) is unscoped and passes every check. `scopeOf` was made a `*Server` method in Phase S
  precisely so it can reach the store — it remains the one resolution point.
- **Token-backed but loopback-only.** A bearer token is server-minted and scope-bound, so it confines
  a *config-following* agent that cannot forge another scope's token — but it is **not** a security
  control against an arbitrary local process (a filesystem read of the identity file = token
  possession). `X-Agent-Scope` alone is purely client-asserted accident prevention (`security.md`).
- **Mint-requires-unscoped.** `tokenAdminGuard` refuses `POST/GET /api/tokens` and
  `POST /api/tokens/{id}/revoke` for ANY scoped caller (header OR a valid token) with `denyScope`
  (the `handleCreateCategory` precedent), so only an unscoped human administers tokens.
- **Denials are log-only.** `denyScope` logs `agentman: out_of_scope: actor=<id> scope=<scope>
  <METHOD> <PATH>` and returns `ErrOutOfScope`. **No `scope.denied` event kind** — never feed
  denials into the SSE stream (noise + a partial-information leak). The event catalog stays at 21.
- **Checks run outside the store tx**, sound only because `task→project` and `project→category` are
  immutable; if a move feature ships, move the checks in-tx (see the `PatchTask`/`PatchProject`
  scope-note comments).
- **Proposals carve-out matches the (category, project) pair** (`isProposals`) — slugs are globally
  unique, so the pair check blocks a slug-squat; a missing project leaves the carve-out inert.

## API Conventions

- JSON in/out; write via `writeJSON`, errors via `writeErr` (`{"error": "..."}`).
- Status codes: `200/201` success, `400` validation, `401` invalid/revoked bearer token
  (`{"error":"unauthorized"}`, Phase S), `403` out of scope (`{"error":"out_of_scope"}`, Phase Q),
  `404` not found, `409` conflict (`{"error":"already_claimed","assignee":…}` for lost claims),
  `500` otherwise.
- Writes read the actor from the **`X-Agent`** header (`actorOf`, default `"human"`).
- **List endpoints return a terse projection** (no body/comments); the full object is only on
  `GET /api/tasks/{id}`. Keep list payloads lean — agents hit them most.
- `{id}` path params accept a global id or a `slug-ref` (`resolveTaskID`).

## Data Access Conventions

- **All DB access goes through `store.go`.** No direct SQLite access from handlers or the CLI.
- **Always parameterize** (`?`); never concatenate caller input into SQL.
- A mutation and its `events` row must be in **one `*sql.Tx`**; **broadcast only after commit**
  (`hub.Broadcast(ev)` in the handler, not the store).
- **The SSE hub stays DB-free.** `hub.Broadcast` is a pure in-memory check — any per-subscriber
  scope must be resolved into a plain value/set **once at Subscribe time**, not looked up per event.
  Phase R's `?category=` stream follows this: the handler resolves the category's project-id set
  (`ProjectIDsInCategory`) into the `subFilter` before subscribing, so `Broadcast` only does a map
  membership test (accepting a small post-open staleness window over re-querying per event).
- Timestamps via SQL `strftime('%Y-%m-%dT%H:%M:%fZ','now')` (UTC ISO-8601 TEXT); set `updated_at`
  explicitly in every `UPDATE`.
- Represent SQL NULLs with the `nullStr`/`nullable`/`nullableID` helpers.

## Error Handling

- Store returns sentinel errors; handlers translate via `writeErr`; the CLI translates HTTP status →
  **exit code** via `client.go exitCodeFor` (single source, used by `doOrFail` and the bulk
  `status`/`assign` loop): `0` ok · `1` generic · `3` not found · `4` conflict · `5` validation ·
  `6` server down · `8` out of scope (any 403) · `9` bad token (401, invalid/revoked bearer); plus
  `7` = `am wait` timeout (CLI-side, no HTTP status). Exit 9 is **distinct from 8 on purpose** — a bad
  credential must hard-fail, not be swallowed as a per-id scope-skip in a bulk loop (ADR-029). Full
  catalog: `0/3/4/5/6/7/8/9`.
- CLI: errors go to **stderr** via `fail(code, fmt, …)`; **stdout stays clean** (only ids on
  create/claim) so command substitution works.

## Validation

- Status: `validStatus` map + SQL `CHECK`. Reject empty title/slug/body with `ErrValidation`.
- `PatchTask` applies only known keys (`status/assignee/title/body/priority/meta`) and ignores
  the rest.
- Meta keys are normalized at the boundary (`normalizeMetaKey` — label rules: trim + lowercase,
  1–50 chars of `a-z 0-9 . _ -`); values are opaque, 1–500 bytes (`maxTitleLen`; empty = remove,
  PATCH only). Two raw keys normalizing to the same key in one request → `ErrValidation`
  (deterministic all-or-nothing requests).

## Logging

- `log.Printf`/`log.Fatalf` to stderr, sparingly (startup line, shutdown, update banner, and
  internal server errors). No structured logging, metrics, or tracing. If you add logging, don't
  log task/comment contents or tokens.
- **Secret hygiene (Phase S).** A bearer-token plaintext is shown **once** at mint (stdout line 1 of
  `am token new`, with the hint on stderr) and never again: the DB stores only its sha256 hash
  (`hashToken`), `am token ls` and `am whoami` never print the value (`token: set`), and it is never
  logged. The plaintext at rest lives only in the per-directory identity file (or `AGENTMAN_TOKEN`).
  Any new credential-bearing surface must follow the same rule — hash at rest, plaintext once, never
  logged/listed/whoami'd.
- **Opt-in request logging** — the `requestLogger` middleware (Phase D2) logs
  `METHOD PATH STATUS LATENCY ACTOR` per request when `am serve --log` is passed or
  `AGENTMAN_LOG` is set (any non-empty value). Off by default.

## Configuration

- Flags on `am serve`: `--port`, `--db`, `--log`, `--proposals CAT/PROJ` (the scope carve-out
  project, default `meta/proposals`; flag beats `AGENTMAN_PROPOSALS`; both segments required or
  startup `fail(1)`). Everything else is env (`AGENTMAN_*`) with sensible defaults (`defaultDBPath`
  → `~/.agentman/agentman.db`, port `8787`). Flags override env where both exist. Add new config as
  an `AGENTMAN_*` env var and/or a flag, default-off / backward-compatible.
- Env vars: `AGENTMAN_URL`, `AGENTMAN_PROJECT`, `AGENTMAN_CATEGORY` (default category scope for
  `ls`/`next`/`wait --ready`/`project new`), `AGENTMAN_SCOPE` (overrides the identity file's scope —
  the `X-Agent-Scope` value; non-empty only, composes per-field with `AGENTMAN_AGENT`),
  `AGENTMAN_TOKEN` (overrides the identity file's bearer token; sent as `Authorization: Bearer`, its
  scope wins over `X-Agent-Scope`; Phase S), `AGENTMAN_AGENT`, `AGENTMAN_AGENT_FILE`, `AGENTMAN_DB`,
  `AGENTMAN_PORT`, `AGENTMAN_PROPOSALS` (serve: the carve-out project), `AGENTMAN_NO_UPDATE_CHECK`,
  `AGENTMAN_LOG`.

## Testing

- `go test -race ./cmd/am/` (or `go test ./...`); table-driven tests (see `cmd/am/update_test.go`).
  Coverage spans pure logic, the store, HTTP, migrations, offline DB tooling, CLI verbs + exit codes,
  scope enforcement, scope tokens (Phase S — store hash-not-plaintext/resolve/revoke, the unscoped
  mint guard, token-scope-wins, 401→exit-9, export/import round-trip), SSE streaming/reconnect (incl.
  category-scoped) + direct hub fan-out unit tests, `am wait`, identity, and the dashboard source
  guards (XSS-sink + dark/light theme assets + CLI↔GUI parity affordances) — 11 test files, 258 tests.
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
