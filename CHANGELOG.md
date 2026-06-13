# Changelog

All notable changes to **agentman** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

When cutting a release, rename the `[Unreleased]` heading to the version + date and start a
fresh `[Unreleased]` section.

## [Unreleased]

### Added

- **Category dashboard + scoped feed (Phase R)** — the human dashboard gains a category-home
  landing view and a per-category drill-down, and the event feed/stream gain a `?category=` lens
  (agentic_brain requirement R6). After this phase the integration-blocking set (O+P+Q) plus the
  human dashboard is complete; only Phase S (scope tokens) and the NICE-to-have items remain.
  - **`GET /api/categories` payload augmented** to a `CategoryStat` per element: the existing
    `Category` fields plus two always-present rollups (no opt-in flag) — `counts`
    (`{todo, doing, blocked, done}` summed over the category's **non-archived** projects; an
    archived project's tasks are excluded even when `?archived=true` lists the category itself)
    and `active_agents` (distinct non-human actors whose recent events touched a task in the
    category within the last **30 minutes**, sorted; the predicate is `task_id IS NOT NULL AND
    actor != 'human'`, so `comment.added` counts as activity while category/project admin events
    and the literal actor `human` do not). `?archived=true` still toggles whether archived
    categories appear; **no scope enforcement** (the dashboard is unscoped). Backed by the new
    store method `ListCategoriesWithStats(includeArchived, window)` (two queries merged in Go).
  - **`?category=<slug>` on `GET /api/events`** (all three access modes: `?since=`, `?tail=`,
    `?before=`) scopes the feed to the events of the category's projects and **intentionally
    EXCLUDES the category's own category-level events** (those have a NULL `project_id` — they
    belong to the All/overview feed, not a single category's drill-down). Composes with `?project=`
    (ANDed). An **unknown category returns 404** (`not_found`) — it flows through the `categoryID`
    `ErrNotFound` sentinel like an unknown project on `/api/tasks`. `RecentEvents`/`ListEvents`/
    `ListEventsBefore` gained the `category` parameter; `RecentEvents` was refactored to compose
    its WHERE clause from a `[]string` slice (like `ListProjects`).
  - **`?category=<slug>` on `GET /api/stream`** (SSE) scopes the live stream and the gap-replay to
    the category's projects, with the same NULL-project exclusion. The project-ID set is resolved
    **once at Subscribe time** (new store method `ProjectIDsInCategory`, threaded through the hub's
    new `subFilter`) so `Broadcast` stays a pure in-memory membership check with no per-event DB
    hits. **Exception:** `project.created` is delivered to every subscriber regardless of scope
    (the existing carve-out, so a new project's tab can appear live). An **unknown category is
    ignored silently** (the subscriber falls back to the unfiltered stream) — matching the
    endpoint's existing unknown-`project` swallow, and deliberately differing from `/api/events`
    (a stream is long-lived and best-effort; a REST query is a one-shot lookup).
  - **Dashboard category-home overview is the new landing view.** Cards per category showing the
    name, four count chips (reusing the status swatch colors), and an active-agents avatar row
    (initials); an **"All" card** opens the cross-category board. Card click (or Enter/Space)
    drills into the category board. **Hash routing** makes views linkable and the browser back
    button work, with `route()` as the single hash→state mapper: `#/` → overview (the landing
    view, also the empty-hash default), `#/all` → the cross-category board (the original board
    behavior), `#/cat/<slug>` → a single category's board (drill-down).
  - **Category board (drill-down):** the project tabs render only that category's projects; the
    "All" tab spans the category's projects; the board, feed, and live stream are all scoped via
    `?category=` (or `?project=` when one project is selected within the category). A header
    **breadcrumb** with a "← Categories" back button and the current view's name appears on the
    board views (hidden on the overview). The overview keeps **one global, unfiltered**
    recent-activity feed (the existing `#feed` aside) — not per-card mini-feeds.
  - **Live updates:** the overview's category counts refresh (debounced 250 ms) on any
    `task.*`/`project.*`/`category.*` event; the SSE stream is re-opened with the new `?category=`
    scope on every view change. The dashboard sends **no scope header** — a human sees everything;
    the category view is a query-param lens, not an identity scope. Existing board features
    (drag-drop, modal, dependency graph, keyboard shortcuts, stale badges, search, label chips,
    meta section) are unchanged; they still mount inside `#board`. New `index.html` `#overview`
    section + header `#breadcrumb`; `app.js` 1820 → 1986 lines, `app.css` 612 → 657 lines.
  - **No new event kinds** (catalog stays **21**), no new error codes, no new exit codes, no
    schema change (`currentSchemaVersion` stays **5**), no migration. `am wait`/`wait.go` and the
    `am` CLI surface are unchanged — the wait stream still deliberately does not narrow (the
    category-scoped REST re-check remains the authority; ADR-023).
  - Tests (+8, now 239): `cmd/am/store_test.go` — `TestListCategoriesCounts` (counts sum only
    non-archived projects' tasks; `active_agents` lists distinct non-human actors in the window,
    counts a commenter, excludes `human`, omits a backdated-only agent; existing
    `ListEvents`/`ListEventsBefore`/`RecentEvents` callers updated for the new `category`
    parameter). `cmd/am/server_test.go` — `TestEventsCategoryFilter` (one category's task events
    only; excludes `category.*` and the other category; covers `?since=`/`?tail=`/`?before=`;
    unknown category → 404; helpers `mustCreateProjectIn`/`eventKinds`). `cmd/am/sse_test.go` —
    `TestSSECategoryScopedStream` (out-of-category dropped, in-category delivered, `project.created`
    carve-out preserved) and `TestSSECategoryReconnectReplay` (a category-scoped reconnect replays
    only that category's gap; helper `openStream`). `cmd/am/hub_test.go` (new) — direct hub unit
    tests `TestHubCategoryScopedBroadcast`, `TestHubProjectScopedBroadcast`,
    `TestHubUnscopedBroadcast`, `TestHubBroadcastNilNoPanic`. `cmd/am/web_test.go`
    `TestDashboardNoXSSSinks` runs automatically over the new `app.js`/`index.html` (all new DOM
    via `el()`/`textContent`).
  - → ADR-028, `frontend.md`, `backend.md`, `data-model.md`, `system-map.md`, `README.md`,
    `known-risks-and-gaps.md`.

- **Scoped agent identity & enforcement (Phase Q)** — agents can be confined to a category
  (default) or a single project (tighter); the server enforces the scope on every mutation and on
  named reads (agentic_brain requirement R4).
  - **Scope on identity (`am init <tasktype> [-c CAT [-p PROJ]]`)**: an optional scope is recorded
    in the per-directory identity file and sent as the new **`X-Agent-Scope`** header
    (`category[/project]`, e.g. `personal` or `work/api`) on every request. `-p` without `-c` is a
    usage error (exit 5); the scope is validated locally (`parseScope`) — no server round-trip, and
    stdout is still only the id so `id=$(am init …)` keeps working. **Identity-file format**: a
    scoped init writes JSON `{"agent":"…","scope":"cat[/proj]"}`; an unscoped init keeps the legacy
    bare-id plain text. **R8 compatibility**: any pre-existing plain-text identity file reads as an
    **unscoped** identity (no migration of identity files) — after upgrading, re-run `am init -c …`
    to gain a scope. `am whoami` prints the bare id on line 1 (unchanged) and a second
    `scope: cat[/proj]` line only when scoped. New env **`AGENTMAN_SCOPE`** overrides the file's
    scope (non-empty only), composing per-field with the existing `AGENTMAN_AGENT` override.
  - **Server enforcement**: `scopeOf(r)` is the SOLE reader of `X-Agent-Scope` (the Phase S
    scope-token swap point); an absent header is unscoped (the human, the dashboard) and unchanged.
    Out-of-scope → **`403 {"error":"out_of_scope"}`** (new `ErrOutOfScope` sentinel) → **new CLI
    exit code `8`** (the catalog is now `0/3/4/5/6/7/8`). A malformed scope (empty/whitespace
    segments or >1 slash) → `400 validation` on every scope-checked endpoint. Every denial is
    **logged server-side** (`agentman: out_of_scope: actor=<id> scope=<scope> <METHOD> <PATH>`) —
    **log-only by design, no new event kind** (the 21-kind catalog is unchanged; feeding denials
    into the SSE stream was rejected as noise + a partial-information leak).
  - **Per-verb coverage (scoped callers only)**: task mutations (claim incl. `steal_stale`, patch
    incl. each id of a bulk `am status`/`am assign`, delete, comment-delete, deps, labels) require
    the task to be in scope; `POST /api/tasks` requires the target project in scope **or** the
    proposals project; `POST /api/tasks/next` merges the scope into the `NextFilter` **before** the
    atomic pick+claim (a scoped agent can never be handed an out-of-scope task, even racing unscoped
    callers); project mutations (patch/archive/unarchive/delete) and `GET …/graph` are project-level
    checks; `POST /api/projects` is allowed only for a **category-scoped** agent in its **own**
    category (project-scoped agents may not create projects); `POST /api/categories` and
    archive/unarchive are **403 for any scoped agent** (the category layer is above every scope).
  - **Reads policy**: **loud 403** on named/explicit out-of-scope reads (`GET /api/tasks/{id}`,
    `…/graph`, an explicit `?project=`/`?category=` that contradicts the scope — mirrors the
    orchestrator's ask-first rule); **silent narrowing** of unfiltered lists (an unfiltered `am ls`
    shows only the agent's world); the **proposals project is readable by all**. `am wait`'s REST
    re-checks carry the scope (out-of-scope `--done`/`--ready` exit 8); the SSE stream itself stays
    unscoped (Phase R residual), so out-of-scope events merely trigger re-checks — no hot-spin, no
    false release.
  - **Proposals carve-out**: task creation (and commenting on one's OWN tasks) in a designated
    proposals project is allowed **from any scope** — `am serve --proposals CAT/PROJ`, env
    `AGENTMAN_PROPOSALS`, default **`meta/proposals`** (flag beats env; both segments required — a
    bare category is rejected at startup, `fail(1)`). The carve-out matches the **(category,
    project) PAIR** at every site (`isProposals`): because slugs are globally unique, a same-slug
    project in another category gets no special treatment (closes a slug-squat where a scoped agent
    could capture other scopes' proposals). It is **inert when the project is missing** (the gate
    passes, the store 404s) and **does not extend to `am next`** (an agent whose scope already
    covers the proposals project still picks them via plain in-scope matching).
  - **`tasks.created_by` via migration v5** (`currentSchemaVersion` 4 → 5; `schema.sql` stays the
    frozen v1 baseline): `ALTER TABLE tasks ADD COLUMN created_by TEXT`, then a best-effort backfill
    from the durable event log — the actor of the task's **LATEST** `task.created` event (latest,
    not first: `tasks.id` is a reusable SQLite rowid, so an id's oldest creation event may belong to
    a deleted predecessor; the newest is the current incarnation). Tasks whose events were pruned
    stay NULL and never match the own-proposal comment rule (the safe direction). New tasks set
    `created_by` from the request actor (`actorOf(r)`, default `"human"`). `created_by` is returned
    by `GET /api/tasks/{id}` (omitted when empty); **list rows deliberately do not carry it** (keeps
    list payloads stable — only the single-task read and the scope checks consult it). Forward-only,
    idempotent, version bump in the same tx; `db.go`'s import ceiling tracks `currentSchemaVersion`,
    so a v5 snapshot now imports and a v6 one is refused.
  - **Scope checks run outside the store transaction** — sound today only because `task→project`
    and `project→category` are immutable; comments on `PatchTask`/`PatchProject` record that the
    checks must move in-tx if a task/project move feature ever ships.
  - **`X-Agent-Scope` is a client-asserted label** (like `X-Agent`) — accident prevention for an
    agent following its config, **not** a security boundary against crafted HTTP. Phase S (scope
    tokens) is the upgrade path. **No new event kinds, no new error bodies beyond `out_of_scope`.**
  - Tests (+32, now 231): `cmd/am/migrate_test.go` — `TestMigrationV5Fresh`,
    `TestMigrationV5ExistingDB` (backfill from `task.created`; pruned-events task stays NULL; a
    reused rowid backfills from the LATEST creation event; idempotent reopen); `TestMigrationV4Fresh`
    /`TestMigrationV4ExistingDB` un-hardcoded to `currentSchemaVersion`. `cmd/am/db_test.go` —
    `TestImportPreCategorySnapshot`/`TestImportRejectsNewerSchema` un-hardcoded. `cmd/am/store_test.go`
    — `TestTaskScopeAndProjectCategory`, `TestNextTaskRaceScopedCategoryMeta`. `cmd/am/server_test.go`
    — `TestScopeClaimEnforcement`, `TestScopeNextEnforcement`, `TestScopeStealStale`,
    `TestScopeProposalsCarveOut`, `TestScopeProposalsConfigurable`,
    `TestScopeProposalsMissingProjectInert`, `TestScopeProposalsWrongCategoryNoCarveOut`,
    `TestScopeProposalsSquat`, `TestScopeMutationSweep`, `TestScopeProjectScopedAgent`,
    `TestScopeReads`, `TestScopeHeaderValidation`, `TestScopeProjectCategoryEndpoints`.
    `cmd/am/cli_test.go` — `TestExitCodeForOutOfScope`, `TestCmdClaimOutOfScopeExit8`,
    `TestCmdNextOutOfScopeExit8`, `TestClientSendsScopeHeader`, `TestCmdStatusBulkOutOfScope`.
    `cmd/am/identity_test.go` — `TestInitScopedWritesJSON`, `TestInitScopedCategoryProject`,
    `TestInitProjectRequiresCategory`, `TestLegacyPlainIdentityUnscoped`, `TestScopeEnvOverride`,
    `TestWhoamiPrintsScope`, `TestParseScope`. `cmd/am/wait_test.go` — `TestWaitReadyScopedTimeout`,
    `TestWaitReadyScopedReleased`, `TestWaitReadyExplicitOutOfScopeExit8`.
    → ADR-027.

- **Task metadata (Phase P)** — free-form `key=value` pairs on tasks, with key-PRESENCE filters
  across the listing and pickup verbs (agentic_brain requirement R7).
  - **`meta` on tasks**: keys are normalized and validated like labels (trimmed, lowercased,
    1–50 chars of `a-z 0-9 . _ -` — `normalizeMetaKey` reuses `labelRe`/`maxLabelLen`); values are
    opaque strings, 1–500 bytes (the title cap — they render onto board cards and SSE payloads).
    Key **presence** (never the value) is the filterable unit. Two raw keys that normalize to the
    same key (e.g. `{"Auto":"a","auto":"b"}`) are rejected on BOTH create and patch —
    `400 validation` (CLI exit 5), message `duplicate meta key after normalization: "auto"` —
    applying both would pick a map-iteration-order winner; rejection keeps requests deterministic
    and all-or-nothing.
  - **API**: `POST /api/tasks` gains optional `"meta": {"k":"v", …}` (all pairs validated up
    front; empty values rejected at create — removal has no meaning there).
    `PATCH /api/tasks/{id}` accepts `"meta"` with upsert semantics — an empty-string value
    **removes** the key (absent-key removal is a silent no-op); non-string values or a non-object
    `meta` → 400; multi-key patches are all-or-nothing (one tx; a validation failure on any key
    rolls back every key). `GET /api/tasks` gains `?meta_key=K` (presence filter; composes with
    every existing filter incl. `ready`/`category`/`status`; bad key → 400), and list rows now
    include `meta` (stitched via one follow-up SELECT — values may contain `,`/`=`, so the labels
    `GROUP_CONCAT` trick is unsafe for them). `GET /api/tasks/{id}` returns `"meta": {…}` (omitted
    when empty). `POST /api/tasks/next` gains `"meta_key": "K"` — only tasks carrying the key are
    pickable (bad key → 400; no carrier → 404; priority-then-FIFO ordering and the single
    conditional-UPDATE atomicity unchanged). Error mapping reuses the existing sentinels
    (`ErrValidation` → 400 → CLI exit 5) — **no new error codes, no new exit codes**.
  - **Events**: **no new event kinds** (catalog stays at **21**). `task.created` data gains
    `"meta": {k: v}` when the task is created with meta; `task.patched` data gains a `"meta"`
    sub-object in the existing delta shape — `{"meta": {"k": [old, new]}}` with `null` for absent
    (removal = `[old, null]`, add = `[null, new]`); one event per PATCH regardless of how many
    keys changed. **Meta-only patches do NOT bump `updated_at`** — meta is metadata like labels;
    refreshing the activity timestamp would keep a stale claim alive (ADR-024 / `AddLabel`
    precedent). Mixed field+meta patches still bump.
  - **CLI**: new repeatable `--meta` flag — the parser gained a `multiFlags` registry,
    `Args.multi`, and `a.all(k)` (single-value flags remain last-wins; `--meta` has no short
    alias). `am new "title" … [--meta k=v]...` sends all pairs in the one POST (`--meta k=` and
    `--meta bare` are usage errors, exit 5; tokens split at the FIRST `=`, so values may contain
    `=`). `am edit <id> [--meta k=v]... [--meta k=]` folds all repeated flags into ONE PATCH (one
    tx, one event); `--meta k=` removes the key; the "nothing to change" message now mentions
    `--meta`. `am ls`/`am next`/`am wait --ready` take a single `--meta KEY` (two `--meta` →
    exit 5; `key=value` form → exit 5; `am wait <id> --done --meta K` is a usage error, exit 1).
    `am show <id>` prints one `meta: k=v k2=v2` line (keys sorted) after the labels line, only
    when meta exists. `usage()` updated for all five verbs.
  - **Dashboard**: the task modal gains a read-only **Meta** section after Labels (sorted keys;
    muted key, monospace value; built with `el()`/`textContent` only); feed/history `task.patched`
    lines append `(meta: k1, k2)` when the event delta contains meta. New CSS:
    `.meta-row`/`.meta-key`/`.meta-val`.
  - **Storage / store API**: new table `task_meta (task_id, key, value, PRIMARY KEY (task_id,
    key))` with `ON DELETE CASCADE` from tasks + index `idx_task_meta_key`, shipped via
    `CREATE TABLE IF NOT EXISTS` in `schema.sql` — **no migration step, no schema_version bump**
    (`currentSchemaVersion` stays 4; the `task_labels` precedent). New `applyMetaTx` (sorted-key
    walk, upsert via `INSERT … ON CONFLICT DO UPDATE`, delete on empty value, returns the delta)
    and `normalizeMetaKey`; `Task.Meta`/`TaskFilter.MetaKey`/`CreateTaskInput.Meta` threaded
    through. **`NextTask` signature changed** to `NextTask(f NextFilter, agent string)` with
    `NextFilter{Project, Category, MetaKey}` — Phase Q extends the struct instead of widening the
    signature again. The NextTask meta predicate is textually identical to ListTasks' (the
    wait/next same-condition invariant: a task that releases `am wait --ready --meta K` must be
    pickable by `am next --meta K`). `getTaskTx` deliberately does NOT load meta (labels parity —
    PATCH/claim responses omit it); the SSE stream is untouched (`--meta` narrows only the REST
    re-check, ADR-023).
  - Tests (+25, now 199): `TestTaskMetaCRUD`, `TestTaskMetaValidation` (incl. normalized-key
    collision rejection on create and patch), `TestPatchTaskMetaAtomicOneEvent`,
    `TestPatchTaskMetaNoOpNoEvent`, `TestMetaOnlyPatchDoesNotBumpUpdatedAt`,
    `TestNextTaskMetaFilter`, `TestNextTaskMetaRaceDistinctWinners`, `TestListTasksMetaKeyFilter`,
    `TestListTasksReturnsMeta`, `TestDeleteTaskCascadesMeta`,
    `TestTaskMetaTableExistsOnReopenedDB` (store); `TestCreateTaskWithMeta`,
    `TestPatchTaskMetaEndpoint`, `TestNextEndpointMetaBody`, `TestListTasksMetaKeyParam` (HTTP);
    `TestWaitReadyMetaNoHotSpin`, `TestWaitReadyMetaReleasedByCreate`,
    `TestWaitReadyMetaReleasedByPrereqDone`, `TestWaitMetaUsageErrors` (wait);
    `TestParseMultiFlag`, `TestCmdNewMetaWireFormat`, `TestCmdEditMetaSinglePatch`,
    `TestCmdNextMetaWireFormat`, `TestCmdLsMetaWireFormat`, `TestCmdShowPrintsMeta` (CLI).
    → ADR-026.

- **agentic_brain foundation (Phase O)** — the category layer, stable IDs, and vault binding
  that let agentman plug into the agentic_brain system (requirements R1/R2/R3/R8).
  - **Categories: a new layer above projects** (`instance → category → project → task`). New
    `categories` table (`uid`, `slug` unique lowercase, `name`, `archived_at`); every project
    belongs to exactly one category. CLI: `am categories [--all]` (terse: slug, name,
    `(archived)`; `--json` includes `uid`), `am category new <slug> [name]` (prints the slug),
    `am category archive|unarchive <slug>` (silent, idempotent). API: `GET /api/categories`
    (`?archived=true` includes archived), `POST /api/categories {slug,name?}` (slug trimmed +
    lowercased server-side, name defaults to slug; dup slug → `409 conflict`),
    `POST /api/categories/{slug}/archive|unarchive` (200, idempotent — no event on a no-op,
    mirroring projects). **`-c` is the global category flag** (env fallback `AGENTMAN_CATEGORY`)
    on `am ls`, `am next`, and `am wait --ready` (`?category=` on `GET /api/tasks` /
    `GET /api/projects`, `{"category"?}` on `POST /api/tasks/next`; composes with every existing
    filter). Exception: `am show <id> -c` still means `--comments` — `main.go` rewrites
    `-c → --comments` for the `show` verb only (`rewriteShowComments`). `am project new` now
    **requires a category** (`-c <slug>` or `AGENTMAN_CATEGORY`; exit 5 otherwise); `am new`
    (tasks) gains no `-c` — project slugs stay **globally unique**, so a project fully determines
    its category. Archiving a category cascades: default views (`GET /api/projects`, unscoped
    `GET /api/tasks`, the unscoped event feed) hide its projects/tasks/events; an explicit
    `?category=` keeps an archived category inspectable (hidden, not blocked-from-read); `am next`
    excludes archived categories unconditionally; creating a task or project under an archived
    category fails with `400 {"error":"category_archived"}` (new sentinel `ErrCategoryArchived`,
    CLI exit 5 — **no new exit codes**). `am wait --ready -c` scopes the readiness re-check; the
    SSE stream stays unscoped for category (no `?category=` on `/api/events`/`/api/stream` yet —
    Phase R). Dashboard kept working: `POST /api/projects` with no category defaults to `general`
    server-side, the feed renders the category event kinds, and the project strip reloads on
    `category.archived`/`category.unarchived`.
  - **Stable IDs (`amc_`/`amp_` + 16 lowercase hex)** — an immutable, unique, generated `uid` on
    categories and projects (8 bytes of `crypto/rand`, stdlib only; insert paths retry on the
    astronomically unlikely UNIQUE collision via `isUniqueErr`). Survives slug renames — the
    vault's canonical correlation key (a bare `p_` prefix was avoided; the vault owns that
    namespace). Exposed in all category/project payloads (`am projects --json` /
    `am categories --json`).
  - **Vault binding + project edit** — `projects.vault_project_id` / `projects.vault_path`
    (two optional strings ≤ 500 bytes; agentman stores the binding, the vault owns its meaning).
    New `PATCH /api/projects/{slug}` (allowed keys: `slug` — validated like create, `409` on dup;
    `name`; `vault_project_id`; `vault_path`; empty string clears a vault field; `uid`/category
    never patchable; unknown keys ignored; no-op → 200 with no event) and
    `am project edit <slug> [--slug NEW] [--name N] [--vault-id X] [--vault-path Y]` (silent
    success; explicit-empty `--vault-id=` / `--vault-path=` clears; exit 1 when nothing to
    change). Project payloads now carry `uid`, `category` (slug), `vault_project_id?`,
    `vault_path?` everywhere (archive/unarchive responses included). **4 new event kinds**:
    `category.created` / `category.archived` / `category.unarchived` (no `project_id` — rendered
    explicitly in the feed) and `project.patched` (compact delta like task patches, e.g.
    `{"slug":["old","new"]}`); total now **21**.
  - **Schema migration v4** (`currentSchemaVersion` 3 → 4): the `categories` table itself ships
    in `schema.sql` (`CREATE TABLE IF NOT EXISTS`); the v4 step adds `projects.category_id`
    (nullable in SQL — SQLite's `ADD COLUMN` can't add a NOT NULL FK — with the NOT NULL
    invariant app-enforced), `projects.uid` (+ unique index `idx_projects_uid`),
    `projects.vault_project_id`, `projects.vault_path`, and `idx_projects_category`; seeds the
    default category **`general`** unconditionally (fresh installs get it too); attaches every
    existing project to it; and backfills a distinct `amp_` uid per project. Task
    ids/refs/`claimed_at`/labels untouched. `am db export` snapshots categories automatically
    (`VACUUM INTO`); `validateImportCandidate` deliberately keeps the v1-baseline required-table
    set so **pre-v4 snapshots stay importable** (they migrate on the next open).
  - **`OpenStore` now refuses a too-new DB** — opening a database whose recorded `schema_version`
    is newer than the binary supports fails with `database schema_version N is newer than
    supported M — upgrade am`, instead of an older binary silently misbehaving against a newer
    schema. Same ceiling `validateImportCandidate` already applied to import snapshots.
  - Tests (+30, now 174): `TestMigrationV4Fresh`, `TestMigrationV4ExistingDB`,
    `TestOpenStoreRejectsNewerSchema` (migrate); `TestCreateCategory`,
    `TestArchiveUnarchiveCategory`, `TestCreateProjectWithCategory`, `TestPatchProject`,
    `TestCategoryArchiveCascade`, `TestListTasksCategoryFilterComposes`,
    `TestNextTaskCategoryScoping`, `TestCreateTaskArchivedCategory` (store);
    `TestCategoryEndpoints`, `TestProjectPayloadAndCategoryFilter`, `TestListTasksCategoryParam`,
    `TestNextEndpointCategoryBody`, `TestPatchProjectEndpoint`,
    `TestCreateTaskArchivedCategory400` (HTTP); `TestCmdCategoryVerbs`,
    `TestCmdProjectNewRequiresCategory`, `TestCmdProjectEdit`, `TestCmdLsCategoryWireFormat`,
    `TestCmdNextCategory`, `TestCmdShowDashCStillPrintsComments`, `TestRewriteShowComments`
    (CLI); `TestWaitReadyCategoryScoped`, `TestWaitReadyCategoryEnv`,
    `TestWaitReadyCategoryTimeout` (wait); `TestExportContainsCategories`,
    `TestImportPreCategorySnapshot`, `TestImportRejectsNewerSchema` (db).

- **Findability (Phase M)** — search and labels, so a grown board stays navigable.
  - **Search: `am ls --grep <text>`** (`GET /api/tasks?q=<text>`) — substring match on **title OR
    body** via SQL `LIKE … ESCAPE '\'`; the wildcards `%`/`_` (and `\`) in the query are escaped, so
    they match literally. Matching is **ASCII-case-insensitive** (SQLite's default LIKE; Unicode
    folding deliberately not applied — fine at personal-board scale). Comments and label names are
    **not** searched. A query over 500 bytes (the title cap) is `400 invalid` (CLI exit 5). The
    dashboard header gains a **search box** (`/` focuses it, 250 ms debounce) that filters the board
    server-side, so the filter survives SSE live reloads and can match description text. New
    `TaskFilter.Query`; helper `likeEscape`.
  - **Labels: `am label <id> [+add …] [-remove …]`** — free-form tags on tasks.
    `am label <id>` alone prints the task's labels space-separated (nothing if none); `+foo` or bare
    `foo` adds, `-bar` removes (silent success, scriptable). The verb takes **raw argv** (dispatched
    before `parse()`, which would swallow `-bar` as a value flag); flag-like tokens are rejected
    rather than treated as labels — `--…` is a usage error and the global value flags `-p`/`-c` are
    refused by name with a hint (both exit 5), so e.g. `am label 12 --json` can't silently remove a
    `json` label. Labels are normalized at the
    boundary — trimmed, lowercased, 1–50 bytes of `a-z 0-9 . _ -` (charset excludes `,` for safe
    `GROUP_CONCAT` splitting and `+`/space for unambiguous CLI tokens); anything else is
    `400 invalid` → exit 5. API: `POST /api/tasks/{id}/labels {"label":…}` /
    `DELETE /api/tasks/{id}/labels/{label}` (both `200 {"status":"ok"}`, idempotent no-ops emit no
    event), `GET /api/tasks?label=<l>` filter, and `labels:[…]` (sorted) on task JSON —
    `am ls --label <l>` / `-l <l>` on the CLI; `am show` prints a `labels:` line; `taskLine` is
    deliberately unchanged (token budget). Storage: new **`task_labels`** join table (inline label
    text, no catalog) propagated via `CREATE TABLE IF NOT EXISTS` in `schema.sql` — no migration
    step, no version bump (the `task_deps` precedent). Adding/removing a label does **not** bump
    `updated_at` (metadata must not refresh a stale claim). **2 new event kinds**:
    `task.labeled` / `task.unlabeled` (total now 17), rendered in the feed as
    *"X labeled #N +bug"*. Dashboard: board cards show up to 3 clickable label chips (click =
    filter by that label; a header chip with ✕ clears it); the task modal gains a **Labels**
    section (chips with ✕ remove + an Enter-to-add input). New store methods `AddLabel` /
    `RemoveLabel`; `TaskFilter.Label`.
  - Tests (+14, now 144): `TestListTasksQueryFilter`, `TestListTasksQueryEscapesLikeWildcards`,
    `TestAddRemoveLabel`, `TestLabelValidation`, `TestListTasksLabelFilter`,
    `TestAddLabelDoesNotBumpUpdatedAt`, `TestDeleteTaskCascadesLabels`,
    `TestTaskLabelsTableExistsOnReopenedDB` (store); `TestListTasksQueryParam`,
    `TestLabelEndpoints` (HTTP); `TestCmdLsGrepWireFormat`, `TestCmdLabelAddRemove`,
    `TestCmdLabelPrintsLabels`, `TestCmdLabelUsage` (CLI).

- **Agent work loop (Phase L)** — the verbs an agent loop needs between "what should I do?" and
  "is my prerequisite finished?".
  - **`am next [-p P]`** (`POST /api/tasks/next {"project"?}`) — atomic pick + claim of the best
    ready task: `todo`, **unassigned**, no open prerequisites, non-archived project. Pick and claim
    are ONE conditional `UPDATE … WHERE id = (SELECT … ORDER BY priority ASC, id ASC LIMIT 1)`, so
    concurrent callers always get distinct tasks. Ordering is priority ASC (0 = most urgent) with an
    **id-ASC FIFO tiebreak** (a pickup queue drains oldest-first — deliberately not the
    `updated_at DESC` display order of `am ls`). Prints only the claimed id (`--json` for the full
    task); nothing ready → `404 not_found` → exit 3 (`next: no ready task`). Tasks pre-assigned to
    the caller are skipped (candidates require `assignee IS NULL`) — use `am claim` for those.
    Emits the existing `task.claimed` event (same payload shape as a claim). New store method
    `NextTask`.
  - **`am wait <id> --done [--timeout D]` / `am wait --ready [-p P] [--timeout D]`** — block until
    a task is `done`, or until some ready task exists. Pure CLI-side SSE consumer (`cmd/am/wait.go`):
    snapshots the event cursor (`/api/events?tail=1`), checks the condition via REST, then follows
    `/api/stream?since=<cursor>` and **re-checks via REST on each relevant event** (event payloads
    are never trusted as state); reconnects from the last seen id on disconnect. The server is
    untouched. `--timeout` accepts a Go duration (`5m`) or bare seconds (`300`); default **10m**.
    Met → exit 0 (`--done` prints nothing; `--ready` prints one ready task id; `--json` prints the
    satisfying task). **New exit code `7`** on timeout (`wait: timeout`); missing task → 3;
    server down → 6.
  - **Bulk `am status <id...> <status>` / `am assign <id...> <agent|me|->`** — the last positional
    is the status/assignee, everything before it is task ids. Client-side loop, one PATCH (and one
    event) per task. Partial failure: each failing id gets its own stderr line
    (`status: #13 not_found`) and the loop continues; exit code is the first failure's mapping
    (0 if all succeeded; server down aborts immediately with 6). New helper `exitCodeFor` in
    `client.go` is now the single status→exit-code source for `doOrFail` and the bulk loop.
  - Tests: `TestNextTaskPicksHighestPriorityReady`, `TestNextTaskFIFOWithinPriority`,
    `TestNextTaskProjectScoping`, `TestNextTaskNoneReady`, `TestNextTaskRaceDistinctWinners`,
    `TestNextTaskEmptyAgentValidation` (store); `TestNextEndpoint`, `TestNextEndpointProjectBody`
    (HTTP); `TestCmdNextPrintsOnlyID`, `TestExitNextNoneReady`, `TestCmdStatusBulk`,
    `TestCmdStatusBulkPartialFailure`, `TestCmdAssignBulk` (CLI); new `wait_test.go` —
    `TestWaitDoneAlreadySatisfied`, `TestWaitDoneEventArrives`, `TestWaitDoneCrossProject`,
    `TestWaitReadyOnPrereqDone`, `TestWaitTimeout`, `TestWaitTaskNotFound`, `TestWaitServerDown`,
    `TestWaitUsageErrors`, `TestParseWaitTimeout`, `TestWaitBadTimeoutExit5`.

- **Stale-claim recovery (Phase K)** — agents that crash after `am claim` no longer hold tasks
  forever.
  - **`am ls --stale <dur>`** (`GET /api/tasks?stale=<dur>`) — lists tasks that are assigned, not
    `done`, and have had no activity (`updated_at`) for at least the given duration (Go syntax,
    e.g. `30m`, `48h`). A malformed or non-positive duration is `400 invalid` (CLI exit 5).
  - **`am claim <id> --steal-stale <dur>`** (`POST /api/tasks/{id}/claim {"steal_stale":"<dur>"}`)
    — atomic takeover of a stale claim via a single conditional `UPDATE … WHERE status!='done' AND
    (assignee IS NULL OR updated_at < cutoff)`, so concurrent stealers get exactly one winner.
    A still-fresh claim loses with `409 {"error":"not_stale","assignee":…}` (CLI exit 4,
    `claim: #N held by X (not stale yet)`); a done task → `409 already_claimed`; stealing on an
    unclaimed task degrades to a normal claim; re-stealing your own task is idempotent (no event).
    Open prerequisites hard-block the steal like a normal claim. New store method
    `StealStaleClaim`; new error type `NotStaleError`.
  - **`tasks.claimed_at` column** (schema **migration v3** — `ALTER TABLE tasks ADD COLUMN
    claimed_at TEXT`). Set by claim/steal and by PATCH-assign; cleared when the assignee is
    removed (`am drop`). Returned as `claimed_at` in task JSON.
  - **New event kind `task.reclaimed`** (total now 15) — emitted on a successful steal, naming the
    previous assignee and the `stale_for` window; rendered in the dashboard feed as
    *"X reclaimed #N from Y"*.
  - **Dashboard stale badge** — a board card in *doing* with an assignee and no activity for 30+
    minutes shows a **⏳ stale** chip.
  - Tests: `TestStealStaleClaim`, `TestStealRaceExactlyOneWinner`, `TestListTasksStaleFilter`,
    `TestClaimSetsClaimedAt`, `TestDropClearsClaimedAt`, `TestMigrationV3AddsClaimedAt` (store/
    migrate); `TestListTasksStaleParam`, `TestStealStaleEndpoint` (HTTP); `TestExitNotStale`,
    `TestStaleFlagsWireFormat` (CLI).

- **Input size limits** — task titles are capped at 500 bytes; task bodies and comment bodies at
  64 KiB; priority must be 0–3. Exceeding a limit returns `400 invalid` (CLI exit 5) instead of
  silently inserting megabyte payloads that render into every board card and SSE event. Enforced
  in the store (`CreateTask`, `PatchTask`, `AddComment`); boundary values accepted.
  Test: `TestInputLimits`.

### Fixed

- **Dashboard `api()` no longer crashes on non-JSON responses** — a proxy error page or truncated
  body now falls through to an `HTTP <status>` error message instead of throwing an uncaught
  `SyntaxError` from `JSON.parse`. (`cmd/am/web/app.js`)
- **SSE Flusher-unsupported error is now JSON** — `handleStream` returned plain text via
  `http.Error` while every other error path returns JSON; now `{"error":"streaming_unsupported"}`.
  (`cmd/am/server.go`)
- **`am db prune --before` validates its date** — a malformed date (e.g. `2026-13-99`) previously
  fed an ISO-8601 string comparison and silently pruned the wrong rows (usually none); it now
  fails with a clear error, both before the confirmation prompt and inside `pruneEvents`.
  Test: `TestPruneEventsRejectsBadDate`. (`cmd/am/db.go`)
- **Event-payload marshal errors are no longer discarded** — `insertEvent` returns the
  `json.Marshal` error instead of writing a corrupted/empty payload into the events table (the
  durable replay cursor). (`cmd/am/store.go`)
- **`am update` semver compare handles prereleases** — a stable tag now beats a prerelease of the
  same triple (`v0.5.0` > `v0.5.0-rc1`); prereleases order lexically. Previously a prerelease
  build never saw the stable release as an update. (`cmd/am/update.go`)
- **SSE reconnect backoff is jittered** — multiple open dashboard tabs no longer reconnect in
  lockstep. (`cmd/am/web/app.js`)

## [0.5.0] - 2026-06-07

### Added

- **Dependency-graph overlay** — a per-project interactive visualization of the task dependency DAG.
  - **Entry:** the **"Graph"** button in the dashboard header + the **`g`** keyboard shortcut (not
    while typing). Opens a full-screen overlay (`#graphOverlay`) reusing the modal focus-trap and
    `Esc`-to-close; a project `<select>` defaults to the selected project; **"Reset view"** + ✕
    close the overlay.
  - **Rendering:** pure vanilla SVG, no library, no npm. A new `svg(tag, attrs)` helper
    (`document.createElementNS`) is parallel to the existing `el()` helper and uses `.textContent`
    for all text — XSS-safe (`TestDashboardNoXSSSinks` passes). Edges are cubic Bézier curves
    with `<marker>` arrowheads.
  - **Layout:** topological longest-path / Kahn's algorithm — prerequisites left, dependents
    right. Dependency-free tasks are placed in a compact grid **"No dependencies" lane** below
    the DAG (so isolated tasks don't pile into one tall column). All tasks in the project appear.
  - **Encoding:** nodes colored by task **priority** (`PRIO` palette) with a status dot and
    Ready/🔒 Blocked indicators. Edges: `done` prerequisite → **green solid** ("cleared"); open
    prerequisite → **amber dashed** ("blocking"). A **bottom-left legend** explains both axes.
  - **Interaction:** click a task → **transitive highlight** — its full upstream prereq path and
    downstream subtree light up in distinct accents; everything else dims. Click the empty canvas
    to clear. The **right detail panel** (built with `el()`) shows title, status/priority/assignee,
    Ready/Blocked, a clickable **Prerequisites** list and **Unblocks** list, and an **"Open task"**
    button → the existing detail modal.
  - **Pan/zoom:** drag to pan, scroll to zoom, `viewBox` manipulation — no library. **"Reset view"**
    restores the initial viewport.
  - **Live:** while open, debounced re-fetch on SSE events affecting the project
    (`task.dep_added/removed`, `task.status`, `task.created/deleted`, `task.assign`,
    `task.patched`), preserving pan/zoom and selection.
  - **Backend:** `GET /api/projects/{slug}/graph` → `{nodes: []Task, edges: []GraphEdge{from,to}}`
    — all tasks as nodes, edges oriented prereq→dependent. Read-only: no writes, no events emitted.
    404 on a missing project. New store method `ProjectGraph`; new types `GraphEdge`,
    `ProjectGraphData`.
  - **Tests:** +4 backend (`TestProjectGraph`, `TestProjectGraphMissingProject` in `store_test.go`;
    `TestProjectGraphEndpoint`, `TestProjectGraphEndpoint404` in `server_test.go`) → **95 tests**
    total. Overlay JS is untested behaviorally (no JS runner — ADR-018); XSS-sink guard covers it.

- **Task dependencies (Phase H)** — tasks can now have prerequisites (other tasks that must be
  `done` first). Many-to-many, same-project only.
  - **CLI:** `am dep add <id> <prereq…>` / `am dep rm <id> <prereq>` — add/remove prerequisite
    edges. `am ls --ready` lists todo tasks with no open prereqs (the safe pick-up list for agents).
    `am ls --blocked` lists tasks with ≥1 open prereq. `am ls` rows show a `[blk:N]` or `[ready]`
    marker. `am show <id>` prints `depends on:` / `blocks:` lines when present.
  - **API:** `POST /api/tasks/{id}/deps {depends_on:<id-or-ref>}` — add edge (same project; rejects
    self-deps, cross-project, cycles). `DELETE /api/tasks/{id}/deps/{depId}` — remove edge.
    `GET /api/tasks?ready=true` / `?blocked=true` — server-side prereq filters.
    `GET /api/tasks/{id}` now returns `depends_on:[…]` and `blocks:[…]`.
  - **Hard-block:** claiming or PATCHing a task to `doing`/`done` while it has open prerequisites
    fails with `409 {"error":"blocked","open_prereqs":[…]}`. CLI maps this to exit 4 and prints
    e.g. `claim: #3 blocked — prereqs not done (#1 #2)`. Edit, comment, assign, and
    status→`todo`/`blocked` are unaffected.
  - **Cycle prevention:** self-deps and transitive cycles are rejected by a recursive CTE
    (`wouldCycle`) — validation error / HTTP 400.
  - **Dashboard:** task modal has a **Dependencies** section — "Depends on" chips (status dot +
    ref link + title + status + ✕ remove), an **"Add prerequisite…"** dropdown of same-project
    tasks (excludes self + existing edges), and a read-only **Blocks** list. Board cards show a
    **🔒 Blocked** tag (`nopen > 0`) or **✓ Ready** tag (`nprereq > 0 && nopen == 0`). Hard-block
    409s surface the blocking prereq ids and revert the card/modal.
  - **Storage:** new join table `task_deps(task_id, depends_on_id)` — composite PK, `ON DELETE
    CASCADE` on both FKs (deleting a task removes its edges in both directions), reverse index
    `idx_task_deps_prereq`. Propagated to existing DBs via `CREATE TABLE IF NOT EXISTS` in
    `schema.sql` — no migration-runner step, no version bump.
  - **Event kinds:** 2 new — `task.dep_added`, `task.dep_removed` (total now 14).
  - **Tests:** +24 (now 91 total) — cycle/self/cross-project rejection, idempotent add/remove,
    cascade, counts, filters, hard-block (claim + patch), HTTP endpoints, 409 blocked, fresh-DB
    table existence.

## [0.4.2] - 2026-06-07

### Changed

- **Minimum Go raised to `1.25.11`** (`go.mod`). Go 1.25.0–1.25.10 ship a standard library with 21
  known advisories (`crypto/tls`, `crypto/x509`, `net/url`, `net/http`, …). With this floor,
  `go install` always builds against a security-patched stdlib — even for installers on an older Go,
  whose toolchain auto-upgrades to ≥ 1.25.11. No source changes; agentman's own code was unaffected.
- **CI builds on the latest stable Go** (`go-version: 'stable'` in `.github/workflows/ci.yml`,
  replacing the exact `go.mod` pin), so `govulncheck` scans a current/patched stdlib instead of a
  frozen one that goes red as CVEs accrue.

## [0.4.1] - 2026-06-07

> Note: `v0.4.0` was accidentally tagged on a stale commit (and that tag was already cached by the
> Go module proxy, which is immutable), so this release ships as **v0.4.1**. Do not use `v0.4.0`.

### Added

- **CI via GitHub Actions (Phase F)** — `.github/workflows/ci.yml` is the project's first CI.
  Triggers on push to `main` and on pull requests. Single `ubuntu-latest` job runs, in order:
  `go build ./...`, `go vet ./...`, `gofmt -l` (fails if non-empty), `go test -race -count=1 ./...`,
  `node --check cmd/am/web/app.js` (JS syntax), and `govulncheck ./...` (blocks on reachable
  vulnerabilities; `@latest` keeps the advisory DB current). All checks pass; 0 reachable
  vulnerabilities. One known non-blocking module-level advisory (`GO-2026-5024`, Windows-only,
  unreachable) is documented in `architecture/known-risks-and-gaps.md`.

- **Expanded automated test coverage (Phase E)** — 9 test files, 71 tests, all green under
  `-race`. Four new test files close the previously-untested layers:
  - **`cli_test.go` (E1)** — CLI verb output (`cmdNew`/`cmdLs`/`cmdStatus`/`cmdNote`/`cmdDrop`)
    and the `doOrFail` exit-code mapping (3 not found · 4 conflict · 5 validation · 6 server
    down); plus table tests for the `parse`/`Args` helpers and the pure formatters
    (`taskLine`/`statusShort`/`assignee`/`trunc`/`apiErr`). Uses `captureStdout`/`captureExit`
    helpers against a real `httptest` server.
  - **`sse_test.go` (E2)** — `TestSSEDeliversLiveEvent` (live mutation arrives over SSE) and
    `TestSSEReplayOnReconnect` (reconnect with `Last-Event-ID` replays missed events; every
    replayed id is strictly greater than the resume cursor).
  - **`identity_test.go` (E3)** — `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT` env
    override, `sanitizeType` table, `newIdentity` format. Isolated via the `AGENTMAN_AGENT_FILE`
    env seam (never writes to `~/.agentman`).
  - **`web_test.go` (E4)** — `TestDashboardNoXSSSinks`: source-level XSS-sink guard that reads
    the embedded `web/app.js` + `web/index.html` via `webFS` and asserts none of `.innerHTML`/
    `.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear. Locks in the `el()`/
    `textContent` convention at `go test` time. No JS runner added (ADR-018).
  - **Testability seam:** `var osExit = os.Exit` in `cli.go`; `fail()` now calls `osExit` so
    tests can intercept exit codes without killing the process. No production behavior change.
  (`cmd/am/cli_test.go`, `cmd/am/sse_test.go`, `cmd/am/identity_test.go`, `cmd/am/web_test.go`,
  `cmd/am/cli.go`)

- **Opt-in request logging (Phase D2)** — `am serve --log` or `AGENTMAN_LOG=1` installs a
  `requestLogger` middleware that logs one line per request after completion:
  `METHOD PATH STATUS LATENCY ACTOR` (actor = `X-Agent`, default `"human"`) to stderr via the
  standard `log` package. Off by default. The middleware is installed outermost so security-guard
  403s are also logged. `statusRecorder` proxies `http.Flusher` so SSE connections continue to
  work. A long-lived SSE connection logs once on disconnect with a large latency (inherent).
  `AGENTMAN_LOG` treats any non-empty value as on; `=1` is canonical.
  (`cmd/am/server.go`, `cmd/am/main.go`, `cmd/am/cli.go`)

- **Events pagination + retention (Phase C2)** — completes Phase C:
  - **`GET /api/events?before=<id>`** — backward cursor: returns events with `id < before`,
    newest-first (default 40, cap 200). Applies the same archived-project filter as `?since=`/`?tail=`
    when no `?project=` is given; an explicit `?project=<slug>` returns that project's events.
    Store method: `ListEventsBefore(before, project, limit)` (`cmd/am/store.go`).
  - **Dashboard "Load older activity"** button at the bottom of the activity feed fetches
    `?before=<oldest-loaded-id>` and appends results. Placed outside `#feedList` so `trimFeed`
    can't remove it. `feedPaginated` disables the feed cap once the user has paged (trade-off: the
    in-browser feed can grow unbounded until the next reload). An end-marker replaces the button
    when all history is loaded. All DOM via `el()` (no `innerHTML`).
    (`cmd/am/web/app.js`, `cmd/am/web/app.css`)
  - **`am db prune (--before <YYYY-MM-DD> | --keep <N>) [--db PATH] [--yes]`** — offline events
    retention (CLI-only, no HTTP route). Refuses while a server is running (same guard as
    `am db import`). Deletes rows from the **`events` table only** (not tasks/comments/projects),
    then runs `VACUUM` (best-effort) to reclaim disk space. Prints `pruned N events` to stderr;
    stdout stays clean. `--before <date>`: same-day events are kept (date-only string sorts before
    same-day ISO timestamps). `--keep N`: keeps the newest N events by id. (`cmd/am/db.go`)
  - Tests: `TestListEventsBefore` (store), `TestEventsBeforeEndpoint` (HTTP); `TestPruneEventsKeep`,
    `TestPruneEventsBefore`, `TestPruneEventsBeforeSameDayBoundary` (prune).

- **Hard delete (Phase C1)** — permanent removal for tasks, comments, and projects:
  - CLI: `am rm <id>` hard-deletes a task and all its comments (silent success; exit 3 if not found).
    `am project rm <slug> --yes` hard-deletes a project **and all its tasks/comments** (cascade);
    `--yes` is required or the command errors with a hint.
  - API: `DELETE /api/tasks/{id}`, `DELETE /api/tasks/{id}/comments/{cid}`,
    `DELETE /api/projects/{slug}` — all return `200 {"status":"deleted"}`; missing target → 404.
    Cascade is via existing FK constraints (`projects → tasks → comments`).
  - Dashboard: inline two-step delete confirms (no native `confirm()`/`prompt()`) in the task modal
    (**Delete task**), per-comment (**×**), and the Manage-projects modal (**Delete project**,
    distinct from Archive). All DOM via `el()`.
  - Three new event kinds: `task.deleted`, `comment.deleted`, `project.deleted` (total now 12).
    `onEvent` handles each: `task.deleted` removes the card and closes the open modal; `comment.deleted`
    refreshes the open modal; `project.deleted` drops the project from selection and reloads.
    Events are **never** deleted — the audit log (including `*.deleted` events) survives hard deletes.
  - Store: `DeleteTask`, `DeleteComment`, `DeleteProject` — each inserts its `*.deleted` event in
    the same tx before the `DELETE`, then commits; broadcast happens after commit.
  - Tests: `TestDeleteTaskCascadesComments`, `TestDeleteTaskNotFound`,
    `TestDeleteCommentRemovesOnlyComment`, `TestDeleteProjectCascades` (store);
    `TestDeleteTaskEndpoint`, `TestDeleteProjectEndpoint`, `TestDeleteCommentEndpoint` (HTTP).
    (`cmd/am/store.go`, `cmd/am/server.go`, `cmd/am/cli.go`, `cmd/am/main.go`, `cmd/am/web/app.js`,
    `cmd/am/web/app.css`)

- **DB export / import** — `am db export [path] [--db PATH]` writes a consistent `VACUUM INTO`
  snapshot (chmod `0o600`, prints the path); `am db import <path> [--db PATH] [--yes]` validates
  the candidate (integrity + foreign-key checks, required tables, schema version), **refuses to
  run while a server is live**, backs up the current DB, then atomically swaps it in. CLI-only —
  there is no HTTP route; it operates directly on the SQLite file. (`cmd/am/db.go`)
- **Project archive / hide** — `am project archive <slug>` / `am project unarchive <slug>`, plus
  `am projects --all` and `GET /api/projects?archived=true` / `POST /api/projects/{slug}/archive`
  and `…/unarchive`. Backed by the first real schema migration (**v2**, adding
  `projects.archived_at`), which exercises the Phase-0 forward-only migration runner end-to-end.
- **Multi-select project filter** on the dashboard — click several project tabs to view their
  boards together; the **All** tab clears the selection.
- **Dashboard archive / unarchive control** — a "⋯ Manage projects" button in the tab bar opens a
  modal listing all projects (active and archived). Active projects have an **Archive** button;
  archived projects show an "Archived" badge and an **Unarchive** button. Archive/unarchive calls
  the existing API endpoints; on success the tab bar refreshes in place and, if the just-archived
  project was selected, the board and feed reload automatically. All DOM is built via the existing
  `el()` helper (no `innerHTML`); the modal focus trap and Esc-to-close are preserved.
  (`cmd/am/web/app.js`, `cmd/am/web/app.css`)

### Fixed

- **500 responses leaked internal error detail (Phase D1).** `writeErr`'s default branch
  previously returned the raw Go error string (SQL messages, file paths, etc.) to the client.
  It now logs the real error server-side (`log.Printf("agentman: internal error: %v", err)` to
  stderr) and returns a generic `{"error":"internal"}` body. All sentinel mappings
  (`ErrNotFound`→404, `ErrValidation`→400, `ErrProjectArchived`→400, `ErrConflict`→409,
  `*ConflictError`→409) are unchanged. Tests: `TestWriteErrHidesInternalDetail`,
  `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`. 46 tests pass total.
  (`cmd/am/server.go`)

- **Archived projects' events appeared in the activity feed.** `ListEvents` and `RecentEvents`
  had no archived filter, so the "All"-view feed kept streaming events from archived projects.
  Both functions now LEFT JOIN `projects` and exclude events whose `project_id` belongs to an
  archived project when no explicit `project=` filter is given. An explicit `?project=<slug>`
  still returns all of that project's events for direct inspection. Regression test:
  `TestFeedHidesArchivedProjectEvents`. (`cmd/am/store.go`, `cmd/am/store_test.go`)
- **`am new -p <archived>` silently created a hidden ticket.** `CreateTask` looked up the project
  slug with no archived check, so tasks created into archived projects were immediately invisible
  everywhere. `CreateTask` now rejects with a new `ErrProjectArchived` sentinel (mapped to
  `400 {"error":"project_archived"}` by the HTTP layer). Regression tests:
  `TestCreateTaskRejectsArchivedProject` (store) and `TestCreateTaskIntoArchivedProject400` (HTTP).
  (`cmd/am/store.go`, `cmd/am/server.go`, `cmd/am/store_test.go`, `cmd/am/server_test.go`)

- **Archived projects' tasks were still shown on the board.** Archiving hid a project's tab and
  column header (`ListProjects` filters archived) but `ListTasks` had no archived filter, so the
  tickets kept rendering in the board's "All" view and in `am ls`. `ListTasks` now excludes tasks
  belonging to archived projects when **no explicit project is requested**; an explicit
  `?project=<slug>` / `am ls -p <slug>` still returns them for direct inspection. Regression test:
  `TestListTasksHidesArchivedProjectTasks`. (`cmd/am/store.go`, `cmd/am/store_test.go`)
- **The board clung to the left edge on wide / ultrawide screens.** The status columns cap at
  `max-width: 480px`, so beyond ~1990px of width the leftover space piled up on the right. The
  board now centers with `justify-content: safe center`; the `safe` keyword falls back to
  `flex-start` when columns overflow, so horizontal scrolling on narrow screens never clips the
  first column. The mobile (≤720px) vertical stack stays top-aligned. (`cmd/am/web/app.css`)
- **Review hardening for DB export/import** (caught during the Phase 1 tester pass): `exportDB`
  now fails fast on a missing source DB instead of silently writing an empty snapshot;
  `validateImportCandidate` checks `rows.Err()` after iterating; `copyFile` propagates the file
  close error via a named return rather than swallowing it on a double `Close`. (`cmd/am/db.go`)

### Changed

- **Documentation brought current** with the shipped features across `README.md`,
  `docs/agent-integration.md`, and the `architecture/` set — new commands, routes, event kinds
  (`project.archived` / `project.unarchived`), schema v2, and the now-exercised migration runner.

## [0.3.0] and earlier

Predate this changelog — see the git history (`v0.1.0` – `v0.3.0`). Highlights: the single-binary
CLI + HTTP/SSE server + embedded dashboard, atomic claim, per-directory agent identity,
`am update` + startup version check, the Phase-0 migration-runner foundation, and the localhost
HTTP guardrails (Host allowlist + write-CSRF guard + CSP).
