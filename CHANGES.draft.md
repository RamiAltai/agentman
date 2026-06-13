# Phase S — Scope tokens (R5, SHOULD)

This file feeds the docs-sync stage. It records every externally visible change,
the design decisions + rationale, deviations, and tests, so docs can be updated
without re-reading the diff.

## What shipped

Scope tokens turn Phase Q's client-asserted scope (accident prevention) into a
real server-enforced boundary: a token is bound server-side to a scope, the
server derives the scope from the token, and a config-following agent that holds
only its own token cannot forge another scope's token. No TLS, no users, no rate
limiting; the loopback (127.0.0.1) bind is unchanged (R9 non-goals).

## Externally visible changes

### CLI

- `am token new --scope <category[/project]>` — mints a scope-bound bearer token.
  - Prints the plaintext token on **stdout line 1** (so `tok=$(am token new --scope work)` works).
  - Prints a one-line hint on **stderr**: `stored in identity; sent as Authorization: Bearer on future requests`.
  - Merges the token into this directory's identity file (preserves the existing
    agent id and scope; does not clobber them).
  - No `--scope` → exit 5 (validation).
- `am token ls [--json]` — lists tokens as `id  scope  created  [revoked]` columns
  (or raw JSON with `--json`). Never shows the plaintext or the hash.
- `am token revoke <id>` — revokes a token; silent success. Unknown id or an
  already-revoked token → exit 3 (not found).
- `am whoami` — prints `token: set` when a token is configured (never the value).
  The agent id stays line 1; `scope:` line is unchanged.

### Exit codes

- **New exit code `9` = bad token** (invalid or revoked). Distinct from `8`
  (out of scope) on purpose: a bad credential must hard-fail, not be swallowed as
  a per-id scope-skip inside a bulk loop. The usage() exit-codes line now ends
  `· 8 out of scope · 9 bad token`.

### HTTP API

- `POST /api/tokens` — mint. Body `{"scope":"cat[/proj]"}`. **Requires an unscoped
  caller** (see mint-requires-unscoped below). Response `201 {"id","scope","token","created_at"}`
  — the **only** response that ever carries the plaintext `token`.
- `GET /api/tokens` — list. Requires unscoped. Returns `[]Token` — `{id, category,
  project?, created_at, revoked_at?}`. Never returns `token` or `token_hash`.
- `POST /api/tokens/{id}/revoke` — revoke. Requires unscoped. `200` with the token
  metadata; `404` for unknown/already-revoked.
- New error: invalid/revoked bearer token → `401 {"error":"unauthorized"}` on ANY
  endpoint (it is `scopeOf` that surfaces it, so every handler inherits it).

### Transport / precedence

- The CLI sends `Authorization: Bearer <token>` when a token is present and
  **stops sending `X-Agent-Scope`** (the server ignores the header when a token
  is present anyway; dropping it keeps the wire honest about scope provenance).
- Token scope **overrides** the `X-Agent-Scope` header: when a bearer token is
  present, its server-side bound scope is authoritative and any header scope is
  ignored.

### Identity file / env

- The per-directory identity JSON gains an optional `token` field
  (`{"agent","scope","token"}`).
- New env override `AGENTMAN_TOKEN` (mirrors `AGENTMAN_AGENT` / `AGENTMAN_SCOPE`):
  overrides the identity file's token.

### Schema

- New `tokens` table via `CREATE TABLE IF NOT EXISTS` in `schema.sql` (plus
  `idx_tokens_hash`). **No migration; `currentSchemaVersion` stays 5** — a fresh
  table needs no backfill, and pre-existing DBs gain it on the next `OpenStore`.
- Columns: `id` (`tk_<16 hex>`), `token_hash` (sha256 of plaintext, hex, UNIQUE),
  `category`, `project` (NULL = category-wide), `created_at`, `revoked_at`
  (NULL = active).

### Token format / hashing

- Plaintext token: `amt_` + 32 lowercase hex (16 bytes of `crypto/rand`).
- Token id: `tk_` + 16 hex (reuses `newUID`).
- Only the `sha256` hash is stored; the plaintext is shown once at mint and never
  persisted, logged, listed, or printed by whoami.

### No new event kind

- Token mint/revoke deliberately emits **no event** — the event-kind catalog stays
  at 21 kinds. Audit token activity via `am serve --log` if wanted (the
  out_of_scope and request logs cover it). The activity feed is task/project/
  category lifecycle, not credential admin.

## Design decisions + rationale (the 7 decisions)

1. **`scopeOf` becomes a method `(s *Server) scopeOf(r)`** so it can reach the
   store to resolve tokens, while remaining the SINGLE reader of request scope.
   No handler reads `Authorization` or `X-Agent-Scope` directly. Token scope wins;
   absent token falls back to the header; absent both is the zero (unscoped) scope.
2. **Token transport is `Authorization: Bearer <tok>`** (standard, case-insensitive
   scheme match per RFC 7235), not a bespoke header.
3. **Invalid/revoked token → new sentinel `ErrInvalidToken` ("unauthorized") →
   HTTP 401 → exit 9.** `ResolveToken` NEVER returns a zero (allow-everything)
   Scope on a miss — that would silently grant the unscoped boundary to a bad
   credential. The error is mandatory and security-critical.
4. **`tokens` table via `CREATE TABLE IF NOT EXISTS`, no migration, version stays 5.**
   A brand-new empty table has nothing to backfill, so a migration step would only
   add risk.
5. **Mint requires UNSCOPED** (the boundary crux): all three token-admin endpoints
   refuse ANY request carrying a scope — whether an `X-Agent-Scope` header OR a
   (valid) bearer token. Only a fully unscoped caller (the human at the CLI /
   dashboard) administers tokens. This reuses the `handleCreateCategory`
   precedent (`if !sc.IsZero() → denyScope`). A bad bearer token still 401s at the
   guard rather than being mistaken for unscoped. So a confined agent can never
   mint a token for another scope.
6. **sha256 hash stored, plaintext never.** Possessing the stored hash does not let
   one authenticate: the server hashes the presented plaintext to compare, so a
   hash cannot be replayed as a credential.
7. **Validate scope at mint:** category must exist and be non-archived; a named
   project must exist AND belong to that category (cross-category bind → 400
   validation). No point minting a token that can never match anything.

## Risks / honesty notes (the 6 risks)

1. **Filesystem-read honesty note (R4):** a process that can read an identity file
   still holds that token and can act as that scope. The boundary Phase S provides
   is narrower and precise: *a config-following agent that cannot forge another
   scope's token is confined to its own scope.* It is NOT protection against an
   attacker with arbitrary filesystem read. This is the same class of caveat as
   security.md's X-Agent note, now upgraded from "any header" to "a server-minted,
   scope-bound, revocable credential."
2. **No TLS / loopback only:** tokens travel in cleartext over the 127.0.0.1 loop;
   acceptable because the bind never leaves loopback (R9). A token is not a
   network-facing secret.
3. **DB export carries token hashes** (see below) — acceptable because hashes are
   not replayable credentials.
4. **Revocation is immediate but coarse:** `ResolveToken` checks `revoked_at` on
   every request, so a revoked token fails at once; there is no expiry/rotation,
   matching the SHOULD scope.
5. **No event audit trail for token admin** (decision 8 above) — intentional;
   `--log` is the audit path.
6. **uid/token collision** is handled by the same retry-on-`isUniqueErr` pattern
   as category/project uids (astronomically unlikely at 16 bytes of entropy).

## DB export/import (resolved deliberately)

`exportDB` uses `VACUUM INTO`, a whole-file snapshot, so the `tokens` table rides
along automatically. **This is acceptable because only sha256 hashes are stored**
— a hash cannot be presented as a credential (the server hashes the presented
plaintext to compare, so possessing a hash does not let you authenticate). We did
**not** add any scrubbing complexity.

`validateImportCandidate`'s required-table set stays the v1 baseline
(`projects, tasks, comments, events, meta`); the `tokens` table is NOT added to it
— same treatment as `task_labels`, `task_meta`, `categories`. A pre-Phase-S
snapshot (no tokens table) still imports, and the table is created by `schema.sql`
on the next `OpenStore`. Confirmed by a round-trip test (export → validate →
import → token still resolves).

## Deviations from the implementation map

- None of substance. One naming choice: the shared mint-requires-unscoped guard is
  factored into a small helper `(s *Server) tokenAdminGuard(w, r) bool` (rather than
  inlining the `scopeOf`/`denyScope` block three times). It mirrors the
  `handleCreateCategory` precedent exactly and keeps the three handlers terse.

## Tests (per file)

### cmd/am/store_test.go
- `TestCreateToken_HashNotPlaintext` — DB `token_hash` != plaintext, == `hashToken(plaintext)`;
  `ListTokens` rows carry no hash/plaintext; id/plaintext format.
- `TestResolveToken` — mint→resolve returns bound scope; unknown → `ErrInvalidToken`;
  revoke-then-resolve → `ErrInvalidToken` and a zero scope.
- `TestCreateToken_ScopeValidation` — unknown category (`ErrNotFound`); unknown
  project (`ErrNotFound`); cross-category project bind (`ErrValidation`); archived
  category (`ErrCategoryArchived`).
- `TestRevokeToken` — unknown id → `ErrNotFound`; double-revoke → `ErrNotFound`.
- Helper `seedScopeWorld`.

### cmd/am/server_test.go
- `TestTokenAdmin_RequiresUnscoped` — POST/GET/revoke with X-Agent-Scope → 403;
  with a scoped bearer token → 403; unscoped → 201/200.
- `TestTokenScopeOverridesHeader` — bearer work-token + `X-Agent-Scope: personal`:
  claim work succeeds, claim personal → 403 (token wins, header does not widen).
- `TestInvalidTokenRejected` — bogus bearer → 401 (not 200, not 403); revoked → 401;
  DELETE with bogus token is 401 NOT 204 (security-regression guard).
- `TestNoTokenPathUnchanged` — no Authorization: header scope behaves as Phase Q
  (one in-scope 200 + one out-of-scope 403).
- `TestTokenScopeMatrix` — re-verifies the Q matrix driven by a minted token:
  next never returns out-of-scope; claim out-of-scope 403; steal-stale out-of-scope
  403; bulk per-id (in-scope patch 200, out-of-scope 403); proposals carve-out
  (create allowed, comment-own allowed, other create 403).
- `TestCreateTokenResponse` — 201 body has the plaintext `token` once; `GET /api/tokens`
  raw JSON never contains `token` or `token_hash`.
- Helpers `mintToken`, `bearer`.

### cmd/am/cli_test.go
- `TestCmdTokenNewWritesIdentity` — `am token new --scope work` (unscoped caller)
  writes the token into the identity file (JSON `token` field) while preserving the
  file's existing agent/scope; prints plaintext on stdout line 1.
- `TestCmdTokenNewRequiresScope` — no `--scope` → exit 5.
- `TestWhoamiPrintsTokenSet` — prints `token: set`, never the value.
- `TestClientSendsBearerNotScope` — with a token set, `do()` sends
  `Authorization: Bearer` and omits `X-Agent-Scope`.
- `TestExitCodeForUnauthorized` — `exitCodeFor(401) == 9`.
- `TestDoOrFailUnauthorized` — a 401 response → `doOrFail` exits 9.

### cmd/am/db_test.go
- `TestExportImportWithTokens` — a DB with a token round-trips through
  export/import; `validateImportCandidate` unchanged; token still resolves after
  import.
