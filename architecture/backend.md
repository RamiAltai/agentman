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
GET   /api/categories               handleListCategories    ?archived=true includes archived; each element is a CategoryStat (Category + counts{todo,doing,blocked,done} over non-archived projects + active_agents in a 30-min window); always present, no scope enforcement
POST  /api/categories               handleCreateCategory    {slug,name?} (slug trimmed + lowercased; name defaults to slug; dup slug → 409)
POST  /api/categories/{slug}/archive   handleArchiveCategory   idempotent (no event on no-op)
POST  /api/categories/{slug}/unarchive handleUnarchiveCategory idempotent (no event on no-op)
GET   /api/projects                 handleListProjects      ?archived=true includes archived; ?category=<slug> scopes (and keeps an archived category inspectable)
POST  /api/projects                 handleCreateProject     {slug,name,category?} (empty category defaults to "general"; unknown → 404; archived → 400 category_archived)
PATCH /api/projects/{slug}          handlePatchProject      {slug?,name?,vault_project_id?,vault_path?} (uid/category never patchable; empty string clears a vault field; no-op → 200, no event; dup slug → 409)
POST  /api/projects/{slug}/archive    handleArchiveProject
POST  /api/projects/{slug}/unarchive  handleUnarchiveProject
GET   /api/tasks                    handleListTasks         ?project=&category=&status=&assignee=&limit=&ready=true|&blocked=true|&stale=<dur>|&q=<text>|&label=<l>|&meta_key=<k>  (no scope ⇒ hides archived-project AND archived-category tasks; category composes with every other filter; stale = assigned + not done + no activity for ≥ dur; q = substring match on title OR body, ASCII-case-insensitive, > 500 bytes → 400; label = exact match after normalization, invalid → 400; meta_key = key-presence match after normalization, invalid → 400; rows include meta)
POST  /api/tasks                    handleCreateTask        {project,title,body?,priority?,assignee?,meta?}  (archived category → 400 category_archived; empty meta values → 400)
GET   /api/tasks/{id}               handleGetTask           (task + comments + recent events + depends_on + blocks + meta)
PATCH /api/tasks/{id}               handlePatchTask         {status?,assignee?,title?,body?,priority?,meta?}  (hard-blocked if open prereqs + doing/done target; meta upsert — empty value removes the key; meta-only patch doesn't bump updated_at)
POST  /api/tasks/next               handleNext              {project?,category?,assignee?,meta_key?} atomic pick+claim of the best ready task (todo, unassigned, no open prereqs, non-archived project AND category, carrying meta_key if given); 404 when none qualifies (or bad slug)
POST  /api/tasks/{id}/claim         handleClaim             (atomic; X-Agent = claimant; hard-blocked if open prereqs → 409 blocked; body {"steal_stale":"<dur>"} → StealStaleClaim stale takeover, 409 not_stale if still fresh)
POST  /api/tasks/{id}/comments      handleComment           {body}
POST  /api/tasks/{id}/deps          handleAddDep            {depends_on: <id-or-ref>}  add prerequisite edge
DELETE /api/tasks/{id}/deps/{depId} handleRemoveDep         remove prerequisite edge
POST  /api/tasks/{id}/labels        handleAddLabel          {label}  attach a label (idempotent; normalized)
DELETE /api/tasks/{id}/labels/{label} handleRemoveLabel     detach a label (idempotent no-op if absent)
GET   /api/events                   handleEvents            ?since=  | ?tail=  | ?before=  | ?project=  | ?category=  (no project ⇒ hides archived-project events; ?category= scopes to the category's projects and EXCLUDES category-level NULL-project events; unknown category → 404)
GET   /api/stream                   handleStream            text/event-stream (SSE); ?project= or ?category= scopes the live stream + gap-replay (same NULL-project exclusion; project.created delivered regardless; unknown category ignored silently → unfiltered)
DELETE /api/tasks/{id}              handleDeleteTask        hard-delete task + comments + dep edges (cascade); 200 {"status":"deleted"}
DELETE /api/tasks/{id}/comments/{cid} handleDeleteComment  hard-delete one comment; 200 {"status":"deleted"}
DELETE /api/projects/{slug}         handleDeleteProject     hard-delete project + tasks + comments (cascade); 200 {"status":"deleted"}
GET   /api/projects/{slug}/graph    handleProjectGraph      {nodes:[]Task, edges:[]GraphEdge{from,to}}; 404 on missing project; read-only (no events)
POST  /api/tokens                   handleCreateToken       {scope:"cat[/proj]"} mint a scope-bound bearer token; 201 {id,scope,token,created_at} — the ONLY response carrying the plaintext token; requires an UNSCOPED caller (tokenAdminGuard → 403 if scoped); scope validated (unknown cat/proj → 404, cross-category bind/archived cat → 400)
GET   /api/tokens                   handleListTokens        []Token {id,category,project?,created_at,revoked_at?}; never token/token_hash; requires unscoped
POST  /api/tokens/{id}/revoke       handleRevokeToken       200 with the token metadata; 404 unknown/already-revoked; requires unscoped
/                                   http.FileServer(embed)  serves cmd/am/web/
```

**Token-admin guard (Phase S / ADR-029).** The three `/api/tokens` routes share
`(s *Server) tokenAdminGuard(w, r)`: it runs `scopeOf(r)` and refuses **any** request carrying a scope
— whether an `X-Agent-Scope` header OR a valid bearer token — with `denyScope` → `403`. Only a fully
unscoped caller (the human at the CLI/dashboard) administers tokens, so a confined agent can never
mint a token for another scope (the boundary crux). A bad/revoked bearer token still 401s here
(`scopeOf` surfaces `ErrInvalidToken`) rather than being mistaken for "unscoped". It mirrors the
`handleCreateCategory` `if !sc.IsZero() → denyScope` precedent. An invalid/revoked bearer token →
`ErrInvalidToken` → `401 {"error":"unauthorized"}` on **any** endpoint (every handler inherits it
through `scopeOf`) → CLI **exit 9**.

`{id}` accepts a global id (`13`) or a project ref (`web-3`), resolved by `store.resolveTaskID`.
Responses are JSON via `writeJSON`; errors via `writeErr`.

**Scope enforcement (Phase Q / ADR-027; tokens Phase S / ADR-029).** Every handler that mutates or
names a resource runs a scope pre-check via `(s *Server) scopeOf(r)` — the **sole** reader of request
scope; no handler reads `Authorization` or `X-Agent-Scope` directly. **Precedence (Phase S):** a
bearer token WINS — `scopeOf` resolves `Authorization: Bearer <tok>` (`bearerToken`, case-insensitive
per RFC 7235) to the token's server-bound scope via `store.ResolveToken` and **ignores any
`X-Agent-Scope` header**; an unknown/revoked token is `ErrInvalidToken` (→ `401 unauthorized` → exit
9), NEVER a silent fallthrough to the header or zero scope. With no token, the `X-Agent-Scope` header
is the scope (absent = unscoped, passes everything; malformed = 400). `scopeOf` is a `*Server` method
(Phase S) precisely so it can reach the store to resolve tokens while staying the one resolution
point. Out-of-scope → `ErrOutOfScope` → `403 {"error":"out_of_scope"}` → CLI exit 8. The per-route
picture:
- **Task mutations** — `checkTaskMut` (the task must be in scope): `POST …/claim` (covers
  `steal_stale` too), `PATCH /api/tasks/{id}` (covers each id of a bulk `am status`/`am assign`),
  `DELETE /api/tasks/{id}`, `DELETE …/comments/{cid}`, `POST/DELETE …/deps[/…]` (checked on the
  *dependent* task only — the store's same-project dep rule covers the prereq), `POST/DELETE
  …/labels[/…]`.
- **`POST /api/tasks`** — `checkCreate`: target project in scope **or** the proposals project; for a
  project-scoped agent anything ≠ its project is 403 (no existence check); for a category-scoped
  agent an unknown slug stays **404** (matching unscoped create).
- **`POST /api/tasks/{id}/comments`** — `checkComment`: in scope, OR (task in the proposals project
  AND `created_by == X-Agent`; NULL/empty `created_by` never matches).
- **`POST /api/tasks/next`** — `narrowScope(allowProposals=false)` merges the scope into the
  `NextFilter` **before** `NextTask`, inside the atomic pick+claim; explicit out-of-scope
  `project`/`category` → 403, absent ones injected from the scope. The proposals carve-out does
  **not** extend to next.
- **`GET /api/tasks`** — `narrowScope(allowProposals=true)`: explicit `?project=`/`?category=` that
  contradicts the scope → 403 (loud); missing filters filled from the scope (silent narrowing);
  `?project=<proposals>` allowed. Same path scopes `am wait --ready`'s REST re-check.
- **`GET /api/tasks/{id}`** — `checkTaskRead`: 403 out of scope (named reads fail loudly);
  proposals tasks 200.
- **`GET /api/projects/{slug}/graph`** — `checkProjectRead` (project-level; proposals allowed).
- **`POST /api/projects`** — allowed only for a category-scoped agent in its OWN effective category
  (empty body category = `general`); 403 for any other category and for project-scoped agents.
- **`PATCH/DELETE /api/projects/{slug}`, archive/unarchive** — `checkProjectMut` (project in scope;
  no proposals carve-out).
- **`POST /api/categories`, archive/unarchive** — 403 for ANY scoped agent (the category layer is
  above every scope).
- **`POST/GET /api/tokens`, `POST /api/tokens/{id}/revoke`** — `tokenAdminGuard`: 403 for ANY scoped
  caller (header OR a valid bearer token); only an unscoped caller mints/lists/revokes (mint-requires-
  unscoped, Phase S). A bad bearer token 401s here, not 403.
- **Untouched by scope enforcement:** `GET /api/events`, `GET /api/stream`, `GET /api/projects`,
  `GET /api/categories` (list endpoints stay unfiltered against `X-Agent-Scope`; the task layer is
  the enforcement point). Phase R added an **unscoped** `?category=` *lens* to `/api/events` +
  `/api/stream` for the human dashboard — a query-param choice, not an identity scope — which
  closes the dashboard side of the Phase Q feed/stream residual; the agent SSE stream (`am wait`)
  still streams unscoped by design. See `known-risks-and-gaps.md`.

The proposals carve-out project is `Server.proposals` (a `Scope`), set from `--proposals CAT/PROJ` /
`AGENTMAN_PROPOSALS` (default `meta/proposals`; `NewServer` defaults to `{meta, proposals}`). The
single helper `isProposals(slug)` matches the **(category, project) pair** — a same-slug project in
another category is not the carve-out (slugs are globally unique, so the pair check blocks a
slug-squat); a missing designated project leaves the gate open and the store 404s (the carve-out
stays inert). Every denial is logged (`denyScope`): `agentman: out_of_scope: actor=<id> scope=<scope>
<METHOD> <PATH>` — **log-only, no event kind**. The check helpers and `narrowScope` take the
`*http.Request` so the log line can carry actor/method/path; the checks run **outside** the store
transaction, which is sound only because `task→project` and `project→category` are immutable (the
`PatchTask`/`PatchProject` scope-note comments record that they must move in-tx if a move feature
ever ships).

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
Key methods: `CreateCategory`, `ListCategories`, `ListCategoriesWithStats` (Phase R),
`ProjectIDsInCategory` (Phase R), `ArchiveCategory`, `UnarchiveCategory`,
`CreateProject(slug, name, category)`, `ListProjects(includeArchived bool, category string)`,
`PatchProject`, `ArchiveProject`,
`UnarchiveProject`, `DeleteProject`, `ListTasks`, `GetTask`, `CreateTask`, `PatchTask`,
`ClaimTask`, `StealStaleClaim`, `NextTask`, `AddComment`, `DeleteComment`, `ListEvents`, `RecentEvents`,
`ListEventsBefore`, `DeleteTask`, `AddDep`, `RemoveDep`, `AddLabel`, `RemoveLabel`, `ProjectGraph`,
the token methods `CreateToken`/`ListTokens`/`RevokeToken`/`ResolveToken` (Phase S),
plus the meta helpers `normalizeMetaKey` and `applyMetaTx` (Phase P).
`ArchiveProject`/`UnarchiveProject` (and their category counterparts) are transactional and
idempotent (no event when already in the target state); all four project lifecycle paths load
their response payload via the shared `getProjectTx`, so every project JSON carries the extended
fields (`uid`, `category`, vault binding).
`CreateTask` checks the target project's `archived_at` before the insert and returns
`ErrProjectArchived` if archived — creation into an archived project is rejected; it also checks
the project's **category** and returns `ErrCategoryArchived` (→ `400 category_archived`) when
that is archived. It writes `tasks.created_by` from `CreateTaskInput.Actor` (Phase Q; the handler
passes `actorOf(r)`, default `"human"`).
The store also exposes two thin scope-support reads: `taskScope(id)` (category slug, project slug,
and `created_by` for a task in one SELECT — everything the scope checks need; `ErrNotFound` for an
unknown id) and `projectCategory(slug)` (a project's category slug; `ErrNotFound` for an unknown
project). `CreateProject` applies the same two checks to its target category (unknown
slug → 404; archived → 400), defaults an empty category to `general` (keeps the dashboard's
`{slug,name}` POST working), and generates the `amp_` uid (`newUID`, with `isUniqueErr` retry).
`PatchProject` (Phase O) mirrors `PatchTask`: allowed keys `slug`/`name`/`vault_project_id`/
`vault_path` (vault fields ≤ 500 bytes, empty string clears), `uid`/`category_id` never
patchable, unknown keys ignored, no-op → success with no event, otherwise one `project.patched`
event with a compact delta.
`ListEvents`/`RecentEvents` use `LEFT JOIN projects p … LEFT JOIN categories c` and,
when no explicit `project=`/`category=` filter is supplied, exclude events whose project OR
category is archived via `(events.project_id IS NULL OR (p.archived_at IS NULL AND c.archived_at IS
NULL))` — mirroring `ListTasks`. An explicit
`?project=<slug>` filter still returns that project's events. Since Phase R all three event
readers take a **`category`** parameter — `ListEvents(since, project, category, limit)`,
`ListEventsBefore(before, project, category, limit)`, `RecentEvents(project, category, limit)`. A
`?category=<slug>` resolves to `c.id=?` (via `categoryID`; unknown slug → `ErrNotFound` → 404),
matching only events whose project lives in the category and thereby **intentionally excluding
category-level (NULL `project_id`) events** — those belong to the All/overview feed, not a single
category's drill-down. `category` composes with `project` (ANDed). `RecentEvents` was refactored to
build its WHERE clause from a `[]string` slice (like `ListProjects`) since it now joins up to three
conditions; `ListEvents`/`ListEventsBefore` kept the incremental `q +=` style.
`ListEventsBefore` is used by the `?before=` cursor branch in `handleEvents` for backward
pagination (default limit 40, cap 200). `TaskFilter.Category` (`?category=`) composes with every other task
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

**Task metadata** (Phase P / ADR-026): tasks carry free-form `key=value` pairs in the
`task_meta` table. Keys are normalized by `normalizeMetaKey` (reuses `labelRe`/`maxLabelLen`:
trim + lowercase, 1–50 chars of `a-z 0-9 . _ -`; failure → `ErrValidation` with its own message
text); values are opaque, 1–500 bytes (`maxTitleLen`). `CreateTask` validates every pair up front
(empty values rejected — removal has no meaning at create) and writes the rows + a `"meta"` field
in the `task.created` event data in the insert tx. `PatchTask` routes a `"meta"` object through
`applyMetaTx(tx, taskID, meta)` — a sorted-key walk that upserts via
`INSERT … ON CONFLICT DO UPDATE`, deletes on an empty value (absent key = silent no-op), and
returns a delta map (`{"k": [old, new]}`, `null` = absent) merged into the `task.patched` event;
any error aborts the caller's tx, so multi-key patches are all-or-nothing. Both paths reject two
raw keys that normalize to the same key (`duplicate meta key after normalization`). A
**meta-only patch skips the `updated_at` bump** (metadata must not refresh a stale claim — the
label/dep precedent); mixed patches still bump. `ListTasks` filters by **key presence** via
`TaskFilter.MetaKey` (`?meta_key=`, an `EXISTS` on `task_meta`) and stitches each row's `meta`
with **one follow-up SELECT** (values may contain `,`/`=`, so the labels `GROUP_CONCAT` trick is
unsafe); `GetTask` loads meta too, but `getTaskTx` deliberately does **not** (labels parity —
PATCH/claim responses omit it). **No new event kinds, error codes, or exit codes** were added.

**Scope tokens** (Phase S / ADR-029): `CreateToken(sc Scope)` validates the scope (category exists +
non-archived; a named project exists AND belongs to that category — cross-category bind → `ErrValidation`,
unknown → `ErrNotFound`, archived → `ErrCategoryArchived`), then inserts a row with `id = newUID("tk_")`,
`token_hash = hashToken(newToken())`, retrying on a UNIQUE collision (`isUniqueErr`, the uid precedent);
it returns the **plaintext once** plus the public `*Token` metadata — the plaintext (`amt_` + 32 hex,
16 bytes of `crypto/rand`) is never stored, only its sha256 hash. `ListTokens` never selects
`token_hash` (the hash never leaves the store). `RevokeToken(id)` is a conditional
`UPDATE … WHERE id=? AND revoked_at IS NULL` — an unknown id OR an already-revoked token both yield
`ErrNotFound` (a no-op revoke is a 404, never silent success). `ResolveToken(plaintext)` looks up
`token_hash = hashToken(plaintext)` and returns the bound `Scope`; an unknown OR revoked token yields
the new sentinel **`ErrInvalidToken`** — NEVER a zero (allow-everything) Scope on a miss, which would
silently grant the unscoped boundary to a bad credential (security-critical; `scopeOf` relies on the
error). `ErrInvalidToken`'s message is `"unauthorized"`, so `writeErr` emits
`401 {"error":"unauthorized"}` → CLI exit 9. **No event kind** is emitted for mint/revoke (the catalog
stays 21; audit via `am serve --log`). The `tokens` table is added via `CREATE TABLE IF NOT EXISTS`,
so **no migration and `currentSchemaVersion` stays 5**.

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
[AND EXISTS (task_meta key)] AND NOT EXISTS (<open-prereq>) ORDER BY t.priority ASC,
t.id ASC LIMIT 1) RETURNING id, project_id`. The open-prereq `NOT EXISTS` matches ListTasks' Ready
filter exactly; ordering is priority ASC (0 = most urgent) with an id-ASC FIFO tiebreak
(deliberately not `am ls`'s `updated_at DESC` display order — a pickup queue drains oldest-first).
The archived-category exclusion is **unconditional** (scoped or not), like the archived-project
rule. Since Phase P the signature is **`NextTask(f NextFilter, agent string)`** with
`NextFilter{Project, Category, MetaKey}`. **Scope contract (Phase Q):** a scoped caller's
`X-Agent-Scope` is merged into `f` by the handler (`narrowScope`) **before** `NextTask` runs, so the
agent's scope is part of the candidate predicate **inside** the atomic pick+claim — a scoped agent
can never be handed an out-of-scope task, even racing unscoped callers. The `MetaKey` predicate is
**textually identical
to ListTasks'** (the wait/next invariant: a task that releases `am wait --ready --meta K` must be
pickable by `am next --meta K`; enforced by comment at both sites).
Zero rows ⇒ `ErrNotFound` (nothing ready, no carrier of the meta key, or — same 404, accepted
ambiguity — a bad project or category slug).
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

Go structs in `cmd/am/store.go`: `Category`, **`CategoryStat`** (Phase R — `Category` plus
`Counts map[string]int` and `ActiveAgents []string`, returned by `ListCategoriesWithStats`),
`Project`, `Task`, `Comment`, `Event`, `TaskFilter`,
`NextFilter`, `CreateTaskInput`, plus **`Scope`** (Phase Q — `{Category, Project}` with
`IsZero`/`String` and the package-level `parseScope`) and **`Token`** (Phase S — `{ID, Category,
Project?, CreatedAt, RevokedAt?}` carrying only public metadata, with `Token.Scope()` reconstructing
the bound `Scope`; `token_hash` is never a field on it). SQL schema in `cmd/am/schema.sql` (embedded
via `//go:embed schema.sql`).
`Category` and `Project` carry a stable `uid` (`amc_`/`amp_` + 16 hex, `newUID`); `Project`
additionally carries `category` (slug), `vault_project_id`, and `vault_path`. `Task` carries
`Meta map[string]string` (`json:"meta,omitempty"`) and `CreatedBy` (`json:"created_by,omitempty"`,
populated by `getTaskTx`/`GetTask`, not by list rows); `TaskFilter` and `NextFilter` carry `MetaKey`;
`CreateTaskInput` carries `Meta` and `Actor` (the latter written to `tasks.created_by` on insert).

## Authentication and Authorization

**No authentication.** The `X-Agent` header is an *actor label* for attribution, not a credential —
any caller can claim any identity. Access control is the `127.0.0.1` bind, now hardened by the
Phase 0 guardrails (Host allowlist + write-CSRF guard, `server.go`, ADR-011) which block
browser-driven cross-origin/DNS-rebinding attacks but are **not** auth (any local process is still
trusted). **Scope confinement is now token-backed (Phase S / ADR-029):** a bearer token
(`Authorization: Bearer`) carries a server-bound scope that wins over `X-Agent-Scope`, mint requires
an unscoped caller, and a bad/revoked token → `401 unauthorized` (exit 9). This confines a
*token-following* agent that cannot forge another scope's token — but it is **not** auth against an
arbitrary local process (loopback-only; a filesystem read of the identity file = token possession).
No per-resource authorization beyond the scope boundary. See `security.md` (ADR-002/ADR-011/ADR-027/
ADR-029 in `decision-records.md`).

## Validation

- Status validated against `validStatus` map and a SQL `CHECK (status IN (...))` constraint
  (`store.go`, `schema.sql`).
- Empty title / slug / comment body rejected with `ErrValidation`; slug must not contain spaces
  (`CreateProject`, `CreateCategory` — category slugs are additionally trimmed + lowercased
  server-side; `PatchProject` validates a new slug the same way).
- Priority coerced via `toInt`. Unknown PATCH keys are ignored (only known fields applied in
  `PatchTask`).
- Meta keys validated by `normalizeMetaKey` (1–50 chars of `a-z 0-9 . _ -` after trim+lowercase);
  meta values 1–500 bytes (`maxTitleLen`; empty = remove, PATCH only); a non-object `meta`,
  non-string values, or two raw keys normalizing to the same key → `ErrValidation` (400).
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
  cleaned up on `r.Context().Done()` (`cmd/am/server.go`, `cmd/am/hub.go`). Subscription scope is
  carried in a `subFilter{projectID, categoryID, projectIDs}` (Phase R): a `?category=` stream
  resolves the category's project-id set **once** at Subscribe time (`ProjectIDsInCategory`), so
  `Hub.Broadcast` stays a pure in-memory membership check (no per-event DB hits). A category-scoped
  subscriber receives an event only when its `ProjectID` is in the set; cross-category and
  category-level (`ProjectID==0`) events are dropped — except `project.created`, which reaches every
  subscriber regardless of scope (so new tabs appear live). A project created *after* a subscription
  opens is not in that set until the stream is re-opened (the dashboard re-opens on view change; the
  REST snapshot is authoritative) — an accepted post-open staleness window, documented in `hub.go`.
- **Startup update check** — `checkForUpdate()` fires a single background goroutine (4s timeout,
  silent on error) (`cmd/am/update.go`).
- **Graceful shutdown** — SIGINT/SIGTERM → cancel base context (unblocks SSE) → `Shutdown(3s)` →
  `PRAGMA wal_checkpoint(TRUNCATE)` → close (`runServe`, `store.Close`).

## External Integrations

- `proxy.golang.org` — version check (`checkForUpdate`); opt out with `AGENTMAN_NO_UPDATE_CHECK=1`.
- `go install …@<ver>` shelled out via `os/exec` in `am update` (`cmdUpdate`).

## Error Handling

Sentinel errors in `store.go`: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrProjectArchived`,
`ErrCategoryArchived`, `ErrOutOfScope` (Phase Q), `ErrInvalidToken` (Phase S — message
`"unauthorized"`),
typed `*ConflictError{Assignee}`, typed `*BlockedError{OpenPrereqs []int64}`, and typed
`*NotStaleError{Assignee}`. `writeErr`
(`server.go`) maps them: 404 / 409 / 400 / 403 / 401, with
`ErrOutOfScope` → `403 {"error":"out_of_scope"}`,
`ErrInvalidToken` → `401 {"error":"unauthorized"}` (→ CLI exit 9), and
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
· `8` out of scope (any 403) · `9` bad token (401, invalid/revoked bearer) · `1` other; plus `7` =
`am wait` timeout (CLI-side, no HTTP status involved). Exit 9 is **distinct from 8 on purpose**: a bad
credential must hard-fail, not be swallowed as a per-id scope-skip inside a bulk loop (ADR-029). Full
catalog: `0/3/4/5/6/7/8/9`.
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

The suite is `go test ./cmd/am/...` (258 tests across 11 `*_test.go` files). `web_test.go` ships
three source-level dashboard guards — `TestDashboardNoXSSSinks` (no `.innerHTML`/`document.write`/
`eval(` etc. in the embedded `web/` assets, locking in the `el()`/`textContent` convention),
`TestDashboardThemeAssets` (ADR-030 theming stays wired), and `TestDashboardParityAffordances`
(ADR-031 CLI↔GUI parity affordances stay wired). The dashboard has no behavioral JS tests — the
project deliberately adopts no JS test runner (preserves the single-binary/no-npm ethos; see
`known-risks-and-gaps.md`). See `contribution-guide.md` (Tests) for the full inventory.

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

- **Migration runner is exercised** end-to-end through v5 (`runMigrations`, ADR-010;
  `currentSchemaVersion = 5`). A DB *newer* than the binary is **not** accepted silently —
  `OpenStore` refuses it with a clear "upgrade am" error.
- **Single-writer** caps write throughput; fine for a personal board, unproven at scale.
- **No request size/time limits** beyond a 1 MiB body cap and `ReadHeaderTimeout`.
