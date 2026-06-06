# Planning Guide for Future Agents

Use this before writing code. The goal is to change agentman without violating its core
invariants (single-writer DB, atomic claims, localhost-no-auth, XSS-safe UI, token-cheap CLI).

## Planning Workflow

1. Read `project-overview.md` + `system-map.md` to place the change.
2. Read the doc for the layer you'll touch (`backend.md` / `frontend.md` / `data-model.md` /
   `security.md`).
3. Identify the **invariants** the change risks (see Architecture Impact Checklist).
4. Locate exact files/symbols (use the Directory Map and route/store tables — they cite paths).
5. Write a short plan: files to touch, new endpoints/fields/events, data + security impact, tests.
6. Get approval (see Approval Gates) before large or invariant-affecting work.
7. Implement, verify, then **update the matching `architecture/` doc(s)** in the same change.

## Feature Intake Checklist

- What user/agent workflow does this serve? Does it fit the "agents drive, human watches" model?
- Is it a CLI change, an API change, a data change, a dashboard change, or several?
- Does it need a new **event kind** (anything mutating that should appear in the live feed does)?
- Is it backward-compatible for existing DBs and existing installed `am` binaries?

## Project Fit Questions

- Does it keep the CLI **token-cheap** (terse, silent success, exit codes)? If it adds chatty
  stdout, reconsider.
- Does it preserve **localhost-no-auth**? If it needs remote/multi-user, that's an auth project
  first (see `security.md`), not a feature bolt-on.
- Does it keep the **single binary / no-build-step** ethos (no npm, no new heavy deps)?

## Architecture Impact Checklist

Flag the change if it touches any of these invariants:
- [ ] DB writer count (must stay 1 — `SetMaxOpenConns(1)`).
- [ ] Atomic claim semantics (`store.go ClaimTask`).
- [ ] "Event in same tx, broadcast after commit" ordering.
- [ ] The `events.id` cursor / SSE `Last-Event-ID` contract.
- [ ] The `127.0.0.1` bind / absence of auth.
- [ ] XSS-safe DOM rendering (`el()` only).

## Frontend Impact Checklist

- [ ] Built with `el()` / `textContent` (no `innerHTML`)?
- [ ] New event kinds handled in `evKind`/`evText`/`describeText`?
- [ ] Keyboard + focus behavior preserved (modal focus trap, `[ ]`, `a`/`n`/`Esc`)?
- [ ] Responsive at mobile/tablet/desktop (manual check; no tests exist)?
- [ ] Will you **rebuild the binary** so embedded assets update?

## Backend Impact Checklist

- [ ] New route registered in `Server.Handler()` + `handleX` + `store.*` method?
- [ ] Inputs validated; errors mapped via `writeErr` (no raw 500s for expected cases)?
- [ ] Mutation emits an `events` row and broadcasts after commit?
- [ ] List endpoints stay terse; full data only on `GET /api/tasks/{id}`?

## Data Impact Checklist

- [ ] Schema change? A **forward-only migration runner exists and is exercised** (ADR-010) — append a
  `{version, apply}` step to `schemaMigrations` and raise `currentSchemaVersion` in `cmd/am/store.go`; do
  **not** rely on `CREATE TABLE IF NOT EXISTS` to alter existing tables. The shipped v2 step (the
  `ALTER TABLE projects ADD COLUMN archived_at TEXT` migration) is the template to copy. Add a migration
  test like `TestMigrationV2AddsArchivedAt` in `cmd/am/migrate_test.go`.
- [ ] New columns threaded through `schema.sql` → `store.go` structs → `CreateTask`/`PatchTask`/
  `getTaskTx` → API → dashboard?
- [ ] Cascade/ownership rules considered (project→tasks→comments)?
- [ ] Does it introduce deletes? Define semantics (none exist today) and the `ref` reuse question.

## Security Impact Checklist

(Run the full `security.md` "Secure Implementation Checklist".) Especially:
- [ ] Parameterized SQL only.
- [ ] No new untrusted text rendered via `innerHTML`.
- [ ] No widening of the network bind without auth + CSRF/`Host` guard + TLS.
- [ ] No secrets in code/logs/query strings.

## Testing Checklist

- [ ] Unit tests for new pure logic (table-driven, like `update_test.go`).
- [ ] If you touch the claim, status, or validation paths, add a behavioral test (these are
  high-value paths now covered by `TestClaimRaceExactlyOneWinner` (`store_test.go`),
  `TestLostClaim409` (`server_test.go`), and the validation tests in `store_test.go` — extend
  these rather than leaving new behavior uncovered).
- [ ] `go vet ./...` and `go test ./...` pass; `gofmt -l cmd/am` empty.

## Approval Gates

Get explicit human approval before:
- Changing the security posture (bind address, auth, exposure).
- Any schema change on a model with existing data (migration risk).
- Adding a dependency or a build/release toolchain.
- Anything touching the atomic-claim or single-writer invariants.
Small, additive, backward-compatible changes (a new optional field, a new CLI flag, a UI tweak)
can proceed with a brief plan.

## Definition of Done

- Code builds (`go build -o am ./cmd/am`), `go vet` + `go test` clean, `gofmt`-clean.
- Behavior verified manually (run `am serve` against a throwaway `--db /tmp/x.db` and exercise it).
- Embedded assets rebuilt if `web/` changed.
- User-facing docs (`README.md`/`docs/`) updated if the CLI/API/flags changed.
- The relevant `architecture/` file(s) updated.

## Documentation Update Rules

Update these when you change the matching thing:
| You changed… | Update… |
|---|---|
| Routes / handlers / request flow | `backend.md`, `system-map.md` |
| Schema / fields / relationships | `data-model.md` (+ ER diagram) |
| Dashboard UI/behavior | `frontend.md` |
| Auth / bind / validation / trust | `security.md` |
| A deliberate trade-off | add an ADR to `decision-records.md` |
| Conventions / commands | `engineering-conventions.md` |
| New risk or gap | `known-risks-and-gaps.md` |
