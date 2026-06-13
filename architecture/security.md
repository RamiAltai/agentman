# Security Architecture

> The single most important fact: **there is no authentication, and authorization is loopback-only.**
> Access control is *entirely* the `127.0.0.1` bind. Scope tokens confine a token-following
> agent server-side but are **not** auth against arbitrary local processes — any change that widens
> the bind, or adds remote access, must still add real auth first. Deliberate decisions
> (`decision-records.md` ADR-002, ADR-029).

## Authentication

**None.** `am serve` binds `127.0.0.1` (`cmd/am/main.go`, `Addr: "127.0.0.1:" + port`) and accepts
all requests. The `X-Agent` header (`server.go actorOf`) is an **actor label for attribution, not a
credential** — any caller may claim any identity, including `"human"` (the dashboard's value).

The `X-Agent-Scope` header and the **bearer token** (`Authorization: Bearer <tok>`)
are both about *scope confinement*, not authentication — there is no user to authenticate. A token
**does** let the server derive a scope it can trust against a *config-following* agent (the token is
server-minted and bound to a scope; the agent cannot forge a token for another scope), but it is not
an identity check: any process that can read the identity file holds that token. See Authorization.

## Authorization

**Scope confinement is now token-backed, but it is still loopback-only — not auth against an
arbitrary local process.** There are no users, roles, or authenticated per-resource permissions.
Anyone who can reach the port can act unscoped (read/mutate everything) by simply sending no token
and no scope header.

**The scope boundary, in three layers:**
- **`X-Agent-Scope` (client-asserted).** An agent may carry an `X-Agent-Scope` header
  (`category[/project]`), enforced on every mutation and on named reads — out-of-scope →
  `403 {"error":"out_of_scope"}` (CLI exit 8). Client-asserted: any caller can send any scope or omit
  it. Accident prevention, not a boundary against crafted HTTP.
- **Bearer tokens (server-derived).** `am token new --scope <cat[/proj]>` mints a
  scope-bound token (the human does this); the agent's CLI then sends it as `Authorization: Bearer`
  on every request. **A bearer token's scope WINS over any `X-Agent-Scope` header** — the server
  resolves the token to its bound scope and ignores the header (the CLI drops the header entirely when
  a token is set). The token is **server-minted and bound to a scope**, so a config-following agent
  that holds only its own token **cannot forge a token for another scope** — a server-minted,
  scope-bound, revocable credential. An invalid or
  revoked token → `401 {"error":"unauthorized"}` (CLI **exit 9**) on ANY endpoint — `scopeOf` surfaces
  it, never a silent fallthrough to unscoped, so a bad credential hard-fails (distinct from exit 8's
  per-id scope-skip).
- **Mint-requires-unscoped (the boundary crux).** All three token-admin endpoints
  (`POST/GET /api/tokens`, `POST /api/tokens/{id}/revoke`) refuse ANY request carrying a scope —
  header OR a valid bearer token (`tokenAdminGuard` → `403`). Only a fully **unscoped** caller (the
  human at the CLI/dashboard) administers tokens, so a confined agent can never mint a token for
  another scope.

**Token storage & transport (security-relevant):**
- **sha256 hash stored, plaintext never.** The plaintext token (`amt_` + 32 hex) is shown **once** at
  mint and never persisted, logged, listed (`am token ls` shows only `id/scope/created/[revoked]`), or
  printed by `am whoami` (`token: set`). The DB stores only `sha256(plaintext)` (`tokens.token_hash`,
  UNIQUE); the server hashes the **presented** plaintext to compare, so a stolen DB row cannot be
  replayed as a credential.
- **DB export carries hashes, non-replayable.** `am db export` (`VACUUM INTO`) snapshots the whole
  file, `tokens` included — acceptable because the hashes are not replayable (see above); no scrubbing
  was added.
- **Cleartext on loopback.** Tokens travel in cleartext over `127.0.0.1` (no TLS); acceptable because
  the bind never leaves loopback. A token is not a network-facing secret.

**Residual honesty note (the precise boundary).** The token model is **loopback-only and has no
users**: a process that can read an identity file (`~/.agentman/agents/*`) holds that token and can
act as that scope. The boundary it provides is narrow and precise — *a config-following agent that
cannot forge another scope's token is confined to its own scope.* It is **not** protection against an
attacker with arbitrary filesystem read, and it is **not** authentication; full remote/multi-user
auth+TLS is out of scope. `scopeOf(r)` in `server.go` is the single reader of request
scope (a `*Server` method so it can resolve tokens) — token-vs-header precedence lives there
alone, no handler touches it.

**Known coverage gaps (deliberate):**
- `GET /api/events` and `GET /api/stream` are **not** narrowed by `X-Agent-Scope` — a scoped agent
  can still read the global activity feed. An *unscoped* `?category=` lens serves the
  human dashboard's category drill-down (a query-param choice, not an identity scope); the agent
  `am wait` stream stays unscoped by design.
- `GET /api/projects` and `GET /api/categories` list endpoints are **not** narrowed — board
  metadata (slugs/names) is visible to any scope; task *data* is the enforcement point
  (`/api/categories` is unscoped on purpose — it serves the unscoped human dashboard).
- An explicit unknown `?project=` for a scoped agent returns 403 (not 404/empty) — the server
  cannot prove it in-scope, so it fails loud (mild existence ambiguity, accepted).

## Trust Boundaries

- **Network boundary:** loopback only. Everything that can open a socket to the port is fully
  trusted. (See `known-risks-and-gaps.md` for why "LAN-only" is *not* a strong boundary if the bind
  is ever changed to `0.0.0.0`.)
- **Content boundary:** all task/comment/title/assignee text is **agent-supplied and untrusted**.
  Treat it as hostile input wherever it's rendered or acted upon.

## Sensitive Assets

- The **SQLite DB** (`~/.agentman/agentman.db`) — contains all board content; no encryption at rest.
  It also holds the `tokens` table, but only the **sha256 hash** of each token, never
  the plaintext — a stolen DB row cannot be replayed (the server compares `hash(presented_plaintext)`).
- **Task/comment bodies** — may hold internal plans, repo names, or secrets an agent pasted.
- **Agent identity files** (`~/.agentman/agents/*`) — carry the agent label and scope (non-secret),
  **plus the agent's plaintext bearer token** (the optional `token` field). The token
  is a scope-bound credential: a process that can read the file can act as that scope. Treat the file
  as scope-sensitive (loopback-only mitigates; see the Authorization honesty note).
- No passwords or API keys are stored. The only credential at rest is the bearer **token hash**
  (sha256, non-replayable) in the `tokens` table; token plaintext lives only in the per-directory
  identity file (or the `AGENTMAN_TOKEN` env), never in the DB.

## Input Validation

- **SQL is parameterized** (`?` placeholders throughout `store.go`) → no SQL injection. **One
  deliberate exception:** `VACUUM INTO` cannot take a bind parameter for its target path (SQLite
  forbids it), so `exportDB` builds the literal by escaping single-quotes — `strings.ReplaceAll(outPath,
  "'", "''")` (`cmd/am/db.go:65-66`). The input is an operator-supplied local export path, not
  agent-supplied; reviewed and accepted.
- Status validated by `validStatus` map **and** a SQL `CHECK` constraint; empty title/slug/body
  rejected (`ErrValidation` → HTTP 400). Unknown `PATCH` fields are ignored (`PatchTask`).
- Request bodies capped at **1 MiB** (`io.LimitReader` in `server.go decode`); `ReadHeaderTimeout`
  set. `{id}` path values resolved/validated by `resolveTaskID`.
- The `?q=` search input is parameterized like everything else, run through `likeEscape` (so
  `%`/`_`/`\` can't act as LIKE wildcards) and capped at 500 bytes (→ 400); labels are validated by
  `normalizeLabel` against a strict charset (`^[a-z0-9._-]+$`, 1–50 bytes) before any SQL
  (`cmd/am/store.go`). Meta keys (incl. the `?meta_key=` filter input) go through
  `normalizeMetaKey` against the same charset; meta values are opaque but capped at 500 bytes and
  always bound as parameters.

## Output Encoding

- **Dashboard is XSS-safe by construction:** `web/app.js` builds DOM via `el()` using `textContent`
  / text nodes and **never `innerHTML`** — a task titled `<img src=x onerror=…>` renders as literal
  text.
- API returns `application/json` via `encoding/json` (auto-escaped).
- The **CLI prints server text verbatim** to the terminal — fine for terminals, but a consumer that
  renders CLI output as HTML would need to encode it (not a current concern).

## Secrets Handling

No secrets in the repo. `.gitignore` excludes the binary and `*.db*`. Identity uses `math/rand` for
a non-security-sensitive suffix (`identity.go`) — acceptable since it's a label, not a credential.
**Bearer tokens** are the one runtime secret: the plaintext (`amt_` + 32 hex, 16 bytes of
`crypto/rand`) is generated and shown **once** at mint, then only ever stored as a sha256 hash in the
DB. It is never logged (the convention bans logging tokens), never returned by `am token ls`/`whoami`,
and the CLI keeps stdout clean (the plaintext is the first stdout line at mint so capture works; the
hint goes to stderr). The plaintext at rest lives only in the per-directory identity file.

## Dependencies

- One direct dependency: `modernc.org/sqlite` (pure Go, no cgo) plus its transitive deps (`go.mod`).
- **Dependency scanning**: `govulncheck ./...` runs in CI (`.github/workflows/ci.yml`) on every
  push to `main` and every PR, failing the build on a *reachable* vulnerability. Residual: no
  Dependabot/automated dependency-update PRs. (One known *unreachable*, Windows-only advisory today:
  `GO-2026-5024` in the indirect `golang.org/x/sys@v0.42.0`; clears at `x/sys ≥ v0.44.0`.)

## Attack Surface

- **HTTP API on loopback** — full read/write, unauthenticated.
- **Browser-driven attacks even on loopback:** with no auth, a malicious website would otherwise
  issue writes to `127.0.0.1:8787` (CSRF) or read via DNS rebinding — and
  because agents *act on* tasks, a poisoned task is an **injection vector into the agent fleet**.
  Mitigated by the Host allowlist (`hostGuard`) and the
  write-CSRF guard (`csrfGuard`) (ADR-011). It is not eliminated: a local non-browser process (no
  `Origin`/`Sec-Fetch-Site`) is still trusted, and reads are not CSRF-gated.
- **`am update`** shells out `go install <fixed module>@<version>` via `os/exec` (`update.go`);
  the version comes from a local CLI arg, the module path is a constant → low risk (no shell string
  interpolation; args passed as a slice).
- **Startup update check** fetches a **fixed** `proxy.golang.org` URL → not SSRF-prone.
- **500 responses return a generic body.** `writeErr`'s default branch logs the real error
  server-side (`log.Printf("agentman: internal error: %v", err)` to stderr) and returns
  `{"error":"internal"}` — internal detail is never sent to clients.
- **`am db export`/`am db import`** (`cmd/am/db.go`) are **CLI-only and add no HTTP route or network
  surface**. Export reads the DB read-only and `VACUUM INTO` a snapshot; import replaces the local DB
  file. See *Local DB Snapshot & Restore* below for the file-handling and liveness controls.

## Local DB Snapshot & Restore

`am db export` / `am db import` (`cmd/am/db.go`) operate directly on the SQLite file, off-band from
the server. Their security-relevant properties:

- **CLI-only, no new surface.** Neither subcommand registers an HTTP route; they add no
  network/listening surface (dispatched before the HTTP client is built).
- **Restrictive file permissions.** The exported snapshot and the pre-import backup are created
  `0o600` (`exportDB` chmods the output; `copyFile` opens with `0o600`); the destination directory is
  created `0o700` (`os.MkdirAll`). Consistent with the unencrypted-at-rest DB — keep snapshots local.
- **Server-controlled import destination.** The import target is always the configured DB path
  (`defaultDBPath()` or `--db`), **never the caller-supplied source argument** — the source is only
  ever read. A malicious source path cannot redirect where the DB is written.
- **Liveness guard.** Import **refuses to run while a server is up**: it probes
  `AGENTMAN_URL` (`/api/projects`) via `isServerRunning` and aborts with "stop `am serve` before
  importing". This avoids replacing a live WAL DB out from under a running process and corrupting it.
- **Candidate validation.** Before replacing anything, import runs `PRAGMA integrity_check` /
  `foreign_key_check`, requires the five core tables (the v1 baseline set — later tables, the
  `tokens` table included, are recreated by `schema.sql`/migrations on open, so old snapshots
  stay importable), and rejects a `schema_version` newer than `currentSchemaVersion` — so a garbage
  or future-schema file is refused, not imported. `OpenStore` applies the same version ceiling at open
  time, so an older binary refuses a too-new DB instead of operating on it.
- **Token hashes ride the snapshot.** `VACUUM INTO` is a whole-file snapshot, so a `tokens` table
  is exported with everything else — acceptable because only non-replayable sha256 hashes are stored
  (the server compares `hash(presented_plaintext)`); no scrubbing is performed (ADR-029).

## Existing Controls

- Loopback-only bind (the primary control).
- **Host-header allowlist** (`server.go hostGuard`) — rejects any Host except `127.0.0.1`/`localhost`/
  `::1`; mitigates DNS rebinding. (ADR-011.)
- **Write-CSRF guard** (`server.go csrfGuard`) — blocks cross-origin browser writes via
  `Sec-Fetch-Site`/`Origin` while allowing the header-less CLI and the same-origin dashboard;
  mitigates malicious-website drive-by writes. (ADR-011.)
- **`X-Content-Type-Options: nosniff` + a dashboard-safe CSP** (`server.go securityHeaders`).
- **Atomic claim** prevents double-claim/race (`store.go ClaimTask` / `StealStaleClaim` /
  `NextTask`, conditional `UPDATE … RETURNING`).
- XSS-safe DOM rendering; parameterized SQL; request body size cap.
- The **`events` table is a de-facto audit log** (actor, kind, timestamp, delta) — but the actor is
  spoofable since `X-Agent` is unauthenticated, so it's attribution, not non-repudiation.
- **Scope confinement** (`X-Agent-Scope` header + **scope tokens**) — the server rejects
  out-of-scope mutations and named reads with `403 out_of_scope` and logs each denial
  (`agentman: out_of_scope: …`). A control for **accidental** cross-scope action by a config-following
  agent. A **server-minted, scope-bound bearer token** (`Authorization: Bearer`) has its
  scope win over the header, mint requires an unscoped caller, and an invalid/revoked token →
  `401 unauthorized` (CLI exit 9). This confines a *token-following* agent that cannot forge another
  scope's token — but it is **not** auth against an arbitrary local process (loopback-only; a
  filesystem read of the identity file = token possession). Denials are log-only — no event kind, and
  token mint/revoke emits no event either (privacy: keeps credential admin out of the unprunable feed).

## Security Gaps

(Current gaps only. Resolved items live in the CHANGELOG.)

1. No authentication; authorization is **loopback-scoped** (by design, but blocks any non-loopback
   use). Scope tokens confine a token-following agent server-side but are not auth against an
   arbitrary local process.
2. No TLS — bearer tokens travel in **cleartext** over the `127.0.0.1` loop. Acceptable only
   because the bind never leaves loopback; a token is not a network-facing secret. (Would be sniffable
   the moment the bind widened — another reason remote access is an auth+TLS project, not a bolt-on.)
3. No rate limiting / brute-force protection (token lookup is also not constant-time — a non-issue at
   loopback scale, infeasible to make constant-time over a DB lookup).
4. Audit actor is spoofable (no identity verification).
5. **Scope confinement is token-backed but loopback-only.** `X-Agent-Scope` alone
   is client-asserted (forgeable/omittable). **Scope tokens** make the scope server-derived
   for a *token-following* agent — a server-minted, scope-bound token whose scope wins over the header,
   minted only by an unscoped caller, so a confined agent cannot forge another scope's token (bad
   token → `401`/exit 9). **Residual**: still not auth against an arbitrary local process — a process
   that reads the identity file holds the token (loopback-only; see the Authorization honesty note).
   Residual reads: `/api/events`, `/api/stream`, `GET /api/projects`, `GET /api/categories` are not
   narrowed by scope (an unscoped `?category=` lens serves the human dashboard, not an identity
   scope) — see Authorization above.

## Secure Implementation Checklist

Before merging a change, confirm:
- [ ] No new SQL uses string concatenation with caller input (use `?` placeholders). The only
      sanctioned exception is the escaped-literal `VACUUM INTO` path in `exportDB` — don't add more.
- [ ] Any new dashboard rendering uses `el()`/`textContent`, never `innerHTML`/`insertAdjacentHTML`.
- [ ] The server still binds `127.0.0.1` **unless** you also added authentication.
- [ ] New endpoints validate inputs and map errors via `writeErr` (no raw 500 text for expected cases; the `default` branch returns a generic `{"error":"internal"}` body and logs detail server-side).
- [ ] No secrets added to the repo, logs, or query strings. **Never log or `ls`/`whoami`-print a
      bearer token plaintext**; store only its sha256 hash, show the plaintext once at mint.
- [ ] If you added remote access: auth + CSRF/`Host` guard + TLS are all present, not just one.
- [ ] Any new local file written from a DB path (snapshots, backups) is created `0o600` / `0o700`,
      and a DB-replacing op writes only to the configured DB path and guards against a live server.
- [ ] `go vet ./...` and `govulncheck ./...` clean.

## Recommended Security Tests

(None exist today.) Add, in priority order:
1. **XSS regression** — POST a task with `<script>`/`<img onerror>` title; assert the API stores it
   literally and (via a DOM test) the dashboard renders text, not an element.
2. **Atomic-claim race** — two concurrent `POST /claim` on one task; assert exactly one 200, one 409.
3. **Authz/exposure guard** — a test asserting the server binds loopback (regression guard for the
   security boundary).
4. **Input validation** — invalid status / empty title / oversized body return 400, not 500.
