# CHANGES.draft — Phase P: task metadata (R7)

Input for the docs-sync stage. Branch `phase-p-meta`, stacked on
`phase-o-foundation`.

## Summary

Tasks can now carry free-form `key=value` metadata pairs. Keys are normalized
and validated like labels (lowercase, 1-50 chars of `a-z 0-9 . _ -`); values
are opaque strings (1-500 bytes). Key PRESENCE (never the value) is the
filterable unit across `am ls`, `am next`, and `am wait --ready`.

## Schema

- New table `task_meta (task_id, key, value, PRIMARY KEY (task_id, key))` with
  `ON DELETE CASCADE` from tasks, plus index `idx_task_meta_key` on `key`.
- Created via `CREATE TABLE IF NOT EXISTS` in schema.sql — **no migration
  step, no schema_version bump** (the task_labels precedent;
  `currentSchemaVersion` stays 4). Verified by
  `TestTaskMetaTableExistsOnReopenedDB`.

## API changes (all externally visible)

- `POST /api/tasks` body gains optional `"meta": {"k":"v", ...}`. All pairs
  validated up front; empty values are rejected at create (removal has no
  meaning there) → 400 `validation`.
- `PATCH /api/tasks/{id}` accepts `"meta": {"k":"v", "k2":""}`. Upsert
  semantics; an empty-string value REMOVES the key (absent-key removal is a
  silent no-op); non-string values or a non-object `meta` → 400. Multi-key
  patches are all-or-nothing (one tx; a validation failure on any key rolls
  back every key).
- `GET /api/tasks` gains `?meta_key=K` (presence filter, composes with every
  existing filter incl. `ready`/`category`/`status`). Bad key → 400. List
  rows now include `meta` (stitched via one follow-up SELECT — values may
  contain `,`/`=`, so the labels GROUP_CONCAT trick is unsafe for them).
- `GET /api/tasks/{id}` response gains `"meta": {...}` (omitted when empty).
- `POST /api/tasks/next` body gains `"meta_key": "K"` — only tasks carrying
  the key are pickable. Bad key → 400; no carrier → 404. Priority-then-FIFO
  ordering and the single conditional-UPDATE atomicity are unchanged.
- Error mapping reuses the existing sentinels (`ErrValidation` → 400 → CLI
  exit 5). **No new error codes, no new exit codes.**

## Events

- **No new event kinds** (catalog stays at 21).
- `task.created` data gains `"meta": {k: v}` when the task is created with
  meta.
- `task.patched` data gains a `"meta"` sub-object in the existing delta
  shape: `{"meta": {"k": [old, new]}}` with `null` for absent (so a removal
  is `[old, null]`, an add `[null, new]`). One event per PATCH regardless of
  how many keys changed.
- **Meta-only patches do NOT bump `updated_at`** — meta is metadata like
  labels; refreshing the activity timestamp would keep a stale claim alive
  (ADR-024 / AddLabel precedent). Mixed field+meta patches still bump.

## CLI

- New repeatable flag `--meta` (the parser gained a `multiFlags` registry,
  `Args.multi`, and `a.all(k)`; single-value flags remain last-wins).
- `am new "title" ... [--meta k=v]...` — all pairs ride in the one POST.
  `--meta k=` (empty value) and `--meta bare` (no `=`) are usage errors
  (exit 5). Tokens split at the FIRST `=`; values may contain `=`.
- `am edit <id> [--meta k=v]... [--meta k=]` — all repeated flags fold into
  ONE PATCH (structural atomicity: the dispatcher's auto+packet flip is one
  tx/one event); `--meta k=` removes the key. "nothing to change" message now
  mentions `--meta`.
- `am ls [--meta KEY]`, `am next [--meta KEY]`, `am wait --ready [--meta KEY]`
  — single key only (two `--meta` → exit 5; `key=value` form → exit 5).
  `am wait <id> --done --meta K` is a usage error (exit 1).
- `am show <id>` prints one `meta: k=v k2=v2` line (keys sorted) after the
  labels line, only when meta exists.
- usage() in main.go updated for all five verbs.

## Dashboard (web/)

- Task modal: read-only "Meta" section after Labels (sorted keys; muted key,
  monospace value; built with el()/textContent only — no innerHTML).
- Feed/history `task.patched` lines append `(meta: k1, k2)` when the event
  delta contains meta.
- New CSS: `.meta-row` / `.meta-key` / `.meta-val` (tones match
  `.dep-status`).

## Internal / store API

- `Task` gains `Meta map[string]string` (`json:"meta,omitempty"`).
- `TaskFilter` gains `MetaKey string`.
- New `normalizeMetaKey` (reuses `labelRe`/`maxLabelLen`, own error text:
  "meta key must be 1-50 chars of a-z 0-9 . _ -"); value cap = `maxTitleLen`.
- New `applyMetaTx(tx, taskID, meta)` — sorted-key walk, upsert via
  `INSERT ... ON CONFLICT DO UPDATE`, delete on empty value, returns the
  delta map; any error aborts the caller's tx.
- **`NextTask` signature changed**: `NextTask(project, category, agent
  string)` → `NextTask(f NextFilter, agent string)` with
  `NextFilter{Project, Category, MetaKey string}` — Phase Q extends the
  struct instead of widening the signature again. Both call sites (handler +
  tests) updated.
- The NextTask meta predicate is textually identical to ListTasks' (the
  wait/next same-condition invariant: a task that releases
  `am wait --ready --meta K` must be pickable by `am next --meta K`),
  enforced by comment at both sites.
- `getTaskTx` deliberately does NOT load meta (labels parity) — so PATCH/
  claim responses omit it, like labels.
- Meta is server-side only state; the SSE stream is untouched (ADR-023: the
  `--meta` wait scope narrows only the REST re-check, never the stream).

## Decisions and rationale

1. **task_meta via CREATE TABLE IF NOT EXISTS, no migration bump** — additive
   table, same path task_labels took; old DBs heal on reopen.
2. **Multi-flag parser registry** (`multiFlags` + `Args.multi` + `a.all()`)
   rather than comma-splitting one value — values are opaque and may contain
   commas; repetition is the unambiguous CLI shape. `--meta` is NOT in
   boolFlags and has no short alias.
3. **Empty value removes on edit, ErrValidation on create** — PATCH needs a
   removal verb inside one atomic body; create has nothing to remove.
4. **Keys normalized like labels, values opaque ≤ maxTitleLen** — keys are
   filter/index material (charset excludes `=`/space/`,` keeping CLI tokens
   and any future concat safe); values render on cards/SSE so they get the
   title cap.
5. **Reuse task.patched/task.created instead of new event kinds** — keeps the
   catalog at 21; the delta sub-object preserves old/new for audit.
6. **Meta-only patches don't bump updated_at** — ADR-024 (labels/deps)
   precedent: metadata edits must not refresh stale-claim liveness.
7. **Meta in ListTasks/GetTask but not getTaskTx** — labels parity; the terse
   tx-internal read stays cheap.
8. **NextFilter struct refactor** — third scope dimension; a fourth string
   parameter would be unreadable and Phase Q adds more.
9. **List stitch via follow-up SELECT, not GROUP_CONCAT** — values may
   contain the separator; one extra parameterized query per list call is the
   safe shape.

## Deviations from the implementation map

- None functionally. One test-plan adjustment: `TestNextTaskMetaFilter`
  drains the second-priority carrier before asserting ErrNotFound (the map's
  scenario list left one unblocked carrier behind), and the mixed-patch
  bump assertion in `TestMetaOnlyPatchDoesNotBumpUpdatedAt` sleeps 10ms past
  the strftime millisecond resolution and patches priority to 1 (the store's
  raw-input default priority is 0, so a 0-patch would be a no-op).

## Tests added (per file)

- `store_test.go`: TestTaskMetaCRUD, TestTaskMetaValidation,
  TestPatchTaskMetaAtomicOneEvent, TestPatchTaskMetaNoOpNoEvent,
  TestMetaOnlyPatchDoesNotBumpUpdatedAt, TestNextTaskMetaFilter,
  TestNextTaskMetaRaceDistinctWinners, TestListTasksMetaKeyFilter,
  TestListTasksReturnsMeta, TestDeleteTaskCascadesMeta,
  TestTaskMetaTableExistsOnReopenedDB (+ helper metaTaskMk; existing
  NextTask call sites updated to the NextFilter signature).
- `server_test.go`: TestCreateTaskWithMeta, TestPatchTaskMetaEndpoint,
  TestNextEndpointMetaBody, TestListTasksMetaKeyParam.
- `wait_test.go`: TestWaitReadyMetaNoHotSpin,
  TestWaitReadyMetaReleasedByCreate, TestWaitReadyMetaReleasedByPrereqDone,
  TestWaitMetaUsageErrors (+ helper createMetaTaskRaw).
- `cli_test.go`: TestParseMultiFlag, TestCmdNewMetaWireFormat,
  TestCmdEditMetaSinglePatch, TestCmdNextMetaWireFormat,
  TestCmdLsMetaWireFormat, TestCmdShowPrintsMeta.
