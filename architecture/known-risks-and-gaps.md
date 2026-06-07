# Known Risks and Gaps

Centralized uncertainty. Severity is the author's judgment for the project's stated scope
(a personal, localhost, agent-driven board). Cross-references point to the detailed doc.

## Architecture Risks

- **Schema-migration runner: forward-only, no rollback path** (was High → now Low). The forward-only
  runner that reads/bumps `meta.schema_version` (ADR-010) is now exercised end-to-end: Phase 2 shipped
  the first real step (`ALTER TABLE projects ADD COLUMN archived_at TEXT` at `version: 2`,
  `currentSchemaVersion = 2`), applied with its version bump in one tx and covered by tests. Residual:
  no down-migrations; a DB recorded at a version newer than the binary is accepted leniently (its
  unknown steps are simply skipped, no error). → `data-model.md`, `decision-records.md` ADR-010.
- **Single-writer throughput ceiling** (Low for stated scope). `SetMaxOpenConns(1)` serializes all
  writes; correct and simple, but caps write concurrency. → ADR-003.
- **Module boundaries are by convention only** (Medium, maintainability). One flat `main` package
  means nothing prevents SQL leaking into handlers or HTTP into the store as the codebase grows.
  → `engineering-conventions.md`.
- **Full-board re-render on each event batch** (Medium at scale). → `frontend.md` IADR-002.

## Product Risks

- ~~**No hard delete; unbounded history**~~ — **RESOLVED (Phase C1)** for hard deletes. Hard-delete
  endpoints now exist: `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`, and
  `DELETE /api/projects/{slug}` (cascade via FK: project→tasks→comments). CLI: `am rm <id>` and
  `am project rm <slug> --yes`. Residuals (Low):
  - **`ref` reuse** — the global `tasks.id` never reuses, but a per-project human `ref` (e.g. `web-3`)
    can be reused if the highest-numbered task is deleted and a new task is then created (accepted,
    no counter/migration added).
  - **Deleted-project events reappear in the unfiltered feed** — because the archived-event filter is
    `LEFT JOIN projects … p.archived_at IS NULL`, and a deleted project has no row (JOIN yields NULL,
    treated as "not archived"). The `project.deleted` event and the deleted project's earlier history
    remain visible in the feed (good for an audit trail; see `data-model.md`).
  - **`events` growth is now partially bounded** — **Phase C2 shipped**: backward cursor pagination
    (`GET /api/events?before=<id>`, `ListEventsBefore`; dashboard "Load older activity" button) and
    offline retention (`am db prune (--before <YYYY-MM-DD> | --keep <N>)`, events-only, refuses while
    a server is running). Residuals (Low):
    - `am db prune` is **manual and offline** — no automated compaction; a long-running instance
      still grows until an operator runs it.
    - The `isServerRunning` offline guard checks `AGENTMAN_URL` (default `http://127.0.0.1:8787`). A
      server running on a non-default port with `AGENTMAN_URL` unset/mismatched would **not** be
      detected, so `am db import` and `am db prune` could run against a live DB. The documented
      instruction is "stop `am serve` first"; the guard is bypassable on non-default ports.
    - The dashboard's paginated feed (`feedPaginated = true`) **disables `trimFeed`** — a
      long-running tab that has clicked "Load older" can grow the feed unbounded until the next reload.
  - **`comments` growth is still unbounded** — comments are only removed individually via the
    hard-delete endpoint (no bulk prune). The dashboard caps render but not DB storage.
  Residual (Low) from earlier: the live SSE broadcast (`hub.Broadcast`) is not archive-filtered —
  an event on a project archived after the SSE connection was opened can flash transiently in the
  feed until the next `ListEvents` reload filters it out. → `data-model.md`.
- **Identity collisions in one directory** (Low). Two agents in the same working dir share the
  per-dir identity unless one sets `AGENTMAN_AGENT`. → ADR-008.
- **Update bootstrap** (Low). A machine must do one manual `go install …@latest` to get a binary
  that *has* `am update`/the startup check; only then is self-update available. → `README.md`.

## Dependency Feature Design Residuals

- **Same-project-only constraint** (Low, by design). `AddDep` rejects cross-project dependency
  edges (`ErrValidation`). Cross-project orchestration must be done at the agent level (e.g. poll
  the prerequisite task and only then claim the dependent). This was an intentional simplification
  (avoids cross-project cascade and visibility questions).
- **Ready/Blocked are derived, not stored statuses** (Low, by design). There is no `ready` or
  `prereq_blocked` value in `tasks.status`; the counts (`nprereq`, `nopen`) are computed via
  subqueries in `ListTasks` and `GetTask`. The `?ready=`/`?blocked=` query params are filter-only.
  This is correct but means a "blocked by deps" task can simultaneously have status `todo` — agents
  should use `am ls --ready` to find actionable work rather than `--status todo`.
- **Dependency UI is untested at the behavioral level** — the prereq chips, add-prereq dropdown,
  and blocks list in the task modal are vanilla JS covered only by the `TestDashboardNoXSSSinks`
  source-level guard. See Testing Gaps below.
- **Dependency-graph overlay is untested at the behavioral level** (same gap, by design). The
  overlay JS (layout, pan/zoom, transitive highlight, detail panel, live refresh) is covered only
  by the `TestDashboardNoXSSSinks` source-level XSS guard. No JS test runner (ADR-018). See
  Testing Gaps below.
- **Graph layout is a simplified layered algorithm** (Low). `computeGraphLayout` uses topological
  longest-path / Kahn's layering with no crossing-minimization. For modest project sizes this is
  clean and fast; for large, dense DAGs edge crossings can accumulate. Mitigated by pan/zoom and
  the separate isolated-task grid lane. Acceptable for a personal board's scale.

## Security Risks

(Full detail in `security.md`.)
- **No authentication/authorization** (by design for loopback; High if the bind is ever widened).
- ~~No CSRF / DNS-rebinding protection~~ — **mitigated in Phase 0** (Host allowlist + write-CSRF
  guard, ADR-011). Residual (Low): not auth — any local non-browser process is still trusted; reads
  are not CSRF-gated.
- **No TLS, no rate limiting** (Medium if exposed).
- ~~**500 responses leak raw error strings** (Low)~~ — **RESOLVED (Phase D1)**. `writeErr`'s default branch now returns `{"error":"internal"}` and logs the real error server-side; no internal detail reaches the client.
- ~~**No dependency vulnerability scanning** (Medium, unmonitored)~~ — **mitigated (Phase F)**.
  `govulncheck ./...` now runs in CI on every push/PR; it blocks on **reachable** vulnerabilities.
  Current state: **0 reachable vulnerabilities**. One known non-blocking advisory:
  - **`GO-2026-5024`** — integer overflow in `golang.org/x/sys@v0.42.0`
    (`windows.NewNTUnicodeString`). **Windows-only; not reachable from agentman** (govulncheck's
    symbol/package scan finds nothing; module-level hit only). Transitive dep via `modernc.org/libc`.
    Clears by upgrading `golang.org/x/sys` to ≥ v0.44.0 if ever desired. Does not affect CI.
- **Spoofable audit actor** (Low) — `events.actor` comes from the unauthenticated `X-Agent` header.

## Testing Gaps

- Coverage now spans store/server/migrate/db/cli/sse/identity/web tests (9 files, 95 tests,
  `-race`-clean): the **atomic claim** (race, `-race`-clean), events cursor, store CRUD/validation,
  validation→status mapping, the Host/CSRF/CSP guards, project archive/unarchive (store round-trip
  + idempotency and the HTTP endpoints incl. 404), the v2 migration (adds `archived_at` +
  apply/bump/idempotency/rollback), DB export/import (roundtrip+perms, backup creation, garbage
  rejection, liveness probe), **feed hiding of archived-project events**
  (`TestFeedHidesArchivedProjectEvents`), **task creation into an archived project**
  (`TestCreateTaskRejectsArchivedProject` store, `TestCreateTaskIntoArchivedProject400` HTTP),
  **hard deletes** (`TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
  `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades` in `store_test.go`;
  `TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`, `TestDeleteCommentEndpoint` in
  `server_test.go`), **events backward pagination** (`TestListEventsBefore` in `store_test.go`;
  `TestEventsBeforeEndpoint` in `server_test.go`), **events prune** (`TestPruneEventsKeep`,
  `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary` in `db_test.go`), and Phase D's
  `TestWriteErrHidesInternalDetail`, `TestRequestLoggerPassesThrough`,
  `TestRequestLoggerPreservesFlusher` (in `server_test.go`).
  Phase E added:
  - **CLI verbs + exit codes** (`cli_test.go`) — `cmdNew`, `cmdLs`, mutations (`cmdStatus`/
    `cmdNote`/`cmdDrop`) silent-on-success, and the `doOrFail` exit-code mapping (3/4/5/6); plus
    table tests for `parse`/`Args` and formatters (`taskLine`/`statusShort`/`assignee`/`trunc`/
    `apiErr`).
  - **SSE streaming + reconnect** (`sse_test.go`) — `TestSSEDeliversLiveEvent` and
    `TestSSEReplayOnReconnect` (gap-replay with dedupe; every replayed id > resume cursor).
  - **Identity** (`identity_test.go`) — `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` env
    override, `sanitizeType` table, `newIdentity` format.
  - **Dashboard XSS-sink guard** (`web_test.go`) — `TestDashboardNoXSSSinks` reads the embedded
    `web/app.js` + `web/index.html` via `webFS` and asserts no `.innerHTML`/`.outerHTML`/
    `.insertAdjacentHTML`/`document.write`/`eval(` appear — a source-level lock on the
    `el()`/`textContent` convention.
  The +24 dependency tests added in that phase cover: cycle/self/cross-project rejection,
  idempotent add/remove, cascade on task delete, `NPrereqs`/`NOpenPrereqs` counts, `Ready`/`Blocked`
  list filters, hard-block on `ClaimTask` and `PatchTask` (409 `blocked`), HTTP add/remove dep
  endpoints, `?ready=`/`?blocked=` query params, and a fresh-DB table-existence check.
  The +4 graph tests cover: `TestProjectGraph` (store: correct nodes + edges shape),
  `TestProjectGraphMissingProject` (store: `ErrNotFound`), `TestProjectGraphEndpoint` (HTTP 200
  with correct shape), `TestProjectGraphEndpoint404` (HTTP 404 for missing project).
  **Still untested:** behavioral dashboard JS — the "Manage projects" modal, the delete confirm
  flows (task/comment/project), the feed pagination button, the dependency section UI (prereq chips,
  add-prereq dropdown, blocks list), the **graph overlay** (layout, pan/zoom, transitive highlight,
  detail panel, live refresh), and other client-side logic — because the project deliberately
  adopts **no JS test runner** (preserves the no-npm/single-binary ethos; ADR-018). The
  `web_test.go` sink guard mitigates XSS regressions at the source level; the dependency UI and
  the graph overlay are additional un-runner-tested JS covered by that same guard.
  → `backend.md`, `frontend.md`, `decision-records.md` ADR-018, ADR-021.

## Documentation Gaps

- ~~No CI to enforce doc/code sync~~ — **RESOLVED (Phase F)**. CI (`.github/workflows/ci.yml`)
  runs build/vet/gofmt/test(-race)/JS-syntax/govulncheck on every push to `main` and on every PR.
  Drift is still possible for prose docs, but code/format/test regressions are now gated.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".
- ~~No CHANGELOG despite tagged releases~~ — **added** (`CHANGELOG.md`, Keep a Changelog format);
  `v0.1.0`–`v0.3.0` predate it and are summarized there.
- ~~Roadmap items live only in conversation~~ — **captured** in `ROADMAP.md` (phased, checkable plan
  for the gaps in this doc). Keep the two in sync as items land.

## Maintainability Concerns

- ~~`gofmt -l` is non-empty~~ — **fixed in Phase 0** (`cmd/am/update_test.go`, `cmd/am/version.go`
  formatted; `gofmt -l cmd/am` is now empty).
- `store.go` (~1292 lines) and `app.js` (~1696 lines) are the largest files and mix several
  responsibilities; fine now, watch for growth.
- No linter beyond `gofmt`/`go vet`; no pre-commit hooks. CI now enforces `gofmt`/`go vet`/
  `go test -race`/`govulncheck` on every push and PR, so format/vet/test drift is caught
  automatically. Pre-commit hooks are still absent (local runs remain manual).

## Scalability Concerns

- Single SQLite file + single writer + full-board re-render → designed for a personal board, not a
  large team or thousands of tasks. No pagination on most reads (list capped only by `limit`/`tail`
  params and a client-side "Done" cap of 50).
- `events` table is append-only; offline pruning via `am db prune` is now available but is
  **manual** — long-running instances still grow unless an operator prunes periodically.
- `comments` table has no bulk prune; individual comments are deleted via the hard-delete endpoint.
- Backup/restore now exists via `am db export` / `am db import` / `am db prune` (all CLI-only);
  `import` and `prune` refuse while a server is running, so they require stopping `am serve` first.

## Unknowns

- Intended scale (concurrent agents, task volume) — undocumented.
- Whether multiple `am serve` processes are ever meant to share one DB (single-writer implies no).
- PR/review/branching process (single-maintainer repo; CI now gates pushes/PRs via GitHub Actions,
  but a formal branching/review policy is still undocumented).
- Target OS/arch matrix for releases (cross-compiles cleanly, but no release matrix is defined).

## Recommended Follow-Ups

1. **Add behavioral tests** for the atomic claim, validation/status mapping, and an XSS regression
   (`net/http/httptest` + a temp DB). Highest risk-reduction per effort.
2. **Decide & document the migration strategy** before any schema change to an existing table.
3. ~~**Add `go vet` + `go test` + `gofmt` + `govulncheck` CI**~~ — **DONE (Phase F)**. CI runs
   build/vet/gofmt/test(-race)/JS-syntax/govulncheck on push to `main` and PRs. (`/.github/workflows/ci.yml`)
4. **Run `gofmt -w cmd/am`** to clear current formatting drift (as its own small change).
5. **Record the missing decisions** (auth, testing, deletes, CI, versioning) as ADRs once chosen.
6. If remote access is ever wanted, treat it as an **auth + CSRF/`Host` + TLS** project per
   `security.md`, not a feature add-on.
