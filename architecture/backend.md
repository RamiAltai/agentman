# Backend Architecture

## Framework and Runtime

- **Go** (module `go 1.25.11`; `go.mod`). Standard-library HTTP — `net/http` with the Go 1.22+
  **method+pattern ServeMux** (e.g. `mux.HandleFunc("GET /api/tasks/{id}", …)` in
  `cmd/am/server.go`). No web framework.
- **SQLite** via `modernc.org/sqlite` v1.51.0 (pure Go, no cgo) — the only direct dependency.
- The server is started by `runServe()` in `cmd/am/main.go`:
  `http.Server{Addr: "127.0.0.1:" + port, ReadHeaderTimeout: 10s, BaseContext: …}`.

## API Structure

Routes are registered in one place — `Server.Handler()` (`cmd/am/server.go`):

```
GET   /api/categories               handleListCategories    ?archived=true includes archived
POST  /api/categories               handleCreateCategory    {slug,name?} (slug trimmed + lowercased; name defaults to slug; dup slug → 409)
POST  /api/categories/{slug}/archive   handleArchiveCategory   idempotent (no event on no-op)
POST  /api/categories/{slug}/unarchive handleUnarchiveCategory idempotent (no event on no-op)
GET   /api/projects                 handleListProjects      ?archived=true includes archived; ?category=<slug> scopes (and keeps an archived category inspectable)
POST  /api/projects                 handleCreateProject     {slug,name,category?} (empty category defaults to "general"; unknown → 404; archived → 400 category_archived)
PATCH /api/projects/{slug}          handlePatchProject      {slug?,name?,vault_project_id?,vault_path?} (uid/category never patchable; empty string clears a vault field; no-op → 200, no event; dup slug → 409)
POST  /api/projects/{slug}/archive    handleArchiveProject
POST  /api/projects/{slug}/unarchive  handleUnarchiveProject
GET   /api/tasks                    handleListTasks         ?project=&category=&status=&assignee=&limit=&ready=true|&blocked=true|&stale=<dur>|&q=<text>|&label=<l>  (no scope ⇒ hides archived-project AND archived-category tasks; category composes with every other filter; stale = assigned + not done + no activity for ≥ dur; q = substring match on title OR body, ASCII-case-insensitive, > 500 bytes → 400; label = exact match after normalization, invalid → 400)
POST  /api/tasks                    handleCreateTask        {project,title,body?,priority?,assignee?}  (archived category → 400 category_archived)
GET   /api/tasks/{id}               handleGetTask           (task + comments + recent events + depends_on + blocks)
PATCH /api/tasks/{id}               handlePatchTask         {status?,assignee?,title?,body?,priority?}  (hard-blocked if open prereqs + doing/done target)
POST  /api/tasks/next               handleNext              {project?,category?,assignee?} atomic pick+claim of the best ready task (todo, unassigned, no open prereqs, non-archived project AND category); 404 when none qualifies (or bad slug)
POST  /api/tasks/{id}/claim         handleClaim             (atomic; X-Agent = claimant; hard-blocked if open prereqs → 409 blocked; body {"steal_stale":"<dur>"} → StealStaleClaim stale takeover, 409 not_stale if still fresh)
POST  /api/tasks/{id}/comments      handleComment           {body}
POST  /api/tasks/{id}/deps          handleAddDep            {depends_on: <id-or-ref>}  add prerequisite edge
DELETE /api/tasks/{id}/deps/{depId} handleRemoveDep         remove prerequisite edge
POST  /api/tasks/{id}/labels        handleAddLabel          {label}  attach a label (idempotent; normalized)
DELETE /api/tasks/{id}/labels/{label} handleRemoveLabel     detach a label (idempotent no-op if absent)
GET   /api/events                   handleEvents            ?since=  | ?tail=  | ?before=  | ?project=  (no project ⇒ hides archived-project events)
GET   /api/stream                   handleStream            text/event-stream (SSE)
DELETE /api/tasks/{id}              handleDeleteTask        hard-delete task + comments + dep edges (cascade); 200 {"status":"deleted"}
DELETE /api/tasks/{id}/comments/{cid} handleDeleteComment  hard-delete one comment; 200 {"status":"deleted"}
DELETE /api/projects/{slug}         handleDeleteProject     hard-delete project + tasks + comments (cascade); 200 {"status":"deleted"}
GET   /api/projects/{slug}/graph    handleProjectGraph      {nodes:[]Task, edges:[]GraphEdge{from,to}}; 404 on missing project; read-only (no events)
/                                   http.FileServer(embed)  serves cmd/am/web/
```

`{id}` accepts a global id (`13`) or a project ref (`web-3`), resolved by `store.resolveTaskID`.
Responses are JSON via `writeJSON`; errors via `writeErr`.

`am db export`/`am db import`/`am db prune` have **no HTTP route** — they are CLI-only local-file
operations. The `db` command is dispatched in `cmd/am/main.go` *before* the HTTP client is built
(`cmdDB`, ahead of `NewClient()`), so it works directly on the SQLite file (`cmd/am/db.go`).

## Request Flow

When `--log` / `AGENTMAN_LOG` is enabled, the chain is wrapped OUTERMOST by `requestLogger`
(so guard 403s are also logged): `requestLogger(securityHeaders(hostGuard(csrfGuard(mux))))`.
Otherwise the chain is `securityHeaders(hostGuard(csrfGuard(mux)))` (Phase 0/ADR-011). Then:
`mux` → `handleX(w, r)` → decode JSON body (`decode`, capped at 1 MiB via `io.LimitReader`) →
call a `store.*` method → on success `hub.Broadcast(event)` **after** the store commits →
`writeJSON`. The actor for writes comes from the `X-Agent` header (`actorOf`, default `"human"`).
The security middleware rejects non-loopback `Host` (403) and cross-origin browser writes (403)
without affecting the CLI, reads, or SSE.

`requestLogger` wraps the response writer in a `statusRecorder` (captures the status code,
defaulting to 200; also implements `http.Flusher` so SSE connections continue to work when
logging is enabled). It logs one line per request after completion:
`METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default `"human"`), via the standard
`log` package to stderr. Note: a long-lived SSE connection logs once on disconnect with a
large latency (inherent).

## Business Logic

Lives in `cmd/am/store.go` (there is no separate "service" layer — the store *is* the domain
logic). Each mutating method returns `(result, *Event, error)`; the handler broadcasts the event.
Key methods: `CreateCategory`, `ListCategories`, `ArchiveCategory`, `UnarchiveCategory`,
`CreateProject(slug, name, category)`, `ListProjects(includeArchived bool, category string)`,
`PatchProject`, `ArchiveProject`,
`UnarchiveProject`, `DeleteProject`, `ListTasks`, `GetTask`, `CreateTask`, `PatchTask`,
`ClaimTask`, `StealStaleClaim`, `NextTask`, `AddComment`, `DeleteComment`, `ListEvents`, `RecentEvents`,
`ListEventsBefore`, `DeleteTask`, `AddDep`, `RemoveDep`, `AddLabel`, `RemoveLabel`, `ProjectGraph`.
`ArchiveProject`/`UnarchiveProject` (and their category counterparts) are transactional and
idempotent (no event when already in the target state); all four project lifecycle paths load
their response payload via the shared `getProjectTx`, so every project JSON carries the extended
fields (`uid`, `category`, vault binding).
`CreateTask` checks the target project's `archived_at` before the insert and returns
`ErrProjectArchived` if archived — creation into an archived project is rejected; it also checks
the project's **category** and returns `ErrCategoryArchived` (→ `400 category_archived`) when
that is archived. `CreateProject` applies the same two checks to its target category (unknown
slug → 404; archived → 400), defaults an empty category to `general` (keeps the dashboard's
`{slug,name}` POST working), and generates the `amp_` uid (`newUID`, with `isUniqueErr` retry).
`PatchProject` (Phase O) mirrors `PatchTask`: allowed keys `slug`/`name`/`vault_project_id`/
`vault_path` (vault fields ≤ 500 bytes, empty string clears), `uid`/`category_id` never
patchable, unknown keys ignored, no-op → success with no event, otherwise one `project.patched`
event with a compact delta.
`ListEvents`/`RecentEvents` use `LEFT JOIN projects p … LEFT JOIN categories c` and,
when no explicit `project=` filter is supplied, exclude events whose project OR category is
archived via `(events.project_id IS NULL OR (p.archived_at IS NULL AND c.archived_at IS NULL))`
— mirroring `ListTasks`. An explicit
`?project=<slug>` filter still returns that project's events. `ListEventsBefore(before, project,
limit)` applies the same archived filter and returns events with `id < before`,
newest-first (default limit 40, cap 200) — used by the `?before=` cursor branch in `handleEvents`
for backward pagination. `TaskFilter.Category` (`?category=`) composes with every other task
filter; with a category scope but no project scope, `ListTasks` still hides archived *projects*
but keeps the (possibly archived) category itself inspectable — same rule as the explicit-project
case.

`AddDep(taskID, dependsOnID, actor)` validates same-project membership, rejects self-deps, and
runs a recursive CTE (`wouldCycle`) to reject transitive cycles before inserting into `task_deps`.
Duplicate edges are idempotent (returns `nil, nil`). `RemoveDep` is also idempotent (no-op if the
edge does not exist). `ProjectGraph(slug)` is **read-only** (no events, no writes): it calls
`ListTasks(TaskFilter{Project: slug})` for nodes and queries `task_deps JOIN tasks` for edges,
returning `{nodes: []Task, edges: []GraphEdge{From, To}}` with edges oriented prereq→dependent.
Returns `ErrNotFound` for a missing project. `ListTasks` now accepts `TaskFilter.Ready` (todo tasks with zero open
prereqs) and `TaskFilter.Blocked` (tasks with ≥1 open prereq); it also selects `NPrereqs` and
`NOpenPrereqs` counts for every row via subqueries. `GetTask` additionally populates `DependsOn`
and `Blocks` slices (full `DepRef` objects).

**Labels & search** (Phase M): `AddLabel(taskID, label, actor)` / `RemoveLabel(taskID, label,
actor)` first run the input through `normalizeLabel` (trim + ASCII-lowercase, then validate:
1–50 bytes matching `^[a-z0-9._-]+$`; failure → `ErrValidation`). Both are idempotent —
re-adding a present label or removing an absent one commits with no event (`nil, nil`-style
no-op); otherwise they emit `task.labeled` / `task.unlabeled` with payload `{"label": l}` in the
same tx. Labeling deliberately does **not** bump `updated_at` (metadata only — refreshing the
activity timestamp would keep a stale claim alive, same precedent as dep edges). `ListTasks`
selects each task's labels via a `GROUP_CONCAT` subquery on `task_labels`; `TaskFilter.Label`
filters with an `EXISTS` equality match on the normalized label, and `TaskFilter.Query` (`?q=`)
matches `title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\'` with the pattern run through
`likeEscape` (escapes `%`, `_`, and `\` so user input can't act as wildcards) — substring,
ASCII-case-insensitive (SQLite LIKE semantics). The handler caps `?q=` at `maxTitleLen`
(500 bytes) → 400, bounding LIKE work.

`ClaimTask` and `PatchTask` call the helper `hasOpenPrereqs` before writing: if any prerequisite
task is not `done`, they return a `*BlockedError{OpenPrereqs: []int64{…}}`. `ClaimTask` blocks
unconditionally on open prereqs; `PatchTask` blocks only when the target status is `doing` or
`done` (other ops — edit, comment, assign, status→todo/blocked — proceed normally).

**Atomic claim** (`ClaimTask`) is the most important invariant — a single conditional statement:

```sql
UPDATE tasks SET assignee=?, status=CASE WHEN status='todo' THEN 'doing' ELSE status END, updated_at=…
 WHERE id=? AND assignee IS NULL AND status!='done'
RETURNING project_id, status;
```

Zero rows ⇒ loser; the code then distinguishes idempotent re-claim by the same agent (returns the
task, no event) from `*ConflictError` (owned by someone else) and `ErrNotFound`.

**Stale-claim takeover** (`StealStaleClaim`, Phase K / ADR-022) reuses the same trick with a
staleness predicate: `UPDATE … WHERE id=? AND status!='done' AND (assignee IS NULL OR updated_at <
cutoff) RETURNING …`, where the cutoff is computed in Go by `staleCutoff` (ISO-8601 UTC with the
exact `strftime('%Y-%m-%dT%H:%M:%fZ')` 3-digit-fraction format, so the lexicographic TEXT
comparison holds). Exactly one concurrent stealer wins; a still-fresh claim loses with a typed
`*NotStaleError{Assignee}`; a `done` task → `*ConflictError`; open prerequisites hard-block like a
normal claim; on an unclaimed task it degrades to a normal claim (plain `task.claimed` event);
re-stealing your own claim is an idempotent no-op (no event, `updated_at` untouched). A successful
takeover emits `task.reclaimed` with `{"assignee":[prev,new],"status":…,"stale_for":…}` in the same
tx. Both claim paths set `tasks.claimed_at`; `am drop` (unassign) clears it. The staleness filter
(`TaskFilter.Stale`, `?stale=<dur>` / `am ls --stale <dur>`) uses `updated_at`, not `claimed_at` —
any activity (claim, patch, status, comment) keeps a claim fresh.

**Atomic pick+claim** (`NextTask`, Phase L / ADR-023) extends the primitive with a subquery:
`UPDATE … WHERE id = (SELECT t.id … WHERE t.status='todo' AND t.assignee IS NULL AND
p.archived_at IS NULL AND c.archived_at IS NULL [AND t.project_id=?] [AND p.category_id=?]
AND NOT EXISTS (<open-prereq>) ORDER BY t.priority ASC,
t.id ASC LIMIT 1) RETURNING id, project_id`. The open-prereq `NOT EXISTS` matches ListTasks' Ready
filter exactly; ordering is priority ASC (0 = most urgent) with an id-ASC FIFO tiebreak
(deliberately not `am ls`'s `updated_at DESC` display order — a pickup queue drains oldest-first).
The archived-category exclusion is **unconditional** (scoped or not), like the archived-project
rule; the optional category scope (`{"category"?}` in the body / `am next -c`) composes with the
project scope.
Zero rows ⇒ `ErrNotFound` (nothing ready, or — same 404, accepted ambiguity — a bad project or
category slug).
Emits the existing `task.claimed` event with the same payload shape as `ClaimTask`. Tasks already
assigned to the caller are skipped (candidates require `assignee IS NULL`). `am wait` has **no
server-side component** — it is a CLI-side SSE consumer over the existing `/api/stream` (see
`cmd/am/wait.go` and ADR-023).

## Data Access

- One `*sql.DB` with **`SetMaxOpenConns(1)`** → single writer, so writes serialize and
  `SQLITE_BUSY` is effectively impossible (`cmd/am/store.go OpenStore`).
- Pragmas set on the DSN (applied per-connection at open): `busy_timeout(5000)`,
  `journal_mode(WAL)`, `foreign_keys(1)`, `synchronous(1)`.
- **All queries are parameterized** (`?` placeholders) — no string-concatenated SQL with user input.
- Mutations + their `events` row run in one `*sql.Tx`; broadcast happens only after commit so SSE
  never announces uncommitted state.

See `data-model.md` for the schema.

## Models and Schemas

Go structs in `cmd/am/store.go`: `Category`, `Project`, `Task`, `Comment`, `Event`, `TaskFilter`,
`CreateTaskInput`. SQL schema in `cmd/am/schema.sql` (embedded via `//go:embed schema.sql`).
`Category` and `Project` carry a stable `uid` (`amc_`/`amp_` + 16 hex, `newUID`); `Project`
additionally carries `category` (slug), `vault_project_id`, and `vault_path`.

## Authentication and Authorization

**No authentication.** The `X-Agent` header is an *actor label* for attribution, not a credential —
any caller can claim any identity. Access control is the `127.0.0.1` bind, now hardened by the
Phase 0 guardrails (Host allowlist + write-CSRF guard, `server.go`, ADR-011) which block
browser-driven cross-origin/DNS-rebinding attacks but are **not** auth (any local process is still
trusted). No per-resource authorization. See `security.md` (ADR-002/ADR-011 in `decision-records.md`).

## Validation

- Status validated against `validStatus` map and a SQL `CHECK (status IN (...))` constraint
  (`store.go`, `schema.sql`).
- Empty title / slug / comment body rejected with `ErrValidation`; slug must not contain spaces
  (`CreateProject`, `CreateCategory` — category slugs are additionally trimmed + lowercased
  server-side; `PatchProject` validates a new slug the same way).
- Priority coerced via `toInt`. Unknown PATCH keys are ignored (only known fields applied in
  `PatchTask`).
- Handlers map `ErrValidation` → HTTP 400.
- Creating a task into an archived project is rejected: `CreateTask` returns `ErrProjectArchived`
  → HTTP 400 `{"error":"project_archived"}`. Creating a task — or a project — under an archived
  **category** is likewise rejected: `ErrCategoryArchived` → HTTP 400
  `{"error":"category_archived"}` (CLI exit 5; no new exit codes).
- Durations (`?stale=` query param, `steal_stale` claim-body field) are parsed with
  `time.ParseDuration` (Go syntax — `30m`, `48h`, not `2d`); a malformed or non-positive value →
  HTTP 400 `{"error":"invalid"}` (CLI exit 5).

## Background Jobs

No job queue. Long-lived goroutines only:
- **SSE connections** — one goroutine per `handleStream` request, with a 15s heartbeat ticker;
  cleaned up on `r.Context().Done()` (`cmd/am/server.go`, `cmd/am/hub.go`).
- **Startup update check** — `checkForUpdate()` fires a single background goroutine (4s timeout,
  silent on error) (`cmd/am/update.go`).
- **Graceful shutdown** — SIGINT/SIGTERM → cancel base context (unblocks SSE) → `Shutdown(3s)` →
  `PRAGMA wal_checkpoint(TRUNCATE)` → close (`runServe`, `store.Close`).

## External Integrations

- `proxy.golang.org` — version check (`checkForUpdate`); opt out with `AGENTMAN_NO_UPDATE_CHECK=1`.
- `go install …@<ver>` shelled out via `os/exec` in `am update` (`cmdUpdate`).

## Error Handling

Sentinel errors in `store.go`: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrProjectArchived`,
`ErrCategoryArchived`,
typed `*ConflictError{Assignee}`, typed `*BlockedError{OpenPrereqs []int64}`, and typed
`*NotStaleError{Assignee}`. `writeErr`
(`server.go`) maps them: 404 / 409 / 400, with
`ConflictError` → `409 {"error":"already_claimed","assignee":…}`,
`BlockedError` → `409 {"error":"blocked","open_prereqs":[…]}`,
`NotStaleError` → `409 {"error":"not_stale","assignee":…}`,
`ErrProjectArchived` → `400 {"error":"project_archived"}`,
`ErrCategoryArchived` → `400 {"error":"category_archived"}`,
`ErrValidation` → `400`; anything else → **HTTP 500 with a generic `{"error":"internal"}` body**
(the real error is logged server-side via `log.Printf("agentman: internal error: %v", err)` to
stderr — it is never sent to the client). Delete handlers (`handleDeleteTask`,
`handleDeleteComment`, `handleDeleteProject`) return `404` via `writeErr` when the target is
missing (`ErrNotFound`). The CLI re-maps HTTP status to **exit codes** via `client.go
exitCodeFor` (the single source, used by `doOrFail` and the bulk `status`/`assign` loop):
`3` not found · `4` conflict/blocked/not-stale · `5` validation/project_archived · `6` server down
· `1` other; plus `7` = `am wait` timeout (CLI-side, no HTTP status involved).
A `blocked` 409 prints e.g. `claim: #3 blocked — prereqs not done (#1 #2)`; a `not_stale` 409
prints e.g. `claim: #3 held by agent-a (not stale yet)`. Bulk `am status`/`am assign` print one
stderr line per failing id (`status: #13 not_found`), continue, and exit with the first failure's
mapped code.

## Observability

Minimal: `log.Printf` to stderr for startup, shutdown, the update banner, and `log.Fatalf` on a
fatal listen error. **No structured logging, metrics, or tracing.**

**Opt-in request logging** is available via `am serve --log` or the `AGENTMAN_LOG` env var (any
non-empty value; use `AGENTMAN_LOG=1`). When enabled, `runServe` logs `request logging enabled`
at startup and installs the `requestLogger` middleware outermost in the chain. It logs one line
per request after completion: `METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default
`"human"`). Plain `log.Printf` lines to stderr — not structured logging. Off by default.

## Testing

There are ten test files (run `go test -race ./cmd/am/`; 174 tests, all green):
- `cmd/am/update_test.go` — version-comparison logic.
- `cmd/am/store_test.go` — atomic-claim race (concurrent, `-race`-clean), events-cursor monotonicity,
  store CRUD + validation (`ErrValidation`), project archive/unarchive round-trip + idempotency,
  hard-delete cascade/not-found (`TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
  `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades`), graph store:
  `TestProjectGraph` (nodes + edges shape), `TestProjectGraphMissingProject` (ErrNotFound), and
  stale-claim recovery (Phase K): `TestStealStaleClaim`, `TestStealRaceExactlyOneWinner`
  (concurrent stealers, exactly one winner), `TestListTasksStaleFilter`, `TestClaimSetsClaimedAt`,
  `TestDropClearsClaimedAt`, and `am next` (Phase L): `TestNextTaskPicksHighestPriorityReady`,
  `TestNextTaskFIFOWithinPriority`, `TestNextTaskProjectScoping`, `TestNextTaskNoneReady`,
  `TestNextTaskRaceDistinctWinners` (concurrent pickers get distinct tasks; one task → one winner),
  `TestNextTaskEmptyAgentValidation`, and search + labels (Phase M): `TestListTasksQueryFilter`,
  `TestListTasksQueryEscapesLikeWildcards` (LIKE wildcards in `?q=` are escaped, not interpreted),
  `TestAddRemoveLabel`, `TestLabelValidation`, `TestListTasksLabelFilter`,
  `TestAddLabelDoesNotBumpUpdatedAt`, `TestDeleteTaskCascadesLabels`,
  `TestTaskLabelsTableExistsOnReopenedDB`, and categories (Phase O): `TestCreateCategory`,
  `TestArchiveUnarchiveCategory`, `TestCreateProjectWithCategory`, `TestPatchProject`,
  `TestCategoryArchiveCascade`, `TestListTasksCategoryFilterComposes`,
  `TestNextTaskCategoryScoping`, `TestCreateTaskArchivedCategory`.
- `cmd/am/server_test.go` — validation→status mapping (400/404/409), `hostGuard`, `csrfGuard`,
  `securityHeaders`, `listenAddr` loopback regression, archive/unarchive endpoints + 404,
  hard-delete HTTP endpoints (`TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`,
  `TestDeleteCommentEndpoint`), Phase D: `TestWriteErrHidesInternalDetail` (500 returns generic
  body, not raw error), `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`, and
  graph endpoint: `TestProjectGraphEndpoint` (200 with correct nodes + edges),
  `TestProjectGraphEndpoint404` (missing project → 404), Phase K: `TestListTasksStaleParam`
  (`?stale=` filter + 400 on bad duration), `TestStealStaleEndpoint` (steal body, `not_stale` 409),
  Phase L: `TestNextEndpoint` (FIFO picks, 404 when drained), `TestNextEndpointProjectBody`,
  and Phase M: `TestListTasksQueryParam` (`?q=` filter + 400 on over-long input),
  `TestLabelEndpoints` (add/remove label endpoints + validation 400),
  and Phase O: `TestCategoryEndpoints`, `TestProjectPayloadAndCategoryFilter`,
  `TestListTasksCategoryParam`, `TestNextEndpointCategoryBody`, `TestPatchProjectEndpoint`,
  `TestCreateTaskArchivedCategory400`
  (via `net/http/httptest`).
- `cmd/am/migrate_test.go` — migration runner (apply/skip/idempotent/rollback), incl. the v2 step
  that adds `projects.archived_at`, the v3 step that adds `tasks.claimed_at`
  (`TestMigrationV3AddsClaimedAt`), and the v4 category/stable-ID step (`TestMigrationV4Fresh`,
  `TestMigrationV4ExistingDB` — seeded `general`, distinct `amp_` uids, task data untouched,
  no double-apply on reopen); `TestOpenStoreRejectsNewerSchema` (the open-time version ceiling).
- `cmd/am/db_test.go` — `db export`/`import` roundtrip + file perms (0o600), backup creation + perms,
  garbage rejection, server-liveness check; `TestPruneEventsKeep`, `TestPruneEventsBefore`,
  `TestPruneEventsBeforeSameDayBoundary` (prune); Phase O: `TestExportContainsCategories`,
  `TestImportPreCategorySnapshot` (v1-baseline required-table set keeps old snapshots
  importable), `TestImportRejectsNewerSchema`.
- `cmd/am/cli_test.go` — CLI command-path + exit-code tests (Phase E1). Exercises verbs against a
  real `httptest` server via a directly-constructed `Client`, using `captureStdout`/`captureExit`
  helpers. Covers: `cmdNew` prints only the numeric id; `cmdLs` produces terse output; mutations
  (`cmdStatus`/`cmdNote`/`cmdDrop`) are silent on success; and the exit-code mapping in
  `client.go doOrFail` — 3 (not found), 4 (conflict), 5 (validation/`project_archived`), 6 (server
  down). Also table-tests for `parse`/`Args` and the pure formatters
  (`taskLine`/`statusShort`/`assignee`/`trunc`/`apiErr`); Phase K added `TestExitNotStale`
  (exit 4 + `not stale yet` message) and `TestStaleFlagsWireFormat` (`--stale` / `--steal-stale`
  wire encoding); Phase L added `TestCmdNextPrintsOnlyID`, `TestExitNextNoneReady` (exit 3),
  `TestCmdStatusBulk`, `TestCmdStatusBulkPartialFailure` (loop continues, stderr names the failing
  id, exit = first failure's code), `TestCmdAssignBulk` (incl. `me`/`-` and single-id regression);
  Phase M added `TestCmdLsGrepWireFormat` (`--grep`/`--label` → `?q=`/`?label=` wire encoding),
  `TestCmdLabelAddRemove` (`+add`/`-remove` token dispatch), `TestCmdLabelPrintsLabels` (bare
  `am label <id>` prints the labels), `TestCmdLabelUsage` (usage errors);
  Phase O added `TestCmdCategoryVerbs`, `TestCmdProjectNewRequiresCategory` (exit 5 without
  `-c`/`AGENTMAN_CATEGORY`), `TestCmdProjectEdit` (incl. explicit-empty vault clears),
  `TestCmdLsCategoryWireFormat`, `TestCmdNextCategory`, and the `-c` alias-rewrite regression
  pair `TestCmdShowDashCStillPrintsComments` / `TestRewriteShowComments`.
- `cmd/am/wait_test.go` — `am wait` (Phase L). `TestWaitDoneAlreadySatisfied`,
  `TestWaitDoneEventArrives`, `TestWaitDoneCrossProject` (`AGENTMAN_PROJECT` must not scope the
  `--done` stream), `TestWaitReadyOnPrereqDone` (blocked task becomes ready when its
  prereq is done), `TestWaitTimeout` (exit 7), `TestWaitTaskNotFound` (exit 3),
  `TestWaitServerDown` (exit 6), `TestWaitUsageErrors`, `TestParseWaitTimeout` (bare seconds +
  Go durations), `TestWaitBadTimeoutExit5`; Phase O added `TestWaitReadyCategoryScoped`,
  `TestWaitReadyCategoryEnv`, `TestWaitReadyCategoryTimeout` (`-c`/`AGENTMAN_CATEGORY` scope the
  readiness re-check; the stream stays unscoped).
- `cmd/am/sse_test.go` — SSE streaming + reconnect (Phase E2). `TestSSEDeliversLiveEvent`
  subscribes to `/api/stream`, creates a task, and asserts the `task.created` event arrives live.
  `TestSSEReplayOnReconnect` reconnects with `Last-Event-ID` and asserts that events created while
  disconnected are replayed and deduplicated (every replayed id is strictly greater than the resume
  cursor).
- `cmd/am/identity_test.go` — identity (Phase E3). `cmdInit`→`resolveAgent` roundtrip,
  `AGENTMAN_AGENT` env override wins, `sanitizeType` table, `newIdentity` format. Isolates via the
  `AGENTMAN_AGENT_FILE` env seam so the real `~/.agentman` is never written.
- `cmd/am/web_test.go` — dashboard XSS-sink guard (Phase E4). `TestDashboardNoXSSSinks` reads the
  embedded `web/app.js` + `web/index.html` via the `webFS` embed.FS and asserts that none of
  `.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear — a source-level
  regression guard that locks in the `el()`/`textContent` XSS-safe DOM convention.

The +24 dependency tests are spread across existing files: `store_test.go` (cycle/self/cross-project
rejection, idempotent add/remove, cascade on task delete, `NPrereqs`/`NOpenPrereqs` counts,
`Ready`/`Blocked` list filters, hard-block on `ClaimTask` and `PatchTask`, fresh-DB table existence);
`server_test.go` (HTTP add/remove dep endpoints, `?ready=`/`?blocked=` query params, 409 blocked
response for claim and patch). The +4 graph tests (`TestProjectGraph`, `TestProjectGraphMissingProject`
in `store_test.go`; `TestProjectGraphEndpoint`, `TestProjectGraphEndpoint404` in `server_test.go`)
brought the total to **95** at the time; Phase J's hygiene tests and Phase K's 10 stale-claim
tests brought it to **107**; Phase L's 23 work-loop tests brought it to **130**; Phase M's 14
findability tests (8 store, 2 server, 4 CLI — listed above) brought it to **144**; Phase O's 30
category/stable-ID/vault/migration tests (8 store, 6 server, 7 CLI, 3 wait, 3 db, 3 migrate —
listed above) bring it to **174**.

SSE streaming/reconnect, CLI verbs, exit-code mapping, and identity are now covered. The
dashboard has a source-level XSS-sink guard but **no behavioral JS tests** — the project
deliberately adopts no JS test runner (preserves the single-binary/no-npm ethos). (See
`known-risks-and-gaps.md`.)

## Where to Add New Features

- **New endpoint:** register it in `Server.Handler()` (`server.go`), add a `handleX`, add the
  backing `store.*` method, and (if it mutates) insert an `events` row in the same tx + broadcast.
- **New task field:** add the column in `schema.sql`, the struct field in `store.go`, thread it
  through `CreateTask`/`PatchTask`/`getTaskTx`, the API, and the dashboard (`web/`).
- **New event kind:** emit via `insertEvent(...)` and handle it in `web/app.js` `evText`/`describeText`.
  Current kinds (21 total): `task.created`, `task.claimed`, `task.reclaimed`, `task.status`,
  `task.assign`, `task.patched`, `task.deleted`, `task.dep_added`, `task.dep_removed`,
  `task.labeled`, `task.unlabeled`, `comment.added`, `comment.deleted`, `project.created`,
  `project.archived`, `project.unarchived`, `project.patched`, `project.deleted`,
  `category.created`, `category.archived`, `category.unarchived`. Events with no project ref
  (the `category.*` kinds) need explicit `evText`/`describeText` cases — the default branch
  renders a literal "null" ref.

## Risks and Gaps

- **Migration runner is now exercised** — Phase 0 added `runMigrations` (ADR-010); Phase 2's first
  step (`ALTER TABLE projects ADD COLUMN archived_at TEXT`) proved the
  additive-column path end-to-end, and v3 (`claimed_at`) and v4 (categories/stable IDs/vault
  binding, `currentSchemaVersion = 4`) followed it. A DB *newer* than the binary is **no longer
  accepted silently** — `OpenStore` refuses it with a clear "upgrade am" error (Phase O).
- **Single-writer** caps write throughput; fine for a personal board, unproven at scale.
- ~~**500s leak raw error strings**~~ — **fixed (Phase D1)**; 500s now return a generic `{"error":"internal"}` body; detail is logged server-side only.
- **No request size/time limits** beyond a 1 MiB body cap and `ReadHeaderTimeout`.
