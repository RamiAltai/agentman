# CHANGES.draft — Phase O: Foundation (categories, stable IDs, vault binding, migration v4)

Input for the docs-sync stage. Covers R1 (category layer), R2 (stable IDs),
R3 (vault binding), R8 (migration) of `agentman_requirements.md`.

## Schema (migration v4)

- New table `categories` (created by `schema.sql`, present on fresh AND
  migrated DBs): `id`, `uid` (TEXT NOT NULL UNIQUE, `amc_<16 hex>`), `slug`
  (TEXT NOT NULL UNIQUE, lowercase), `name`, `created_at`, `archived_at`.
- `projects` gains four columns via migration v4 (the CREATE TABLE in
  `schema.sql` stays the frozen v1 baseline):
  - `category_id INTEGER REFERENCES categories(id)` — **nullable in SQL,
    NOT NULL by app invariant** (see Decisions below).
  - `uid TEXT` + `CREATE UNIQUE INDEX idx_projects_uid` (UNIQUE not allowed in
    ADD COLUMN), format `amp_<16 hex>`.
  - `vault_project_id TEXT`, `vault_path TEXT` (vault binding, both optional).
  - `CREATE INDEX idx_projects_category ON projects(category_id)`.
- v4 seeds a default category `general` ("General") **unconditionally** (fresh
  installs get it too), attaches every existing project to it, and backfills a
  distinct `amp_` uid per project. Task ids/refs/`claimed_at`/labels untouched.
- `currentSchemaVersion` 3 → 4.

## Event kinds (catalog 17 → 21)

- `category.created` `{slug}` — `project_id` NULL.
- `category.archived` `{slug}` — `project_id` NULL.
- `category.unarchived` `{slug}` — `project_id` NULL.
- `project.patched` — compact delta like task patches, e.g.
  `{"slug":["old","new"]}`, `{"vault_project_id":[null,"p_9"]}`.
- NULL-project category events reach unscoped SSE subscribers only (project-
  scoped subscribers filter on project id; fine for O, Phase R revisits).

## API

- `GET /api/categories` (+`?archived=true`) → `[{id, uid, slug, name,
  created_at, archived_at?}]`.
- `POST /api/categories` `{slug, name?}` → 201 category; `409 conflict` on dup
  slug; `400 validation` on bad slug. Slug is trimmed + lowercased server-side;
  name defaults to slug.
- `POST /api/categories/{slug}/archive` / `/unarchive` → 200, idempotent
  (no event on no-op), mirroring projects.
- `PATCH /api/projects/{slug}` — NEW. Allowed keys: `slug` (validated like
  create; `409` on dup), `name` (non-empty), `vault_project_id`, `vault_path`
  (plain strings ≤ 500 bytes; empty string clears). Unknown keys ignored;
  `uid`/`category_id` never patchable. No-op → 200, no event. One
  `project.patched` event otherwise.
- `GET /api/projects` gains `?category=<slug>`; project payloads now carry
  `uid`, `category` (slug), `vault_project_id?`, `vault_path?`.
- `POST /api/projects` accepts optional `category`; **empty defaults to
  "general"** (keeps the dashboard's `{slug,name}` POST working). Unknown
  category → 404; archived category → 400 `category_archived`.
- `GET /api/tasks` gains `?category=` (composes with `project=`, `status=`,
  `assignee=`, `ready=`, `blocked=`, `stale=`, `label=`, `q=`).
- `POST /api/tasks/next` accepts `{"category"?}` alongside `{"project"?}`;
  unknown slug → 404 (same as project).
- `POST /api/tasks` into a project whose category is archived → 400
  `{"error":"category_archived"}`.
- New error body `{"error":"category_archived"}` → HTTP 400 → CLI exit 5.
  **No new exit codes** (still 0/3/4/5/6/7).
- NO `?category=` on `/api/events` or `/api/stream` in Phase O (Phase R).

## Archived-category cascade semantics

- Default views hide content under an archived category: `GET /api/projects`,
  unscoped `GET /api/tasks`, and the default event feed
  (`/api/events` without `?project=`) all require both `p.archived_at IS NULL`
  AND `c.archived_at IS NULL`.
- Explicit scope stays inspectable (hidden, not blocked-from-read): an explicit
  `?category=` (projects or tasks) drops the category-archived condition, and
  the explicit `?project=` branches are unchanged.
- `next` excludes archived categories UNCONDITIONALLY (scoped or not), same as
  its existing archived-project rule.
- Writes are blocked: task creation and project creation under an archived
  category → `category_archived`.

## CLI

- `-c` is now the global **category** flag (`canonFlag c → category`); env
  fallback `AGENTMAN_CATEGORY`. **Exception:** `am show <id> -c` still means
  `--comments` — `main.go` rewrites `-c → --comments` for the `show` verb only
  (`rewriteShowComments`).
- `am categories [--all] [--json]` — list categories (terse: slug, name,
  "(archived)"); `--json` includes `uid`.
- `am category new <slug> [name]` — prints slug. `am category archive|
  unarchive <slug>` — silent success.
- `am project new <slug> [name] -c <category>` — **category now required**
  (flag or `AGENTMAN_CATEGORY`); exit 5 with
  "no category: pass -c <slug> or set AGENTMAN_CATEGORY" otherwise.
  Note: `am new` (tasks) gains no `-c` — project slugs stay globally unique, so
  a project fully determines the category.
- `am project edit <slug> [--slug NEW] [--name N] [--vault-id X]
  [--vault-path Y]` — NEW; silent success; `--vault-id=` / `--vault-path=`
  (explicit empty) clear the binding; exit 1 when nothing to change.
- `am ls -c <cat>` filters by category (`--all` suppresses it, like `-p`).
- `am next -c <cat>` scopes the atomic pick; bogus slug → exit 3 (same
  ambiguity note as `-p`).
- `am wait --ready -c <cat>` scopes the readiness condition. The SSE stream
  stays **unscoped** for category (no `?category=` on /api/stream yet); events
  just trigger the category-scoped REST re-check (ADR-023 pattern). `-c` with
  `--done` is ignored, like `-p` today.
- usage() updated: category verbs, `-c CAT` on ls/next/wait, `project new ...
  -c <category>`, `project edit`, AGENTMAN_CATEGORY in the Env paragraph.
  Exit-codes line unchanged.

## Dashboard (minimal keep-working)

- Feed renders explicit cases for `category.created/archived/unarchived`
  ("who archived category slug") in `evText` + `describeText` — the default
  branch would render a literal "null" ref for project-less events.
  `project.patched` falls through to the default (has a project ref).
- Project strip reloads on `category.archived`/`category.unarchived` so the
  cascade shows live.
- `POST /api/projects` from the dashboard still works (server defaults the
  category to "general").

## db export/import

- `validateImportCandidate` deliberately keeps the **v1 baseline** required-
  table set (no categories/task_deps/task_labels): pre-v4 snapshots stay
  importable and migrate on the next OpenStore. Code comment added saying so.
- `schema_version` ceiling automatically picks up 4; a v5-stamped snapshot is
  rejected.
- `exportDB` unchanged (VACUUM INTO snapshots everything, categories included).

## Behavior changes

- `OpenStore` now refuses to open a DB whose recorded `schema_version` is newer
  than the binary supports ("database schema_version N is newer than supported
  M — upgrade am"): `am serve` and every CLI command error out cleanly against
  a too-new DB instead of misbehaving (e.g. an older binary inserting projects
  the newer schema's queries would silently hide). Same ceiling
  `validateImportCandidate` already applies to import snapshots.

## Decisions (ADR-grade)

1. **Stable-ID format `amc_`/`amp_` + 16 lowercase hex** (8 bytes crypto/rand,
   stdlib only — no ULID dep). Immutable after creation; survives slug renames;
   the vault's canonical correlation key. A bare `p_` prefix was avoided because
   the vault's own project IDs use that namespace (R2). Insert paths retry up
   to 3 attempts on a uid UNIQUE collision (`isUniqueErr`); the migration
   backfill assigns per-row without retry (collision odds ~2^-64).
2. **`projects.category_id` is nullable in SQL** — deviation from the
   requirement's "NOT NULL FK": SQLite `ALTER TABLE ADD COLUMN` cannot add a
   NOT NULL column without a constant default (wrong for an FK). The NOT NULL
   invariant is app-enforced: `CreateProject` always sets it, v4 backfills all
   NULLs to `general`, and nothing can clear it (`category_id` not patchable).
3. **Globally-unique project slugs** (no per-category namespacing): task refs
   like `web-3` and `AGENTMAN_PROJECT` keep working unchanged, and `am new`
   needs no `-c`. A slug names exactly one project across the instance.
4. **`-c` collision fix**: `-c` becomes the category alias everywhere except
   `am show`, where a pre-parse token rewrite preserves the documented
   `am show <id> -c` (comments). Chosen over a per-verb alias table to keep
   canonFlag simple.
5. **Dashboard default-category**: `POST /api/projects` with empty category
   maps to `general` server-side rather than 400, so the existing dashboard
   (and any script posting `{slug,name}`) keeps working without a UI change.
6. **`wait --ready -c` streams unscoped**: REST re-check carries the scope;
   stream-side category filtering arrives with Phase R. Slightly chattier
   (re-checks on out-of-scope events) but correct.
7. **Old snapshots importable by design**: required-tables set in
   `validateImportCandidate` is pinned to the v1 baseline.

## Deviations from the implementation map

- `ArchiveProject`/`UnarchiveProject` (not listed as touched) were lightly
  refactored to load their return payload via the new `getProjectTx`, so the
  archive/unarchive responses carry the same extended project JSON
  (uid/category/vault) as every other endpoint instead of zero-valued fields.
  Event kinds, idempotency, and tx shape unchanged.
- The `am show -c` rewrite was extracted into `rewriteShowComments()` (instead
  of an inline loop in `main()`) so the regression test can exercise the real
  rewrite path.
- Everything else follows the map, including all flagged decisions as approved.

## Tests added/extended (per file)

- `cmd/am/migrate_test.go`: `TestMigrationV4Fresh` (version 4, categories
  table, seeded general with `amc_` uid, new projects columns),
  `TestMigrationV4ExistingDB` (hand-built v3 DB → v4: both projects in
  general, distinct `amp_` uids, task ids/refs/claimed_at/labels untouched,
  reopen keeps uids / no double-apply); `TestMigrationV3AddsClaimedAt` updated
  to assert `currentSchemaVersion` instead of literal 3; new `uidRe` helper;
  `TestOpenStoreRejectsNewerSchema` (bump meta.schema_version past current,
  reopen → error naming both versions).
- `cmd/am/store_test.go`: `TestCreateCategory`, `TestArchiveUnarchiveCategory`,
  `TestCreateProjectWithCategory`, `TestPatchProject`,
  `TestCategoryArchiveCascade`, `TestListTasksCategoryFilterComposes`,
  `TestNextTaskCategoryScoping` (acceptance sketch 3),
  `TestCreateTaskArchivedCategory`; `TestTaskLabelsTableExistsOnReopenedDB`
  updated to `currentSchemaVersion`; existing callsites updated to the new
  `CreateProject`/`ListProjects`/`NextTask` signatures.
- `cmd/am/server_test.go`: `TestCategoryEndpoints`,
  `TestProjectPayloadAndCategoryFilter`, `TestListTasksCategoryParam`,
  `TestNextEndpointCategoryBody`, `TestPatchProjectEndpoint`,
  `TestCreateTaskArchivedCategory400`; new helper `mustCreateCategory`.
- `cmd/am/cli_test.go`: `TestCmdCategoryVerbs`,
  `TestCmdProjectNewRequiresCategory`, `TestCmdProjectEdit`,
  `TestCmdLsCategoryWireFormat`, `TestCmdNextCategory`,
  `TestCmdShowDashCStillPrintsComments` (alias-rewrite regression),
  `TestRewriteShowComments`.
- `cmd/am/wait_test.go`: `TestWaitReadyCategoryScoped`,
  `TestWaitReadyCategoryEnv`, `TestWaitReadyCategoryTimeout`; new goroutine-safe
  helper `createTaskRaw`.
- `cmd/am/db_test.go`: `TestExportContainsCategories`,
  `TestImportPreCategorySnapshot`, `TestImportRejectsNewerSchema`.
