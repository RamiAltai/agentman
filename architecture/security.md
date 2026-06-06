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

- **SQL is fully parameterized** (`?` placeholders throughout `store.go`) → no SQL injection.
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
- **Browser-driven attacks even on loopback:** with no auth, **no CSRF protection, no `Host`-header
  / DNS-rebinding guard, and default same-origin CORS**, a malicious website you visit can issue
  writes to `127.0.0.1:8787` (CSRF) or read via DNS rebinding. Because agents *act on* tasks, a
  poisoned task is an **injection vector into the agent fleet** — the highest-severity surface here.
- **`am update`** shells out `go install <fixed module>@<version>` via `os/exec` (`update.go`);
  the version comes from a local CLI arg, the module path is a constant → low risk (no shell string
  interpolation; args passed as a slice).
- **Startup update check** fetches a **fixed** `proxy.golang.org` URL → not SSRF-prone.
- **500 responses leak raw error strings** (`writeErr` default branch) — minor info disclosure.

## Existing Controls

- Loopback-only bind (the primary control).
- **Atomic claim** prevents double-claim/race (`store.go ClaimTask`, conditional `UPDATE … RETURNING`).
- XSS-safe DOM rendering; parameterized SQL; request body size cap.
- The **`events` table is a de-facto audit log** (actor, kind, timestamp, delta) — but the actor is
  spoofable since `X-Agent` is unauthenticated, so it's attribution, not non-repudiation.

## Security Gaps

1. No authentication / authorization (by design, but blocks any non-loopback use).
2. No CSRF protection and no DNS-rebinding (`Host` allowlist) guard.
3. No TLS (a token over plain HTTP would be sniffable).
4. No rate limiting / brute-force protection.
5. 500s expose internal error text.
6. No dependency vulnerability scanning.
7. Audit actor is spoofable (no identity verification).

## Secure Implementation Checklist

Before merging a change, confirm:
- [ ] No new SQL uses string concatenation with caller input (use `?` placeholders).
- [ ] Any new dashboard rendering uses `el()`/`textContent`, never `innerHTML`/`insertAdjacentHTML`.
- [ ] The server still binds `127.0.0.1` **unless** you also added authentication.
- [ ] New endpoints validate inputs and map errors via `writeErr` (no raw 500 text for expected cases).
- [ ] No secrets added to the repo, logs, or query strings.
- [ ] If you added remote access: auth + CSRF/`Host` guard + TLS are all present, not just one.
- [ ] `go vet ./...` and `govulncheck ./...` clean.

## Recommended Security Tests

(None exist today.) Add, in priority order:
1. **XSS regression** — POST a task with `<script>`/`<img onerror>` title; assert the API stores it
   literally and (via a DOM test) the dashboard renders text, not an element.
2. **Atomic-claim race** — two concurrent `POST /claim` on one task; assert exactly one 200, one 409.
3. **Authz/exposure guard** — a test asserting the server binds loopback (regression guard for the
   security boundary).
4. **Input validation** — invalid status / empty title / oversized body return 400, not 500.
