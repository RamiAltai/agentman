# Project Review — 2026-06-09

Independent review of agentman at `v0.5.0` (HEAD). Scope: gaps, improvements, and proposed
roadmap items. Items already cataloged in `architecture/known-risks-and-gaps.md` are not
repeated; this covers what's new.

## Overall assessment

Strong project. The docs-to-code discipline (ADRs, risk register, phased roadmap, 95 tests,
race-clean CI with govulncheck) is well above typical for a personal tool, and the core design
choices — single writer, events-as-cursor, no-npm dashboard, terse agent-first CLI — are
coherent and consistently applied. The findings below are mostly polish and product direction,
not structural problems.

## 1. Housekeeping / doc drift (fix first, all small)

- **CHANGELOG not cut for v0.5.0.** HEAD is tagged `v0.5.0`, but the dependency + graph work
  still sits under `[Unreleased]`. Rename to `## [0.5.0] - 2026-06-08` and open a fresh
  `[Unreleased]`.
- **ROADMAP.md is stale.** B1/B2 still say "commit pending fixes + tag v0.4.0" — both long
  done (tags through v0.5.0 exist). Check them off; the "Suggested order" footer also needs a
  refresh. The roadmap is the project's front door for contributors and agents; stale state
  here undermines an otherwise excellent doc set.
- `known-risks-and-gaps.md` line counts ("store.go ~1292 lines, app.js ~1696") drift with every
  change — consider dropping exact numbers.

## 2. Bugs / correctness (verified in code)

- **`app.js` `api()` — uncaught `JSON.parse`.** `const data = txt ? JSON.parse(txt) : null`
  throws on any non-JSON response (proxy error page, truncated body) and crashes the calling
  flow. Wrap in try/catch, fall back to `HTTP <status>` message. _S_
- **`server.go` `handleStream` Flusher check uses `http.Error`** (plain text) while every other
  error is JSON — the dashboard's `api()` would choke on it (compounds the bug above). Use
  `writeErr`/`writeJSON`. _S_
- **`db.go` prune `--before` is unvalidated.** The date string goes straight into
  `created_at < ?` string comparison. Works for valid `YYYY-MM-DD` via ISO-8601 lexicographic
  ordering, but `--before 2026-13-99` or a typo silently prunes nothing (or everything, if a
  future format change breaks the ordering assumption). Validate with `time.Parse`. _S_
- **`store.go` event insert: `json.Marshal` error discarded** (`b, _ := json.Marshal(data)`).
  Low likelihood, but a marshal failure would silently corrupt the audit trail the whole
  replay/reconnect design depends on. _S_
- **No length limits on title/body/comments.** The 1 MiB `io.LimitReader` is the only cap; a
  runaway agent can insert ~1 MB titles that render into every board card and SSE event.
  Add validation (e.g. title ≤ 500 chars, body/comment ≤ 64 KB) → `ErrValidation`. _S_
- **Priority unvalidated** — any int accepted; docs say 0–3. Clamp or reject. _S_
- **`update.go` semver compare ignores prerelease segments** — a `-rc` build won't see the
  stable release as newer. Minor given the audience. _S_
- ~~Dashboard drag-drop optimistic move not reverted~~ — **withdrawn on closer reading**:
  `moveTask`'s catch already restores the previous status and re-renders on any failure.
- **SSE reconnect backoff has no jitter** — multiple open tabs resync in lockstep. Cosmetic at
  localhost scale; one line to fix. _S_

## 3. The biggest product gap: stale claims

There is **no lease/timeout/recovery story for `am claim`**. This is the most important gap
for the stated mission: agents crash, hit token limits, or get killed mid-task constantly.
Today the task sits in `doing` assigned to a dead agent until a human notices and runs
`am drop`. For a board "designed for AI agents," dead-agent recovery is core, not optional.

Suggested design (fits existing architecture, no daemon needed):

- Add `claimed_at` (or reuse `updated_at`) and a `--stale <duration>` filter:
  `am ls --stale 2h` → tasks in `doing` untouched (no status change, no comment) for 2h.
- `am claim <id> --steal-stale 2h` → atomic conditional UPDATE that takes over only if the
  current claim is stale. Keeps the single-conditional-UPDATE claim semantics; loser still
  gets 409. Emit a `task.reclaimed` event naming the previous assignee.
- Optional later: `am touch <id>` as an explicit cheap heartbeat (~5 tokens), and a dashboard
  "stale" badge on doing-cards older than a threshold.

## 4. Agent-ergonomics gaps (the product's edge — invest here)

In rough priority order:

1. **`am next`** — atomically pick *and claim* the highest-priority ready task in one call
   (`am next -p web` → claims and prints id, exit 3 if none). Today this is
   `am ls --ready` + parse + `am claim` + handle the race — three round-trips and a failure
   mode. One verb collapses the whole agent work loop; this is the single highest-value
   addition for the token-cheap mission. ~15 tokens per pickup.
2. **`am wait`** — block until a condition (`am wait <id> --done --timeout 300`,
   `am wait --ready -p web`). Server already has SSE; the CLI can ride the same stream.
   Eliminates external polling loops in orchestration scripts.
3. **Search** — `am ls --grep "login"` / `GET /api/tasks?q=`. SQLite LIKE is fine; FTS5 later
   if ever needed. Agents currently can't find "that task about X" without listing everything.
4. **Labels** — `am label <id> +bug +urgent`, `am ls -l bug`. Status+project is too coarse
   once multiple agent types share a board (e.g. route review-tasks vs code-tasks).
5. **Bulk status/assign** — `am status 12 13 14 done`. Cheap to add, saves N round-trips.
6. **Claim idempotency signal** — re-claiming a task you already own exits 0 identically to a
   fresh claim; scripts can't distinguish. Consider distinct output or exit code.
7. **Structured `am show --json` is good; `am ls --json` could take `--fields`** to trim
   token cost of parsing. Minor.

## 5. Distribution & operations

- **Release binaries.** `go install` requires a Go toolchain — a real adoption barrier for
  the "zero ops" pitch. Add goreleaser (or a 20-line workflow) publishing darwin/linux/windows
  binaries on tag push; `am update` can then download the binary directly instead of shelling
  out to `go install`. Homebrew tap later if traction warrants.
- **Automatic event compaction.** `am db prune` exists but is manual/offline. A retention
  option on the server (`am serve --retain 90d`, prune on startup + daily ticker, same
  single-writer connection so no offline requirement) closes the "long-running instance grows
  forever" residual properly.
- **`isServerRunning` guard** (known residual): cheap improvement — also probe the port
  derived from `--db`'s lock state or document loudly in `--help` that the guard only checks
  `AGENTMAN_URL`.

## 6. Proposed roadmap

### Phase J — Correctness & hygiene — **DONE (2026-06-09)**
- [x] J1 Cut CHANGELOG 0.5.0; refresh ROADMAP checkboxes (§1)
- [x] J2 `api()` JSON.parse guard + `handleStream` JSON error (§2)
- [x] J3 Validate prune `--before` date; check `json.Marshal` err in event insert (§2)
- [x] J4 Title/body/comment length limits + priority bounds → `ErrValidation` (§2)
  - Plus `am update` prerelease semver compare and SSE backoff jitter (§2).
  - Tests added: `TestInputLimits`, `TestPruneEventsRejectsBadDate`, 4 prerelease cases in
    `TestUpdateAvailable`. Drag-drop revert finding withdrawn (already handled).

### Phase K — Stale-claim recovery — **DONE (2026-06-12)**
- [x] K1 `claimed_at` + `am ls --stale <dur>` + stale badge on dashboard
- [x] K2 `am claim --steal-stale <dur>` atomic takeover + `task.reclaimed` event

### Phase L — Agent work loop — **DONE (2026-06-13)**
- [x] L1 `am next` — atomic pick+claim of best ready task
- [x] L2 `am wait` — block on task/board conditions over SSE
- [x] L3 Bulk `am status`/`am assign` over multiple ids

### Phase M — Findability — **DONE (2026-06-13)**
- [x] M1 Search: `?q=` API + `am ls --grep` + dashboard filter box
- [x] M2 Labels: schema + CLI + API + board chips/filter

### Phase N — Distribution (M)
- [ ] N1 Goreleaser release binaries; `am update` downloads binary
- [ ] N2 Server-side auto-prune (`--retain`) replacing manual-only compaction

### Phase O — agentic_brain foundation — **DONE (2026-06-13)**

First phase of the agentic_brain integration train (see `agentman_requirements.md`, outside
this repo): categories, stable IDs, vault binding, migration v4. Satisfies requirements
**R1 + R2 + R3 + R8**.

- [x] O1 Category layer (R1) — `categories` table, `am categories`/`am category new|archive|
  unarchive`, `-c` + `AGENTMAN_CATEGORY` on `ls`/`next`/`wait --ready`, `?category=` filters,
  archived-category cascade (`400 category_archived` on writes)
- [x] O2 Stable IDs (R2) — immutable `amc_`/`amp_` uids on categories/projects (crypto/rand,
  16 hex); slugs renameable via `am project edit --slug` without breaking the correlation key
- [x] O3 Vault binding (R3) — `vault_project_id`/`vault_path` on projects;
  `am project edit --vault-id/--vault-path` + `PATCH /api/projects/{slug}`
- [x] O4 Migration v4 (R8) — seeds default category `general`, attaches existing projects,
  backfills uids; zero data loss (task ids/refs/`claimed_at`/labels untouched); pre-v4 snapshots
  stay importable; `OpenStore` now rejects a too-new `schema_version`
  - 4 new event kinds (`category.created/archived/unarchived`, `project.patched`; total 21);
    +30 tests (now 174). → ADR-025, `CHANGELOG.md`.
  - Next in the train: **Phase P** task metadata (shipped, see below), **Phase Q** scoping
    enforcement (identity scope, exit 8, proposal-inbox carve-out, R4), **Phase R** category
    dashboard + scoped feed (R6), **Phase S** scope tokens (R5).

### Phase P — Task metadata — **DONE (2026-06-13)**

Second phase of the agentic_brain train: free-form `key=value` pairs on tasks with key-presence
filters across the listing and pickup verbs. Satisfies requirement **R7**.

- [x] P1 `task_meta` table + `meta` on create/patch/get/list — keys normalized like labels,
  values opaque ≤ 500 bytes; empty value removes on edit (rejected at create); duplicate keys
  after normalization rejected on both paths; meta-only patches don't bump `updated_at`;
  no migration step (`CREATE TABLE IF NOT EXISTS`, schema version stays 4)
- [x] P2 presence filters — `?meta_key=` on `GET /api/tasks`, `"meta_key"` in
  `POST /api/tasks/next`, `--meta KEY` on `am ls`/`am next`/`am wait --ready`; `NextTask`
  refactored to take a `NextFilter` struct (Phase Q extension point); the next/wait predicates
  are textually identical so a released wait is always pickable
- [x] P3 CLI repeatable `--meta k=v` (new `multiFlags` parser registry) on `am new`/`am edit`
  (all flags fold into one request); `am show` `meta:` line; dashboard modal Meta section +
  `(meta: …)` feed suffix on `task.patched`
  - No new event kinds (catalog stays 21), no new error/exit codes; +25 tests (now 199).
    → ADR-026, `CHANGELOG.md`.
  - Next in the train: **Phase Q** scoping enforcement (shipped, see below), **Phase R** category
    dashboard + scoped feed (R6), **Phase S** scope tokens (R5).

### Phase Q — Scoped agent identity & enforcement — **DONE (2026-06-13)**

Third phase of the agentic_brain train: confine an agent to a category (or one project) and enforce
it server-side. Satisfies requirement **R4**.

- [x] Q1 Scope identity — `am init <tasktype> -c CAT [-p PROJ]` (JSON identity file; legacy
  plain-text = unscoped, re-init after upgrade), `AGENTMAN_SCOPE` override, `am whoami` scope line;
  the CLI sends `X-Agent-Scope` on every request
- [x] Q2 Server enforcement — `scopeOf(r)` (sole reader, the Phase S swap point) + per-verb
  `check*`/`narrowScope`; out of scope → `403 out_of_scope` → **new CLI exit code 8**; reads policy
  loud-named / silent-unfiltered / proposals-readable; `am next` scope merged inside the atomic
  pick+claim; category endpoints 403 for any scope, project-create only for a category-scoped agent
  in its own category
- [x] Q3 Proposals carve-out — `am serve --proposals` / `AGENTMAN_PROPOSALS` (default
  `meta/proposals`), keyed by the (category, project) pair (slug-squat-proof), inert when missing,
  not extended to `am next`; own-proposal comments via `tasks.created_by` (migration **v5**,
  best-effort backfill from the latest `task.created` event)
  - Denials log-only (no new event kind; catalog stays 21); `X-Agent-Scope` is client-asserted
    (accident prevention, not auth — Phase S scope tokens upgrade it); +32 tests (now 231).
    → ADR-027, `CHANGELOG.md`.
  - Next in the train: **Phase R** category dashboard + scoped feed (R6), **Phase S** scope
    tokens (R5).

### Later / if demand appears
- Due dates + `--due-before` filter; webhooks on event kinds (Slack/CI triggers);
  MCP server mode (`am mcp` exposing the board as MCP tools — natural fit for the
  Claude-agent audience and likely a strong differentiator); multi-board/remote mode
  (the full Phase-G auth+TLS project — keep parked).

### Sequencing rationale
J is an afternoon and removes real crashes. K is the gap most likely to bite actual users
(dead agents holding tickets). L sharpens the core differentiator (cheapest possible agent
loop). M and N are growth features — worth doing when a second user or a second agent
framework shows up.
