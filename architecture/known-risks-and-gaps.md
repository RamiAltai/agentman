# Known Risks and Gaps

Centralized uncertainty. Severity is the author's judgment for the project's stated scope
(a personal, localhost, agent-driven board). Cross-references point to the detailed doc.

## Architecture Risks

- **No schema-migration path** (High). `OpenStore` runs `CREATE TABLE IF NOT EXISTS` only;
  `meta.schema_version` is written but never read. Any change to an existing table won't apply to
  existing DBs. → `data-model.md`, `decision-records.md` IADR-003.
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
- **No CSRF / DNS-rebinding (`Host` allowlist) protection** (Medium) — a malicious website can drive
  the loopback API, and because agents act on tasks this is an **agent-injection** vector.
- **No TLS, no rate limiting** (Medium if exposed).
- **500 responses leak raw error strings** (Low).
- **No dependency vulnerability scanning** (Medium, unmonitored) — run `govulncheck ./...` manually.
- **Spoofable audit actor** (Low) — `events.actor` comes from the unauthenticated `X-Agent` header.

## Testing Gaps

- Only `cmd/am/update_test.go` exists (3 tests, version logic). **Untested:** every HTTP handler,
  the store, the **atomic claim** (the project's most important invariant), SSE/reconnect, the CLI,
  identity, and the entire dashboard. → `backend.md`, `frontend.md`. Highest-value additions:
  atomic-claim race, validation/status-code mapping, XSS regression.

## Documentation Gaps

- No CI to enforce doc/code sync, so drift is possible. → `architecture/README.md`.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".
- No CHANGELOG despite tagged releases (`v0.1.0`–`v0.3.0`).
- Roadmap items (auth, remote access, prebuilt binaries, labels/due-dates) live only in
  conversation, not the repo — unconfirmed.

## Maintainability Concerns

- **`gofmt -l` is non-empty** (Low, easy fix): `cmd/am/update_test.go` and `cmd/am/version.go`
  are unformatted as committed. Run `gofmt -w cmd/am`.
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
