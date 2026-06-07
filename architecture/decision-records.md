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

### ADR-010: Minimal in-code schema-migration runner
- Status: Active (foundation landed in Phase 0; first real migration pending Phase 2)
- Context: `CREATE TABLE IF NOT EXISTS` cannot add columns to existing DBs (IADR-003); upcoming
  archive (a new column) and DB import (a version-compatibility check) both need a version story.
- Decision: A forward-only, idempotent runner in `store.go` — `readSchemaVersion` +
  `runMigrations(db, currentSchemaVersion, schemaMigrations)` wired into `OpenStore` after
  `schema.sql`. Each step applies its change **and** bumps `meta.schema_version` in one tx;
  integer-ordered; no new dependency. `schemaMigrations` is **empty at v1** (foundation only).
- Rationale: enables additive schema evolution + import version checks without a migration library.
- Consequences: forward-only (no down-migrations); a DB at a **newer** version than the binary is
  currently accepted silently (to be gated by the Phase 1 import check / a future `cur>target`
  guard); an unparseable `schema_version` defaults to 1, so migration steps must stay idempotent.
- Evidence: `cmd/am/store.go` (`runMigrations`/`readSchemaVersion`/`schemaMigrations`, `OpenStore`);
  `cmd/am/migrate_test.go`.

### ADR-011: Localhost HTTP guardrails (Host allowlist + write-CSRF + CSP), no auth
- Status: Active
- Context: localhost-no-auth remains the posture (ADR-002), but a malicious website can drive the
  loopback API (CSRF) and DNS rebinding bypasses the same-origin assumption; upcoming
  archive/import make those gaps destructive.
- Decision: Middleware around `Handler()` — a **Host-header allowlist** (`127.0.0.1`/`localhost`/
  `::1`, else 403; DNS-rebinding guard), a **write-CSRF guard** (block cross-origin browser writes
  via `Sec-Fetch-Site`/`Origin`, exempting header-less non-browser clients so the CLI works), and
  `X-Content-Type-Options: nosniff` + a dashboard-safe CSP (`style-src 'self' 'unsafe-inline'` for
  the app's inline style attributes). Auth/TLS stay deferred.
- Rationale: closes the realistic browser-driven attack surface without adding auth, preserving the
  CLI, the same-origin dashboard, and SSE.
- Consequences: cross-origin browser writes are blocked; CLI + dashboard unaffected. This is **not
  authentication** — any local process can still call the API.
- Evidence: `cmd/am/server.go` (`hostGuard`/`csrfGuard`/`securityHeaders`, `Handler`);
  `cmd/am/server_test.go`.

### ADR-012: DB export/import as CLI-only file operations, no HTTP route
- Status: Active
- Context: "Back up = copy one file" (ADR-001) needs a safe, consistent snapshot while the server may
  hold the single WAL connection (ADR-003); restore must not corrupt a live DB.
- Decision: `am db export [path]` / `am db import <path>` operate **directly on the SQLite file**,
  dispatched in `main.go` before the HTTP client is built — **no server endpoint**. Export uses
  `VACUUM INTO` for an online, consistent snapshot, then `chmod 0o600`. Import validates the
  candidate (`PRAGMA integrity_check`, `foreign_key_check`, 5 required tables, `schema_version <=
  currentSchemaVersion`), **refuses while a server is running** (probes `AGENTMAN_URL /api/projects`),
  prompts unless `--yes`, backs up the existing DB (`0o600`) into the DB's dir, removes stale
  `-wal`/`-shm`, then does an atomic copy. The `VACUUM INTO` path is an **escaped string literal**
  (single-quotes doubled), **not** a bound `?` param — a deliberate, reviewed exception to "all SQL
  parameterized", because SQLite forbids a bind param there; the destination is always the configured
  DB path, never caller-supplied.
- Rationale: keeps backup/restore out of the loopback attack surface (ADR-011); avoids fighting the
  single-writer connection; refuse-while-serving prevents clobbering a live DB.
- Consequences: backup/restore is local-only (no remote admin); a malformed snapshot path string is
  the only place untrusted text reaches SQL, and it is escaped + server-controlled.
- Evidence: `cmd/am/db.go` (`exportDB` VACUUM INTO + `0o600`, `validateImportCandidate`,
  `isServerRunning`, `importDB` backup/atomic copy); `cmd/am/main.go` (`db` dispatch before client);
  `cmd/am/cli.go` (`yes` in boolFlags); `cmd/am/db_test.go`.

### ADR-013: Project archive as a reversible soft-delete (first real migration, v2)
- Status: Active
- Context: "Delete / archival semantics" was an open Missing Decision; agents accumulate stale
  projects but their tasks/comments/events must survive (events are the append-only backbone, ADR-004).
- Decision: Archive is a **reversible soft-delete** via a nullable `projects.archived_at` TEXT column
  (NULL = active, ISO-8601 UTC timestamp when archived) — **not** a hard delete or cascade.
  `ArchiveProject`/`UnarchiveProject` are transactional and idempotent (no event when already in the
  target state), emitting `project.archived` / `project.unarchived`. `ListProjects(includeArchived)`
  adds `WHERE p.archived_at IS NULL` when false. Adding this column is the **first real schema
  migration** (`currentSchemaVersion = 2`): `schemaMigrations` now carries
  `{version: 2}` running `ALTER TABLE projects ADD COLUMN archived_at TEXT`, which **exercises the
  Phase-0 forward-only runner end-to-end** (ADR-010 / IADR-003) — each step plus the
  `meta.schema_version` bump commit in one tx.
- Rationale: preserves history and is fully reversible; proves the migration runner works on a real
  additive change rather than leaving it untested.
- Consequences: archived projects are hidden by default but never garbage-collected; reaching v2
  requires the runner (a v1 DB is migrated forward on open).
- Evidence: `cmd/am/store.go` (`currentSchemaVersion = 2`, `schemaMigrations` v2 ALTER,
  `ArchiveProject`/`UnarchiveProject`/`ListProjects`); `cmd/am/server.go` (`/archive`/`/unarchive`,
  `?archived=true`); `cmd/am/cli.go` (`am project archive/unarchive`, `am projects --all`);
  `cmd/am/web/app.js` (`project.archived`/`project.unarchived`); `cmd/am/migrate_test.go`,
  `cmd/am/store_test.go`, `cmd/am/server_test.go`.

### ADR-014: Multi-select project filter resolved client-side
- Status: Active
- Context: The dashboard moved from a single active project to selecting several at once; the
  `/api/tasks` query filter (`?project`) only accepts one slug.
- Decision: Frontend tracks `let selected = new Set()` (empty = "All"); `toggleProject(slug)` adds/
  removes a slug or clears all. Filtering is **client-side except for the single-project case**:
  `qstr` sets `project=` **only when `selected.size === 1`** (server-side filter); for 0 or 2+
  selected it loads all tasks and `renderBoard` filters via `selected.has(t.project)`. `card()` shows
  the project tag whenever `selected.size !== 1`. No multi-project server query was added.
- Rationale: avoids extending the server query API for a UI concern; the single-project fast path
  still narrows the payload, and the board is already a debounced full re-render (IADR-002), so
  client-side filtering is essentially free.
- Consequences: with 2+ projects selected the client fetches all tasks (capped at `limit: 500`) and
  filters in memory; large boards inherit the IADR-002 re-render limit.
- Evidence: `cmd/am/web/app.js` (`selected` Set, `toggleProject`, `qstr` `selected.size === 1`,
  `renderBoard` `selected.has(t.project)`, `tab()`/`card()`).

### ADR-015: Hard-delete endpoints with FK cascade and retained audit log (Phase C1)
- Status: Active
- Context: No API existed to delete tasks, comments, or projects — removal was only possible by
  editing the SQLite file directly. Archive (ADR-013) is reversible and projects-only; a distinct
  "permanently remove" path was needed. The existing `ON DELETE CASCADE` FKs
  (`projects → tasks → comments`, `tasks → comments`) and the `foreign_keys(1)` DSN pragma meant
  cascade was already wired but unused via the API.
- Decision:
  1. **Hard delete (irreversible)** — not a second soft-delete. Archive already covers "hide";
     the new surface is permanent removal.
  2. **Cascade via existing FKs** — no new SQL; deleting a project removes all its tasks and
     comments; deleting a task removes all its comments.
  3. **Events retained as audit log** — `events.project_id` / `events.task_id` are denormalized
     nullable non-FKs (`schema.sql` defines no FK on `events`), so event rows survive hard deletes.
     Each delete method inserts a `*.deleted` event in the same transaction before the `DELETE`, then
     commits. The handler broadcasts after commit (consistent with all other mutations).
  4. **`ref` reuse accepted** — the global `tasks.id` autoincrement never reuses (wire refs are
     stable), but a per-project human `ref` (e.g. `web-3`) can be reused if the highest-numbered
     task is deleted and a new task is created. No counter/migration was added — acceptable for a
     personal board.
  5. **`am project rm <slug> --yes` requires `--yes`** — without the flag the CLI errors with a
     hint and a non-zero exit (destructive cascade; guard against accidents). `am rm <id>` is silent
     on success (agent-friendly), exits 3 if not found.
- New event kinds: `task.deleted`, `comment.deleted`, `project.deleted` (total now 12).
- Routes: `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`,
  `DELETE /api/projects/{slug}` — all return `200 {"status":"deleted"}`; `ErrNotFound` → 404.
  The existing `csrfGuard` already gates DELETE methods.
- Dashboard: inline two-step delete confirms for tasks (in modal), comments (per-row ×), and
  projects (in Manage-projects modal). No native `confirm()`/`prompt()` — blocked in webviews.
  All DOM via `el()`.
- Known behavior: a deleted project's historical events reappear in the unfiltered activity feed
  because the archived-event filter (`p.archived_at IS NULL`) passes a NULL (no row) as "not
  archived". Acceptable as an audit trail; documented in `data-model.md`.
- Evidence: `cmd/am/store.go` (`DeleteTask`/`DeleteComment`/`DeleteProject`); `cmd/am/server.go`
  (`handleDeleteTask`/`handleDeleteComment`/`handleDeleteProject`, route table); `cmd/am/cli.go`
  (`cmdRm`, `project rm`); `cmd/am/main.go` (`rm` dispatch); `cmd/am/web/app.js` (delete buttons,
  `onEvent` for `*.deleted`); `cmd/am/store_test.go` + `cmd/am/server_test.go` (7 new tests).

### ADR-016: Events retention via offline prune + backward cursor pagination (Phase C2)
- Status: Active
- Context: The `events` table is append-only (ADR-004); long-running instances grow without bound.
  Phase C1 added hard deletes for tasks/comments/projects but explicitly left event retention for C2.
  The dashboard "Load older activity" need drove a `?before=` cursor alongside the existing `?since=`
  and `?tail=` query modes.
- Decision:
  1. **`?before=` backward cursor** on `GET /api/events` — `ListEventsBefore(before, project, limit)`
     returns events with `id < before`, newest-first (default limit 40, cap 200), applying the same
     archived-project filter as `ListEvents`/`RecentEvents`. The `handleEvents` handler dispatches to
     it when a `before=` param is present; returns `{"events":[...]}`.
  2. **`am db prune (--before <YYYY-MM-DD> | --keep <N>) [--yes]`** — CLI-only offline maintenance,
     dispatched in `main.go` before the HTTP client is built (same as `am db import`). Refuses while
     a server is running (probes `AGENTMAN_URL`). Deletes rows from the **`events` table only** (not
     comments/tasks/projects), then runs `VACUUM` best-effort. Prints `pruned N events` to stderr;
     stdout stays clean. `--before`: date-only string sorts before same-day ISO timestamps, so same-day
     events are kept. `--keep N`: keeps the newest N by id. Confirms unless `--yes`.
  3. **Dashboard "Load older activity"** button placed **outside** `#feedList` (so `trimFeed` can't
     remove it); `feedPaginated` disables `trimFeed` once the user has paged. End-marker replaces the
     button when exhausted. All DOM via `el()`.
- Rationale: cursor pagination avoids a server-side scan on every feed load and lets the dashboard
  lazily extend history on demand. Offline-only prune keeps retention out of the loopback attack
  surface (ADR-011) and avoids fighting the single-writer connection (ADR-003). Events-only scope
  is intentional — tasks/comments already have hard-delete; event rows are denormalized (ADR-004),
  so pruning them doesn't break referential integrity.
- Residuals: prune is manual (no scheduled compaction); the `isServerRunning` guard checks
  `AGENTMAN_URL` and is bypassable on non-default ports (same residual as `am db import`);
  `feedPaginated` disabling `trimFeed` can grow the in-browser feed unbounded until reload.
- Evidence: `cmd/am/store.go` (`ListEventsBefore`); `cmd/am/server.go` (`handleEvents` `?before=`
  branch); `cmd/am/db.go` (`pruneEvents`, `cmdDB` prune case); `cmd/am/web/app.js`
  (`feedOldest`, `feedPaginated`, `loadOlderActivity`, `loadOlderBtn`);
  `cmd/am/store_test.go` (`TestListEventsBefore`); `cmd/am/server_test.go` (`TestEventsBeforeEndpoint`);
  `cmd/am/db_test.go` (`TestPruneEventsKeep`, `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary`).

### ADR-017: Generic 500 responses + opt-in request logging (Phase D)
- Status: Active
- Context: `writeErr`'s default branch returned raw Go error text (SQL messages, file paths) to
  clients — minor info exposure. There was also no visibility into request traffic without adding
  a full logging framework.
- Decision:
  1. **D1 — opaque 500s.** The `writeErr` default branch now logs the real error server-side
     (`log.Printf("agentman: internal error: %v", err)` to stderr) and returns a generic
     `{"error":"internal"}` body. All sentinel mappings are unchanged.
  2. **D2 — opt-in request logging.** A `requestLogger` middleware + `statusRecorder` wrapper
     (captures status, defaults 200, proxies `http.Flusher` for SSE). Enabled by `am serve --log`
     or `AGENTMAN_LOG=1` (any non-empty value enables it). Off by default. Installed outermost so
     security-guard 403s are also logged. Logs `METHOD PATH STATUS LATENCY ACTOR` via the
     standard `log` package to stderr (plain lines, not structured).
- Rationale: keep internal detail out of HTTP responses (good practice even on loopback); a
  lightweight opt-in log line is useful for debugging without imposing structured logging on a
  personal board. Plain `log.Printf` stays consistent with the existing logging convention.
- Consequences: clients receive `{"error":"internal"}` on unexpected errors; detail is in the
  server's stderr. `AGENTMAN_LOG` treats any non-empty value as on — document `=1` as the
  canonical form (`=0`/`=false` also enable it). SSE connections log once on disconnect with a
  large latency (inherent to long-lived connections). Still no metrics, tracing, or structured
  logging.
- Evidence: `cmd/am/server.go` (`writeErr` default, `requestLogger`, `statusRecorder`,
  `Server.logRequests`, `Handler()` wrapping); `cmd/am/main.go` (`runServe` log toggle,
  `usage()` `[--log]`); `cmd/am/cli.go` (`boolFlags["log"]`); `cmd/am/server_test.go`
  (`TestWriteErrHidesInternalDetail`, `TestRequestLoggerPassesThrough`,
  `TestRequestLoggerPreservesFlusher`).

### ADR-018: No JS test runner — XSS safety enforced by convention + Go source guard (Phase E4)
- Status: Active
- Context: The dashboard has no automated tests. Options considered: (a) adopt a JS runner such as
  node + jsdom or Playwright; (b) document the deliberate choice not to and enforce what matters
  most via existing tooling.
- Decision: **No JS test runner.** Adding npm/jsdom/Playwright would introduce a build step and an
  npm dependency, violating the single-binary / no-build-step / no-npm project invariant (ADR-001,
  ADR-007). Instead, dashboard XSS safety is enforced by two layers:
  1. **Convention:** all DOM construction uses `el()` / `textContent` (never `innerHTML` or related
     sinks), as codified in the Anti-Patterns section of `engineering-conventions.md` and in the
     comment block at the top of `web_test.go`.
  2. **Go source-level sink guard:** `TestDashboardNoXSSSinks` in `cmd/am/web_test.go` reads the
     embedded `web/app.js` + `web/index.html` via the `webFS` embed.FS at `go test` time and asserts
     that `.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` do not appear.
     A future accidental sink assignment fails `go test` before it ships.
- Rationale: the XSS-safe DOM convention is the highest-value thing to enforce automatically;
  doing so at the Go level costs nothing extra (same `go test` run, no new dependencies). Behavioral
  JS logic (modal flows, delete confirms, feed pagination) remains manually verified — acceptable for
  a personal board where the risk surface is localhost-only.
- Consequences: behavioral dashboard JS is not automatically tested (documented gap in
  `known-risks-and-gaps.md` and `frontend.md`). The sink guard is the chosen mitigation for the
  most dangerous class of dashboard regression (XSS). Contributors must not add a JS runner.
- Evidence: `cmd/am/web_test.go` (`TestDashboardNoXSSSinks`); `cmd/am/server.go`
  (`//go:embed web`, `webFS`); `cmd/am/web/app.js` (`el()` helper, no `.innerHTML` usage).

### ADR-019: GitHub Actions CI (Phase F)
- Status: Active
- Context: No CI existed (no `.github/`), so format drift, vet failures, test regressions, and
  dependency vulnerabilities went undetected between manual runs. The project had a working
  `govulncheck`-clean codebase but no automated enforcement.
- Decision: Add `.github/workflows/ci.yml` — a single `ubuntu-latest` job triggered on **push to
  `main`** and on **pull_request**. Steps in order:
  1. `actions/checkout@v4`
  2. `actions/setup-go@v5` with `go-version-file: go.mod` and `cache: true` (pins Go to the
     `go 1.25.0` directive in `go.mod`; no separate version pin to drift)
  3. **Build** — `go build ./...`
  4. **Vet** — `go vet ./...`
  5. **gofmt** — `gofmt -l .` fails if any file is unformatted (enforces the zero-drift state)
  6. **Test (race)** — `go test -race -count=1 ./...` (matches the local command in contribution-guide.md)
  7. **JS syntax check** — `node --check cmd/am/web/app.js` (Node is preinstalled on `ubuntu-latest`)
  8. **govulncheck** — `go install golang.org/x/vuln/cmd/govulncheck@latest` then
     `govulncheck ./...`; **blocks on reachable vulnerabilities only**. `@latest` ensures the
     advisory DB is always current without pinning a version that would need manual bumps.
- Rationale: closes the long-standing "no CI" and "no dependency vulnerability scanning" gaps
  (see `known-risks-and-gaps.md`). Single job keeps setup simple; `go-version-file` avoids a
  second place to update when Go is bumped. `govulncheck`'s reachability analysis means
  transitive-but-unused advisories do not break CI; only exploitable paths block.
- Known advisory (non-blocking): **`GO-2026-5024`** in `golang.org/x/sys@v0.42.0` (integer
  overflow in `windows.NewNTUnicodeString`). Windows-only; **not reachable** from agentman
  (govulncheck's symbol/package scan finds nothing). Transitive dep via `modernc.org/libc`.
  Clears by upgrading `golang.org/x/sys` to ≥ v0.44.0 if ever desired. CI is green.
- Consequences: every push to `main` and every PR is gated on build/vet/format/test/vuln.
  Pre-commit hooks are still absent (local runs are manual). No CD/release automation added.
- Evidence: `.github/workflows/ci.yml`; `known-risks-and-gaps.md` (Phase F notes).

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

### IADR-003: No schema-migration framework — RESOLVED (Phase 0)
- Confidence: High (now confirmed/resolved by ADR-010)
- Original inference: relied on `CREATE TABLE IF NOT EXISTS` only; `meta.schema_version` written but
  never read; no `ALTER`/migration runner.
- Status: **Resolved + exercised.** Phase 0 added a forward-only runner (ADR-010) that reads/bumps
  `meta.schema_version`; Phase 2 supplied its **first real step** — `schemaMigrations` now holds the
  v2 `ALTER TABLE projects ADD COLUMN archived_at TEXT` (ADR-013), so `currentSchemaVersion = 2` and
  the runner is exercised end-to-end (no remaining residual risk).
- Evidence: `cmd/am/store.go` (`runMigrations`, `schemaMigrations` v2), `cmd/am/migrate_test.go`.

### IADR-004: Native HTML5 drag-and-drop (no library, no touch)
- Confidence: High
- Inferred Decision: Use native `dragstart`/`drop` for status changes; provide keyboard (`[ ]`) and
  the status dropdown as the accessible/touch fallback.
- Evidence: `cmd/am/web/app.js card()/moveTask()`.
- Risk if Wrong: Low — fallback paths exist.

## Missing Decisions

These are **undecided/undocumented** in the repo (decide + record before building):
- **Authentication / remote-access model** — discussed but not chosen or written down.
- **Testing strategy & coverage targets** — Phase E closed the major gaps (CLI, SSE, identity,
  dashboard XSS guard; ADR-018); behavioral dashboard JS is a documented deliberate gap. No
  formal coverage target policy exists.
- **Schema migration approach** — resolved + exercised; see IADR-003 / ADR-010 / ADR-013.
- **Delete / archival semantics** — archive resolved as a reversible soft-delete (ADR-013); hard
  delete resolved (ADR-015, Phase C1); `events` retention resolved (ADR-016, Phase C2: offline prune
  + `?before=` cursor pagination); `comments` retention remains undecided (no bulk prune).
- ~~**CI/CD & release automation**~~ — **CI resolved (Phase F / ADR-019)**. `.github/workflows/ci.yml`
  gates push/PR with build/vet/gofmt/test(-race)/JS-syntax/govulncheck. Release automation (CD)
  and a stated versioning policy remain undecided — releases are still manual `git tag` + push.
- **Versioning / CHANGELOG policy** — tags exist (`v0.1.0`–`v0.3.0`); `CHANGELOG.md` (Keep a
  Changelog format) and `ROADMAP.md` now exist in the repo root. Release automation and a stated
  versioning policy remain undocumented.
