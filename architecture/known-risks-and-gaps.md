# Known Risks and Gaps

Centralized uncertainty. Severity is the author's judgment for the project's stated scope
(a personal, localhost, agent-driven board). Cross-references point to the detailed doc.

## Architecture Risks

- **Schema-migration runner: foundation landed, unexercised** (was High → now Low/Medium). Phase 0
  added a forward-only runner that reads/bumps `meta.schema_version` (ADR-010), but `schemaMigrations`
  is empty, so the additive-column path is unproven until Phase 2. Residual: no down-migrations; a DB
  newer than the binary is accepted silently. → `data-model.md`, `decision-records.md` ADR-010.
- **Single-writer throughput ceiling** (Low for stated scope). `SetMaxOpenConns(1)` serializes all
  writes; correct and simple, but caps write concurrency. → ADR-003.
- **Module boundaries are by convention only** (Medium, maintainability). One flat `main` package
  means nothing prevents SQL leaking into handlers or HTTP into the store as the codebase grows.
  → `engineering-conventions.md`.
- **Full-board re-render on each event batch** (Medium at scale). → `frontend.md` IADR-002.

## Product Risks

- **No delete/archival** (Medium). No API to delete a project/task/comment; `events`/`comments`
  grow unbounded. Operators must edit the DB file directly. → `data-model.md`.
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

- Phase 0 added store/server/migrate tests: the **atomic claim** (race, `-race`-clean), events
  cursor, store CRUD/validation, validation→status mapping, and the new Host/CSRF/CSP guards are now
  covered. **Still untested:** SSE streaming/reconnect, the CLI commands, identity, and the entire
  dashboard (no JS test runner). → `backend.md`, `frontend.md`. Next highest-value: an XSS regression
  test for the dashboard and CLI-path tests.

## Documentation Gaps

- No CI to enforce doc/code sync, so drift is possible. → `architecture/README.md`.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".
- No CHANGELOG despite tagged releases (`v0.1.0`–`v0.3.0`).
- Roadmap items (auth, remote access, prebuilt binaries, labels/due-dates) live only in
  conversation, not the repo — unconfirmed.

## Maintainability Concerns

- ~~`gofmt -l` is non-empty~~ — **fixed in Phase 0** (`cmd/am/update_test.go`, `cmd/am/version.go`
  formatted; `gofmt -l cmd/am` is now empty).
- `store.go` (~691 lines) and `app.js` (~591 lines) are the largest files and mix several
  responsibilities; fine now, watch for growth.
- No linter beyond `gofmt`/`go vet`; no pre-commit hooks.

## Scalability Concerns

- Single SQLite file + single writer + full-board re-render → designed for a personal board, not a
  large team or thousands of tasks. No pagination on most reads (list capped only by `limit`/`tail`
  params and a client-side "Done" cap of 50).
- `events` table is append-only with no retention — long-running instances grow indefinitely.

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
