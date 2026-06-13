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

- [x] **B1 · Commit the pending fixes + docs** — _S_ — **done** (shipped across v0.4.x).
- [x] **B2 · Tag and push releases** — _S_ — **done** (v0.4.1, v0.4.2, v0.5.0 tagged; v0.4.0 was
  mis-tagged and superseded by v0.4.1). Note: the v0.5.0 CHANGELOG section was cut late —
  remember to rename `[Unreleased]` **before** tagging.
- [ ] **B3 · Keep the CHANGELOG going** — _S, ongoing process_
  - Add a `Fixed`/`Added`/`Changed` entry with every user-facing change from now on.

## Phase C — Data lifecycle (medium)

- [x] **C1 · Hard-delete endpoints** — _M_ — **shipped (Phase C1)**
  - `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`, `DELETE /api/projects/{slug}`;
    store methods `DeleteTask`/`DeleteComment`/`DeleteProject`; CLI `am rm <id>` and
    `am project rm <slug> --yes`; dashboard inline two-step confirms; 3 new event kinds
    (`task.deleted`, `comment.deleted`, `project.deleted`); 7 new tests. `ref` reuse accepted
    (no counter). → ADR-015, `data-model.md`, `CHANGELOG.md`.
- [x] **C2 · Bound `events` growth (pagination + retention)** — _M_ — **shipped (Phase C2)**
  - `GET /api/events?before=<id>` backward cursor (`ListEventsBefore`; default 40, cap 200; same
    archived-project filter as `?since=`/`?tail=`). Dashboard "Load older activity" button
    (`feedOldest`/`feedPaginated`/`loadOlderActivity`; outside `#feedList`; end-marker when
    exhausted; `trimFeed` skipped after first paginate).
  - `am db prune (--before <YYYY-MM-DD> | --keep <N>) [--yes]` — offline, events-only, refuses
    while server is running, VACUUM after, prints `pruned N events` to stderr.
  - Tests: `TestListEventsBefore`, `TestEventsBeforeEndpoint`, `TestPruneEventsKeep`,
    `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary`. → ADR-016, `data-model.md`.
  - **Phase C is now COMPLETE.** Residuals: prune is manual/offline; `isServerRunning` guard is
    bypassable on non-default ports (applies to `import` + `prune`); `comments` growth still unbounded.

## Phase D — Error handling & observability — **COMPLETE**

- [x] **D1 · Stop leaking raw error strings on 500** — _S_ — **shipped (Phase D1)**
  - `writeErr`'s default branch now logs the real error server-side
    (`log.Printf("agentman: internal error: %v", err)`) and returns `{"error":"internal"}`.
    All sentinel mappings unchanged. Test: `TestWriteErrHidesInternalDetail`. (`cmd/am/server.go`)
- [x] **D2 · Minimal request/observability logging** — _S–M_ — **shipped (Phase D2)**
  - `requestLogger` middleware + `statusRecorder` (proxies `http.Flusher` for SSE). Enabled by
    `am serve --log` or `AGENTMAN_LOG=1` (any non-empty value). Off by default. Installed outermost
    so guard 403s are also logged. Logs `METHOD PATH STATUS LATENCY ACTOR` to stderr.
    Tests: `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`.
    Residuals: still no metrics, tracing, or structured logging; `AGENTMAN_LOG` treats any
    non-empty value as on (`=0`/`=false` also enable it — document `=1` as canonical).
    → `backend.md`, ADR-017. (`cmd/am/server.go`, `cmd/am/main.go`, `cmd/am/cli.go`)

## Phase E — Test coverage (the untested areas) — **COMPLETE**

- [x] **E1 · CLI command-path tests** — _M_ — **shipped (Phase E1)**
      `cmd/am/cli_test.go`: `captureStdout`/`captureExit` helpers; `cmdNew`/`cmdLs`/mutations
      against a real `httptest` server; exit-code mapping 3/4/5/6; formatter/parse table tests.
      Seam: `var osExit = os.Exit` in `cli.go` so `fail()` is interceptable in tests.
- [x] **E2 · SSE streaming / reconnect test** — _M_ — **shipped (Phase E2)**
      `cmd/am/sse_test.go`: `TestSSEDeliversLiveEvent` (live mutation arrives) and
      `TestSSEReplayOnReconnect` (reconnect with `Last-Event-ID`; replayed ids > resume cursor).
- [x] **E3 · Identity tests** — _S_ — **shipped (Phase E3)**
      `cmd/am/identity_test.go`: `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` env
      override, `sanitizeType` table, `newIdentity` format. Uses `AGENTMAN_AGENT_FILE` seam.
- [x] **E4 · Dashboard JS — XSS regression + runner decision** — _M_ — **shipped (Phase E4)**
      Decision: **no JS test runner** (preserves no-npm/single-binary ethos; ADR-018).
      `cmd/am/web_test.go` `TestDashboardNoXSSSinks`: source-level sink guard via Go + `webFS`
      embed.FS. Behavioral dashboard JS remains manually verified (documented gap). → `frontend.md`,
      `decision-records.md` ADR-018.

## Phase F — CI & tooling — **COMPLETE**

- [x] **F1 · Add CI** — _M_ — **shipped (Phase F)**. `.github/workflows/ci.yml`: GitHub Actions,
      `ubuntu-latest`, triggers on push to `main` and on PRs. Steps: `actions/checkout@v4` →
      `actions/setup-go@v5` (go-version-file + cache) → `go build ./...` → `go vet ./...` →
      `gofmt -l` (fail if non-empty) → `go test -race -count=1 ./...` → `node --check
      cmd/am/web/app.js` → `govulncheck @latest` (blocks on reachable vulns). CI is green;
      0 reachable vulnerabilities. One known non-blocking advisory (`GO-2026-5024`, Windows-only,
      unreachable) documented in `known-risks-and-gaps.md` and `decision-records.md` ADR-019.
      → `architecture/known-risks-and-gaps.md`, ADR-019.

## Phase I — Dependency-graph overlay (shipped, beyond original roadmap)

This feature was built on top of Phase H and has shipped.

- [x] **I1 · Interactive dependency-graph overlay** — _L_ — **shipped (Phase I)**
  - **"Graph"** button in the header + `g` keyboard shortcut → full-screen SVG overlay.
  - Layered DAG layout (topological longest-path / Kahn's), isolated-task grid lane below.
  - Pure vanilla SVG via new `svg()` helper (`createElementNS`) — no library, no npm.
  - Nodes colored by priority (`PRIO` palette); edges colored by prereq-satisfied state
    (green solid = cleared, amber dashed = blocking); bottom-left legend.
  - Click a node → transitive highlight (upstream + downstream); right detail panel with
    clickable prereq/unblocks lists and **"Open task"** button.
  - Pan (drag) + zoom (wheel) + Reset view.
  - Live refresh: debounced re-fetch on SSE events for the project; preserves pan/zoom and
    selection.
  - Backend: `GET /api/projects/{slug}/graph` (read-only, no events; 404 on missing project);
    `ProjectGraph` store method; `GraphEdge` / `ProjectGraphData` types.
  - +4 backend tests (`TestProjectGraph`, `TestProjectGraphMissingProject`,
    `TestProjectGraphEndpoint`, `TestProjectGraphEndpoint404`); total now 95.
  → ADR-021, `frontend.md`, `backend.md`, `CHANGELOG.md`.

## Phase H — Task dependencies (shipped, beyond original roadmap)

This feature was requested after the original roadmap was written and has shipped.

- [x] **H1 · Task prerequisite graph** — _L_ — **shipped (Phase H)**
  - `task_deps` join table (many-to-many, same-project, `ON DELETE CASCADE` both directions,
    propagated via `CREATE TABLE IF NOT EXISTS` — no migration step).
  - CLI: `am dep add <id> <prereq…>`, `am dep rm <id> <prereq>`, `am ls --ready` / `--blocked`,
    `[blk:N]` / `[ready]` markers in `am ls` output, `depends on:` / `blocks:` in `am show`.
  - API: `POST /api/tasks/{id}/deps`, `DELETE /api/tasks/{id}/deps/{depId}`, `?ready=` / `?blocked=`
    on `GET /api/tasks`, `depends_on` / `blocks` in `GET /api/tasks/{id}`.
  - Hard-block: claiming or moving to `doing`/`done` with open prereqs → `409 {"error":"blocked",
    "open_prereqs":[…]}` (exit 4 in CLI). Edit/comment/assign/`todo`/`blocked` unaffected.
  - Cycle prevention: recursive CTE rejects self-deps and transitive cycles (→ 400).
  - Dashboard: modal Dependencies section (prereq chips, add-prereq dropdown, Blocks list);
    card **🔒 Blocked** / **✓ Ready** tags; hard-block 409 reverts the UI.
  - 2 new event kinds (`task.dep_added`, `task.dep_removed`; total now 14); +24 tests (now 91).
  → ADR-020, `data-model.md`, `backend.md`, `frontend.md`, `CHANGELOG.md`.

## Phase O — agentic_brain foundation (shipped, beyond original roadmap)

The first phase of the agentic_brain integration train (requirements R1/R2/R3/R8 in
`agentman_requirements.md`, outside this repo). Tracked in `REVIEW.md` alongside Phases J–N.

- [x] **O1 · Category layer + stable IDs + vault binding + migration v4** — _L_ — **shipped (Phase O)**
  - New `categories` layer above projects (`instance → category → project → task`); `am
    categories` / `am category new|archive|unarchive`; `-c` / `AGENTMAN_CATEGORY` scope on
    `am ls`/`am next`/`am wait --ready`; `?category=` on `/api/tasks` + `/api/projects`;
    archived-category cascade (hidden by default, inspectable when scoped, writes blocked with
    `400 category_archived`).
  - Immutable stable ids `amc_`/`amp_` (+16 hex, crypto/rand) on categories/projects;
    `am project edit` (`--slug` rename, `--vault-id`/`--vault-path` binding) +
    `PATCH /api/projects/{slug}`.
  - Migration **v4** (`currentSchemaVersion = 4`): seeds default category `general`, attaches
    existing projects, backfills uids; zero data loss; pre-v4 snapshots stay importable;
    `OpenStore` rejects a DB with a newer `schema_version` than the binary supports.
  - 4 new event kinds (total 21); +30 tests (now 174).
  → ADR-025, `data-model.md`, `backend.md`, `CHANGELOG.md`. Phase **P** (task metadata) has
  shipped (below); phases **Q** (scoping enforcement), **R** (category dashboard), and
  **S** (scope tokens) follow.

## Phase P — Task metadata (shipped, beyond original roadmap)

The second phase of the agentic_brain train (requirement R7 in `agentman_requirements.md`,
outside this repo). Tracked in `REVIEW.md` alongside Phases J–O.

- [x] **P1 · Task metadata: `key=value` pairs + presence filters** — _L_ — **shipped (Phase P)**
  - New `task_meta` table (`CREATE TABLE IF NOT EXISTS` — no migration step, schema version
    stays 4); keys normalized like labels, values opaque ≤ 500 bytes; key PRESENCE is the
    filterable unit.
  - API: `"meta"` on `POST /api/tasks` (empty values rejected) and `PATCH /api/tasks/{id}`
    (upsert; empty value removes; multi-key all-or-nothing; duplicate-after-normalization keys
    rejected); `?meta_key=` on `GET /api/tasks`; `"meta_key"` on `POST /api/tasks/next`;
    `meta` in list rows and `GET /api/tasks/{id}`.
  - CLI: repeatable `--meta k=v` on `am new`/`am edit` (new `multiFlags` parser registry; all
    flags fold into one request), single `--meta KEY` on `am ls`/`am next`/`am wait --ready`,
    `meta:` line in `am show`. Dashboard: read-only modal Meta section + `(meta: …)` feed suffix.
  - No new event kinds (`task.created`/`task.patched` deltas carry meta; total stays 21);
    meta-only patches don't bump `updated_at`; `NextTask` refactored to a `NextFilter` struct
    (Phase Q extension point); +25 tests (now 199).
  → ADR-026, `data-model.md`, `backend.md`, `CHANGELOG.md`.

## Phase Q — Scoped agent identity & enforcement (shipped, beyond original roadmap)

The third phase of the agentic_brain train (requirement R4 in `agentman_requirements.md`,
outside this repo). Tracked in `REVIEW.md` alongside Phases J–P.

- [x] **Q1 · Scope identity + server enforcement + exit 8** — _L_ — **shipped (Phase Q)**
  - `am init <tasktype> -c CAT [-p PROJ]` records a scope (JSON identity file; legacy plain-text =
    unscoped); `AGENTMAN_SCOPE` overrides; `am whoami` shows a `scope:` line. The CLI sends
    `X-Agent-Scope` on every request; `scopeOf(r)` is the sole server-side reader (Phase S
    swap-point).
  - Enforcement on every mutation + named reads: out of scope → `403 {"error":"out_of_scope"}` →
    **new CLI exit code 8**. Reads policy: loud 403 on named/explicit out-of-scope, silent narrowing
    of unfiltered lists, proposals readable by all. `am next` merges the scope into the `NextFilter`
    inside the atomic pick+claim. Category endpoints 403 for any scope; project-create only for a
    category-scoped agent in its own category.
  - Proposals carve-out (`am serve --proposals` / `AGENTMAN_PROPOSALS`, default `meta/proposals`):
    task creation + own-proposal comments allowed from any scope, matched by the (category, project)
    pair (slug-squat-proof), inert when missing, not extended to `am next`.
  - `tasks.created_by` via **migration v5** (`currentSchemaVersion = 5`; best-effort backfill from
    the latest `task.created` event). Denials log-only — no new event kind (catalog stays 21).
    `X-Agent-Scope` is client-asserted (accident prevention, not auth; Phase S scope tokens upgrade
    it). +32 tests (now 231).
  → ADR-027, `data-model.md`, `backend.md`, `security.md`, `known-risks-and-gaps.md`, `CHANGELOG.md`.
    Next in the train: **Phase R** category dashboard + scoped feed (R6, shipped below), **Phase S**
    scope tokens (R5).

## Phase R — Category dashboard + scoped feed (shipped, beyond original roadmap)

The fourth phase of the agentic_brain train (requirement R6 in `agentman_requirements.md`, outside
this repo). Tracked in `REVIEW.md` alongside Phases J–Q.

- [x] **R1 · Category-home dashboard + `?category=` feed/stream** — _L_ — **shipped (Phase R)**
  - The human dashboard opens to a **category-home** overview (cards per category showing task
    counts + recently-active agents), drills into a single category's board, and keeps an **"All"**
    cross-category board — driven by linkable URL hashes (`#/`, `#/all`, `#/cat/<slug>`) with a
    "← Categories" breadcrumb and per-view SSE re-scoping. `GET /api/categories` is augmented to a
    `CategoryStat` (counts over non-archived projects + active agents in a 30-min window).
  - `GET /api/events` and `GET /api/stream` gain an unscoped **`?category=`** lens scoping to the
    category's projects' events and **excluding** instance-wide category-level (NULL-project)
    events; the hub resolves the category's project-id set once at Subscribe (`ProjectIDsInCategory`/
    `subFilter`) so `Broadcast` stays in-memory, preserving the `project.created` carve-out. Unknown
    category → 404 on `/api/events`, ignored silently on `/api/stream`.
  - No new event kinds (catalog stays 21), no new error/exit codes, no schema change
    (`currentSchemaVersion` stays 5), no migration; `am wait`'s stream left deliberately unscoped.
    +8 tests (now 239). → ADR-028, `frontend.md`, `backend.md`, `data-model.md`, `system-map.md`,
    `README.md`, `CHANGELOG.md`.
  - **After Phase R the integration-blocking set (O + P + Q) plus the human dashboard is DONE.**
    Only the train's **Phase S** (scope tokens, R5) and the NICE-to-have items remain.

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

Phases A, B (except the ongoing B3 process), C, D, E, F, H, I, O, and P are **complete**. **G**
stays parked unless the access model changes. For newer work, see `REVIEW.md` Phases J–P: Phase J
(correctness & hygiene), Phase K (stale-claim recovery — `am ls --stale`,
`am claim --steal-stale`), Phase L (agent work loop — `am next`, `am wait`, bulk
`status`/`assign`), Phase M (findability — `am ls --grep`/`--label`, `am label`), Phase O
(agentic_brain foundation — categories, stable IDs, vault binding, migration v4), Phase P
(task metadata — `--meta` k=v pairs + presence-filtered `next`/`wait`), Phase Q (scoped agent
identity & enforcement — `am init -c`, `X-Agent-Scope`, exit 8, proposals carve-out, migration v5),
and Phase R (category dashboard + scoped feed — category-home + drill-down, hash routing,
`?category=` on `/api/events`+`/api/stream`) have shipped. With Phase R the integration-blocking set
(O + P + Q) plus the human dashboard is complete; only the agentic_brain train's **Phase S** (scope
tokens) and the NICE items (release binaries, server-side auto-prune) remain.
