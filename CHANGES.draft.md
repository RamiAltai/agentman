# CHANGES.draft — Phase Q: scoped agent identity & enforcement (R4)

Input for the docs-sync stage. Branch `phase-q-scoping`, stacked on
`phase-p-meta`.

## Summary

Agents can now be confined to a category (default) or a single project
(tighter). `am init <tasktype> -c CAT [-p PROJ]` records a scope in the
identity file; the CLI sends it as `X-Agent-Scope` on every request; the
server enforces it on every mutation and (loudly, for named targets) on
reads. Out-of-scope → `403 {"error":"out_of_scope"}` → new CLI exit code
**8**. One carve-out: task creation (and commenting on one's OWN tasks) in
the designated proposals project (default `meta/proposals`) is allowed from
any scope.

Honesty note (must survive into security.md updates): `X-Agent-Scope` is a
client-asserted label like `X-Agent` — this is accident prevention for an
agent following its config, not a security boundary against crafted HTTP.
R5 (scope tokens) is what upgrades it.

## Schema (migration v5)

- `currentSchemaVersion` 4 → 5. schema.sql untouched (frozen v1 baseline).
- v5: `ALTER TABLE tasks ADD COLUMN created_by TEXT`, then a best-effort
  backfill: `created_by` = the actor of the task's LATEST `task.created`
  event (latest, not first: tasks.id is a reusable SQLite rowid and a
  deleted task's events survive, so an id's oldest creation event may belong
  to a deleted predecessor — the newest always belongs to the current
  incarnation). Tasks whose events were pruned stay NULL — they simply never
  match the own-proposal comment rule (the safe direction).
- `created_by` is set on every new task from the request actor: the server
  passes `actorOf(r)`, which defaults to "human" when no `X-Agent` header is
  present; the store-level `actorOr` fallback "anon" is only reachable for
  direct store callers that pass an empty actor.
- Forward-only, idempotent, version bump in the same tx (house pattern).
  db.go's import ceiling tracks `currentSchemaVersion` automatically; a v5
  snapshot now imports, a v6 one is refused.

## API changes (all externally visible)

### New header: `X-Agent-Scope`
- Format `category[/project]`, e.g. `personal` or `work/api`. Trimmed and
  lowercased server-side like slugs. Absent header = unscoped (the human,
  the dashboard) — sees and mutates everything, zero behavior change.
- Malformed scope (empty/whitespace segments, >1 slash) → 400 `validation`
  on EVERY scope-checked endpoint.
- Well-formed but unknown slugs are not validated against the DB: mutations
  403, unfiltered lists come back empty.
- `scopeOf(r)` in server.go is the SOLE reader of the header — Phase S
  (scope tokens) swaps the scope source there without touching handlers.

### New error: `403 {"error":"out_of_scope"}`
- Mapped from the new `ErrOutOfScope` sentinel in writeErr.
- Every denial is logged server-side:
  `agentman: out_of_scope: actor=<id> scope=<scope> <METHOD> <PATH>`.
  Log-only by design — NO new event kind (the events catalog is unchanged;
  feeding denials into the SSE stream was rejected as noise + a partial-
  information leak; revisit only with a real audit requirement).

### Per-verb enforcement (scoped callers only)
- `POST /api/tasks` — target project must be in scope OR be the proposals
  project. Unknown target slug stays 404 (matching unscoped), except for a
  project-scoped agent where anything ≠ its project is 403 without an
  existence check.
- `POST /api/tasks/{id}/claim` — one check covers claim AND `steal_stale`
  (stealing is scope-checked exactly like a claim).
- `PATCH /api/tasks/{id}` — covers status/assign/edit/drop and each id of a
  bulk `am status`/`am assign` (the CLI loops per id, so Phase L partial-
  failure semantics compose: out-of-scope ids get their own stderr line,
  the loop continues, exit = FIRST failure's mapping).
- `POST /api/tasks/{id}/comments` — in scope, OR (task in proposals AND
  `created_by` == `X-Agent`). NULL/empty created_by never matches.
- `DELETE /api/tasks/{id}`, `DELETE .../comments/{cid}`,
  `POST/DELETE .../deps[...]` (checked on the dependent task only — the
  store's same-project dep rule covers the prereq), `POST/DELETE
  .../labels[...]` — task must be in scope.
- `POST /api/tasks/next` — scope is merged into NextFilter BEFORE the store
  call, i.e. INSIDE the atomic pick+claim: a scoped agent can never be
  handed an out-of-scope task, even racing unscoped callers. Explicit
  out-of-scope `project`/`category` in the body → 403; absent ones are
  injected from the scope. The proposals carve-out does not extend to next
  (an agent whose scope covers the proposals project still picks them via
  plain in-scope matching).
- `GET /api/tasks` — silent narrowing: explicit `?project=`/`?category=`
  that contradicts the scope → 403 (loud); missing filters are filled from
  the scope (an unfiltered list shows only the agent's world);
  `?project=<proposals>` is allowed (proposals are readable by all). This
  same path scopes `am wait --ready`'s REST re-check.
- `GET /api/tasks/{id}` — 403 if out of scope (named reads fail loudly, the
  orchestrator's "ask-first outside subtree" rule); proposals tasks 200.
- `GET /api/projects/{slug}/graph` — project-level read check, proposals
  allowed.
- `POST /api/projects` — allowed for a category-scoped agent in its OWN
  category (effective category, i.e. empty body category = "general");
  403 for any other category and for project-scoped agents.
- `PATCH/DELETE /api/projects/{slug}`, archive/unarchive — project must be
  in scope (no proposals carve-out for project mutations).
- `POST /api/categories`, archive/unarchive — 403 for ANY scoped agent
  (the category layer is above every scope).
- **Untouched:** `GET /api/events`, `GET /api/stream` (Phase R residual —
  scoped agents can currently still read the global feed; documented gap),
  `GET /api/projects`, `GET /api/categories` (list endpoints stay
  unfiltered; the task layer is the enforcement point this phase).

### Proposals carve-out designation
- `am serve --proposals CAT/PROJ` flag, `AGENTMAN_PROPOSALS` env, default
  `meta/proposals`. Flag beats env. Both segments required — a bare
  category is rejected at startup (`fail(1)`) because it would widen the
  carve-out to a whole category.
- The carve-out matches the (category, project) PAIR everywhere it is
  consulted (create, comment, task read, project read, list narrowing): a
  project that merely shares the designated slug but lives in another
  category gets no special treatment and falls through to the normal scope
  rules. Slugs are globally unique, so without the category check a scoped
  agent could squat the slug inside its own category and capture other
  scopes' proposals (fixed in review round 1; `isProposals` in server.go is
  the single helper for the slug-keyed sites).
- No existence check: if the designated project does not exist, scoped
  creates into it pass the gate and 404 in the store — the carve-out is
  inert, never an error or a hole.
- `NewServer` defaults to `{meta, proposals}` so embedded/test servers
  behave like production.

## CLI

- `am init <tasktype> [-c CAT [-p PROJ]]` — optional scope. `-p` without
  `-c` → exit 5. Scope validated locally via parseScope (no server round-
  trip). Stdout is STILL only the id (`id=$(am init ...)` keeps working).
- Identity file format: scoped init writes JSON
  `{"agent":"...","scope":"cat[/proj]"}`; unscoped init keeps the legacy
  bare-id plain text; any pre-existing plain-text file reads as an unscoped
  identity (R8 compat — no migration of identity files).
- New env `AGENTMAN_SCOPE` — overrides the file's scope (non-empty values
  only), composing with the existing `AGENTMAN_AGENT` override. Resolution:
  env beats file, per field independently.
- `am whoami` — line 1 unchanged (the bare id); a second line
  `scope: cat[/proj]` appears only when scoped.
- New exit code **8** (out of scope), from any 403. Wired through
  `exitCodeFor` (single source) → doOrFail and bulk verbs get it for free;
  `am claim` prints `claim: #<id> out of scope`; `am next` prints
  `next: <err>` (only reachable with an explicit out-of-scope -p/-c — the
  unfiltered form is silently narrowed); `am wait` exits 8 from either
  re-check (`--done` on an out-of-scope task, `--ready` with explicit
  out-of-scope -p/-c).
- `am wait` sends the scope on its REST re-checks AND the SSE request; the
  stream itself is unscoped server-side (Phase R), so out-of-scope events
  merely trigger re-checks that keep failing — no hot-spin, no false
  release (the wait/next predicate invariant holds within scope).
- usage() updated: init/whoami/serve lines, a Scope paragraph, AGENTMAN_SCOPE
  + AGENTMAN_PROPOSALS env entries, and the exit-codes line now ends
  `· 7 timed out · 8 out of scope`.

## Behavior notes / decisions (ADR material)

1. **Reads policy** — loud 403 on named/explicit out-of-scope reads (GET
   task, graph, explicit list filters), silent narrowing on unfiltered
   lists, proposals project readable by all. Rationale: named reads failing
   loudly mirrors the orchestrator's ask-first rule; unfiltered reads
   narrowing silently keeps `am ls` ergonomic for scoped agents.
2. **Events/stream untouched in Q** — category-scoped SSE is Phase R; the
   global feed remains readable by scoped agents (documented residual).
3. **Denials are log-only** — no `scope.denied` event kind.
4. **tasks.created_by via migration v5** with best-effort events backfill;
   pruned-events tasks stay NULL and never match the own-proposal rule.
5. **Proposals designated by (category, project) pair** (`--proposals` /
   env), default meta/proposals; enforced as the pair at every check site (a
   same-slug project in another category is not the carve-out); inert when
   missing; the carve-out does not extend to `am next`; comments restricted
   to the proposal's creator (plus anyone whose scope covers it and the
   unscoped human).
6. **Identity file JSON-when-scoped** with plain-text legacy = unscoped.
7. **One resolution point** — `scopeOf(r)`; Phase S swaps the source there.
8. **Category endpoints 403 for scoped agents; project create allowed for
   category-scoped agents in their own category** (project-scoped agents
   may not create projects).
9. **Scope checks run outside the store tx** — sound because task→project
   and project→category are immutable; comments on PatchTask/PatchProject
   say the checks must move in-tx if a move feature ever ships.

## Known risks / residuals (for known-risks-and-gaps.md)

- Client-asserted header: not a boundary against crafted HTTP (R5 fixes).
- `/api/events` + `/api/stream` leak cross-scope activity to scoped agents
  until Phase R.
- `GET /api/projects` / `/api/categories` lists are not narrowed (board
  metadata visible; task data is not).
- Unknown explicit `?project=` for a scoped agent returns 403 (not 404/
  empty): the server cannot prove it in-scope; mild existence ambiguity is
  accepted in exchange for a fail-loud default. Same for project-scoped
  agents creating into unknown slugs.
- `ListTasks` rows do not include `created_by` (only GET /task and the
  scope checks read it) — deliberate, keeps list payloads stable.
- TOCTOU on scope checks vs. mutation is impossible today only due to
  immutability (decision 9).
- The pre-claim `checkTaskMut` on claim/steal means a scoped agent racing
  for a task gets 403 before any claim-conflict information (no holder
  leak).

## Deviations from the implementation map

- Check helpers take `*http.Request` (`checkTaskMut(r, sc, id)` etc.) in
  addition to the mapped `(sc, taskID)` signatures, so the denial log line
  can carry actor/method/path; `narrowScope` likewise gained `r`. Pure
  plumbing, no behavioral deviation.
- `checkComment` derives the actor via `actorOf(r)` instead of a separate
  `actor` parameter (same value, one source).
- Added `checkProjectRead`/`checkProjectMut` as named helpers for the
  project-level checks the map described inline.
- `checkCreate` for category-scoped agents returns 404 (not 403) for an
  unknown target slug, matching unscoped create semantics; the map's
  ordering put the proposals slug check first, which is preserved.

## Tests (added/extended, per file)

- `cmd/am/migrate_test.go` — `TestMigrationV5Fresh` (column + version 5),
  `TestMigrationV5ExistingDB` (hand-built v4 DB → backfill from
  task.created event; pruned-events task stays NULL; a reused rowid
  backfills from the LATEST creation event, not a deleted predecessor's;
  idempotent reopen);
  `TestMigrationV4Fresh`/`TestMigrationV4ExistingDB` un-hardcoded to
  `currentSchemaVersion` (TestOpenStoreRejectsNewerSchema already used
  `currentSchemaVersion+1`).
- `cmd/am/db_test.go` — `TestImportPreCategorySnapshot` and
  `TestImportRejectsNewerSchema` un-hardcoded from 4/5 to
  `currentSchemaVersion`(+1).
- `cmd/am/store_test.go` — `TestTaskScopeAndProjectCategory` (taskScope,
  created_by default + GetTask surface, projectCategory, ErrNotFound),
  `TestNextTaskRaceScopedCategoryMeta` (N workers, Category+MetaKey filter,
  out-of-scope + keyless decoys at higher priority → distinct in-scope
  meta-carrying winners).
- `cmd/am/server_test.go` — helpers `scopedBoard`, `mustCreateProposals`,
  `scoped`; `TestScopeClaimEnforcement` (acceptance 2),
  `TestScopeNextEnforcement` (acceptance 3), `TestScopeStealStale`
  (acceptance 4), `TestScopeProposalsCarveOut` +
  `TestScopeProposalsConfigurable` + `TestScopeProposalsMissingProjectInert`
  (acceptance 8), `TestScopeProposalsWrongCategoryNoCarveOut` (designated
  slug in a non-designated category: create/explicit-list/graph all 403 via
  the normal rules; in-scope agent unaffected) + `TestScopeProposalsSquat`
  (work-scoped agent creates work/proposals; another scope's create into the
  slug is 403, not captured), `TestScopeMutationSweep` (every mutating verb, 403
  out-of-scope vs success in-scope), `TestScopeProjectScopedAgent`,
  `TestScopeReads` (named 403 / narrowing / proposals / graph),
  `TestScopeHeaderValidation` (400s + unknown-slug scope),
  `TestScopeProjectCategoryEndpoints`.
- `cmd/am/cli_test.go` — `TestExitCodeForOutOfScope`,
  `TestCmdClaimOutOfScopeExit8`, `TestCmdNextOutOfScopeExit8`,
  `TestClientSendsScopeHeader`, `TestCmdStatusBulkOutOfScope` (per-id 403
  line + continue + exit 8; 404-before-403 ordering → exit 3).
- `cmd/am/identity_test.go` — `TestInitScopedWritesJSON`,
  `TestInitScopedCategoryProject`, `TestInitProjectRequiresCategory`,
  `TestLegacyPlainIdentityUnscoped`, `TestScopeEnvOverride`,
  `TestWhoamiPrintsScope`, `TestParseScope` (table incl. String/IsZero).
- `cmd/am/wait_test.go` — `TestWaitReadyScopedTimeout` (out-of-scope ready
  never releases → exit 7), `TestWaitReadyScopedReleased` (in-scope create
  releases with id), `TestWaitReadyExplicitOutOfScopeExit8`.
