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
  - **`events`/`comments` growth is still unbounded** — C2 (events pagination + retention/prune) is
    pending. The dashboard caps render but not DB storage.
  Residual (Low) from earlier: the live SSE broadcast (`hub.Broadcast`) is not archive-filtered —
  an event on a project archived after the SSE connection was opened can flash transiently in the
  feed until the next `ListEvents` reload filters it out. → `data-model.md`.
- **Identity collisions in one directory** (Low). Two agents in the same working dir share the
  per-dir identity unless one sets `AGENTMAN_AGENT`. → ADR-008.
- **Update bootstrap** (Low). A machine must do one manual `go install …@latest` to get a binary
  that *has* `am update`/the startup check; only then is self-update available. → `README.md`.

## Security Risks

(Full detail in `security.md`.)
- **No authentication/authorization** (by design for loopback; High if the bind is ever widened).
- ~~No CSRF / DNS-rebinding protection~~ — **mitigated in Phase 0** (Host allowlist + write-CSRF
  guard, ADR-011). Residual (Low): not auth — any local non-browser process is still trusted; reads
  are not CSRF-gated.
- **No TLS, no rate limiting** (Medium if exposed).
- **500 responses leak raw error strings** (Low).
- **No dependency vulnerability scanning** (Medium, unmonitored) — run `govulncheck ./...` manually.
- **Spoofable audit actor** (Low) — `events.actor` comes from the unauthenticated `X-Agent` header.

## Testing Gaps

- Coverage now spans store/server/migrate/db tests: the **atomic claim** (race, `-race`-clean), events
  cursor, store CRUD/validation, validation→status mapping, the Host/CSRF/CSP guards, project
  archive/unarchive (store round-trip + idempotency and the HTTP endpoints incl. 404), the v2 migration
  (adds `archived_at` + apply/bump/idempotency/rollback), DB export/import (roundtrip+perms, backup
  creation, garbage rejection, liveness probe), **feed hiding of archived-project events**
  (`TestFeedHidesArchivedProjectEvents`), **task creation into an archived project**
  (`TestCreateTaskRejectsArchivedProject` store, `TestCreateTaskIntoArchivedProject400` HTTP), and
  **hard deletes** (`TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
  `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades` in `store_test.go`;
  `TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`, `TestDeleteCommentEndpoint` in
  `server_test.go`) are all covered. **Still untested:** SSE streaming/reconnect, identity, most CLI
  command paths, and the entire dashboard — including the "Manage projects" modal and the new delete
  confirm flows (task/comment/project) — as no JS test runner exists.
  → `backend.md`, `frontend.md`. Next highest-value: an XSS regression test for the dashboard and
  CLI-path tests.

## Documentation Gaps

- No CI to enforce doc/code sync, so drift is possible. → `architecture/README.md`.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".
- ~~No CHANGELOG despite tagged releases~~ — **added** (`CHANGELOG.md`, Keep a Changelog format);
  `v0.1.0`–`v0.3.0` predate it and are summarized there.
- ~~Roadmap items live only in conversation~~ — **captured** in `ROADMAP.md` (phased, checkable plan
  for the gaps in this doc). Keep the two in sync as items land.

## Maintainability Concerns

- ~~`gofmt -l` is non-empty~~ — **fixed in Phase 0** (`cmd/am/update_test.go`, `cmd/am/version.go`
  formatted; `gofmt -l cmd/am` is now empty).
- `store.go` (~850 lines) and `app.js` (~615 lines) are the largest files and mix several
  responsibilities; fine now, watch for growth.
- No linter beyond `gofmt`/`go vet`; no pre-commit hooks.

## Scalability Concerns

- Single SQLite file + single writer + full-board re-render → designed for a personal board, not a
  large team or thousands of tasks. No pagination on most reads (list capped only by `limit`/`tail`
  params and a client-side "Done" cap of 50).
- `events` table is append-only with no retention — long-running instances grow indefinitely.
- Backup/restore now exists via `am db export` / `am db import` (CLI-only, `VACUUM INTO` snapshot);
  import still refuses while a server is running, so it requires stopping `am serve` first.

## Unknowns

- Intended scale (concurrent agents, task volume) — undocumented.
- Whether multiple `am serve` processes are ever meant to share one DB (single-writer implies no).
- PR/review/branching process (single-maintainer repo, no CI).
- Target OS/arch matrix for releases (cross-compiles cleanly, but no release matrix is defined).

## Recommended Follow-Ups

1. **Add behavioral tests** for the atomic claim, validation/status mapping, and an XSS regression
   (`net/http/httptest` + a temp DB). Highest risk-reduction per effort.
2. **Decide & document the migration strategy** before any schema change to an existing table.
3. **Add `go vet` + `go test` + `gofmt` + `govulncheck` CI** (no `.github/` exists) to stop drift.
4. **Run `gofmt -w cmd/am`** to clear current formatting drift (as its own small change).
5. **Record the missing decisions** (auth, testing, deletes, CI, versioning) as ADRs once chosen.
6. If remote access is ever wanted, treat it as an **auth + CSRF/`Host` + TLS** project per
   `security.md`, not a feature add-on.
