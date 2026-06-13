# Known Risks and Gaps

Centralized uncertainty. Severity is the author's judgment for the project's stated scope
(a personal, localhost, agent-driven board). Cross-references point to the detailed doc.

## Architecture Risks

- **Schema-migration runner: forward-only, no rollback path** (Low). The forward-only runner reads
  and bumps `meta.schema_version` (ADR-010); each step applies its version bump in one tx and is
  test-covered (`currentSchemaVersion = 5`). Residual: no down-migrations. `OpenStore` refuses a DB
  recorded at a version newer than the binary supports (clear "upgrade am" error), the same ceiling
  `validateImportCandidate` applies to snapshots. → `data-model.md`,
  `decision-records.md` ADR-010/ADR-025.
- **Single-writer throughput ceiling** (Low for stated scope). `SetMaxOpenConns(1)` serializes all
  writes; correct and simple, but caps write concurrency. → ADR-003.
- **Module boundaries are by convention only** (Medium, maintainability). One flat `main` package
  means nothing prevents SQL leaking into handlers or HTTP into the store as the codebase grows.
  → `engineering-conventions.md`.
- **Full-board re-render on each event batch** (Medium at scale). → `frontend.md` IADR-002.

## Product Risks

- **Delete and history residuals** (Low). Hard-delete endpoints exist (`DELETE /api/tasks/{id}`,
  `DELETE /api/tasks/{id}/comments/{cid}`, `DELETE /api/projects/{slug}` cascade via FK; CLI
  `am rm <id>`, `am project rm <slug> --yes`), with these accepted residuals:
  - **`ref` reuse** — the global `tasks.id` never reuses, but a per-project human `ref` (e.g. `web-3`)
    can be reused if the highest-numbered task is deleted and a new task is then created (accepted,
    no counter/migration added).
  - **Deleted-project events reappear in the unfiltered feed** — because the archived-event filter is
    `LEFT JOIN projects … p.archived_at IS NULL`, and a deleted project has no row (JOIN yields NULL,
    treated as "not archived"). The `project.deleted` event and the deleted project's earlier history
    remain visible in the feed (good for an audit trail; see `data-model.md`).
  - **`events` growth is only partially bounded** — backward cursor pagination
    (`GET /api/events?before=<id>`, `ListEventsBefore`) and offline retention
    (`am db prune (--before <YYYY-MM-DD> | --keep <N>)`, events-only, refuses while a server is
    running) exist, but:
    - `am db prune` is **manual and offline** — no automated compaction; a long-running instance
      still grows until an operator runs it.
    - The `isServerRunning` offline guard checks `AGENTMAN_URL` (default `http://127.0.0.1:8787`). A
      server running on a non-default port with `AGENTMAN_URL` unset/mismatched would **not** be
      detected, so `am db import` and `am db prune` could run against a live DB. The documented
      instruction is "stop `am serve` first"; the guard is bypassable on non-default ports.
    - The dashboard's paginated feed (`feedPaginated = true`) **disables `trimFeed`** — a
      long-running tab that has clicked "Load older" can grow the feed unbounded until the next reload.
  - **`comments` growth is still unbounded** — comments are only removed individually via the
    hard-delete endpoint (no bulk prune). The dashboard caps render but not DB storage.
  - The live SSE broadcast (`hub.Broadcast`) is not archive-filtered — an event on a project archived
    after the SSE connection was opened can flash transiently in the feed until the next `ListEvents`
    reload filters it out. → `data-model.md`.
- **Category events are invisible to project-/category-scoped SSE subscribers** (Low, deliberate).
  The `category.*` event kinds carry a NULL `project_id`, so they reach **unscoped** subscribers
  only — a `?project=`- or `?category=`-scoped stream filters them out. `/api/events` and
  `/api/stream` take a `?category=` lens that intentionally excludes category-level NULL-project
  events — a category drill-down shows work *inside* the category; the instance-wide `category.*`
  events live on the All/overview feed. `am wait --ready -c` deliberately streams unscoped and
  re-checks via the category-scoped REST call (chattier but correct). → ADR-025, ADR-028.
- **Hub category-stream post-open project staleness window** (Low, by design). A
  `?category=` SSE subscription resolves the category's project-id set **once** at Subscribe
  (`ProjectIDsInCategory`) so `Broadcast` stays a pure in-memory check; a project created *after*
  that subscription opens is not in the set, so its task events don't stream until the dashboard
  re-opens the stream (which it does on every view change). The `project.created` carve-out still
  surfaces the new project itself, and the REST snapshot is authoritative. Accepted to keep
  `Broadcast` DB-free. → ADR-028, `hub.go`.
- **Overview count-refresh debounce can fire after navigating away** (Low, cosmetic). On
  the category-home overview, `onEvent` debounces a `loadOverview()` (250 ms) on task/project/
  category events; the debounced callback **re-checks `view === "overview"` at fire time** so it
  never writes to the now-hidden `#overview`. Harmless either way. → `frontend.md`.
- **`project.patched` is not in the `onEvent` `loadProjects()` trigger set** (Low; ADR-031).
  `loadProjects()` rebuilds the **left rail** (via `renderRail()`). The GUI project-edit (rename +
  vault binding) reloads the rail on the *editing* client (via the `openManage()` reload), but
  `onEvent` does not call `loadProjects()` on a `project.patched` event, so a *remote* dashboard does
  not live-refresh another client's project rename in its left rail until its next reload / view
  change. This follows the existing liveness pattern (the trigger set is
  `project.created`/`project.unarchived`/`project.archived` +
  `category.created`/`category.archived`/`category.unarchived`). → ADR-031, `frontend.md`.
- **Identity collisions in one directory** (Low). Two agents in the same working dir share the
  per-dir identity unless one sets `AGENTMAN_AGENT`. → ADR-008.
- **Update bootstrap** (Low). A machine must do one manual `go install …@latest` to get a binary
  that *has* `am update`/the startup check; only then is self-update available. → `README.md`.

## Dependency Feature Design Residuals

- **Same-project-only constraint** (Low, by design). `AddDep` rejects cross-project dependency
  edges (`ErrValidation`). Cross-project orchestration must be done at the agent level (e.g. poll
  the prerequisite task and only then claim the dependent). This was an intentional simplification
  (avoids cross-project cascade and visibility questions).
- **Ready/Blocked are derived, not stored statuses** (Low, by design). There is no `ready` or
  `prereq_blocked` value in `tasks.status`; the counts (`nprereq`, `nopen`) are computed via
  subqueries in `ListTasks` and `GetTask`. The `?ready=`/`?blocked=` query params are filter-only.
  This is correct but means a "blocked by deps" task can simultaneously have status `todo` — agents
  should use `am ls --ready` to find actionable work rather than `--status todo`.
- **Dependency UI is untested at the behavioral level** — the prereq chips, add-prereq dropdown,
  and blocks list in the task modal are vanilla JS covered only by the `TestDashboardNoXSSSinks`
  source-level guard. See Testing Gaps below.
- **Dependency-graph overlay is untested at the behavioral level** (same gap, by design). The
  overlay JS (layout, pan/zoom, transitive highlight, detail panel, live refresh) is covered only
  by the `TestDashboardNoXSSSinks` source-level XSS guard. No JS test runner (ADR-018). See
  Testing Gaps below.
- **Graph layout is a simplified layered algorithm** (Low). `computeGraphLayout` uses topological
  longest-path / Kahn's layering with no crossing-minimization. For modest project sizes this is
  clean and fast; for large, dense DAGs edge crossings can accumulate. Mitigated by pan/zoom and
  the separate isolated-task grid lane. Acceptable for a personal board's scale.

## Task-Metadata Residuals

- **Values are presence-filtered only, by design** (Low). `?meta_key=`/`--meta KEY` match a key's
  existence, never its value — a value-match filter would be a new decision (ADR-026). Workers
  read values from `am show <id> --json`.
- **List-row meta stitch adds one bind variable per returned row** (Low). `ListTasks` fills
  `meta` via a follow-up `SELECT … WHERE task_id IN (?,?,…)` — one placeholder per row, bounded
  by the list `limit` (the CLI sends 50; the dashboard 500), far below SQLite's bind-variable
  ceiling. Revisit if the limit cap is ever raised dramatically.
- **List payloads are no longer strictly terse** (Low). List rows now carry each task's full
  `meta` map (values ≤ 500 bytes each) — a deliberate widening of the "terse projection"
  convention so filters and workers don't need a per-task `GET`. Tasks with many large meta
  values fatten every list response and SSE-triggered board reload.

## Scope-Enforcement Residuals

- **Scope is token-backed but still loopback-only** (Medium, by design). `X-Agent-Scope` alone is
  a client-asserted label (forgeable/omittable, accident prevention). Scope tokens turn it into a
  server-derived credential — a server-minted, scope-bound bearer token whose scope wins over the
  header, minted only by an unscoped caller (`tokenAdminGuard`), resolved in the single `scopeOf(r)`
  point. This confines a *token-following* agent that cannot forge another scope's token
  (bad token → 401 → exit 9). **Residual**: not auth against an arbitrary local process — see
  *Scope-Token Residuals* below. → `security.md`, ADR-027, ADR-029.
- **`/api/events` + `/api/stream` are not narrowed by `X-Agent-Scope`** (Low, deliberate). A
  scoped agent can still read the global activity feed; the SSE stream stays unscoped against the
  identity scope, so `am wait` re-checks via the scoped REST call (chattier but correct — no
  hot-spin, no false release). Both endpoints take an *unscoped* `?category=` query-param lens (a
  human's category drill-down, not an identity scope); the agent `am wait` stream remains
  intentionally unscoped. → ADR-028.
- **`GET /api/projects` / `GET /api/categories` lists are not narrowed by scope** (Low,
  deliberate). Board *metadata* (slugs/names) is visible to any scope; task *data* is the
  enforcement point. `/api/categories` carries per-category stats for the unscoped human dashboard
  (category view is a query-param lens, not an identity scope). → ADR-028.
- **Unknown explicit `?project=` for a scoped agent returns 403, not 404/empty** (Low, by design).
  The server cannot prove an unknown slug in-scope, so it fails loud — mild existence ambiguity
  accepted in exchange for a fail-loud default. Same for a project-scoped agent creating into an
  unknown slug.
- **`created_by` backfill is best-effort** (Low). Tasks whose `task.created` events were pruned
  (`am db prune`) before the v5 migration stay NULL and never match the own-proposal comment rule
  (the safe direction). `ListTasks` rows deliberately omit `created_by` (only `GET /task` and the
  scope checks read it — keeps list payloads stable).
- **Exit 8 reachable from a host-guard 403** (Low, cosmetic). `am wait`'s re-checks map any 403 to
  exit 8; a host-allowlist rejection (`hostGuard`) would surface as "out of scope" rather than a
  guard error. Localhost-only, low impact.
- **TOCTOU between scope check and mutation is impossible only via immutability** (Low). The scope
  pre-checks run outside the store tx; this is sound solely because `task→project` and
  `project→category` are immutable today. If a task/project move feature ships, the checks must move
  in-tx (recorded in the `PatchTask`/`PatchProject` scope-note comments). → ADR-027.

## Scope-Token Residuals

- **Filesystem read = token possession** (Medium, by design — the central honesty note). A process
  that can read the per-directory identity file (`~/.agentman/agents/*`) holds the plaintext token and
  can act as that scope. The boundary scope tokens provide is precise: *a config-following agent that
  cannot forge another scope's token is confined to its own scope* — it is **not** protection against
  an attacker with arbitrary filesystem read, and **not** authentication. Loopback-only mitigates; the
  full remote/multi-user auth+TLS project stays parked. → `security.md`, ADR-029.
- **Tokens travel in cleartext over loopback** (Low, by design). No TLS; acceptable only because the
  bind never leaves `127.0.0.1`. A token is not a network-facing secret. → ADR-029.
- **DB export carries token hashes** (Low, accepted). `am db export` (`VACUUM INTO`) snapshots the
  `tokens` table, but only sha256 hashes are stored and the server compares `hash(presented_plaintext)`,
  so a hash is non-replayable; no scrubbing was added. → `data-model.md`, ADR-029.
- **Revocation is immediate but coarse** (Low, by design). `ResolveToken` checks `revoked_at` on
  every request, so a revoked token fails at once, but there is no expiry/rotation (matches the SHOULD
  scope). → ADR-029.
- **Token-hash lookup is not constant-time** (Low). `ResolveToken` is a single indexed
  `WHERE token_hash=?` lookup, not a constant-time compare — a non-issue at loopback scale, and
  constant-time over a DB lookup is infeasible. → ADR-029.
- **`am init` can clobber a stored token** (Low, accepted). `am token new` merges into the existing
  identity record, but a later `am init` rewrites the file and may drop the `token` field; the token
  is re-mintable, so this is accepted. → `identity.go`, ADR-029.

## Security Risks

(Full detail in `security.md`.)
- **No authentication/authorization** (by design for loopback; High if the bind is ever widened).
- **CSRF / DNS-rebinding** — a Host allowlist + write-CSRF guard are in place (ADR-011). Residual
  (Low): not auth — any local non-browser process is still trusted; reads are not CSRF-gated.
- **No TLS, no rate limiting** (Medium if exposed).
- **Spoofable audit actor** (Low) — `events.actor` comes from the unauthenticated `X-Agent` header.
- **Scope confinement is token-backed but loopback-only** (Medium, by design) — `X-Agent-Scope`
  alone is not a boundary against crafted HTTP; scope tokens make the scope server-derived for a
  token-following agent (server-minted, scope-bound, mint-requires-unscoped, bad token → 401/exit 9),
  but a process that reads the identity file still holds the token. See *Scope-Enforcement Residuals*
  + *Scope-Token Residuals* above and `security.md`.

## Testing Gaps

The backend is well covered (store/server/migrate/db/cli/sse/hub/identity/wait/web, `-race`-clean);
for the full test list see `architecture/contribution-guide.md` (Tests). The standing gap is the
**frontend**: the project adopts **no JS test runner** (preserves the no-npm/single-binary ethos;
ADR-018), so all behavioral dashboard JS is exercised only by the `web_test.go` source-level guards
(`TestDashboardNoXSSSinks`, `TestDashboardThemeAssets`, `TestDashboardParityAffordances`), never by a
runner. Un-runner-tested behaviors include:

- **Left-rail navigation** — `renderRail`, collapse persistence (`am.railCollapsed`),
  `goProject`/`pendingProject` single-project select, off-canvas drawer + `#railBackdrop` on mobile.
- **Category overview + hash routing** — overview cards, drill-down, top-bar scope title, per-view
  stream re-open, debounced count refresh.
- **Modal + delete flows** — the Manage modal (category + project lists), task/comment/project
  delete-confirm flows, the editable Meta section (ADR-031), the dependency section UI (prereq chips,
  add-prereq dropdown, blocks list), and the graph overlay (layout, pan/zoom, transitive highlight,
  detail panel, live refresh).
- **CLI↔GUI parity affordances** (ADR-031) — create/archive category, new-project category picker,
  project edit, board-filter popover, meta editing, release.
- **Feedback affordances** — loading skeletons and non-blocking toasts.
- **Accessibility** — modal focus-trap + focus restore, Escape-to-close, `prefers-reduced-motion`
  suppression.
- **Redesigned card/chip rendering** — status pill, always-present P0–P3 priority chip, reserved
  trouble sub-row, status-colored left edge, violet theme tokens.
- **Dark/light theme toggle** (ADR-030) — toggle click, system-follow while unset, `am.theme`
  persistence.

→ `frontend.md`, `decision-records.md` ADR-018, ADR-030, ADR-031.

## Documentation Gaps

- CI gates code/format/test regressions, but **prose-doc drift** is still possible — docs are not
  machine-checked against the code.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".

## Maintainability Concerns

- `store.go` and `app.js` are the largest files and mix several responsibilities; fine now,
  watch for growth.
- No linter beyond `gofmt`/`go vet`. CI enforces `gofmt`/`go vet`/`go test -race`/`govulncheck` on
  every push and PR, but pre-commit hooks are absent (local runs remain manual).

## Scalability Concerns

- Single SQLite file + single writer + full-board re-render → designed for a personal board, not a
  large team or thousands of tasks. No pagination on most reads (list capped only by `limit`/`tail`
  params and a client-side "Done" cap of 50).
- `events` table is append-only; offline pruning via `am db prune` is now available but is
  **manual** — long-running instances still grow unless an operator prunes periodically.
- `comments` table has no bulk prune; individual comments are deleted via the hard-delete endpoint.
- Backup/restore now exists via `am db export` / `am db import` / `am db prune` (all CLI-only);
  `import` and `prune` refuse while a server is running, so they require stopping `am serve` first.

## Unknowns

- Intended scale (concurrent agents, task volume) — undocumented.
- Whether multiple `am serve` processes are ever meant to share one DB (single-writer implies no).
- PR/review/branching process (single-maintainer repo; CI now gates pushes/PRs via GitHub Actions,
  but a formal branching/review policy is still undocumented).
- Target OS/arch matrix for releases (cross-compiles cleanly, but no release matrix is defined).

## Recommended Follow-Ups

1. **Decide & document the migration strategy** before any schema change to an existing table.
2. **Record the missing decisions** (auth, testing, deletes, CI, versioning) as ADRs once chosen.
3. If remote access is ever wanted, treat it as an **auth + CSRF/`Host` + TLS** project per
   `security.md`, not a feature add-on.
