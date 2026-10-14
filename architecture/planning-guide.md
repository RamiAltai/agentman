# Planning Guide for Future Agents

Use this before writing code. The goal is to change agentman without violating its core
invariants (single-writer DB, atomic claims, localhost-no-auth, server-enforced agent scope,
XSS-safe UI, token-cheap CLI). "No auth" means no users or login: scoped agent identities and
bearer scope tokens confine agents, they do not authenticate anyone (`security.md` Authorization).

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
- [ ] Atomic claim semantics (`store.go ClaimTask` / `StealStaleClaim` / `NextTask`).
- [ ] "Event in same tx, broadcast after commit" ordering.
- [ ] The `events.id` cursor / SSE `Last-Event-ID` contract.
- [ ] The `127.0.0.1` bind / absence of auth.
- [ ] Scope enforcement: every new or changed handler resolves the caller's scope with `scopeOf` and
  gates with the matching `check*` / `narrowScope` / `unscopedGuard` helper (`server.go`); a
  deliberately unscoped read goes on the `security.md` Known coverage gaps list.
- [ ] task→project and project→category stay immutable — the scope pre-checks run outside the store
  tx and are sound only because of that (`PatchTask` / `PatchProject` scope notes). A move feature
  must move the scope check inside the tx.
- [ ] The proposals carve-out (`s.proposals`, `isProposals` / `isProposalsPair`) stays consistent
  across every check site that uses it (`checkTaskRead`, `checkComment`, `checkCreate`,
  `checkProjectRead`, `narrowScope`).
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

- [ ] Schema change? There is **no migration runner** (ADR-033) — edit `cmd/am/schema.sql` (still
  `schema_version` 1; `OpenStore` refuses any other) and add a `cmd/am/schema_test.go` case. An
  existing DB file is not altered (`CREATE TABLE IF NOT EXISTS`), so it needs a fresh DB; once a live
  schema with data to keep has to change, reintroduce migrations first.
- [ ] New columns threaded through `schema.sql` → `store.go` structs → `CreateTask`/`PatchTask`/
  `getTaskTx` → API → dashboard?
- [ ] Cascade/ownership rules considered (project→tasks→comments)?
- [ ] Does it introduce deletes? Hard deletes already exist (ADR-015) — `DELETE /api/tasks/{id}`,
  `DELETE /api/tasks/{id}/comments/{cid}`, `DELETE /api/projects/{slug}`, CLI `am rm <id>` and
  `am project rm <slug> --yes`. Match those semantics: irreversible hard delete (archive is the
  reversible path, ADR-013), cascade via the existing `ON DELETE CASCADE` FKs, insert the matching
  `*.deleted` event in the **same transaction** before the `DELETE` and broadcast after commit, and
  leave `events` rows intact as the audit log. `ref`/`id` reuse is an **accepted** residual, not an
  open question — see `known-risks-and-gaps.md` and `data-model.md`.

## Security Impact Checklist

(Run the full `security.md` "Secure Implementation Checklist".) Especially:
- [ ] Parameterized SQL only.
- [ ] No new untrusted text rendered via `innerHTML`.
- [ ] No widening of the network bind without auth + CSRF/`Host` guard + TLS.
- [ ] No new endpoint skips `scopeOf` + a scope check unless it is deliberately unscoped (and listed
  as a coverage gap in `security.md`).
- [ ] No secrets in code/logs/query strings.

## Testing Checklist

- [ ] Unit tests for new pure logic (table-driven, like `update_test.go`).
- [ ] If you touch the claim, status, or validation paths, add a behavioral test. High-value paths
  are now covered by `TestClaimRaceExactlyOneWinner` (`store_test.go`), `TestLostClaim409`
  (`server_test.go`), and the validation tests in `store_test.go` — extend these rather than
  leaving new behavior uncovered.
- [ ] If you add a new CLI command or exit-code path, test it via `captureExit`/`captureStdout`
  against a `newTestServer(t)` — see `cli_test.go` for the pattern.
- [ ] If you add new identity logic, use the `AGENTMAN_AGENT_FILE` env seam — see
  `identity_test.go`.
- [ ] Do **not** add a JS test runner. If you add DOM construction code, verify it uses `el()`/
  `textContent` only, and add the new sink pattern to `TestDashboardNoXSSSinks` in `web_test.go`
  if needed (ADR-018).
- [ ] `go vet ./...` and `go test -race ./...` pass; `gofmt -l cmd/am` empty.
  CI (`.github/workflows/ci.yml`) enforces these on every push to `main` and on every PR — passing
  locally before pushing avoids a CI round-trip.

## Approval Gates

Get explicit human approval before:
- Changing the security posture (bind address, auth, exposure).
- Any schema change on a model with existing data (migration risk).
- Adding a dependency or a build/release toolchain.
- Anything touching the atomic-claim or single-writer invariants.
- Changing scope/token behavior: scope checks or helpers, the proposals carve-out, token
  mint/resolve/revoke, or making task→project / project→category mutable.
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
