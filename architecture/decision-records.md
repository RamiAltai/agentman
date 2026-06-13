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
- Evidence: `go.mod` (`modernc.org/sqlite v1.51.0`, `go 1.25.11`); `README.md` "Why"/"Install".

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
- Consequences: forward-only (no down-migrations); a DB at a **newer** version than the binary was
  originally accepted silently — **closed in Phase O** (ADR-025): `OpenStore` now refuses to open a
  DB whose recorded `schema_version` exceeds `currentSchemaVersion`, the same ceiling
  `validateImportCandidate` applies to import snapshots; an unparseable `schema_version` defaults
  to 1, so migration steps must stay idempotent.
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
  2. `actions/setup-go@v5` with `go-version: 'stable'` and `cache: true` — build/test on the
     latest stable Go (always carrying current stdlib security patches), NOT the exact version
     pinned in `go.mod`. (An exact pin makes `govulncheck` red as stdlib CVEs accrue against that
     frozen patch version; `go.mod` still declares the `1.25.11` *minimum* for users.)
  3. **Build** — `go build ./...`
  4. **Vet** — `go vet ./...`
  5. **gofmt** — `gofmt -l .` fails if any file is unformatted (enforces the zero-drift state)
  6. **Test (race)** — `go test -race -count=1 ./...` (matches the local command in contribution-guide.md)
  7. **JS syntax check** — `node --check cmd/am/web/app.js` (Node is preinstalled on `ubuntu-latest`)
  8. **govulncheck** — `go install golang.org/x/vuln/cmd/govulncheck@latest` then
     `govulncheck ./...`; **blocks on reachable vulnerabilities only**. `@latest` ensures the
     advisory DB is always current without pinning a version that would need manual bumps.
- Rationale: closes the long-standing "no CI" and "no dependency vulnerability scanning" gaps
  (see `known-risks-and-gaps.md`). Single job keeps setup simple. `govulncheck`'s reachability
  analysis means transitive-but-unused advisories do not break CI; only exploitable paths block —
  but note that stdlib advisories ARE reachable, so CI must run on a patched toolchain
  (`go-version: 'stable'`), not a frozen patch version (lesson learned: pinning `go 1.25.0` made CI
  red against 21 accrued stdlib CVEs even though the code was unaffected when built with a current Go).
- Known advisory (non-blocking): **`GO-2026-5024`** in `golang.org/x/sys@v0.42.0` (integer
  overflow in `windows.NewNTUnicodeString`). Windows-only; **not reachable** from agentman
  (govulncheck's symbol/package scan finds nothing). Transitive dep via `modernc.org/libc`.
  Clears by upgrading `golang.org/x/sys` to ≥ v0.44.0 if ever desired. CI is green.
- Consequences: every push to `main` and every PR is gated on build/vet/format/test/vuln.
  Pre-commit hooks are still absent (local runs are manual). No CD/release automation added.
- Evidence: `.github/workflows/ci.yml`; `known-risks-and-gaps.md` (Phase F notes).

### ADR-020: Task dependency model — join table, same-project, hard-block on claim/doing/done, derived ready/blocked state (Phase H)
- Status: Active
- Context: Agents need to express sequencing — task B should not be started until task A is done.
  The board had no prerequisite mechanism, so agents either polled manually or relied on out-of-band
  coordination.
- Decision:
  1. **Join table (`task_deps`)** rather than a column. A many-to-many relationship between tasks
     requires a join table; a column (e.g. `depends_on_id`) can only express one parent. The join
     table has a composite PK `(task_id, depends_on_id)` and `ON DELETE CASCADE` FKs on both
     columns (so deleting a task removes all its edges in both directions automatically). Propagated
     to existing DBs via `CREATE TABLE IF NOT EXISTS` in `schema.sql` — no migration-runner step, no
     version bump required (contrast with `ALTER TABLE projects ADD COLUMN archived_at`, which needed
     the v2 runner step; a new table just needs to be in `schema.sql`).
  2. **Same-project-only constraint.** A dependency may only link two tasks within the same project.
     Cross-project deps add cascade, visibility, and query-complexity problems disproportionate to
     the benefit for a personal board. Rejected at `AddDep` with `ErrValidation` → HTTP 400.
  3. **Cycle prevention via recursive CTE (`wouldCycle`).** Self-deps and transitive cycles are
     rejected. The CTE walks the existing `depends_on` graph forward from `dependsOnID` and checks
     whether `taskID` is reachable, catching A→B→C→A before the insert.
  4. **Hard-block on claim and on PATCH to `doing`/`done`.** A task with ≥1 open prerequisite
     (status != `done`) cannot be claimed (`ClaimTask`) and cannot be PATCHed to status `doing` or
     `done` (`PatchTask`). Other ops — edit, comment, assign, status→`todo`/`blocked` — are
     **unaffected**: a task can be commented on and edited before its prereqs are done. The block
     returns a typed `*BlockedError{OpenPrereqs []int64}` → `409 {"error":"blocked","open_prereqs":[…]}`.
     CLI maps it to exit 4 and prints the open prereq ids.
  5. **Ready/Blocked are derived, not stored statuses.** There is no `ready` value in
     `tasks.status`. `NPrereqs`/`NOpenPrereqs` are computed via subqueries in `ListTasks` and
     `GetTask`. `?ready=true` and `?blocked=true` are server-side filters; `[ready]`/`[blk:N]`
     are CLI display markers. This avoids a status-sync problem (a prereq completing must not
     require updating every dependent task) and keeps the status field a simple enumeration.
  6. **Two new event kinds** — `task.dep_added`, `task.dep_removed` — following the existing
     `noun.verb` convention. Total event kinds: 14.
- Rationale: join table is the canonical relational model for many-to-many. Same-project constraint
  keeps the query surface small and avoids cross-project cascade. Hard-blocking claim/doing/done
  enforces ordering without requiring agent cooperation; soft fields (edit/comment) are left
  unblocked to preserve ergonomics. Derived state avoids the consistency hazard of a stored
  `ready` column.
- Consequences: cross-project dependencies are unsupported; agents must coordinate cross-project
  sequencing manually. A "blocked by deps" task can have status `todo` — agents should use
  `am ls --ready` rather than `am ls --status todo` to find truly actionable work.
- Evidence: `cmd/am/schema.sql` (`task_deps` table + index); `cmd/am/store.go` (`DepRef`,
  `BlockedError`, `Task.NPrereqs`/`NOpenPrereqs`/`DependsOn`/`Blocks`, `TaskFilter.Ready`/
  `Blocked`, `AddDep`, `RemoveDep`, `hasOpenPrereqs`, `wouldCycle`); `cmd/am/server.go`
  (`handleAddDep`, `handleRemoveDep`, `?ready=`/`?blocked=` params, `BlockedError` → 409);
  `cmd/am/cli.go` (`cmdDep`, `--ready`/`--blocked` flags, `taskLine` `[blk:N]`/`[ready]`);
  `cmd/am/web/app.js` (deps section, card tags, hard-block UX); 24 new tests.

### ADR-021: Dependency-graph overlay — vanilla SVG, no graph library (Phase I)
- Status: Active
- Context: The task-dependency DAG (Phase H / ADR-020) ships as CLI + API; the human dashboard
  needs a visual companion so humans can see the whole graph at a glance, trace chains, and spot
  bottlenecks without reading raw CLI output. The no-npm/single-binary/no-build-step invariant
  (ADR-001, ADR-007) rules out any npm graph library (d3, Cytoscape, Elk.js, etc.).
- Decision:
  1. **Vanilla SVG via a `svg()` helper (`createElementNS`), no library, no npm.** A new `svg(tag,
     attrs)` function parallels the existing `el(tag, props, ...kids)` HTML helper: it calls
     `document.createElementNS(SVG_NS, tag)`, sets attributes, and is the sole SVG construction
     primitive in `app.js`. The technique was ported from a reference DFD renderer. All text is set
     via `.textContent` (never `innerHTML`) — XSS-safe, guarded by `TestDashboardNoXSSSinks`.
  2. **Layered DAG layout using topological longest-path / Kahn's algorithm.** Prerequisites
     are placed to the left, dependents to the right, each layer placed at depth = max(predecessor
     depths) + 1. Dependency-free (isolated) tasks are collected into a compact grid **"No
     dependencies" lane** below the DAG so they don't pile into one tall column; all tasks are
     still shown. No crossing-minimization — a deliberate simplification acceptable for personal-
     board scale; pan/zoom and the isolated lane mitigate readability for denser graphs.
  3. **Entry:** a **"Graph"** button in the header `.actions` + the **`g`** keyboard shortcut
     (suppressed while a text input has focus). Opens `#graphOverlay` — a full-screen overlay
     reusing the existing modal focus-trap + `Esc`-to-close. Overlay has: a project `<select>`
     (defaults to the selected project), a **"Reset view"** button, a close ✕, the SVG canvas
     (`#graphSvg`), a **right detail panel** (`#graphDetail`), and a **bottom-left legend**
     (`#graphLegend`).
  4. **Node/edge encoding.** Nodes are colored by task **priority** (`PRIO` palette); each node
     shows a status dot and a Ready/🔒 Blocked indicator. Edges are colored by prereq-satisfied
     state: a `done` prerequisite → **green solid** ("cleared"); an open prerequisite → **amber
     dashed** ("blocking"). A legend explains both axes.
  5. **Transitive-path highlight.** Clicking a node runs a BFS both backward (upstream ancestors
     — "what leads to it") and forward (downstream subtree — "what it unblocks"), applying
     distinct CSS accent classes while dimming all other nodes/edges. Clicking the empty canvas
     clears the selection.
  6. **Right detail panel.** Built with `el()` (never `innerHTML`): task title, status/priority/
     assignee, Ready/Blocked state, a clickable **Prerequisites** list, a clickable **Unblocks**
     list (both navigate the graph selection), and an **"Open task"** button that invokes the
     existing `openTask()` detail modal.
  7. **Pan (drag) + zoom (wheel) + Reset view.** Implemented via `viewBox` manipulation on the SVG
     element. A "Reset view" button restores `graphInitialView`.
  8. **Live refresh.** `graphMaybeRefresh` is called from `onEvent` for project-affecting events
     (`task.dep_added/removed`, `task.status`, `task.created/deleted`, `task.assign`,
     `task.patched`). It debounces re-fetches via `graphRefreshTimer` and restores
     `graphViewState` + `graphSelectedId` after the re-render.
  9. **Backend: read-only `GET /api/projects/{slug}/graph`** — `handleProjectGraph` →
     `store.ProjectGraph(slug)`. Reuses `ListTasks(TaskFilter{Project: slug})` for nodes and a
     `task_deps JOIN tasks` query for edges oriented prereq→dependent
     (`{from: depends_on_id, to: task_id}`). Returns `{nodes: []Task, edges: []GraphEdge}`.
     No writes, no events emitted. 404 on a missing project. New types: `GraphEdge`, `ProjectGraphData`.
  10. **Tests:** +4 backend (`TestProjectGraph`, `TestProjectGraphMissingProject` in
      `store_test.go`; `TestProjectGraphEndpoint`, `TestProjectGraphEndpoint404` in `server_test.go`).
      Total: **95 tests**. The overlay JS is untested behaviorally (no JS runner — ADR-018);
      the `TestDashboardNoXSSSinks` source-level guard covers XSS safety.
- Rationale: preserves the no-npm/single-binary invariant (ADR-001/ADR-007); `createElementNS` is
  standard browser API that requires no build step; the `svg()` helper keeps the XSS-safe
  `textContent` discipline consistent throughout the codebase. Layered longest-path is the
  canonical DAG visualization algorithm; crossing-minimization adds complexity disproportionate to
  a personal board. The isolated-task lane prevents the layout from becoming unusable when most
  tasks have no deps.
- Consequences: no graph library dependency; SVG is built imperatively (verbose but transparent);
  the layout algorithm is simplified (no Sugiyama-style crossing minimization — denser graphs may
  have edge crossings); behavioral JS is not automatically tested (ADR-018 deliberate gap).
- Evidence: `cmd/am/web/app.js` (`svg()`, `computeGraphLayout`, `renderGraph`, `renderGraphDetail`,
  `graphMaybeRefresh`, `openGraphOverlay`/`closeGraphOverlay`, graph state variables);
  `cmd/am/web/index.html` (`#graphOverlay`, `#graphSvg`, `#graphDetail`, `#graphLegend`,
  `#graphBtn`, `#graphReset`, `#graphProjectSel`); `cmd/am/web/app.css` (`.graph-*`, `.gnode-*`,
  `.gedge-*`, `.gd-*`); `cmd/am/server.go` (`handleProjectGraph`); `cmd/am/store.go`
  (`ProjectGraph`, `GraphEdge`, `ProjectGraphData`); `cmd/am/store_test.go` + `cmd/am/server_test.go`
  (4 new graph tests).

### ADR-022: Stale-claim recovery — staleness from `updated_at`, steal via conditional UPDATE, no lease daemon (Phase K)
- Status: Active
- Context: Agents crash after `am claim`, leaving tasks assigned forever — nothing on the board
  distinguishes "agent is working" from "agent is dead", and other agents have no safe way to take
  the work over (`am assign` is an unconditional overwrite that could rob a live agent).
- Decision:
  1. **Staleness is judged from `updated_at`, not a heartbeat.** A task is stale when
     `assignee IS NOT NULL AND status != 'done' AND updated_at < now - dur`. Every meaningful
     action (claim, status change, comment, edit) already bumps `updated_at`, so an agent posting
     `am note` progress keeps its claim fresh with zero new protocol. The caller chooses the
     window per call (`--stale <dur>` / `--steal-stale <dur>`, Go duration syntax) — no global
     config. The cutoff is computed in Go (`staleCutoff`) in the exact
     `strftime('%Y-%m-%dT%H:%M:%fZ')` 3-digit-fraction format so the TEXT comparison sorts
     correctly.
  2. **`tasks.claimed_at` column (schema migration v3)** records when the current claim started —
     set by claim/steal/PATCH-assign, cleared on unassign (`am drop`). It is informational
     (returned in task JSON); the stale predicate deliberately uses `updated_at` so long-running
     but active work is never considered stale.
  3. **Steal is an atomic conditional UPDATE, mirroring ClaimTask:** `UPDATE … WHERE id=? AND
     status!='done' AND (assignee IS NULL OR updated_at < cutoff) RETURNING …`
     (`StealStaleClaim`). Exactly one concurrent stealer wins; losers get a typed
     `*NotStaleError{Assignee}` → `409 {"error":"not_stale","assignee":…}` → CLI exit 4. A done
     task → `*ConflictError`; open prereqs hard-block like a normal claim; stealing your own task
     is idempotent (no event).
  4. **Steal on an unclaimed task degrades to a normal claim** (the `assignee IS NULL` arm) —
     `--steal-stale` is a strict superset of `claim`, so an orchestrator can use one code path;
     the emitted event is then a plain `task.claimed`.
  5. **New event kind `task.reclaimed`** (15 total), emitted in the same tx as the takeover, with
     data `{"assignee":[prev,new],"status":…,"stale_for":"30m0s"}` — the audit log names who was
     robbed and under what window.
  6. **No lease/heartbeat daemon and no auto-reaper.** Recovery is pull-based: a human or
     orchestrator decides when to steal. The server never reassigns work on its own.
  7. **Plain `am assign` intentionally remains an unconditional overwrite** — it is the human
     "I know what I'm doing" escape hatch; `--steal-stale` is the safe path agents should use.
- Rationale: `updated_at` staleness needs no new agent protocol and is monotone with actual
  activity; the conditional-UPDATE pattern is already the project's proven race-safety primitive
  (ADR-020's hard-block, the original atomic claim); a reaper daemon would add background state
  and policy (what window?) the caller can express better per call.
- Consequences: an agent that works for hours without posting any update can be robbed — agents
  should `am note` at milestones (already the documented flow); choosing too small a window is
  the caller's risk. Stolen work may be half-done; the `task.reclaimed` event + comments are the
  handoff context.
- Evidence: `cmd/am/store.go` (`StealStaleClaim`, `NotStaleError`, `staleCutoff`,
  `TaskFilter.Stale`, `Task.ClaimedAt`, migration v3); `cmd/am/server.go` (`steal_stale` body
  field, `?stale=` param, `not_stale` 409); `cmd/am/cli.go` (`--stale`, `--steal-stale`, exit-code
  mapping); `cmd/am/web/app.js` (`.tag-stale` badge, `task.reclaimed` feed rendering);
  `cmd/am/store_test.go` (`TestStealStaleClaim`, `TestStealRaceExactlyOneWinner`,
  `TestListTasksStaleFilter`, `TestClaimSetsClaimedAt`, `TestDropClearsClaimedAt`);
  `cmd/am/server_test.go` (`TestListTasksStaleParam`, `TestStealStaleEndpoint`);
  `cmd/am/cli_test.go` (`TestExitNotStale`, `TestStaleFlagsWireFormat`);
  `cmd/am/migrate_test.go` (`TestMigrationV3AddsClaimedAt`).

### ADR-023: Agent work loop — atomic `am next` via conditional UPDATE-with-subquery, `am wait` as a CLI-side SSE consumer, bulk verbs as a client-side loop (Phase L)
- Status: Active
- Context: An agent loop needs three primitives the board lacked: "give me the best thing to work
  on" without a list-then-claim race window, "block until my prerequisite is finished" without
  polling, and "mark these five subtasks done" without five invocations.
- Decision:
  1. **`am next` is pick + claim in ONE statement** (`NextTask`): `UPDATE tasks SET assignee=…,
     status='doing', claimed_at=…, updated_at=… WHERE id = (SELECT t.id … ORDER BY t.priority ASC,
     t.id ASC LIMIT 1) RETURNING id, project_id` — the same conditional-UPDATE race primitive as
     ClaimTask/StealStaleClaim (ADR-022), serialized by `SetMaxOpenConns(1)`, so N concurrent
     callers get N distinct tasks. Candidates: `status='todo' AND assignee IS NULL`, no open
     prerequisites (the exact `NOT EXISTS` predicate of ListTasks' Ready filter), non-archived
     project, optional project scope.
  2. **FIFO tiebreak: `priority ASC, id ASC`** — deliberately NOT the `updated_at DESC` display
     order of `am ls`; a pickup queue should drain oldest-first, and recently-touched-first would
     starve old tasks. 0 is the most urgent priority (matching ListTasks).
  3. **`next` skips tasks pre-assigned to the caller** — candidates require `assignee IS NULL`,
     so a task already assigned to you is never returned by `am next`; claim it explicitly with
     `am claim <id>`. Keeps the predicate one uniform condition with no per-caller arm.
  4. **No new event kind** — a `next` pickup IS a claim; it emits `task.claimed` with the same
     payload shape (`{"assignee":[null,agent],"status":"doing"}`). Event-kind catalog stays at 15.
  5. **404 ambiguity accepted on `am next`** — an empty board and a bad `-p` slug both map to
     `404 not_found` → exit 3 (`next: no ready task`). An agent loop treats both as "nothing to
     do here"; disambiguating would cost an extra round-trip or error shape for no loop benefit.
  6. **`am wait` is a pure CLI-side SSE consumer; the server is untouched** (`cmd/am/wait.go`).
     Exactly two conditions: `am wait <id> --done` and `am wait --ready [-p P]`. It snapshots the
     event cursor (`/api/events?tail=1`) BEFORE the first REST condition check, then follows
     `/api/stream?since=<cursor>` (reconnecting from the last seen id), and on each relevant event
     **re-evaluates the condition via REST** — event payloads are never trusted as state. The
     existing `?since=` replay closes the check/subscribe race. `Client.http`'s 10s timeout would
     kill a stream, so cmdWait uses its own un-timed `http.Client` bounded by a
     `context.WithTimeout` over the whole wait.
  7. **Exit code 7 = wait timeout** (default window 10m; `--timeout` takes a Go duration or bare
     seconds). Distinct from 4 (conflict) and 6 (server down) so a loop can branch on "condition
     just didn't happen yet".
  8. **Bulk `am status`/`am assign` stay client-side** — a loop of per-id PATCHes, one
     `task.status`/`task.assign` event per task (the feed and SSE consumers keep per-task
     semantics; no new bulk endpoint or transaction). Partial failure: one stderr line per failing
     id, the loop continues, exit code is the FIRST failure's mapping via the new `exitCodeFor`
     helper (extracted from `doOrFail`, now the single status→exit-code source).
- Rationale: the conditional-UPDATE-with-subquery removes the agent's biggest race (two agents
  `am ls --ready` then both claim the same top task) with zero new locking machinery; wait-as-client
  keeps the server's SSE surface frozen and reuses the replay cursor that already exists for the
  dashboard; bulk-as-loop preserves the one-event-per-mutation invariant that the feed, dashboard,
  and `am wait` itself rely on.
- Consequences: `am next` under heavy contention serializes on the single writer (fine at personal
  scale); a bulk command is not atomic across ids (documented partial-failure contract instead);
  `am wait --ready` re-checks on every in-scope event (cheap REST GET, but chatty boards cause
  more checks); the 404 ambiguity means a typo'd `-p` slug looks like an empty board.
- Evidence: `cmd/am/store.go` (`NextTask`); `cmd/am/server.go` (`POST /api/tasks/next`,
  `handleNext`); `cmd/am/wait.go` (`cmdWait`, `waiter`, `readSSEFrame`, `parseWaitTimeout`);
  `cmd/am/cli.go` (`cmdNext`, bulk `cmdStatus`/`cmdAssign`, `bulkPatch`); `cmd/am/client.go`
  (`exitCodeFor`); `cmd/am/store_test.go` (`TestNextTask*` incl. `TestNextTaskRaceDistinctWinners`);
  `cmd/am/server_test.go` (`TestNextEndpoint`, `TestNextEndpointProjectBody`); `cmd/am/cli_test.go`
  (`TestCmdNextPrintsOnlyID`, `TestExitNextNoneReady`, `TestCmdStatusBulk`,
  `TestCmdStatusBulkPartialFailure`, `TestCmdAssignBulk`); `cmd/am/wait_test.go` (10 wait tests).

### ADR-024: Findability — LIKE-with-ESCAPE search (no FTS5), inline label join table (no catalog), labels don't bump `updated_at` (Phase M)
- Status: Active
- Context: A grown board needs "find the task about X" (search) and "show me the frontend work"
  (labels). Both must stay within the project's constraints: single binary, no migrations unless
  unavoidable, token-cheap CLI output, event log as the only side channel.
- Decision:
  1. **Search is plain `LIKE` with `ESCAPE '\'`, not FTS5** (`TaskFilter.Query`, `likeEscape`).
     `?q=` / `am ls --grep` matches a substring of **title OR body**; the query's `%`/`_`/`\` are
     escaped so they match literally. FTS5 would add a virtual table + sync triggers (a real
     migration) for a personal-scale board where a linear LIKE scan is fine.
  2. **ASCII-case-insensitive, documented as such** — SQLite's default LIKE folds ASCII only;
     Unicode case folding is deliberately not applied (would need ICU or lower() normalization
     columns). This is the documented contract of `--grep`.
  3. **Search scope exclusions:** `?q=` does **not** search comments or label names. Comments are
     a thread, not the task's identity; labels have their own exact filter (`?label=`).
  4. **Labels are an inline join table, no catalog** — `task_labels(task_id, label TEXT)` with a
     composite PK and `ON DELETE CASCADE`; a label "exists" iff some task carries it. No separate
     `labels` table to create/rename/garbage-collect. Added via `CREATE TABLE IF NOT EXISTS` in
     `schema.sql` — **no migration step, no version bump** (schema version stays 3; the
     `task_deps` precedent). Not added to `validateImportCandidate`'s required set, so pre-M
     snapshots stay importable.
  5. **Label normalization at the boundary** (`normalizeLabel`): trim, lowercase, 1–50 bytes
     matching `^[a-z0-9._-]+$`. The charset excludes `,` (so `GROUP_CONCAT` output splits safely
     into the list payload) and `+`/space (so the CLI's `+add`/`-remove` tokens are unambiguous).
     Lowercasing at write AND filter time makes the `?label=` SQL `=` comparison predictable
     (`=` is case-sensitive even though LIKE isn't).
  6. **Labeling does NOT bump `updated_at`** — labels are metadata; refreshing the activity
     timestamp would keep a stale claim alive (`--steal-stale` judges staleness from
     `updated_at`, ADR-022). Same precedent as dep edges. Guarded by
     `TestAddLabelDoesNotBumpUpdatedAt`.
  7. **Two new event kinds** — `task.labeled` / `task.unlabeled` with `{"label": l}` payloads
     (catalog 15 → **17**). Idempotent no-ops (duplicate add, absent remove) commit without an
     event, like deps.
  8. **`cmdLabel` takes raw argv** — dispatched in `main.go` before `parse()`, because the parser
     would consume a removal token (`-bar`) as a value flag. `am label <id>` alone prints the
     labels space-separated. Flag-like tokens are rejected, not interpreted: a `--…` token is a
     usage error, and the known global value flags `-p`/`-c` are refused by name with a hint
     (both exit 5) — so a habitual `am label 12 --json` or `-p web` can't silently add or remove
     a label.
  9. **Deliberately deferred:** no `--label` on `am next` / `am new` (keep the pickup predicate
     uniform; add when demand appears), and **no labels in `taskLine`** (`am ls` row token budget;
     labels are in `--json` and `am show`).
- Rationale: both features ride existing machinery — the WHERE-clause builder in `ListTasks`, the
  `AddDep`/`RemoveDep` tx + idempotency shape, the `CREATE TABLE IF NOT EXISTS` no-migration path —
  so the change surface is small and convention-clean.
- Consequences: LIKE search is O(n) over tasks (fine at personal scale; FTS5 is the upgrade path);
  Unicode-cased titles ("Über") only match exact-case queries; the board can't list "all labels"
  cheaply without a `SELECT DISTINCT` (acceptable — no catalog endpoint shipped); a label filter +
  search box both filter server-side, so the dashboard's SSE debounce re-applies them on reload.
- Evidence: `cmd/am/store.go` (`likeEscape`, `normalizeLabel`, `AddLabel`, `RemoveLabel`,
  `TaskFilter.Query/Label`); `cmd/am/schema.sql` (`task_labels`); `cmd/am/server.go`
  (`handleAddLabel`, `handleRemoveLabel`, `?q=`/`?label=` in `handleListTasks`); `cmd/am/cli.go`
  (`cmdLabel`, `--grep`/`--label` in `cmdLs`); `cmd/am/web/app.js` (search box, label chips +
  filter, modal Labels section); tests listed in the Phase M CHANGELOG entry.

### ADR-025: Category layer + stable IDs + vault binding — `amc_`/`amp_` crypto/rand uids, nullable FK with app-enforced NOT NULL, globally-unique slugs, `-c` flag with a `show` carve-out, open-time version ceiling (Phase O)
- Status: Active
- Context: The agentic_brain integration (requirements R1/R2/R3/R8) needs a **category** layer
  above projects (one instance, one DB, agents scoped down later), **stable IDs** the vault can
  bind to across slug renames, **vault binding metadata** on projects, and a migration that
  carries every existing DB forward with zero data loss. This is Phase O ("Foundation") of that
  train; scoping enforcement (Phase Q), the category dashboard (Phase R), and scope tokens
  (Phase S) build on it, with task metadata (Phase P) in parallel.
- Decision:
  1. **Stable-ID format: `amc_`/`amp_` + 16 lowercase hex** (`newUID` — 8 bytes of `crypto/rand`,
     stdlib only; no ULID dependency). Immutable after creation; survives slug renames; the
     vault's canonical correlation key. A bare `p_` prefix was avoided because the vault's own
     project IDs use that namespace and sit next to these in the same binding fields. Insert
     paths retry up to 3 attempts on a uid UNIQUE collision (`isUniqueErr`); the v4 migration
     backfill assigns per-row without retry (collision odds ~2⁻⁶⁴).
  2. **`projects.category_id` is nullable in SQL — NOT NULL by app invariant.** A deliberate
     deviation from the requirement's "NOT NULL FK": SQLite's `ALTER TABLE ADD COLUMN` cannot add
     a NOT NULL column without a constant default, which is wrong for an FK. The invariant is
     enforced in the app instead: `CreateProject` always sets it, the v4 migration backfills all
     NULLs to `general`, and nothing can clear it (`category_id` is not patchable). UNIQUE is
     likewise not allowed in `ADD COLUMN`, hence the `idx_projects_uid` unique index for
     `projects.uid`.
  3. **Project slugs stay globally unique** (no per-category namespacing). Task refs like `web-3`
     and `AGENTMAN_PROJECT` keep working unchanged, and `am new` needs no `-c` — a slug names
     exactly one project across the instance, so a project fully determines its category.
  4. **`-c` becomes the global category flag** (`canonFlag c → category`; env fallback
     `AGENTMAN_CATEGORY`) — with one carve-out: `am show <id> -c` is the documented comments
     toggle, so `main.go` rewrites `-c → --comments` for the `show` verb only
     (`rewriteShowComments`, run before `parse()`). Chosen over a per-verb alias table to keep
     `canonFlag` simple.
  5. **Archived-category cascade mirrors the archived-project rules.** Default views
     (`GET /api/projects`, unscoped `GET /api/tasks`, the unscoped event feed) require both
     `p.archived_at IS NULL` AND `c.archived_at IS NULL`; an explicit `?category=` drops the
     category-archived condition so the scope stays inspectable (hidden, not blocked-from-read);
     `next` excludes archived categories unconditionally; writes are blocked — task/project
     creation under an archived category → `ErrCategoryArchived` →
     `400 {"error":"category_archived"}` → CLI exit 5 (**no new exit codes**).
  6. **Dashboard default-category compatibility:** `POST /api/projects` with an empty category
     maps to `general` server-side rather than 400, so the existing dashboard (and any script
     posting `{slug,name}`) keeps working without a UI change. The v4 migration seeds `general`
     **unconditionally**, so fresh installs have it too.
  7. **`am wait --ready -c` streams unscoped:** `/api/stream` has no `?category=` yet (Phase R);
     the unscoped stream just triggers the category-scoped REST re-check (the ADR-023 pattern —
     event payloads are never trusted as state). Slightly chattier (re-checks on out-of-scope
     events) but correct.
  8. **Open-time schema-version ceiling:** `OpenStore` refuses a DB whose recorded
     `schema_version` is newer than `currentSchemaVersion` ("database schema_version N is newer
     than supported M — upgrade am") — migrations only run forward, so an older binary would
     otherwise operate on (and corrupt or silently hide) too-new data. Mirrors the ceiling
     `validateImportCandidate` already applies to import snapshots (closes the ADR-010 residual).
  9. **Old snapshots stay importable by design:** `validateImportCandidate`'s required-table set
     is pinned to the **v1 baseline** (no `categories`/`task_deps`/`task_labels`) — later tables
     are created by `schema.sql`/migrations on the next `OpenStore`, so pre-v4 snapshots import
     and migrate cleanly.
- New event kinds: `category.created` / `category.archived` / `category.unarchived` (these carry
  **no `project_id`**, so they reach unscoped SSE subscribers only and need explicit feed-render
  cases — the default branch would print a literal "null" ref) and `project.patched` (compact
  delta, modeled on task patches). Catalog 17 → **21**.
- Rationale: one instance with a category layer beats one-instance-per-domain (single dashboard,
  scoped agents later); crypto/rand hex keeps the stdlib-only invariant; the nullable-FK deviation
  is the only way to ship the column through SQLite's ALTER TABLE while the app preserves the
  semantic invariant; global slug uniqueness protects every existing ref and env contract; the
  show-verb rewrite preserves a documented CLI surface at near-zero complexity.
- Consequences: category moves are unsupported (`category_id` not patchable — revisit when
  needed); the `-c` rewrite means `show` can never grow a category filter under that letter;
  category events are invisible to project-scoped SSE subscribers until Phase R; `am project new`
  is no longer zero-config (requires `-c` or `AGENTMAN_CATEGORY`, though `general` always
  exists).
- Evidence: `cmd/am/store.go` (migration v4, `newUID`, `isUniqueErr`, `CreateCategory`,
  `ListCategories`, `ArchiveCategory`/`UnarchiveCategory`, `CreateProject`, `PatchProject`,
  `getProjectTx`, `TaskFilter.Category`, `NextTask`, the cascade WHERE clauses, the `OpenStore`
  ceiling); `cmd/am/schema.sql` (`categories` table; projects CREATE TABLE frozen as the v1
  baseline); `cmd/am/server.go` (category routes, `PATCH /api/projects/{slug}`,
  `ErrCategoryArchived` → 400); `cmd/am/cli.go` (`canonFlag`, `categoryFor`, `cmdCategories`,
  `cmdCategory`, `project new`/`edit`); `cmd/am/main.go` (`rewriteShowComments`);
  `cmd/am/wait.go` (category-scoped `checkReady`); `cmd/am/db.go` (v1-baseline comment);
  `cmd/am/web/app.js` (category feed cases, project-strip reload); the 30 Phase O tests listed
  in the CHANGELOG entry.

### ADR-026: Task metadata — `task_meta` join table, key-presence filters, repeatable-flag CLI parser, empty-value removal, delta reuse with no new event kinds, `NextFilter` refactor (Phase P)
- Status: Active
- Context: The agentic_brain integration (requirement R7) needs free-form `key=value` pairs on
  tasks — e.g. marking a task `auto=true` so an autonomous worker loop can wait for and pick up
  exactly the tasks meant for it — with the pair settable at create and edit time and the key
  filterable across `am ls`, `am next`, and `am wait --ready`. This is Phase P of the train ADR-025
  opened; scoping enforcement (Phase Q) builds on the same filter plumbing.
- Decision:
  1. **`task_meta` join table, not a JSON column** — `task_meta(task_id, key, value)` with
     composite PK `(task_id, key)`, `ON DELETE CASCADE`, and index `idx_task_meta_key`. Like
     `task_labels` there is no separate catalog — a key exists iff some task carries it. A JSON
     column would make the presence filter a string-scan instead of an indexed `EXISTS`. Shipped
     via `CREATE TABLE IF NOT EXISTS` in `schema.sql` — **no migration step, no version bump**
     (`currentSchemaVersion` stays 4; the `task_labels`/`task_deps` precedent; old DBs heal on
     reopen, guarded by `TestTaskMetaTableExistsOnReopenedDB`).
  2. **Repeatable-flag parser registry** (`multiFlags` + `Args.multi` + `a.all(k)`) rather than
     comma-splitting one flag value — meta values are opaque and may contain commas; repetition is
     the unambiguous CLI shape. A flag is multi OR single (last-wins), never both; `--meta` is the
     first multi flag, is NOT in `boolFlags`, and has no short alias. Tokens split at the FIRST
     `=`, so values may themselves contain `=`.
  3. **Empty value removes on edit, `ErrValidation` on create** — PATCH needs a removal verb
     inside one atomic body (`--meta k=` / `"k":""`); create has nothing to remove, so an empty
     value there is a 400. Absent-key removal is a silent no-op.
  4. **Keys normalized like labels, values opaque ≤ `maxTitleLen`** — `normalizeMetaKey` reuses
     `labelRe`/`maxLabelLen` (trim, lowercase, 1–50 chars of `a-z 0-9 . _ -`): keys are
     filter/index material, and the charset excludes `=`/space/`,`, keeping CLI tokens and any
     future concat safe. Values render on cards/SSE payloads so they get the 500-byte title cap,
     but are otherwise uninterpreted.
  5. **Duplicate keys after normalization are rejected** (create AND patch) — two raw keys
     collapsing to one normalized key (`{"Auto":"a","auto":"b"}`) would make the winner
     map-iteration-nondeterministic and record a just-written value as "old" in the delta;
     instead `400 validation` with `duplicate meta key after normalization: "auto"`. Keeps
     requests deterministic and all-or-nothing. **No new error codes, no new exit codes.**
  6. **Reuse `task.created`/`task.patched` instead of new event kinds** — the catalog stays at
     **21**. `task.created` data gains `"meta": {k: v}`; `task.patched` gains a `"meta"`
     sub-object in the existing delta shape (`{"meta": {"k": [old, new]}}`, `null` = absent), so
     old/new are preserved for audit. One event per PATCH regardless of key count; `applyMetaTx`
     walks keys in sorted order so payloads and failure points are deterministic, and any error
     aborts the caller's tx (multi-key atomicity).
  7. **Meta-only patches do NOT bump `updated_at`** — meta is metadata like labels; refreshing
     the activity timestamp would keep a stale claim alive (`--steal-stale` judges staleness from
     `updated_at` — the ADR-022/ADR-024 `AddLabel`/dep-edge precedent). Mixed field+meta patches
     still bump. Guarded by `TestMetaOnlyPatchDoesNotBumpUpdatedAt`.
  8. **Meta in `ListTasks`/`GetTask` but not `getTaskTx`** (labels parity) — the terse tx-internal
     read stays cheap, so PATCH/claim responses omit meta like they omit labels. The list stitch
     is **one follow-up SELECT, not `GROUP_CONCAT`** — values may contain the separator, so the
     labels concat trick is unsafe; one extra parameterized query per list call is the safe shape.
  9. **`NextTask(project, category, agent)` → `NextTask(f NextFilter, agent)`** with
     `NextFilter{Project, Category, MetaKey}` — a fourth string parameter would be unreadable and
     Phase Q adds more scope dimensions; extending the struct beats widening the signature again.
  10. **Same-predicate next/wait invariant** — the NextTask meta predicate is textually identical
      to ListTasks' `MetaKey` predicate (enforced by comment at both sites): a task that releases
      `am wait --ready --meta K` must be pickable by `am next --meta K`, so a worker loop can't
      hot-spin on a wait that `next` then refuses. `--meta` on `wait --ready` narrows only the
      REST re-check, never the SSE stream (ADR-023: event payloads are triggers, not state).
- Rationale: every piece rides proven machinery — the `EXISTS` filter builder, the
  `CREATE TABLE IF NOT EXISTS` no-migration path, the delta-event shape, the conditional-UPDATE
  pickup — so the change surface is small; presence-only filtering keeps the predicate cheap and
  indexed while values stay free-form; rejecting normalization collisions buys determinism for
  the price of one map lookup.
- Consequences: values are not filterable (presence only — a value-match filter would be a new
  decision); list responses grow by the tasks' meta payloads (values ≤ 500 bytes each); the list
  stitch adds one bind variable per returned row (bounded by the list `limit`); the dashboard can
  display but not edit meta (CLI/API-only writes for now).
- Evidence: `cmd/am/schema.sql` (`task_meta` + `idx_task_meta_key`); `cmd/am/store.go`
  (`normalizeMetaKey`, `applyMetaTx`, `Task.Meta`, `TaskFilter.MetaKey`, `NextFilter`,
  `CreateTaskInput.Meta`, the ListTasks stitch, the NextTask predicate comment); `cmd/am/server.go`
  (`meta` in create/patch bodies, `?meta_key=`, `meta_key` in `handleNext`); `cmd/am/cli.go`
  (`multiFlags`, `parseMetaFlags`, `metaKeyArg`, `cmdShow` meta line); `cmd/am/wait.go`
  (`checkReady` metaKey); `cmd/am/web/app.js` (modal Meta section, `task.patched` feed suffix);
  the 25 Phase P tests listed in the CHANGELOG entry.

### ADR-027: Scoped agent identity & enforcement — client-asserted `X-Agent-Scope`, one `scopeOf` resolution point, hybrid read policy, proposals carve-out by (category, project) pair, `tasks.created_by` via migration v5, denials log-only (Phase Q)
- Status: Active
- Context: The agentic_brain integration (requirement R4) needs agents **confined** to a slice of
  the board — a category (the common case) or a single project (tighter) — so an autonomous fleet
  can share one instance without one agent mutating another's work. Phase O built the category
  layer for exactly this; Phase Q is the enforcement. This is the third phase of the train ADR-025
  opened (Phase P task metadata in parallel); the category dashboard + scoped feed (Phase R) and
  scope tokens (Phase S) build on it.
- Decision:
  1. **Client-asserted scope, one resolution point.** A scope rides as the `X-Agent-Scope` header
     (`category[/project]`, trimmed + lowercased like slugs); `scopeOf(r)` in `server.go` is the
     **sole** reader of it. Phase S (scope tokens) swaps the scope's *source* there without touching
     a single handler. Like `X-Agent`, the header is **accident prevention for an agent following
     its config, not a security boundary** against crafted HTTP (no auth — `security.md`'s caveat
     applies); R5 scope tokens are the upgrade path. An absent header is the zero (unscoped) Scope
     and passes every check; a malformed one (empty/whitespace segments, >1 slash) is `400
     validation` everywhere (`parseScope` wraps `ErrValidation`); well-formed but unknown slugs are
     **not** validated against the DB (mutations 403, unfiltered lists come back empty).
  2. **Hybrid enforcement placement, justified by immutability.** The scope pre-checks
     (`checkTaskMut`/`checkTaskRead`/`checkComment`/`checkCreate`/`checkProjectMut`/`checkProjectRead`/
     `narrowScope`) run in the handler, **outside** the store transaction. This is sound only
     because `task→project` and `project→category` are immutable today (`category_id` is not
     patchable — ADR-025; a task never moves projects). The one exception that must stay in-tx is
     **`am next`**: `narrowScope` merges the scope into the `NextFilter` **before** `NextTask`, so
     the scope is part of the candidate predicate inside the atomic pick+claim — a scoped agent can
     never be handed an out-of-scope task even racing unscoped callers. Comments on
     `PatchTask`/`PatchProject` record that the pre-checks must move in-tx if a move feature ever
     ships.
  3. **Reads policy: loud where named, silent where browsing.** Named/explicit out-of-scope reads
     fail **loudly** with 403 — `GET /api/tasks/{id}`, `GET …/graph`, and an explicit
     `?project=`/`?category=` that contradicts the scope — mirroring the orchestrator's
     "ask-first outside your subtree" rule. Unfiltered lists are **silently narrowed** (missing
     filters filled from the scope), keeping `am ls` ergonomic for a scoped agent. The **proposals
     project is readable by all** (proposals are meant to be seen). `am wait` carries the scope on
     its REST re-checks (out-of-scope `--done`/`--ready` → exit 8) but the SSE **stream stays
     unscoped** (Phase R residual) — out-of-scope events merely trigger re-checks that keep failing,
     no hot-spin and no false release (the ADR-023 pattern: event payloads are triggers, not state).
  4. **`am next` carve-out does NOT extend the proposals exception.** `narrowScope(…, allowProposals)`
     is `true` for reads, `false` for next: an agent whose scope already covers the proposals project
     still picks them by plain in-scope matching, but the carve-out never auto-feeds another scope's
     proposals into a pickup.
  5. **Denials are log-only — no `scope.denied` event kind.** Every 403 logs
     `agentman: out_of_scope: actor=<id> scope=<scope> <METHOD> <PATH>` server-side (`denyScope`).
     Feeding denials into the SSE feed was rejected as noise + a partial-information leak; the
     21-kind catalog is **unchanged** this phase. Revisit only with a real audit requirement.
  6. **`tasks.created_by` via migration v5, best-effort backfill from the event log.**
     `currentSchemaVersion` 4 → 5 (`schema.sql` stays the frozen v1 baseline): `ALTER TABLE tasks
     ADD COLUMN created_by TEXT`, then backfill from the **LATEST** `task.created` event's actor —
     **latest, not first**, because `tasks.id` is a reusable SQLite rowid (no `AUTOINCREMENT`) and
     `DeleteTask` leaves a deleted task's events behind, so an id's oldest creation event may belong
     to a deleted predecessor; the newest is always the current incarnation's. Tasks whose events
     were pruned stay NULL and never match the own-proposal comment rule (the safe direction). New
     tasks set `created_by` from `actorOf(r)` (default `"human"`). `created_by` is exposed only on
     `GET /api/tasks/{id}` (and read by the scope checks) — **not** on list rows, keeping list
     payloads stable. Forward-only, idempotent, version bump in the same tx; the import ceiling
     tracks `currentSchemaVersion` (v5 imports, v6 refused).
  7. **Proposals carve-out keyed by the (category, project) PAIR, inert when missing.** Task
     creation — and commenting on one's OWN proposal tickets (`created_by == X-Agent`,
     NULL/empty never matches) — in the designated proposals project is allowed from any scope.
     The project is `--proposals CAT/PROJ` / `AGENTMAN_PROPOSALS` / default `meta/proposals` (flag
     beats env; both segments required — a bare category is rejected at `am serve` startup,
     `fail(1)`, because it would widen the carve-out to a whole category). The carve-out matches the
     **pair** at every site (`isProposals` is the single slug-keyed helper; `checkTaskRead`/
     `checkComment` check the pair directly): slugs are globally unique (ADR-025), so without the
     category check a scoped agent could **squat** the slug inside its own category and capture every
     other scope's proposals. A same-slug project in another category falls through to the normal
     rules; a **missing** designated project leaves the gate open and the store 404s, so the
     carve-out is inert, never an error or a hole. `NewServer` defaults to `{meta, proposals}` so
     embedded/test servers behave like production.
  8. **Category endpoints 403 for any scoped agent; project create only for a category-scoped agent
     in its own category.** The category layer sits above every scope, so `POST /api/categories` and
     category archive/unarchive are 403 for any non-zero scope. `POST /api/projects` is allowed for
     a category-scoped agent creating into its **own** effective category (empty body category =
     `general`) and 403 otherwise — and **always** 403 for a project-scoped agent (a project scope
     cannot reshape its category). Project mutations (patch/archive/unarchive/delete) require the
     project itself in scope (no proposals carve-out — proposing is creating tasks, never reshaping
     the project).
- New error: `ErrOutOfScope` → `403 {"error":"out_of_scope"}` → **CLI exit code 8** (wired through
  `exitCodeFor`, the single source, so `doOrFail` and the bulk verbs inherit it). No new event
  kinds, no schema-visible changes beyond `tasks.created_by`.
- Rationale: a single header + a single `scopeOf` reader keeps the whole feature swap-ready for
  scope tokens; placing the checks outside the tx is cheap and provably safe under today's
  immutability invariants; the loud-named / silent-unfiltered split matches how an orchestrator and
  an exploring agent each read the board; the pair-keyed carve-out is the minimum that both lets any
  agent file proposals and resists slug squatting; log-only denials avoid leaking cross-scope
  activity into the feed.
- Consequences (accepted residuals — see `known-risks-and-gaps.md`): `/api/events` and `/api/stream`
  still leak cross-scope activity to scoped agents until Phase R; `GET /api/projects` /
  `GET /api/categories` lists are not narrowed (board metadata visible, task data is not); an
  explicit unknown `?project=` (or a project-scoped create into an unknown slug) returns 403 rather
  than 404/empty — the server cannot prove it in-scope, so it fails loud; `created_by` backfill is
  best-effort (pruned-events tasks stay NULL); exit 8 from `am wait`'s host-guard path is cosmetic;
  TOCTOU between a scope check and the mutation is impossible only because of immutability — revisit
  with any move feature.
- Evidence: `cmd/am/store.go` (`Scope`/`parseScope`/`Scope.String`/`IsZero`, `ErrOutOfScope`,
  migration v5, `taskScope`, `projectCategory`, `Task.CreatedBy`, the `CreateTask` `created_by`
  insert, the `PatchTask`/`PatchProject`/`NextTask` scope-note comments); `cmd/am/server.go`
  (`scopeOf`, `scopeAllows`, `denyScope`, `checkTaskMut`/`checkTaskRead`/`checkComment`/`isProposals`/
  `checkCreate`/`checkProjectRead`/`checkProjectMut`/`narrowScope`, the per-handler pre-checks,
  `Server.proposals`, `writeErr` `ErrOutOfScope` → 403, `NewServer` default); `cmd/am/identity.go`
  (`identityRecord`, `resolveIdentity`/`resolveScope`, scoped `cmdInit`, `cmdWhoami` scope line);
  `cmd/am/client.go` (`Client.scope`, the `X-Agent-Scope` send, `exitCodeFor` 403 → 8);
  `cmd/am/wait.go` (`waiter.scope`, the 403 → exit 8 re-checks); `cmd/am/main.go` (`--proposals` /
  `AGENTMAN_PROPOSALS`, `usage()` scope/exit-8 lines); the 32 Phase Q tests listed in the CHANGELOG
  entry.

### ADR-028: Category dashboard + scoped feed — hub category fan-out resolved at Subscribe, `?category=` feed/stream filtering excluding category-level events, category stats folded into `/api/categories`, dashboard hash routing with overview-as-landing (Phase R)
- Status: Active
- Context: The agentic_brain integration (requirement R6) needs a **human** view organized by
  category — a category-home landing page showing where work is happening, drill-down into a
  single category's board, and a feed/stream that can be scoped to one category. This is the
  fourth phase of the train ADR-025 opened (after Phase P task metadata, Phase Q scoping
  enforcement); only Phase S (scope tokens, R5) remains after it. The Phase Q residual that
  `/api/events` and `/api/stream` were not category-filterable (and the Phase O note that
  `category.*` events would be "revisited in Phase R") are both closed here. Unlike the agent
  scope of Phase Q, the dashboard is **unscoped** — a human sees everything; "category view" is a
  query-param lens, not an identity scope (no `X-Agent-Scope` is sent).
- Decision:
  1. **Hub category fan-out resolved at Subscribe into a project-ID set, not per-event.** A
     category subscription resolves the category's projects **once** when the stream opens
     (`ProjectIDsInCategory` → `map[int64]bool`, carried in the hub's new `subFilter`/`subscriber`
     fields), so `Broadcast` stays a pure in-memory membership check with **no per-event DB hits**
     — the R9 SSE contract that the hub remains non-blocking and in-memory (the IADR-001 SSE
     design). A category-scoped subscriber receives an event only when its `ProjectID` is in the
     resolved set; cross-category and **category-level (ProjectID==0) events are dropped**.
  2. **`project.created` carve-out preserved through the category branch.** A brand-new project's
     `project.created` reaches every subscriber regardless of scope (the pre-existing carve-out, so
     a new tab can appear live even on a category-scoped dashboard). This is the one event that
     bypasses the membership check.
  3. **Post-open project staleness window accepted.** A project created *after* a category stream
     subscribes is not in that subscriber's resolved project-ID set, so its task events won't stream
     until the dashboard re-opens the stream (which it does on every view change). The REST snapshot
     remains the source of truth, and the `project.created` carve-out still surfaces the new project
     itself. Re-resolving the set per event (or invalidating on `project.created`) was rejected as
     re-introducing DB work into `Broadcast` for a window the dashboard already closes on navigation.
     Documented in `hub.go`.
  4. **`?category=` feed/stream filtering EXCLUDES category-level (NULL-project) events by design.**
     On `GET /api/events` (all of `?since=`/`?tail=`/`?before=`) and `GET /api/stream`, a category
     scope matches only events whose project lives in the category (`c.id=?` in the SQL; the
     in-memory set check on the stream). The category's own `category.*` events (NULL `project_id`)
     are **instance-wide admin events that belong to the All/overview feed**, not a single
     category's drill-down. Composes with `?project=` (ANDed). `RecentEvents` was refactored to
     compose its WHERE clause from a `[]string` slice (it now joins up to three conditions —
     project, category, archived-cascade); `ListEvents`/`ListEventsBefore` kept the incremental
     `q +=` style (their archived-cascade branch is mutually exclusive with the project/category
     branches).
  5. **Unknown-category divergence: `/api/events` 404s, `/api/stream` falls back silently.** An
     unknown category on the REST feed flows through the `categoryID` `ErrNotFound` sentinel →
     `404 not_found` (a one-shot lookup should fail loudly, like an unknown project on
     `/api/tasks`). On the long-lived SSE stream an unknown category is **ignored silently** (the
     subscriber sees the unfiltered stream) — matching the endpoint's existing unknown-`project`
     swallow; a best-effort stream should not be torn down over a stale slug.
  6. **Category counts + `active_agents` folded into `GET /api/categories`, always present.** The
     overview needs them on first paint; a separate endpoint would add a round-trip and a
     partial-render flash. `ListCategoriesWithStats` runs two queries merged in Go: `counts`
     (`{todo, doing, blocked, done}`) sum only the category's **non-archived** projects' tasks (an
     archived project's tasks are excluded even when `?archived=true` lists the category), and
     `active_agents` are the distinct non-human actors on **task-bearing** events
     (`task_id IS NOT NULL AND actor != 'human'`) within a **30-minute** window, sorted. The
     `task_id IS NOT NULL` predicate makes `comment.added` count as activity while category/project
     admin churn does not; excluding the literal `human` keeps the signal to autonomous agents. No
     opt-in flag and **no scope enforcement** (the dashboard is unscoped).
  7. **Dashboard hash routing with the category overview as the landing view.** Views are linkable
     and the browser back button works, with `route()` as the single hash→state mapper: `#/` →
     overview (category home, the landing view and empty-hash default), `#/all` → the cross-category
     board (the original behavior), `#/cat/<slug>` → a single category's board. A view change resets
     the within-view project selection and re-opens the SSE stream with the new scope. The overview
     keeps one global, unfiltered recent-activity feed; the category board scopes board/feed/stream
     via `?category=` (or `?project=` when one project is selected). All new DOM is built with
     `el()`/`textContent` (no `innerHTML`), so the existing `TestDashboardNoXSSSinks` guard covers it.
  8. **No-JS-runner verification stands (ADR-018).** The server surface (the `?category=` filtering,
     the augmented `/api/categories` payload, the hub fan-out) is covered by Go tests
     (`server_test.go`, `sse_test.go`, the new `hub_test.go`, `store_test.go`); the rendering
     (overview cards, hash routing, breadcrumb, per-view stream re-open) relies on
     preview/smoke + the source-level XSS-sink guard, consistent with the deliberate no-JS-runner
     decision (no npm/jsdom). The hub's membership logic was pulled into direct unit tests
     (`hub_test.go`) precisely because it is the load-bearing in-memory invariant.
- New error / kinds / schema: **none.** No new event kinds (catalog stays **21**), no new error
  codes, no new exit codes, no schema change (`currentSchemaVersion` stays **5**), no migration.
  `am wait`/`wait.go` is unchanged — the wait stream still deliberately does not narrow on a
  category scope (the category-scoped REST re-check is the authority, ADR-023); adding `?category=`
  to the wait stream was judged non-trivial and skipped per the map's "only if trivial" condition.
- Rationale: resolving the category fan-out once keeps `Broadcast` a pure in-memory check (the SSE
  hub's defining property); the NULL-project exclusion keeps a drill-down showing work *inside* the
  category while instance-wide admin events stay on the overview; folding stats into the existing
  list endpoint avoids a first-paint flash; hash routing is the minimal linkable/back-button
  mechanism with no router library; the unscoped dashboard matches the human operator's role.
- Consequences (accepted residuals — see `known-risks-and-gaps.md`): the **post-open project
  staleness window** on a category stream (closed on view change; REST is authoritative); a
  **cosmetic overview-count-debounce** can fire after navigating away — guarded by a re-check of
  `view` at fire time so it never writes to the now-hidden `#overview`; the overview's `counts`
  derivation is covered by `TestListCategoriesCounts` but the rendering is not behaviorally tested
  (no JS runner); the `/api/events`+`/api/stream` Phase Q residual is now **closed for the
  dashboard** via `?category=`, but `am wait`'s SSE stream still streams unscoped by design.
- Evidence: `cmd/am/hub.go` (`subFilter`, `subscriber.categoryID`/`projectIDs`, the Subscribe-time
  resolution, the `Broadcast` membership check + `project.created` carve-out + post-open-window
  comment); `cmd/am/store.go` (`CategoryStat`, `ListCategoriesWithStats`, `ProjectIDsInCategory`,
  the `category` parameter + NULL-project-exclusion comments on `ListEvents`/`ListEventsBefore`/
  `RecentEvents`, the `RecentEvents` `[]string` WHERE refactor); `cmd/am/server.go`
  (`handleListCategories` → `ListCategoriesWithStats`, the `?category=` plumbing in `handleEvents`,
  the resolve-once + silent-fallback in `handleStream`, `Subscribe(subFilter{…})`);
  `cmd/am/web/{index.html,app.css,app.js}` (`#overview`/`#breadcrumb`, `route`/`navigate`/
  `applyView`/`loadOverview`/`renderOverview`/`catCard`/`allCard`/`setBreadcrumb`, `viewParams`/
  `projectsInView`, the per-view stream re-open and debounced count refresh); the 8 Phase R tests
  listed in the CHANGELOG entry (`hub_test.go`, `TestEventsCategoryFilter`,
  `TestSSECategoryScopedStream`, `TestSSECategoryReconnectReplay`, `TestListCategoriesCounts`).

### ADR-029: Scope tokens — token-scope-wins in the single `scopeOf` resolution point, sha256-hash-not-plaintext storage, mint-requires-unscoped, exit-9/401 for bad tokens, no token event kind (Phase S)
- Status: Active
- Context: The agentic_brain integration (requirement R5, SHOULD) needs Phase Q's **client-asserted**
  scope (`X-Agent-Scope`, accident prevention only — any caller can forge or omit it, ADR-027) to
  become a **server-enforced** boundary: a credential the server binds to a scope, so a
  config-following agent that holds only its own token cannot act as another scope. This is the
  **fifth and final** phase of the train ADR-025 opened (after P metadata, Q scoping, R dashboard).
  Non-goals (R9, unchanged): no TLS, no users, no rate limiting; the bind never leaves `127.0.0.1`.
- Decision (the seven choices):
  1. **`scopeOf` becomes a method `(s *Server) scopeOf(r)`** so it can reach the store to resolve
     tokens while remaining the **SINGLE** reader of request scope (the ADR-027 swap-point realized).
     No handler reads `Authorization` or `X-Agent-Scope` directly. **Precedence: a bearer token WINS**
     — its server-side bound scope is authoritative and any `X-Agent-Scope` header is ignored; absent
     a token the header is the scope; absent both is the zero (unscoped) Scope. A `bearerToken(r)`
     helper extracts the token from `Authorization: Bearer <tok>` (case-insensitive scheme per
     RFC 7235), not a bespoke header.
  2. **Invalid/revoked token → new sentinel `ErrInvalidToken` ("unauthorized") → HTTP 401 → CLI exit
     9.** `ResolveToken` NEVER returns a zero (allow-everything) Scope on a miss — that would silently
     grant the unscoped boundary to a bad credential. **Exit 9 is distinct from exit 8 (out of scope)
     on purpose:** a bad credential must **hard-fail**, not be swallowed as a per-id scope-skip inside
     a bulk verb's loop. `exitCodeFor(401) == 9`; `writeErr` maps `ErrInvalidToken` →
     `401 {"error":"unauthorized"}`.
  3. **sha256 hash stored, plaintext never.** The plaintext token is `amt_` + 32 lowercase hex
     (16 bytes of `crypto/rand`, `newToken`); only `hashToken(plaintext)` (hex sha256) is stored
     (`token_hash`, UNIQUE). The plaintext is shown **once** at mint and never persisted, logged,
     listed, or printed by `whoami`. Possessing the stored hash does not let one authenticate — the
     server hashes the **presented** plaintext to compare, so a hash cannot be replayed as a
     credential. The token id is `tk_` + 16 hex (`newUID`).
  4. **`tokens` table via `CREATE TABLE IF NOT EXISTS`, no migration, `currentSchemaVersion` stays 5.**
     A brand-new empty table has nothing to backfill, so a migration step would only add risk — the
     `task_deps`/`task_labels`/`task_meta` precedent. Columns: `id`, `token_hash`, `category`,
     `project` (NULL = category-wide), `created_at`, `revoked_at` (NULL = active); plus
     `idx_tokens_hash`.
  5. **Mint requires UNSCOPED (the boundary crux).** The three token-admin endpoints
     (`POST/GET /api/tokens`, `POST /api/tokens/{id}/revoke`) refuse ANY request carrying a scope —
     whether an `X-Agent-Scope` header OR a (valid) bearer token — via the shared
     `(s *Server) tokenAdminGuard(w, r)` helper (`if !sc.IsZero() → denyScope`, the
     `handleCreateCategory` precedent). Only a fully unscoped caller (the human at the CLI/dashboard)
     administers tokens, so a confined agent can never mint a token for another scope. A bad bearer
     token still 401s at the guard rather than being mistaken for "unscoped".
  6. **DB export carries token hashes, deliberately un-scrubbed.** `exportDB` uses `VACUUM INTO`
     (a whole-file snapshot), so the `tokens` table rides along — acceptable precisely because only
     non-replayable sha256 hashes are stored (see choice 3). `validateImportCandidate`'s required-table
     set stays the v1 baseline (`projects, tasks, comments, events, meta`); `tokens` is NOT added —
     same treatment as `task_labels`/`task_meta`/`categories`, so a pre-Phase-S snapshot still imports
     and the table is created by `schema.sql` on the next `OpenStore`.
  7. **Validate scope at mint.** `CreateToken` rejects a token that can never match anything: the
     category must exist and be non-archived (`ErrNotFound`/`ErrCategoryArchived`); a named project
     must exist AND belong to that category (cross-category bind → `ErrValidation` → 400).
- New error / kinds / schema: **new `ErrInvalidToken` → 401, new CLI exit 9.** **No new event kind**
  — token mint/revoke deliberately emits nothing (the catalog stays **21 kinds**); audit token
  activity via `am serve --log` (the out_of_scope and request logs cover it). Keeping credential admin
  out of the unprunable activity feed avoids leaking token existence. **No schema migration**
  (`currentSchemaVersion` stays **5**). New CLI env `AGENTMAN_TOKEN`; identity-file `token` field.
- Rationale: making `scopeOf` the one place token-vs-header precedence is decided keeps every handler
  untouched (the swap-point ADR-027 reserved); storing hashes not plaintext makes a stolen DB inert
  as a credential; mint-requires-unscoped is the structural property that confines an agent (it cannot
  escalate by minting); a distinct exit 9 keeps a bad credential from being silently tolerated by the
  bulk-verb scope-skip semantics.
- Consequences / residuals (accepted — see `known-risks-and-gaps.md`): this is **loopback-only with
  no users** — a process that can read an identity file holds the token and can act as that scope, so
  Phase S is **not** protection against arbitrary filesystem read; it upgrades R4's caveat from "any
  header" to "a server-minted, scope-bound, revocable credential" but does **not** fully close it.
  Revocation is immediate (`ResolveToken` checks `revoked_at` every request) but coarse — no
  expiry/rotation (matches the SHOULD scope). Token-hash lookup is not constant-time (a non-issue at
  loopback scale; constant-time over a DB lookup is infeasible). `am init` after a `token new` could
  overwrite the token field (re-mintable, accepted). The full remote/multi-user auth+TLS project
  (Phase G) stays parked.
- Evidence: `cmd/am/store.go` (`Token`/`Token.Scope`, `ErrInvalidToken`, `CreateToken`/`ListTokens`/
  `RevokeToken`/`ResolveToken`, `newToken`/`hashToken`); `cmd/am/schema.sql` (`tokens` +
  `idx_tokens_hash`); `cmd/am/server.go` (`(s *Server) scopeOf`/`bearerToken`, `tokenAdminGuard`,
  `handleCreateToken`/`handleListTokens`/`handleRevokeToken`, the `/api/tokens` routes,
  `ErrInvalidToken` → 401 in `writeErr`); `cmd/am/client.go` (`token` field, `Authorization: Bearer`
  send + `X-Agent-Scope` drop, `exitCodeFor(401)==9`, `doOrFail` 9 case); `cmd/am/identity.go`
  (`identityRecord.Token`, `resolveToken`, `AGENTMAN_TOKEN`, `whoami` `token: set`); `cmd/am/cli.go`
  (`cmdToken`, `storeToken`); the 17 Phase S tests across `store_test.go`/`server_test.go`/
  `cli_test.go`/`db_test.go` (listed in the CHANGELOG entry).

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
