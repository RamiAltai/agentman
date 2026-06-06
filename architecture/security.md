# Security Architecture

> The single most important fact: **there is no authentication or authorization.** Access control
> is *entirely* the `127.0.0.1` bind. Any change that widens that bind, or adds remote access,
> must add real auth first. This is a deliberate decision (`decision-records.md` ADR-002).

## Authentication

**None.** `am serve` binds `127.0.0.1` (`cmd/am/main.go`, `Addr: "127.0.0.1:" + port`) and accepts
all requests. The `X-Agent` header (`server.go actorOf`) is an **actor label for attribution, not a
credential** — any caller may claim any identity, including `"human"` (the dashboard's value).

## Authorization

**None.** There are no roles, ownership checks, or per-resource permissions. Anyone who can reach
the port can read and mutate every project/task/comment.

## Trust Boundaries

- **Network boundary:** loopback only. Everything that can open a socket to the port is fully
  trusted. (See `known-risks-and-gaps.md` for why "LAN-only" is *not* a strong boundary if the bind
  is ever changed to `0.0.0.0`.)
- **Content boundary:** all task/comment/title/assignee text is **agent-supplied and untrusted**.
  Treat it as hostile input wherever it's rendered or acted upon.

## Sensitive Assets

- The **SQLite DB** (`~/.agentman/agentman.db`) — contains all board content; no encryption at rest.
- **Task/comment bodies** — may hold internal plans, repo names, or secrets an agent pasted.
- **Agent identity files** (`~/.agentman/agents/*`) — non-secret labels, low sensitivity.
- No passwords, API keys, or tokens are stored anywhere (Confirmed: no such columns/files).

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

## Output Encoding

- **Dashboard is XSS-safe by construction:** `web/app.js` builds DOM via `el()` using `textContent`
  / text nodes and **never `innerHTML`** — a task titled `<img src=x onerror=…>` renders as literal
  text. (Confirmed; this was explicitly verified during development.)
- API returns `application/json` via `encoding/json` (auto-escaped).
- The **CLI prints server text verbatim** to the terminal — fine for terminals, but a consumer that
  renders CLI output as HTML would need to encode it (not a current concern).

## Secrets Handling

No secrets in the repo or runtime. `.gitignore` excludes the binary and `*.db*`. Identity uses
`math/rand` for a non-security-sensitive suffix (`identity.go`) — acceptable since it's a label, not
a credential.

## Dependencies

- One direct dependency: `modernc.org/sqlite` (pure Go, no cgo) plus its transitive deps (`go.mod`).
- **No dependency scanning** (no `.github/`, no Dependabot, no `govulncheck` in CI). Supply-chain
  risk is unmonitored. Run `govulncheck ./...` manually before releases (recommended).

## Attack Surface

- **HTTP API on loopback** — full read/write, unauthenticated.
- **Browser-driven attacks even on loopback:** historically, with no auth and no CSRF/`Host` guard, a
  malicious website could issue writes to `127.0.0.1:8787` (CSRF) or read via DNS rebinding — and
  because agents *act on* tasks, a poisoned task is an **injection vector into the agent fleet**.
  **As of Phase 0 (ADR-011) this is mitigated** by the Host allowlist (`hostGuard`) and the
  write-CSRF guard (`csrfGuard`). It is not eliminated: a local non-browser process (no `Origin`/
  `Sec-Fetch-Site`) is still trusted, and reads are not CSRF-gated.
- **`am update`** shells out `go install <fixed module>@<version>` via `os/exec` (`update.go`);
  the version comes from a local CLI arg, the module path is a constant → low risk (no shell string
  interpolation; args passed as a slice).
- **Startup update check** fetches a **fixed** `proxy.golang.org` URL → not SSRF-prone.
- **500 responses leak raw error strings** (`writeErr` default branch) — minor info disclosure.
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
  `foreign_key_check`, requires the five core tables, and rejects a `schema_version` newer than
  `currentSchemaVersion` — so a garbage or future-schema file is refused, not imported.

## Existing Controls

- Loopback-only bind (the primary control).
- **Host-header allowlist** (`server.go hostGuard`) — rejects any Host except `127.0.0.1`/`localhost`/
  `::1`; mitigates DNS rebinding. (Added Phase 0, ADR-011.)
- **Write-CSRF guard** (`server.go csrfGuard`) — blocks cross-origin browser writes via
  `Sec-Fetch-Site`/`Origin` while allowing the header-less CLI and the same-origin dashboard;
  mitigates malicious-website drive-by writes. (Added Phase 0, ADR-011.)
- **`X-Content-Type-Options: nosniff` + a dashboard-safe CSP** (`server.go securityHeaders`).
- **Atomic claim** prevents double-claim/race (`store.go ClaimTask`, conditional `UPDATE … RETURNING`).
- XSS-safe DOM rendering; parameterized SQL; request body size cap.
- The **`events` table is a de-facto audit log** (actor, kind, timestamp, delta) — but the actor is
  spoofable since `X-Agent` is unauthenticated, so it's attribution, not non-repudiation.

## Security Gaps

1. No authentication / authorization (by design, but blocks any non-loopback use).
2. ~~No CSRF / DNS-rebinding guard~~ — **mitigated in Phase 0** by the Host allowlist + write-CSRF
   guard (ADR-011). Residual: these are not auth, so any *local* process can still call the API.
3. No TLS (a token over plain HTTP would be sniffable).
4. No rate limiting / brute-force protection.
5. 500s expose internal error text.
6. No dependency vulnerability scanning.
7. Audit actor is spoofable (no identity verification).

## Secure Implementation Checklist

Before merging a change, confirm:
- [ ] No new SQL uses string concatenation with caller input (use `?` placeholders). The only
      sanctioned exception is the escaped-literal `VACUUM INTO` path in `exportDB` — don't add more.
- [ ] Any new dashboard rendering uses `el()`/`textContent`, never `innerHTML`/`insertAdjacentHTML`.
- [ ] The server still binds `127.0.0.1` **unless** you also added authentication.
- [ ] New endpoints validate inputs and map errors via `writeErr` (no raw 500 text for expected cases).
- [ ] No secrets added to the repo, logs, or query strings.
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
