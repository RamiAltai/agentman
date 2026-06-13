# Contribution Guide

Practical onboarding. Pair this with `engineering-conventions.md` (how to write code) and
`planning-guide.md` (how to plan).

## Setup

- Install **Go 1.25.11+** (`go.mod` requires `go 1.25.11` for a security-patched stdlib; older
  1.25.x auto-upgrades via the Go toolchain). No other toolchain — no
  npm/node, no C compiler (pure-Go SQLite).
- Clone and build:

```sh
git clone https://github.com/RamiAltai/agentman
cd agentman
go build -o am ./cmd/am
./am version
```

There is no Makefile or Dockerfile — `go` is the whole toolchain. CI is configured in
`.github/workflows/ci.yml` (GitHub Actions): it runs `go build`, `go vet`, `gofmt -l`,
`go test -race -count=1 ./...`, `node --check cmd/am/web/app.js`, and `govulncheck` on
every push to `main` and on every pull request. Contributors should expect CI to gate PRs.

## Commands

```sh
go build -o am ./cmd/am                 # build the single binary (CLI + server)
go vet ./...                            # static analysis
go test ./...                           # run tests
gofmt -l cmd/am                         # list unformatted files (should be empty)
gofmt -w cmd/am                         # format
./am serve --db /tmp/dev.db --port 8788 # run a throwaway server (never touch your real DB)
./am help                               # full CLI surface
```

⚠️ After editing anything in `cmd/am/web/`, **rebuild** (`go build`) — the dashboard is embedded
via `//go:embed`, so a running/old binary serves stale assets. Hard-refresh the browser too.

## Tests

This is the authoritative testing reference for the project; other docs link here.

```sh
go test ./cmd/am/...                         # run the suite
go test -race ./cmd/am/...                   # with the race detector (what CI runs)
go test -run TestUpdateAvailable -v ./cmd/am/...
```

The suite is **258 tests across 11 `*_test.go` files**, living next to the code in `cmd/am/`.
Rough breakdown (run `go test -v` for the live list):

| File | ~Tests | Covers |
|---|---:|---|
| `store_test.go` | 78 | CRUD + validation, the atomic-claim race (exactly one winner), the strictly-increasing events cursor, archive/unarchive, stale-claim steal, search + labels, categories, task metadata, scope, category stats, and scope tokens. |
| `server_test.go` | 58 | HTTP status mapping (404 / 400 / lost-claim 409), Host/CSRF guards + security headers, `writeErr` hiding internal detail, the request logger, and the endpoint surface for filters, labels, categories, meta, scope enforcement, the `?category=` feed filter, and the token admin API. |
| `cli_test.go` | 53 | CLI command paths + exit-code mapping (3/4/5/6/8/9). Builds a `Client` against an `httptest` server; uses the `captureStdout`/`captureExit` seams (below). Covers terse output, silent mutations, bulk ops, wire encoding of flags, and pure formatter/parse table tests. |
| `wait_test.go` | 20 | `am wait` — already-satisfied, event-driven, and cross-project `--done` waits; category/meta/scope-filtered `--ready` waits; exit 7 timeout / 3 not-found / 6 server-down; `parseWaitTimeout` table. |
| `migrate_test.go` | 12 | The forward-only migration runner (apply + version bump, skip ≤ current, idempotency), each schema version's columns, and the open-time version ceiling. |
| `db_test.go` | 12 | `am db` export/import roundtrip (+ perms, backup, garbage rejection, liveness probe), events prune, and token round-trip. |
| `identity_test.go` | 11 | `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` override, `sanitizeType`/`parseScope`/`newIdentity`, scoped identity. Uses the `AGENTMAN_AGENT_FILE` seam. |
| `sse_test.go` | 4 | SSE live delivery, `Last-Event-ID` gap-replay with dedupe, and category-scoped streaming + reconnect replay. |
| `hub_test.go` | 4 | Direct hub fan-out: category/project/unscoped broadcast targeting and a nil-event no-panic guard. |
| `update_test.go` | 3 | Version-comparison logic. |
| `web_test.go` | 3 | Dashboard source-level asset guards (see below). |

### Dashboard guards (`web_test.go`)

These three source-level tests read the embedded `web/` assets via the `webFS` `embed.FS` and fail
the build if a UI invariant regresses — there is no JS runtime in the suite (ADR-018):

- **`TestDashboardNoXSSSinks`** — asserts none of `.innerHTML` / `.outerHTML` /
  `.insertAdjacentHTML` / `document.write` / `eval(` appear in `app.js` or `index.html` (XSS-sink
  ban; DOM is built with `el()`/`textContent`).
- **`TestDashboardThemeAssets`** (ADR-030) — asserts the dark/light theme wiring stays present: the
  `:root[data-theme="light"]` CSS override block, the inline `am.theme` FOUC-guard script, and the
  `#themeToggle` button.
- **`TestDashboardParityAffordances`** (ADR-031) — asserts the CLI↔GUI parity affordances stay
  wired: the create/archive-category, project-category-picker, project-edit, board-filter,
  editable-meta, and release markers across `app.js`/`index.html`/`app.css`.

Behavioral dashboard JS (modal flows, delete confirms, feed pagination, the left rail) is **not**
exercised by tests — see `known-risks-and-gaps.md`. New behavioral tests are welcome.

### Test Seams

Three seams exist specifically to make the CLI and identity layers testable without process-level
side effects. Use them when adding tests for CLI commands or identity:

| Seam | File | How to use in tests |
|---|---|---|
| `var osExit = os.Exit` | `cli.go` | Replace with a panic via `captureExit(t, fn)` in `cli_test.go` to intercept exit codes without killing the process. |
| `captureStdout(t, fn)` | `cli_test.go` | Redirects `os.Stdout` to a pipe for the duration of `fn`; returns captured output. Safe with `t.Setenv`. |
| `AGENTMAN_AGENT_FILE` env | `identity.go` | Set in tests (`t.Setenv`) to redirect the identity file to `t.TempDir()`, so the real `~/.agentman` is never written. |

To build a `Client` against an `httptest` server without needing a real network:

```go
ts := newTestServer(t)                            // spins up a temp server + temp DB
c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
cmdNew(c, parse([]string{"title", "-p", "proj"}))
```

### JS test runner

The project deliberately adopts **no JS test runner** (no npm, no jsdom — preserves the
single-binary/no-build-step ethos; see `decision-records.md` ADR-018). Dashboard XSS safety is
enforced by the `el()`/`textContent` convention plus the `TestDashboardNoXSSSinks` Go source-level
sink guard in `web_test.go`. Behavioral dashboard JS logic is not automatically tested.

## Inspecting Logs / Behavior

- The server logs to **stderr** (startup line, shutdown, the "update available" banner). Run it in
  the foreground or redirect: `./am serve --db /tmp/dev.db > /tmp/am.log 2>&1 &`.
- Inspect data directly: `sqlite3 /tmp/dev.db 'SELECT id,status,assignee,title FROM tasks;'`.
- Watch the live stream: `curl -N http://127.0.0.1:8788/api/stream`.
- Drive the API with the CLI: `AGENTMAN_URL=http://127.0.0.1:8788 ./am ls`.

## Common Change Locations

| Want to change… | Edit… |
|---|---|
| An HTTP endpoint / request handling | `cmd/am/server.go` |
| SQL / a query / the claim logic | `cmd/am/store.go` |
| The DB schema | `cmd/am/schema.sql` (+ structs in `store.go`) |
| A CLI command or its output | `cmd/am/cli.go` (+ dispatch in `cmd/am/main.go`) |
| CLI ↔ server HTTP / exit codes | `cmd/am/client.go` |
| DB export/import (`am db`) | `cmd/am/db.go` |
| The dashboard | `cmd/am/web/{index.html,app.css,app.js}` |
| Agent identity | `cmd/am/identity.go` |
| `am update` / version check | `cmd/am/update.go`, `cmd/am/version.go` |

## Adding a Feature

1. Plan with `planning-guide.md`. 2. Make the smallest change that fits the conventions.
3. Build + vet + test + gofmt. 4. Verify by running `am serve` against a throwaway DB.
5. Update `README.md`/`docs/` (if user-facing) and the matching `architecture/` doc.

## Adding Backend Functionality (e.g. a new endpoint)

1. Register the route in `Server.Handler()` (`server.go`), e.g. `mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)`.
2. Write `handleDeleteTask` — decode/validate, call a store method, `writeErr` on error.
3. Add the store method in `store.go` — parameterized SQL, mutation + `events` row in one tx,
   return `(result, *Event, error)`.
4. In the handler, `hub.Broadcast(ev)` **after** the store call returns.
5. Add the new event kind to the dashboard (`web/app.js evText`/`describeText`) if it should show.
6. (Recommended) add a test for the new behavior.

## Adding Frontend Functionality

- Extend the imperative renderers in `app.js` (`card`, `renderModal`, `renderBoard`, `feedItem`).
  Build DOM with `el()` (never `innerHTML`). Style with the CSS variables in `app.css`.
- Navigation lives in the left rail: `renderRail()` (built from `renderRail`/`railItem`) draws the
  brand + Overview + All tasks + per-category projects + actions; `goProject()` switches scope. Touch
  these — not the removed `renderTabs()` — to change how scopes are selected.
- Preserve keyboard/focus behavior (modal focus trap, `a`/`n`/`[`/`]`/`Esc`).
- Rebuild the binary and hard-refresh.

## Updating Data Models

- Add the column in `schema.sql` and the field in the relevant `store.go` struct; thread it through
  create/patch/get and the API and UI.
- ✅ A **forward-only migration runner exists** (`runMigrations` in `cmd/am/store.go`, ADR-010). To
  change an existing table, append a `{version, apply}` step to `schemaMigrations` and raise
  `currentSchemaVersion`; add a `migrate_test.go` case. `CREATE TABLE IF NOT EXISTS` in `schema.sql`
  still won't alter existing tables — use the runner for that.

## Adding Tests

- Put tests next to code as `*_test.go` in `cmd/am/`. Use table-driven style (`update_test.go`).
- For the store, open an `OpenStore(t.TempDir()+"/x.db")` and assert behavior — see `store_test.go`
  for the atomic-claim race and events-cursor patterns.
- For HTTP, use `net/http/httptest` against `Server.Handler()` with a temp `--db` — see
  `server_test.go` for status-mapping and guard examples.
- For schema changes, follow `migrate_test.go`: assert the runner applies the step and bumps
  `meta.schema_version`, and that a fresh DB lands on `currentSchemaVersion`.
- For CLI command paths, construct a `Client` directly against a `newTestServer(t)` and use
  `captureStdout`/`captureExit` — see `cli_test.go` for the pattern.
- For identity, set `AGENTMAN_AGENT_FILE` via `t.Setenv` to redirect the file to `t.TempDir()` —
  see `identity_test.go`.
- Do **not** add a JS test runner. Dashboard XSS safety is enforced by `TestDashboardNoXSSSinks` in
  `web_test.go`; extend that test if you add new sink-like patterns.

## Common Mistakes

- Forgetting to rebuild after a `web/` edit (stale embedded UI).
- Printing to stdout in a CLI command (breaks `id=$(am new …)` and wastes agent tokens).
- Broadcasting before commit, or doing SQL in a handler instead of the store.
- Assuming a schema change reaches existing DBs (it won't).
- Running `am serve` against your real `~/.agentman/agentman.db` while testing — use `--db /tmp/...`.

## Unknowns

- No documented PR/branch/review process (single-maintainer repo). CI gates code quality on push/PR,
  but a formal review/branching policy has not been written down. Confirm with the maintainer before assuming one.
