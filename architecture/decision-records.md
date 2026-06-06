# Architecture Decision Records

Records below are split into **Confirmed** (stated in docs/code) and **Inferred** (deduced from
implementation — verify before relying on them). Do not promote an inferred decision to confirmed
without evidence.

## Confirmed Decisions

### ADR-001: Single static Go binary with pure-Go SQLite
- Status: Active
- Context: Needed dead-simple distribution and operation ("zero ops").
- Decision: One Go binary embedding everything; SQLite via `modernc.org/sqlite` (no cgo).
- Rationale: cross-compiles to all platforms, no C toolchain, no DB server, "back up = copy one file".
- Consequences: requires Go **1.25+** (modernc's floor); binary ~15 MB; single artifact for CLI+server.
- Evidence: `go.mod` (`modernc.org/sqlite v1.51.0`, `go 1.25.0`); `README.md` "Why"/"Install".

### ADR-002: Localhost-only, no authentication
- Status: Active
- Context: A personal, single-user board.
- Decision: Bind `127.0.0.1`; no auth/authz; `X-Agent` is an attribution label only.
- Rationale: simplest secure-by-default for local use.
- Consequences: not usable across hosts without adding auth; trust boundary = the loopback bind.
- Evidence: `cmd/am/main.go` (`Addr: "127.0.0.1:"+port`); `README.md` "Security"; `cmd/am/server.go actorOf`.

### ADR-003: The server is the sole DB writer (single connection, WAL)
- Status: Active
- Context: Multiple agents claim tasks concurrently; must avoid double-claim and `SQLITE_BUSY`.
- Decision: `db.SetMaxOpenConns(1)`; WAL + `busy_timeout` via DSN; all writes go through `am serve`.
- Rationale: serializes writes; makes the atomic claim trivially correct.
- Consequences: write throughput capped at one connection; the CLI must use the HTTP API, never the
  DB file directly.
- Evidence: `cmd/am/store.go OpenStore` (`SetMaxOpenConns(1)`, `_pragma=journal_mode(WAL)`).

### ADR-004: Append-only `events` table as the live-update backbone
- Status: Active
- Context: Real-time dashboard + reliable reconnect.
- Decision: Every mutation writes an `events` row in the same tx; `events.id` is the SSE cursor /
  `Last-Event-ID`; broadcast happens only after commit.
- Rationale: one durable source of truth for the feed, SSE replay, and polling fallback.
- Consequences: `events` grows unbounded (no retention policy yet).
- Evidence: `cmd/am/schema.sql` (events comments); `cmd/am/store.go insertEvent`; `cmd/am/hub.go`.

### ADR-005: Token-efficient CLI as the primary agent interface
- Status: Active
- Context: Agents (LLMs) pay per token; the CLI is their main surface.
- Decision: Terse text output, silent success, machine-branchable exit codes
  (`0/1/3/4/5/6`); `--json` only when needed.
- Rationale: a full pick-up→done cycle costs ~65–75 tokens.
- Consequences: stdout must stay clean (only ids on create/claim) so `id=$(am new …)` works.
- Evidence: `cmd/am/cli.go`, `cmd/am/client.go doOrFail`; `README.md` "Why"/"CLI reference".

### ADR-006: `cmd/am/` layout + module path for `go install`
- Status: Active
- Context: Distribute via `go install`; the installed command must be named `am`.
- Decision: Module `github.com/RamiAltai/agentman`; the `main` package lives in `cmd/am/` so
  `go install …/cmd/am@latest` yields an `am` binary.
- Rationale: idiomatic Go; correct binary name.
- Consequences: `go install …@latest` resolves to the highest **git tag** — releases must be tagged
  (`v0.1.0`, `v0.2.0`, `v0.3.0` exist).
- Evidence: `go.mod`; `cmd/am/` path; `README.md` "Install"/"Updating"; `git tag`.

### ADR-007: Embedded vanilla dashboard (no build step), XSS-safe
- Status: Active
- Decision: `cmd/am/web/` (HTML/CSS/JS) embedded via `//go:embed web`; DOM built with `el()` using
  `textContent`, never `innerHTML`.
- Rationale: no npm/build toolchain; safe rendering of untrusted agent text.
- Consequences: editing the UI requires rebuilding the binary; no minification/tree-shaking.
- Evidence: `cmd/am/server.go` (`//go:embed web`); `cmd/am/web/app.js el()`.

### ADR-008: Per-directory agent identity file
- Status: Active
- Context: Agent runtimes spawn a fresh shell per command, so `export AGENTMAN_AGENT=…` doesn't persist.
- Decision: `am init <tasktype>` writes a `{tasktype}_{DDMMYY}_{rand}` id to
  `~/.agentman/agents/<sha1(cwd)>`; the CLI reads it (env `AGENTMAN_AGENT` overrides).
- Rationale: an agent runs `am init` once, then uses `am` normally.
- Consequences: two agents in the **same** directory share an identity unless one sets the env var.
- Evidence: `cmd/am/identity.go` (comments + `identityFile`/`resolveAgent`).

### ADR-009: Distribution via `go install` + self-update
- Status: Active
- Decision: `go install` is the supported install path; `am update` re-runs it; `am serve` checks
  `proxy.golang.org` at startup and logs when a newer tag exists (`AGENTMAN_NO_UPDATE_CHECK=1` opts out).
- Rationale: zero release infrastructure; every target already has Go.
- Consequences: machines without Go can't install yet (prebuilt binaries explicitly deferred).
- Evidence: `cmd/am/update.go`; `README.md` "Updating".

## Inferred Decisions

### IADR-001: SSE chosen over WebSockets
- Confidence: High
- Context: Live updates are one-directional (server → browser).
- Inferred Decision: Use Server-Sent Events, not WebSockets.
- Evidence: `cmd/am/server.go handleStream` (`text/event-stream`); `cmd/am/hub.go`; no WS code.
- Risk if Wrong: Low — would only matter if bidirectional client push were needed.

### IADR-002: Snapshot-reconcile rendering (debounced full reload), not client diffing
- Confidence: High
- Inferred Decision: On each SSE event the dashboard updates the feed immediately and debounces a
  full `loadBoard()` (250 ms) rather than applying granular diffs.
- Evidence: `cmd/am/web/app.js onEvent`/`renderBoard`.
- Risk if Wrong: Medium — O(n) re-render limits very large boards; revisit before scaling.

### IADR-003: No schema-migration framework
- Confidence: High
- Inferred Decision: Rely on `CREATE TABLE IF NOT EXISTS` only; `meta.schema_version` is written but
  never read; no `ALTER`/migration runner.
- Evidence: `cmd/am/store.go OpenStore`; `cmd/am/schema.sql`.
- Risk if Wrong: **High** — altering a column on existing DBs silently won't apply; plan migrations
  before any schema change beyond adding a new table.

### IADR-004: Native HTML5 drag-and-drop (no library, no touch)
- Confidence: High
- Inferred Decision: Use native `dragstart`/`drop` for status changes; provide keyboard (`[ ]`) and
  the status dropdown as the accessible/touch fallback.
- Evidence: `cmd/am/web/app.js card()/moveTask()`.
- Risk if Wrong: Low — fallback paths exist.

## Missing Decisions

These are **undecided/undocumented** in the repo (decide + record before building):
- **Authentication / remote-access model** — discussed but not chosen or written down.
- **Testing strategy & coverage targets** — only `update_test.go` exists; no policy.
- **Schema migration approach** — see IADR-003.
- **Delete / archival semantics** — no delete endpoint; no `events`/`comments` retention.
- **CI/CD & release automation** — no `.github/`; releases are manual `git tag` + push.
- **Versioning / CHANGELOG policy** — tags exist (`v0.1.0`–`v0.3.0`) but no CHANGELOG or stated scheme.
