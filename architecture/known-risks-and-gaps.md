# Known Risks and Gaps

Centralized uncertainty. Severity is the author's judgment for the project's stated scope
(a personal, localhost, agent-driven board). Cross-references point to the detailed doc.

## Architecture Risks

- **Schema-migration runner: forward-only, no rollback path** (was High → now Low). The forward-only
  runner that reads/bumps `meta.schema_version` (ADR-010) is now exercised end-to-end: Phase 2 shipped
  the first real step (`ALTER TABLE projects ADD COLUMN archived_at TEXT` at `version: 2`),
  Phase K the second (`ALTER TABLE tasks ADD COLUMN claimed_at TEXT` at `version: 3`),
  Phase O the third (`version: 4` — `projects.category_id`/`uid`/vault columns, `general` seed,
  uid backfill), and Phase Q the fourth (`version: 5` — `tasks.created_by` + best-effort backfill
  from the latest `task.created` event; `currentSchemaVersion = 5`), each applied with its version
  bump in one tx and covered by tests. Residual:
  no down-migrations. The too-new-DB leniency is **resolved (Phase O)**: `OpenStore` now refuses a
  DB recorded at a version newer than the binary supports (clear "upgrade am" error), the same
  ceiling `validateImportCandidate` applies to snapshots. → `data-model.md`,
  `decision-records.md` ADR-010/ADR-025.
- **Single-writer throughput ceiling** (Low for stated scope). `SetMaxOpenConns(1)` serializes all
  writes; correct and simple, but caps write concurrency. → ADR-003.
- **Module boundaries are by convention only** (Medium, maintainability). One flat `main` package
  means nothing prevents SQL leaking into handlers or HTTP into the store as the codebase grows.
  → `engineering-conventions.md`.
- **Full-board re-render on each event batch** (Medium at scale). → `frontend.md` IADR-002.

## Product Risks

- ~~**No hard delete; unbounded history**~~ — **RESOLVED (Phase C1)** for hard deletes. Hard-delete
  endpoints now exist: `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`, and
  `DELETE /api/projects/{slug}` (cascade via FK: project→tasks→comments). CLI: `am rm <id>` and
  `am project rm <slug> --yes`. Residuals (Low):
  - **`ref` reuse** — the global `tasks.id` never reuses, but a per-project human `ref` (e.g. `web-3`)
    can be reused if the highest-numbered task is deleted and a new task is then created (accepted,
    no counter/migration added).
  - **Deleted-project events reappear in the unfiltered feed** — because the archived-event filter is
    `LEFT JOIN projects … p.archived_at IS NULL`, and a deleted project has no row (JOIN yields NULL,
    treated as "not archived"). The `project.deleted` event and the deleted project's earlier history
    remain visible in the feed (good for an audit trail; see `data-model.md`).
  - **`events` growth is now partially bounded** — **Phase C2 shipped**: backward cursor pagination
    (`GET /api/events?before=<id>`, `ListEventsBefore`; dashboard "Load older activity" button) and
    offline retention (`am db prune (--before <YYYY-MM-DD> | --keep <N>)`, events-only, refuses while
    a server is running). Residuals (Low):
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
  Residual (Low) from earlier: the live SSE broadcast (`hub.Broadcast`) is not archive-filtered —
  an event on a project archived after the SSE connection was opened can flash transiently in the
  feed until the next `ListEvents` reload filters it out. → `data-model.md`.
- **Category events are invisible to project-/category-scoped SSE subscribers** (Low, deliberate).
  The `category.*` event kinds carry a NULL `project_id`, so they reach **unscoped** subscribers
  only — a `?project=`- or `?category=`-scoped stream filters them out. Phase R made this a
  **deliberate semantic**: `/api/events` and `/api/stream` now take a `?category=` lens (closing
  the "revisit in Phase R" note), and that lens intentionally excludes category-level NULL-project
  events — a category drill-down shows work *inside* the category; the instance-wide `category.*`
  events live on the All/overview feed. `am wait --ready -c` still deliberately streams unscoped
  and re-checks via the category-scoped REST call (chattier but correct — the wait stream was left
  unchanged). → ADR-025, ADR-028.
- **Hub category-stream post-open project staleness window** (Low, by design; Phase R). A
  `?category=` SSE subscription resolves the category's project-id set **once** at Subscribe
  (`ProjectIDsInCategory`) so `Broadcast` stays a pure in-memory check; a project created *after*
  that subscription opens is not in the set, so its task events don't stream until the dashboard
  re-opens the stream (which it does on every view change). The `project.created` carve-out still
  surfaces the new project itself, and the REST snapshot is authoritative. Accepted to keep
  `Broadcast` DB-free. → ADR-028, `hub.go`.
- **Overview count-refresh debounce can fire after navigating away** (Low, cosmetic; Phase R). On
  the category-home overview, `onEvent` debounces a `loadOverview()` (250 ms) on task/project/
  category events; the debounced callback **re-checks `view === "overview"` at fire time** so it
  never writes to the now-hidden `#overview`. Harmless either way. → `frontend.md`.
- **`project.patched` is not in the `onEvent` `loadProjects()` trigger set** (Low, deferred
  follow-up; ADR-031). The new GUI project-edit (rename + vault binding) reloads the project strip on
  the *editing* client (via the `openManage()` reload), but `onEvent` does not call `loadProjects()`
  on a `project.patched` event, so a *remote* dashboard does not live-refresh another client's
  project rename in its tab strip until its next reload / view change. This follows the existing
  liveness pattern (the trigger set is `project.created`/`project.unarchived`/`project.archived` +
  `category.created`/`category.archived`/`category.unarchived`) — adding `project.patched` would
  close it. Pre-existing pattern, not a regression. → ADR-031, `frontend.md`.
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

## Task-Metadata Residuals (Phase P)

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

## Scope-Enforcement Residuals (Phase Q)

- **Scope is now token-backed but still loopback-only** (Medium, by design). `X-Agent-Scope` alone is
  a client-asserted label (forgeable/omittable, accident prevention). **Phase S (scope tokens)**
  turned it into a server-derived credential — a server-minted, scope-bound bearer token whose scope
  wins over the header, minted only by an unscoped caller (`tokenAdminGuard`), resolved in the single
  `scopeOf(r)` point. This confines a *token-following* agent that cannot forge another scope's token
  (bad token → 401 → exit 9). **Residual**: not auth against an arbitrary local process — see
  *Scope-Token Residuals* below. → `security.md`, ADR-027, ADR-029.
- **`/api/events` + `/api/stream` are not narrowed by `X-Agent-Scope`** (Low, deliberate). A
  scoped agent can still read the global activity feed; the SSE stream stays unscoped against the
  identity scope, so `am wait` re-checks via the scoped REST call (chattier but correct — no
  hot-spin, no false release). **Phase R closed the dashboard side** by adding an *unscoped*
  `?category=` query-param lens to both endpoints (a human's category drill-down, not an identity
  scope); the agent `am wait` stream remains intentionally unscoped. → ADR-028.
- **`GET /api/projects` / `GET /api/categories` lists are not narrowed by scope** (Low,
  deliberate). Board *metadata* (slugs/names) is visible to any scope; task *data* is the
  enforcement point. Phase R **kept these unscoped on purpose** — `/api/categories` even gained
  per-category stats for the unscoped human dashboard (category view is a query-param lens, not an
  identity scope). → ADR-028.
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

## Scope-Token Residuals (Phase S)

- **Filesystem read = token possession** (Medium, by design — the central honesty note). A process
  that can read the per-directory identity file (`~/.agentman/agents/*`) holds the plaintext token and
  can act as that scope. The boundary Phase S provides is precise: *a config-following agent that
  cannot forge another scope's token is confined to its own scope* — it is **not** protection against
  an attacker with arbitrary filesystem read, and **not** authentication. This upgrades Phase Q's
  caveat (header → server-minted scope-bound revocable credential) but does **not** fully close R4's
  accident-prevention framing. Loopback-only mitigates; the full remote/multi-user auth+TLS project
  (Phase G) stays parked. → `security.md`, ADR-029.
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
- ~~No CSRF / DNS-rebinding protection~~ — **mitigated in Phase 0** (Host allowlist + write-CSRF
  guard, ADR-011). Residual (Low): not auth — any local non-browser process is still trusted; reads
  are not CSRF-gated.
- **No TLS, no rate limiting** (Medium if exposed).
- ~~**500 responses leak raw error strings** (Low)~~ — **RESOLVED (Phase D1)**. `writeErr`'s default branch now returns `{"error":"internal"}` and logs the real error server-side; no internal detail reaches the client.
- ~~**No dependency vulnerability scanning** (Medium, unmonitored)~~ — **mitigated (Phase F)**.
  `govulncheck ./...` now runs in CI on every push/PR; it blocks on **reachable** vulnerabilities.
  Current state: **0 reachable vulnerabilities**. One known non-blocking advisory:
  - **`GO-2026-5024`** — integer overflow in `golang.org/x/sys@v0.42.0`
    (`windows.NewNTUnicodeString`). **Windows-only; not reachable from agentman** (govulncheck's
    symbol/package scan finds nothing; module-level hit only). Transitive dep via `modernc.org/libc`.
    Clears by upgrading `golang.org/x/sys` to ≥ v0.44.0 if ever desired. Does not affect CI.
- **Spoofable audit actor** (Low) — `events.actor` comes from the unauthenticated `X-Agent` header.
- **Scope confinement is token-backed but loopback-only** (Medium, by design; Phase Q + Phase S) —
  `X-Agent-Scope` alone is not a boundary against crafted HTTP; **Phase S scope tokens** make the
  scope server-derived for a token-following agent (server-minted, scope-bound, mint-requires-unscoped,
  bad token → 401/exit 9), but a process that reads the identity file still holds the token. See
  *Scope-Enforcement Residuals* + *Scope-Token Residuals* above and `security.md`.

## Testing Gaps

- Coverage now spans store/server/migrate/db/cli/sse/hub/identity/wait/web tests (11 files, 258
  tests, `-race`-clean): the **atomic claim** (race, `-race`-clean), events cursor, store CRUD/validation,
  validation→status mapping, the Host/CSRF/CSP guards, project archive/unarchive (store round-trip
  + idempotency and the HTTP endpoints incl. 404), the v2 migration (adds `archived_at` +
  apply/bump/idempotency/rollback), DB export/import (roundtrip+perms, backup creation, garbage
  rejection, liveness probe), **feed hiding of archived-project events**
  (`TestFeedHidesArchivedProjectEvents`), **task creation into an archived project**
  (`TestCreateTaskRejectsArchivedProject` store, `TestCreateTaskIntoArchivedProject400` HTTP),
  **hard deletes** (`TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
  `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades` in `store_test.go`;
  `TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`, `TestDeleteCommentEndpoint` in
  `server_test.go`), **events backward pagination** (`TestListEventsBefore` in `store_test.go`;
  `TestEventsBeforeEndpoint` in `server_test.go`), **events prune** (`TestPruneEventsKeep`,
  `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary` in `db_test.go`), and Phase D's
  `TestWriteErrHidesInternalDetail`, `TestRequestLoggerPassesThrough`,
  `TestRequestLoggerPreservesFlusher` (in `server_test.go`).
  Phase E added:
  - **CLI verbs + exit codes** (`cli_test.go`) — `cmdNew`, `cmdLs`, mutations (`cmdStatus`/
    `cmdNote`/`cmdDrop`) silent-on-success, and the exit-code mapping 3/4/5/6/7 (now centralized
    in `exitCodeFor`; 7 is the wait timeout, exercised in `wait_test.go`); plus
    table tests for `parse`/`Args` and formatters (`taskLine`/`statusShort`/`assignee`/`trunc`/
    `apiErr`).
  - **SSE streaming + reconnect** (`sse_test.go`) — `TestSSEDeliversLiveEvent` and
    `TestSSEReplayOnReconnect` (gap-replay with dedupe; every replayed id > resume cursor).
  - **Identity** (`identity_test.go`) — `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` env
    override, `sanitizeType` table, `newIdentity` format.
  - **Dashboard XSS-sink guard** (`web_test.go`) — `TestDashboardNoXSSSinks` reads the embedded
    `web/app.js` + `web/index.html` via `webFS` and asserts no `.innerHTML`/`.outerHTML`/
    `.insertAdjacentHTML`/`document.write`/`eval(` appear — a source-level lock on the
    `el()`/`textContent` convention.
  The +24 dependency tests added in that phase cover: cycle/self/cross-project rejection,
  idempotent add/remove, cascade on task delete, `NPrereqs`/`NOpenPrereqs` counts, `Ready`/`Blocked`
  list filters, hard-block on `ClaimTask` and `PatchTask` (409 `blocked`), HTTP add/remove dep
  endpoints, `?ready=`/`?blocked=` query params, and a fresh-DB table-existence check.
  The +4 graph tests cover: `TestProjectGraph` (store: correct nodes + edges shape),
  `TestProjectGraphMissingProject` (store: `ErrNotFound`), `TestProjectGraphEndpoint` (HTTP 200
  with correct shape), `TestProjectGraphEndpoint404` (HTTP 404 for missing project).
  Phase K added 10 stale-claim tests: the steal + exactly-one-winner race
  (`TestStealStaleClaim`, `TestStealRaceExactlyOneWinner`), the stale filter
  (`TestListTasksStaleFilter`), `claimed_at` set/clear (`TestClaimSetsClaimedAt`,
  `TestDropClearsClaimedAt`), the v3 migration (`TestMigrationV3AddsClaimedAt`), the HTTP
  surfaces (`TestListTasksStaleParam`, `TestStealStaleEndpoint`), and the CLI mapping
  (`TestExitNotStale`, `TestStaleFlagsWireFormat`).
  Phase L added 23 work-loop tests: `NextTask` ordering/scoping and the pick+claim race
  (`TestNextTaskPicksHighestPriorityReady`, `TestNextTaskFIFOWithinPriority`,
  `TestNextTaskProjectScoping`, `TestNextTaskNoneReady`, `TestNextTaskRaceDistinctWinners`,
  `TestNextTaskEmptyAgentValidation`), the HTTP surface (`TestNextEndpoint`,
  `TestNextEndpointProjectBody`), the CLI (`TestCmdNextPrintsOnlyID`, `TestExitNextNoneReady`,
  bulk `TestCmdStatusBulk`/`TestCmdStatusBulkPartialFailure`/`TestCmdAssignBulk`), and `am wait`
  end to end in `wait_test.go` (already-done, event-driven, cross-project `--done`, ready-on-prereq,
  timeout exit 7, not-found, server-down, usage, `parseWaitTimeout`).
  Phase M added 14 findability tests: search (`TestListTasksQueryFilter`,
  `TestListTasksQueryEscapesLikeWildcards`, `TestListTasksQueryParam`) and labels
  (`TestAddRemoveLabel`, `TestLabelValidation`, `TestListTasksLabelFilter`,
  `TestAddLabelDoesNotBumpUpdatedAt`, `TestDeleteTaskCascadesLabels`,
  `TestTaskLabelsTableExistsOnReopenedDB`, `TestLabelEndpoints`), plus the CLI surface
  (`TestCmdLsGrepWireFormat`, `TestCmdLabelAddRemove`, `TestCmdLabelPrintsLabels`,
  `TestCmdLabelUsage`).
  Phase O added 30 category/stable-ID/vault/migration tests: the store layer
  (`TestCreateCategory`, `TestArchiveUnarchiveCategory`, `TestCreateProjectWithCategory`,
  `TestPatchProject`, `TestCategoryArchiveCascade`, `TestListTasksCategoryFilterComposes`,
  `TestNextTaskCategoryScoping`, `TestCreateTaskArchivedCategory`), the HTTP surface
  (`TestCategoryEndpoints`, `TestProjectPayloadAndCategoryFilter`, `TestListTasksCategoryParam`,
  `TestNextEndpointCategoryBody`, `TestPatchProjectEndpoint`,
  `TestCreateTaskArchivedCategory400`), the CLI (`TestCmdCategoryVerbs`,
  `TestCmdProjectNewRequiresCategory`, `TestCmdProjectEdit`, `TestCmdLsCategoryWireFormat`,
  `TestCmdNextCategory`, `TestCmdShowDashCStillPrintsComments`, `TestRewriteShowComments`),
  category-scoped wait (`TestWaitReadyCategoryScoped`, `TestWaitReadyCategoryEnv`,
  `TestWaitReadyCategoryTimeout`), the v4 migration + version ceiling (`TestMigrationV4Fresh`,
  `TestMigrationV4ExistingDB`, `TestOpenStoreRejectsNewerSchema`), and db export/import
  (`TestExportContainsCategories`, `TestImportPreCategorySnapshot`,
  `TestImportRejectsNewerSchema`).
  Phase P added 25 task-metadata tests: the store layer (`TestTaskMetaCRUD`,
  `TestTaskMetaValidation` — incl. normalized-key collision rejection on create and patch,
  `TestPatchTaskMetaAtomicOneEvent`, `TestPatchTaskMetaNoOpNoEvent`,
  `TestMetaOnlyPatchDoesNotBumpUpdatedAt`, `TestNextTaskMetaFilter`,
  `TestNextTaskMetaRaceDistinctWinners`, `TestListTasksMetaKeyFilter`,
  `TestListTasksReturnsMeta`, `TestDeleteTaskCascadesMeta`,
  `TestTaskMetaTableExistsOnReopenedDB`), the HTTP surface (`TestCreateTaskWithMeta`,
  `TestPatchTaskMetaEndpoint`, `TestNextEndpointMetaBody`, `TestListTasksMetaKeyParam`),
  meta-scoped waits (`TestWaitReadyMetaNoHotSpin`, `TestWaitReadyMetaReleasedByCreate`,
  `TestWaitReadyMetaReleasedByPrereqDone`, `TestWaitMetaUsageErrors`), and the CLI
  (`TestParseMultiFlag`, `TestCmdNewMetaWireFormat`, `TestCmdEditMetaSinglePatch`,
  `TestCmdNextMetaWireFormat`, `TestCmdLsMetaWireFormat`, `TestCmdShowPrintsMeta`).
  Phase Q added 32 scope/enforcement tests: the store layer (`TestTaskScopeAndProjectCategory`,
  `TestNextTaskRaceScopedCategoryMeta`), the HTTP scope sweep (`TestScopeClaimEnforcement`,
  `TestScopeNextEnforcement`, `TestScopeStealStale`, `TestScopeProposalsCarveOut`,
  `TestScopeProposalsConfigurable`, `TestScopeProposalsMissingProjectInert`,
  `TestScopeProposalsWrongCategoryNoCarveOut`, `TestScopeProposalsSquat`, `TestScopeMutationSweep`,
  `TestScopeProjectScopedAgent`, `TestScopeReads`, `TestScopeHeaderValidation`,
  `TestScopeProjectCategoryEndpoints`), the v5 `created_by` migration (`TestMigrationV5Fresh`,
  `TestMigrationV5ExistingDB`), the CLI exit-8 mapping (`TestExitCodeForOutOfScope`,
  `TestCmdClaimOutOfScopeExit8`, `TestCmdNextOutOfScopeExit8`, `TestClientSendsScopeHeader`,
  `TestCmdStatusBulkOutOfScope`), scoped identity (`TestInitScopedWritesJSON`,
  `TestInitScopedCategoryProject`, `TestInitProjectRequiresCategory`,
  `TestLegacyPlainIdentityUnscoped`, `TestScopeEnvOverride`, `TestWhoamiPrintsScope`,
  `TestParseScope`), and scoped waits (`TestWaitReadyScopedTimeout`, `TestWaitReadyScopedReleased`,
  `TestWaitReadyExplicitOutOfScopeExit8`).
  Phase R added 8 category-dashboard/scoped-feed tests: category stats
  (`TestListCategoriesCounts` in `store_test.go`), the `?category=` feed filter
  (`TestEventsCategoryFilter` in `server_test.go`), the category-scoped SSE stream + reconnect
  (`TestSSECategoryScopedStream`, `TestSSECategoryReconnectReplay` in `sse_test.go`), and the new
  `hub_test.go` unit tests of the in-memory fan-out (`TestHubCategoryScopedBroadcast`,
  `TestHubProjectScopedBroadcast`, `TestHubUnscopedBroadcast`, `TestHubBroadcastNilNoPanic`). The
  dashboard **rendering** (overview cards, hash routing, breadcrumb, per-view stream re-open, live
  count refresh) stays in the behavioral-JS gap below — the server surface those views ride on is
  the part that is Go-tested.
  Phase S added 17 scope-token tests: the store layer (`TestCreateToken_HashNotPlaintext`,
  `TestResolveToken`, `TestCreateToken_ScopeValidation`, `TestRevokeToken`), the HTTP surface
  (`TestTokenAdmin_RequiresUnscoped`, `TestTokenScopeOverridesHeader`, `TestInvalidTokenRejected` —
  incl. a DELETE-with-bogus-token-is-401-not-204 security-regression guard, `TestNoTokenPathUnchanged`,
  `TestTokenScopeMatrix`, `TestCreateTokenResponse`), the CLI (`TestCmdTokenNewWritesIdentity`,
  `TestCmdTokenNewRequiresScope`, `TestWhoamiPrintsTokenSet`, `TestClientSendsBearerNotScope`,
  `TestExitCodeForUnauthorized`, `TestDoOrFailUnauthorized`), and export/import round-trip
  (`TestExportImportWithTokens`).
  **Still untested:** behavioral dashboard JS — the **Manage** modal (category + project lists), the
  delete confirm
  flows (task/comment/project), the feed pagination button, the dependency section UI (prereq chips,
  add-prereq dropdown, blocks list), the **graph overlay** (layout, pan/zoom, transitive highlight,
  detail panel, live refresh), the search box and label chips/Labels section (Phase M), the
  editable modal Meta section (Phase P / ADR-031), the **CLI↔GUI parity affordances** (create/archive
  category, new-project category picker, project edit, board-filter popover, meta editing, release —
  ADR-031), the **category overview + hash routing** (overview cards,
  drill-down, breadcrumb/back, per-view stream re-open, debounced count refresh — Phase R), the
  **dark/light theme toggle** (toggle click, system-follow while unset, `am.theme` persistence —
  ADR-030), and other client-side logic — because the project deliberately
  adopts **no JS test runner** (preserves the no-npm/single-binary ethos; ADR-018). The `web_test.go`
  source guards mitigate regressions at the source level: `TestDashboardNoXSSSinks` for XSS sinks,
  `TestDashboardThemeAssets` for the theme-asset wiring (the override block, the FOUC script, the
  toggle button), and `TestDashboardParityAffordances` for the parity-affordance markers
  (create/archive-category, category picker, project edit, filter popover, editable meta, release);
  the dependency UI and the graph overlay are additional un-runner-tested JS covered by the same
  guard pattern.
  → `backend.md`, `frontend.md`, `decision-records.md` ADR-018, ADR-021, ADR-031.

## Documentation Gaps

- ~~No CI to enforce doc/code sync~~ — **RESOLVED (Phase F)**. CI (`.github/workflows/ci.yml`)
  runs build/vet/gofmt/test(-race)/JS-syntax/govulncheck on every push to `main` and on every PR.
  Drift is still possible for prose docs, but code/format/test regressions are now gated.
- Several decisions are **undocumented** (auth model, testing strategy, migrations, deletes, CI,
  versioning) → `decision-records.md` "Missing Decisions".
- ~~No CHANGELOG despite tagged releases~~ — **added** (`CHANGELOG.md`, Keep a Changelog format);
  `v0.1.0`–`v0.3.0` predate it and are summarized there.
- ~~Roadmap items live only in conversation~~ — **captured** in `ROADMAP.md` (phased, checkable plan
  for the gaps in this doc). Keep the two in sync as items land.

## Maintainability Concerns

- ~~`gofmt -l` is non-empty~~ — **fixed in Phase 0** (`cmd/am/update_test.go`, `cmd/am/version.go`
  formatted; `gofmt -l cmd/am` is now empty).
- `store.go` and `app.js` are the largest files and mix several responsibilities; fine now,
  watch for growth.
- No linter beyond `gofmt`/`go vet`; no pre-commit hooks. CI now enforces `gofmt`/`go vet`/
  `go test -race`/`govulncheck` on every push and PR, so format/vet/test drift is caught
  automatically. Pre-commit hooks are still absent (local runs remain manual).

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

1. **Add behavioral tests** for the atomic claim, validation/status mapping, and an XSS regression
   (`net/http/httptest` + a temp DB). Highest risk-reduction per effort.
2. **Decide & document the migration strategy** before any schema change to an existing table.
3. ~~**Add `go vet` + `go test` + `gofmt` + `govulncheck` CI**~~ — **DONE (Phase F)**. CI runs
   build/vet/gofmt/test(-race)/JS-syntax/govulncheck on push to `main` and PRs. (`/.github/workflows/ci.yml`)
4. **Run `gofmt -w cmd/am`** to clear current formatting drift (as its own small change).
5. **Record the missing decisions** (auth, testing, deletes, CI, versioning) as ADRs once chosen.
6. If remote access is ever wanted, treat it as an **auth + CSRF/`Host` + TLS** project per
   `security.md`, not a feature add-on.
