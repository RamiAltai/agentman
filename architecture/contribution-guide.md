# Contribution Guide

Practical onboarding. Pair this with `engineering-conventions.md` (how to write code) and
`planning-guide.md` (how to plan).

## Setup

- Install **Go 1.25+** (`go.mod` requires `go 1.25.0`; modernc's floor). No other toolchain — no
  npm/node, no C compiler (pure-Go SQLite).
- Clone and build:

```sh
git clone https://github.com/RamiAltai/agentman
cd agentman
go build -o am ./cmd/am
./am version
```

There is no Makefile, Dockerfile, or CI config — `go` is the whole toolchain.

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

## Running Tests

```sh
go test ./...            # or: go test ./cmd/am/
go test -run TestUpdateAvailable -v ./cmd/am/
```

Today the only tests are in `cmd/am/update_test.go` (version-comparison logic). Most of the system
(HTTP, store, claim, SSE, CLI, UI) is **untested** — see `known-risks-and-gaps.md`. New behavioral
tests are very welcome; start with the claim/validation paths.

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
- Preserve keyboard/focus behavior (modal focus trap, `a`/`n`/`[`/`]`/`Esc`).
- Rebuild the binary and hard-refresh.

## Updating Data Models

- Add the column in `schema.sql` and the field in the relevant `store.go` struct; thread it through
  create/patch/get and the API and UI.
- ⚠️ **No migration runner exists** — `CREATE TABLE IF NOT EXISTS` won't alter existing DBs. For any
  change to an existing table, design a migration first (and add the code that reads
  `meta.schema_version`). Coordinate with `decision-records.md` IADR-003.

## Adding Tests

- Put tests next to code as `*_test.go` in `cmd/am/`. Use table-driven style (`update_test.go`).
- For HTTP, use `net/http/httptest` against `Server.Handler()` with a temp `--db`. For the store,
  open an `OpenStore(t.TempDir()+"/x.db")` and assert behavior (great place to lock down the atomic
  claim).

## Common Mistakes

- Forgetting to rebuild after a `web/` edit (stale embedded UI).
- Printing to stdout in a CLI command (breaks `id=$(am new …)` and wastes agent tokens).
- Broadcasting before commit, or doing SQL in a handler instead of the store.
- Assuming a schema change reaches existing DBs (it won't).
- Running `am serve` against your real `~/.agentman/agentman.db` while testing — use `--db /tmp/...`.

## Unknowns

- No documented PR/branch/review process (single-maintainer repo, no CI). Confirm with the maintainer
  before assuming one.
