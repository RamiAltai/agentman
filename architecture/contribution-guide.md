# Contribution Guide

Practical onboarding. Pair this with `engineering-conventions.md` (how to write code) and
`planning-guide.md` (how to plan).

## Setup

- Install **Go 1.25.11+** (`go.mod` requires `go 1.25.11` for a security-patched stdlib; older
  1.25.x auto-upgrades via the Go toolchain). No other toolchain — no
  npm/node, no C compiler (pure-Go SQLite).
- Clone and build:

```sh
git clone https://github.com/RamiAltai/agentman
cd agentman
go build -o am ./cmd/am
./am version
```

There is no Makefile or Dockerfile — `go` is the whole toolchain. CI is configured in
`.github/workflows/ci.yml` (GitHub Actions): it runs `go build`, `go vet`, `gofmt -l`,
`go test -race -count=1 ./...`, `node --check cmd/am/web/app.js`, and `govulncheck` on
every push to `main` and on every pull request. Contributors should expect CI to gate PRs.

## Commands

```sh
go build -o am ./cmd/am                 # build the single binary (CLI + server)
go vet ./...                            # static analysis
go test ./...                           # run tests
gofmt -l cmd/am                         # list unformatted files (should be empty)
gofmt -w cmd/am                         # format
./am serve --db /tmp/dev.db --port 8788 # run a throwaway server (never touch your real DB)
./am help                               # full CLI surface
```

⚠️ After editing anything in `cmd/am/web/`, **rebuild** (`go build`) — the dashboard is embedded
via `//go:embed`, so a running/old binary serves stale assets. Hard-refresh the browser too.

## Running Tests

```sh
go test -race ./cmd/am/                     # run all tests with the race detector (258 tests)
go test ./...                               # equivalent short form
go test -run TestUpdateAvailable -v ./cmd/am/
```

Tests live next to the code in `cmd/am/` (11 test files):

- `update_test.go` — version-comparison logic.
- `store_test.go` — CRUD + validation, the atomic claim race (exactly one winner), archive/unarchive
  round-trip + idempotency, the strictly-increasing events cursor, feed hiding archived-project events
  (`TestFeedHidesArchivedProjectEvents`), task creation rejected into an archived project
  (`TestCreateTaskRejectsArchivedProject`), and stale-claim recovery (`TestStealStaleClaim`,
  `TestStealRaceExactlyOneWinner` — exactly one concurrent stealer wins, `TestListTasksStaleFilter`,
  `TestClaimSetsClaimedAt`, `TestDropClearsClaimedAt`), and findability (Phase M): the `?q=` search
  filter incl. LIKE-wildcard escaping, and labels (add/remove idempotency, validation, `?label=`
  filter, delete cascade, no `updated_at` bump, fresh-table existence); and categories (Phase O):
  create/archive/unarchive, project-in-category creation, `PatchProject`, the archived-category
  cascade, the composing `?category=` task filter, category-scoped `NextTask`, and task creation
  into an archived category rejected; and task metadata (Phase P): meta CRUD + validation (incl.
  duplicate-after-normalization rejection), one-event atomic multi-key patches, no-op patches
  emit no event, meta-only patches don't bump `updated_at`, the meta-filtered `NextTask` (incl.
  the distinct-winners race), the `MetaKey` list filter, list rows returning meta, delete
  cascade, and fresh-table existence on reopen; and scope (Phase Q): `taskScope`/`projectCategory`
  + `created_by` default (`TestTaskScopeAndProjectCategory`) and the scoped+meta `NextTask` race
  (`TestNextTaskRaceScopedCategoryMeta`); and category stats (Phase R): `TestListCategoriesCounts`
  (`ListCategoriesWithStats` — non-archived-only counts, recently-active non-human agents); and
  scope tokens (Phase S): `TestCreateToken_HashNotPlaintext` (hash != plaintext, `ListTokens` carries
  no hash/plaintext), `TestResolveToken` (resolve/unknown/revoked → `ErrInvalidToken`),
  `TestCreateToken_ScopeValidation` (unknown/cross-category/archived rejected), `TestRevokeToken`.
- `server_test.go` — HTTP status mapping (404 / 400 / lost-claim 409), the Host/CSRF guards and
  security headers, the archive/unarchive endpoints, HTTP 400 on task creation into an archived
  project (`TestCreateTaskIntoArchivedProject400`), `TestWriteErrHidesInternalDetail` (500 returns
  generic body), `TestRequestLoggerPassesThrough`, `TestRequestLoggerPreservesFlusher`, the
  `?stale=` filter (`TestListTasksStaleParam`), the steal-stale claim body
  (`TestStealStaleEndpoint`), the `?q=` search param incl. 400 on over-long input
  (`TestListTasksQueryParam`), the label endpoints (`TestLabelEndpoints`), and the Phase O
  surfaces (`TestCategoryEndpoints`, `TestProjectPayloadAndCategoryFilter`,
  `TestListTasksCategoryParam`, `TestNextEndpointCategoryBody`, `TestPatchProjectEndpoint`,
  `TestCreateTaskArchivedCategory400`), the Phase P meta surfaces (`TestCreateTaskWithMeta`,
  `TestPatchTaskMetaEndpoint`, `TestNextEndpointMetaBody`, `TestListTasksMetaKeyParam`), and the
  Phase Q scope sweep (`TestScopeClaimEnforcement`, `TestScopeNextEnforcement`,
  `TestScopeStealStale`, the proposals carve-out cases `TestScopeProposalsCarveOut`/`…Configurable`/
  `…MissingProjectInert`/`…WrongCategoryNoCarveOut`/`…Squat`, `TestScopeMutationSweep`,
  `TestScopeProjectScopedAgent`, `TestScopeReads`, `TestScopeHeaderValidation`,
  `TestScopeProjectCategoryEndpoints`), the Phase R `?category=` feed filter
  (`TestEventsCategoryFilter` — one category's task events only, excludes `category.*` and the
  other category, all of `?since=`/`?tail=`/`?before=`, unknown category → 404), and the Phase S
  scope-token surface (`TestTokenAdmin_RequiresUnscoped` — mint/list/revoke 403 for any scope,
  `TestTokenScopeOverridesHeader`, `TestInvalidTokenRejected` — 401 incl. a DELETE-bogus-token-is-401
  guard, `TestNoTokenPathUnchanged`, `TestTokenScopeMatrix`, `TestCreateTokenResponse` — plaintext
  once, never in `ls`).
- `migrate_test.go` — the forward-only migration runner (apply + version bump, skip ≤ current,
  idempotency, rollback), the v2 `archived_at` / v3 `claimed_at` columns, the v4
  category/stable-ID/vault migration (`TestMigrationV4Fresh`, `TestMigrationV4ExistingDB`), the v5
  `created_by` migration (`TestMigrationV5Fresh`, `TestMigrationV5ExistingDB` — latest-event
  backfill, pruned→NULL, reused-rowid case, idempotent reopen), and the open-time version ceiling
  (`TestOpenStoreRejectsNewerSchema`).
- `db_test.go` — `am db` export/import (roundtrip + perms, backup creation, garbage rejection,
  server-liveness probe), prune (`TestPruneEventsKeep`, `TestPruneEventsBefore`,
  `TestPruneEventsBeforeSameDayBoundary`), Phase O (`TestExportContainsCategories`,
  `TestImportPreCategorySnapshot`, `TestImportRejectsNewerSchema`), and Phase S
  `TestExportImportWithTokens` (a DB with a token round-trips; the token still resolves after import).
- `cli_test.go` — CLI command-path + exit-code tests (Phase E1). Constructs a `Client` directly
  against an `httptest` server. `captureStdout` captures os.Stdout via a pipe; `captureExit` stubs
  the `osExit` var (see "Test Seams" below) to intercept exit codes as panics. Covers: `cmdNew`
  prints only the numeric id; `cmdLs` terse output; mutations (`cmdStatus`/`cmdNote`/`cmdDrop`)
  silent on success; exit-code mapping 3/4/5/6/7 (incl. `TestExitNotStale` — exit 4 with `not stale
  yet`; 7, the wait timeout, is exercised in `wait_test.go`); `cmdNext` prints only the claimed id
  and exits 3 when nothing is ready; bulk `status`/`assign` (multiple ids, partial-failure exit
  codes); `--stale`/`--steal-stale` wire encoding (`TestStaleFlagsWireFormat`); `--grep`/`--label`
  wire encoding (`TestCmdLsGrepWireFormat`) and the `am label` verb (`TestCmdLabelAddRemove`,
  `TestCmdLabelPrintsLabels`, `TestCmdLabelUsage` — incl. rejection of flag-like tokens); the
  Phase O category verbs (`TestCmdCategoryVerbs`, `TestCmdProjectNewRequiresCategory`,
  `TestCmdProjectEdit`, `TestCmdLsCategoryWireFormat`, `TestCmdNextCategory`, and the
  `am show -c` alias-rewrite regression `TestCmdShowDashCStillPrintsComments` /
  `TestRewriteShowComments`); the Phase P `--meta` surface (`TestParseMultiFlag` — the repeatable
  `multiFlags` parser, `TestCmdNewMetaWireFormat`, `TestCmdEditMetaSinglePatch` — repeated flags
  fold into one PATCH, `TestCmdNextMetaWireFormat`, `TestCmdLsMetaWireFormat`,
  `TestCmdShowPrintsMeta`); the Phase Q exit-8 surface (`TestExitCodeForOutOfScope`,
  `TestCmdClaimOutOfScopeExit8`, `TestCmdNextOutOfScopeExit8`, `TestClientSendsScopeHeader`,
  `TestCmdStatusBulkOutOfScope` — per-id 403 line + continue, 404-before-403 → exit 3); the Phase S
  scope-token surface (`TestCmdTokenNewWritesIdentity` — merges the token into the identity file,
  plaintext on stdout line 1; `TestCmdTokenNewRequiresScope` — exit 5; `TestWhoamiPrintsTokenSet`;
  `TestClientSendsBearerNotScope` — `Authorization: Bearer` sent, `X-Agent-Scope` dropped;
  `TestExitCodeForUnauthorized` — `exitCodeFor(401)==9`; `TestDoOrFailUnauthorized` — 401 → exit 9);
  and pure formatter/parse table tests.
- `sse_test.go` — SSE streaming + reconnect (Phase E2). `TestSSEDeliversLiveEvent` opens
  `/api/stream`, creates a task, and asserts the `task.created` event arrives live.
  `TestSSEReplayOnReconnect` reconnects with `Last-Event-ID` and verifies gap-replay with
  dedupe (every replayed id strictly greater than the resume cursor). Phase R added
  `TestSSECategoryScopedStream` (category-scoped delivery + the `project.created` carve-out) and
  `TestSSECategoryReconnectReplay` (a category-scoped reconnect replays only that category's gap).
- `hub_test.go` — direct hub fan-out unit tests (Phase R): `TestHubCategoryScopedBroadcast`
  (in-category delivered, out-of-category + category-level NULL-project dropped, `project.created`
  delivered regardless), `TestHubProjectScopedBroadcast`, `TestHubUnscopedBroadcast`,
  `TestHubBroadcastNilNoPanic`.
- `identity_test.go` — identity (Phase E3). `cmdInit`→`resolveAgent` roundtrip, `AGENTMAN_AGENT`
  env override wins, `sanitizeType` table, `newIdentity` format. Uses the `AGENTMAN_AGENT_FILE` env
  seam so the real `~/.agentman` is never written. Phase Q added scoped-identity tests
  (`TestInitScopedWritesJSON`, `TestInitScopedCategoryProject`, `TestInitProjectRequiresCategory`,
  `TestLegacyPlainIdentityUnscoped`, `TestScopeEnvOverride`, `TestWhoamiPrintsScope`,
  `TestParseScope`).
- `web_test.go` — dashboard source-level asset guards. `TestDashboardNoXSSSinks` (Phase E4) reads
  the embedded `web/app.js` + `web/index.html` via the `webFS` embed.FS and asserts none of
  `.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear.
  `TestDashboardThemeAssets` (ADR-030) asserts the dark/light theme wiring stays present: the
  `:root[data-theme="light"]` CSS override block, the inline `am.theme` FOUC-guard script, and the
  `#themeToggle` button. `TestDashboardParityAffordances` (ADR-031) asserts the CLI↔GUI parity
  affordances stay wired: the create/archive-category, project-category-picker, project-edit,
  board-filter, editable-meta, and release markers in `app.js`/`index.html`/`app.css`.
- `wait_test.go` — `am wait` (Phase L). Already-satisfied, event-driven (`TestWaitDoneEventArrives`,
  `TestWaitReadyOnPrereqDone`), and cross-project (`TestWaitDoneCrossProject` — `AGENTMAN_PROJECT`
  must not scope the `--done` stream) waits; exit 7 on timeout, exit 3 not-found, exit 6 server
  down, usage errors, and the `parseWaitTimeout` table. Phase O added the category-scoped
  `--ready` waits (`TestWaitReadyCategoryScoped`, `TestWaitReadyCategoryEnv`,
  `TestWaitReadyCategoryTimeout`); Phase P added the meta-scoped `--ready` waits
  (`TestWaitReadyMetaNoHotSpin`, `TestWaitReadyMetaReleasedByCreate`,
  `TestWaitReadyMetaReleasedByPrereqDone`, `TestWaitMetaUsageErrors`); Phase Q added the scoped
  `--ready` waits (`TestWaitReadyScopedTimeout`, `TestWaitReadyScopedReleased`,
  `TestWaitReadyExplicitOutOfScopeExit8`).

Behavioral dashboard JS (the modal flows, delete confirms, feed pagination) remains untested —
see `known-risks-and-gaps.md`. New behavioral tests are welcome.

### Test Seams

Three seams exist specifically to make the CLI and identity layers testable without process-level
side effects. Use them when adding tests for CLI commands or identity:

| Seam | File | How to use in tests |
|---|---|---|
| `var osExit = os.Exit` | `cli.go` | Replace with a panic via `captureExit(t, fn)` in `cli_test.go` to intercept exit codes without killing the process. |
| `captureStdout(t, fn)` | `cli_test.go` | Redirects `os.Stdout` to a pipe for the duration of `fn`; returns captured output. Safe with `t.Setenv`. |
| `AGENTMAN_AGENT_FILE` env | `identity.go` | Set in tests (`t.Setenv`) to redirect the identity file to `t.TempDir()`, so the real `~/.agentman` is never written. |

To build a `Client` against an `httptest` server without needing a real network:

```go
ts := newTestServer(t)                            // spins up a temp server + temp DB
c := &Client{base: ts.URL, agent: "tester", http: ts.Client()}
cmdNew(c, parse([]string{"title", "-p", "proj"}))
```

### JS test runner

The project deliberately adopts **no JS test runner** (no npm, no jsdom — preserves the
single-binary/no-build-step ethos; see `decision-records.md` ADR-018). Dashboard XSS safety is
enforced by the `el()`/`textContent` convention plus the `TestDashboardNoXSSSinks` Go source-level
sink guard in `web_test.go`. Behavioral dashboard JS logic is not automatically tested.

## Inspecting Logs / Behavior

- The server logs to **stderr** (startup line, shutdown, the "update available" banner). Run it in
  the foreground or redirect: `./am serve --db /tmp/dev.db > /tmp/am.log 2>&1 &`.
- Inspect data directly: `sqlite3 /tmp/dev.db 'SELECT id,status,assignee,title FROM tasks;'`.
- Watch the live stream: `curl -N http://127.0.0.1:8788/api/stream`.
- Drive the API with the CLI: `AGENTMAN_URL=http://127.0.0.1:8788 ./am ls`.

## Common Change Locations

| Want to change… | Edit… |
|---|---|
| An HTTP endpoint / request handling | `cmd/am/server.go` |
| SQL / a query / the claim logic | `cmd/am/store.go` |
| The DB schema | `cmd/am/schema.sql` (+ structs in `store.go`) |
| A CLI command or its output | `cmd/am/cli.go` (+ dispatch in `cmd/am/main.go`) |
| CLI ↔ server HTTP / exit codes | `cmd/am/client.go` |
| DB export/import (`am db`) | `cmd/am/db.go` |
| The dashboard | `cmd/am/web/{index.html,app.css,app.js}` |
| Agent identity | `cmd/am/identity.go` |
| `am update` / version check | `cmd/am/update.go`, `cmd/am/version.go` |

## Adding a Feature

1. Plan with `planning-guide.md`. 2. Make the smallest change that fits the conventions.
3. Build + vet + test + gofmt. 4. Verify by running `am serve` against a throwaway DB.
5. Update `README.md`/`docs/` (if user-facing) and the matching `architecture/` doc.

## Adding Backend Functionality (e.g. a new endpoint)

1. Register the route in `Server.Handler()` (`server.go`), e.g. `mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)`.
2. Write `handleDeleteTask` — decode/validate, call a store method, `writeErr` on error.
3. Add the store method in `store.go` — parameterized SQL, mutation + `events` row in one tx,
   return `(result, *Event, error)`.
4. In the handler, `hub.Broadcast(ev)` **after** the store call returns.
5. Add the new event kind to the dashboard (`web/app.js evText`/`describeText`) if it should show.
6. (Recommended) add a test for the new behavior.

## Adding Frontend Functionality

- Extend the imperative renderers in `app.js` (`card`, `renderModal`, `renderBoard`, `feedItem`).
  Build DOM with `el()` (never `innerHTML`). Style with the CSS variables in `app.css`.
- Preserve keyboard/focus behavior (modal focus trap, `a`/`n`/`[`/`]`/`Esc`).
- Rebuild the binary and hard-refresh.

## Updating Data Models

- Add the column in `schema.sql` and the field in the relevant `store.go` struct; thread it through
  create/patch/get and the API and UI.
- ✅ A **forward-only migration runner exists** (`runMigrations` in `cmd/am/store.go`, ADR-010). To
  change an existing table, append a `{version, apply}` step to `schemaMigrations` and raise
  `currentSchemaVersion`; add a `migrate_test.go` case. `CREATE TABLE IF NOT EXISTS` in `schema.sql`
  still won't alter existing tables — use the runner for that.

## Adding Tests

- Put tests next to code as `*_test.go` in `cmd/am/`. Use table-driven style (`update_test.go`).
- For the store, open an `OpenStore(t.TempDir()+"/x.db")` and assert behavior — see `store_test.go`
  for the atomic-claim race and events-cursor patterns.
- For HTTP, use `net/http/httptest` against `Server.Handler()` with a temp `--db` — see
  `server_test.go` for status-mapping and guard examples.
- For schema changes, follow `migrate_test.go`: assert the runner applies the step and bumps
  `meta.schema_version`, and that a fresh DB lands on `currentSchemaVersion`.
- For CLI command paths, construct a `Client` directly against a `newTestServer(t)` and use
  `captureStdout`/`captureExit` — see `cli_test.go` for the pattern.
- For identity, set `AGENTMAN_AGENT_FILE` via `t.Setenv` to redirect the file to `t.TempDir()` —
  see `identity_test.go`.
- Do **not** add a JS test runner. Dashboard XSS safety is enforced by `TestDashboardNoXSSSinks` in
  `web_test.go`; extend that test if you add new sink-like patterns.

## Common Mistakes

- Forgetting to rebuild after a `web/` edit (stale embedded UI).
- Printing to stdout in a CLI command (breaks `id=$(am new …)` and wastes agent tokens).
- Broadcasting before commit, or doing SQL in a handler instead of the store.
- Assuming a schema change reaches existing DBs (it won't).
- Running `am serve` against your real `~/.agentman/agentman.db` while testing — use `--db /tmp/...`.

## Unknowns

- No documented PR/branch/review process (single-maintainer repo). CI gates code quality on push/PR,
  but a formal review/branching policy has not been written down. Confirm with the maintainer before assuming one.
