# Roadmap

A prioritized, checkable plan for the gaps known today. Severity/effort reflect agentman's stated
scope — a **personal, localhost, agent-driven** board. Cross-references point to
[`architecture/known-risks-and-gaps.md`](architecture/known-risks-and-gaps.md) and the matching
design docs. Effort key: **S** ≈ a few lines + a test · **M** ≈ a focused change across 2–3 files ·
**L** ≈ a feature or new surface.

When you finish an item, check it off here and add a `CHANGELOG.md` entry.

---

## Phase A — Finish the archive feature (recommended next)

Archiving currently hides a project's **tab** (`ListProjects`) and its **board tasks**
(`ListTasks`, fixed) but is not enforced anywhere else. Close the remaining seams.

- [x] **A1 · Hide archived projects' events from the feed** — _S_
  - Why: in the "All" view the activity drawer keeps streaming an archived project's events even
    though its board/tab are gone (inconsistent with "hide archived").
  - Do: in `ListEvents` and `RecentEvents` (`cmd/am/store.go`), exclude events whose `project_id`
    belongs to an archived project when no explicit `project=` filter is given (LEFT JOIN
    `projects` and add `p.archived_at IS NULL`, mirroring the `ListTasks` fix). Keep explicit
    `?project=<slug>` unfiltered.
  - Accept: archiving a project drops its lines from the unfiltered feed; `?project=<archived>`
    still returns them; SSE reconcile shows the same. Add a store test.
- [x] **A2 · Guard task creation into an archived project** — _S_
  - Why: `am new -p <archived>` / `POST /api/tasks` silently creates a ticket that is then
    immediately hidden (`CreateTask` looks up the slug with no archived check).
  - Do: in `CreateTask` (`cmd/am/store.go`), reject with `ErrValidation` (or a dedicated error
    mapped to 409/400) when the target project is archived; surface a clear CLI message.
  - Accept: creating into an archived project fails with a clear error + non-zero exit; creating
    into an active project is unaffected. Add a store test + an HTTP mapping test.
- [x] **A3 · Dashboard archive / unarchive control** — _M–L_
  - Why: archiving is CLI/API-only; the human's decluttering action isn't available on the human's
    dashboard (you can create a project with ＋ but not archive one, nor restore it).
  - Do: add an archive affordance per project (e.g. a small ⋯ menu on the active project tab, or a
    control in a "manage projects" view) calling `POST /api/projects/{slug}/archive` /
    `…/unarchive`; add an "show archived" toggle that lists archived projects (`?archived=true`)
    with an unarchive action. Build DOM with `el()` only (no `innerHTML`); preserve keyboard/focus
    behavior; rebuild the embedded binary. (`cmd/am/web/app.js`, `app.css`, `index.html`)
  - Accept: a user can archive and restore a project entirely from the dashboard; live SSE updates
    the tab bar and board without a reload. → `architecture/frontend.md`.

## Phase B — Release hygiene (do now)

- [ ] **B1 · Commit the pending fixes + docs** — _S_
  - The archived-task hiding fix, the ultrawide centering fix, and the doc sync are uncommitted.
    Commit them (e.g. `fix: hide archived projects' tasks from the board`,
    `fix: center board on ultrawide screens`, `docs: …`).
- [ ] **B2 · Tag and push v0.4.0** — _S_
  - Rename the `CHANGELOG.md` `[Unreleased]` heading to `## [0.4.0] - <date>`; then
    `git tag -a v0.4.0 -m "…" && git push origin v0.4.0`. `go install …/cmd/am@latest` resolves
    `latest` from the highest semver tag. → `README.md` (Updating).
- [ ] **B3 · Keep the CHANGELOG going** — _S, process_
  - Add a `Fixed`/`Added`/`Changed` entry with every user-facing change from now on.

## Phase C — Data lifecycle (medium)

- [x] **C1 · Hard-delete endpoints** — _M_ — **shipped (Phase C1)**
  - `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`, `DELETE /api/projects/{slug}`;
    store methods `DeleteTask`/`DeleteComment`/`DeleteProject`; CLI `am rm <id>` and
    `am project rm <slug> --yes`; dashboard inline two-step confirms; 3 new event kinds
    (`task.deleted`, `comment.deleted`, `project.deleted`); 7 new tests. `ref` reuse accepted
    (no counter). → ADR-015, `data-model.md`, `CHANGELOG.md`.
- [ ] **C2 · Bound `events` / `comments` growth** — _M_ — (remaining half of Phase C)
  - Why: both grow unbounded (no retention/pagination); long-running instances bloat. The dashboard
    only caps render, not storage.
  - Do: choose one — (a) a retention/compaction job or `am db prune --before <date>`; and/or
    (b) `?before=`/cursor pagination on `GET /api/events`. → `data-model.md` (Retention is an open
    Unknown today).

## Phase D — Error handling & observability

- [ ] **D1 · Stop leaking raw error strings on 500** — _S_
  - Why: `writeErr`'s default branch returns the raw Go error to the client (minor info exposure).
  - Do: log the detail server-side; return a generic `{"error":"internal"}` body. (`cmd/am/server.go`)
  - Accept: 500s no longer echo internal messages; the detail still hits stderr.
- [ ] **D2 · Minimal request/observability logging** — _S–M, optional_
  - Why: today only startup/shutdown/update are logged — no request logging, metrics, or tracing.
  - Do: a lightweight request-log middleware (method, path, status, latency) behind a flag or env.
    → `backend.md` (Observability gap).

## Phase E — Test coverage (the untested areas)

Highest risk-reduction per effort; see `known-risks-and-gaps.md` "Testing Gaps".

- [ ] **E1 · CLI command-path tests** — _M_ — exercise `ls/new/claim/status/assign/note/edit/drop/
      projects/project/db` against an `httptest` server + temp `--db`; assert terse output, silent
      success, and exit-code mapping (`client.go doOrFail`).
- [ ] **E2 · SSE streaming / reconnect test** — _M_ — connect to `/api/stream`, emit a mutation,
      assert the event arrives; reconnect with `Last-Event-ID` and assert gap replay + dedupe.
- [ ] **E3 · Identity tests** — _S_ — `am init` writes the per-dir file; `resolveAgent` reads it;
      `AGENTMAN_AGENT` overrides (`cmd/am/identity.go`).
- [ ] **E4 · Dashboard JS — XSS regression + a runner decision** — _M_ — there is no JS test runner.
      Either adopt a tiny one (e.g. node + jsdom) for an XSS regression (agent text rendered as
      literal text, never markup) and the multi-select/archive logic, or document the deliberate
      choice not to. → `frontend.md`.

## Phase F — CI & tooling

- [ ] **F1 · Add CI** — _M_ — a GitHub Actions workflow running `go build`, `go vet`, `go test
      -race`, `gofmt -l` (fail if non-empty), and `govulncheck ./...` on push/PR. No `.github/`
      exists today, so doc/code drift and regressions go uncaught. → `known-risks-and-gaps.md`.

## Phase G — Security posture (deferred by design)

agentman is loopback-only with no auth; the bind **is** the access control, hardened by the
Phase-0 Host allowlist + write-CSRF guard + CSP. The items below are **intentionally not done**
and only matter if the network bind ever widens. (`architecture/security.md`)

- [ ] **G1 · Only if remote/multi-user is ever wanted** — treat it as an **auth + CSRF + TLS**
      project, not a feature bolt-on: real authentication (the `X-Agent` actor is a spoofable label,
      not a credential), TLS, rate limiting, and per-resource authorization. Until then, these are
      accepted residuals for the stated scope.

---

### Suggested order

**B1 → B2** (ship v0.4.0 with the current fixes) → **A1 → A2 → A3** (finish archive) → **F1** (CI to
lock it in) → **E1–E4** (close the biggest test gaps) → **D1**, then **C1/C2** as the product grows.
**G** stays parked unless the access model changes.
