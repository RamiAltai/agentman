# Project Overview

## Summary

**agentman** (binary: `am`) is a tiny, self-hosted ticketing board — "an extremely simple
GitHub Projects" — **purpose-built for AI agents to drive**. One Go binary is both the server
(`am serve`, a localhost web dashboard + HTTP/JSON API) and the CLI (`am ls`, `am claim`, …).
Agents pick up / claim / comment / re-status tasks through a terse, token-cheap CLI; a human
watches progress live in a browser via Server-Sent Events. Data lives in an embedded SQLite file.

Confidence: **High** — stated directly in `README.md` and `docs/agent-integration.md`, and
matched by the code (CLI optimized for terse output; SSE live feed; localhost-only).

## Product Purpose

A shared task board that multiple autonomous agents use as their work queue, with a real-time
human-facing view. The defining constraint is **token efficiency for agents**: short commands,
silent success, machine-branchable exit codes.

Evidence:
- `README.md` "Why": "Built for agents, not humans first… A full pick-up→done cycle is ~65–75 tokens."
- `docs/agent-integration.md`: a `CLAUDE.md` snippet teaching agents the `am` cheatsheet.
- `cmd/am/cli.go`: terse formatters; mutations print nothing or just an id.

## Main Users

- **AI agents** (primary) — e.g. Claude Code sessions — invoking the `am` CLI or HTTP API.
  Each agent has a human-readable identity like `bugfix_050626_4821` (`cmd/am/identity.go`).
- **A human operator** (secondary) — runs `am serve` and watches/edits the board in the browser
  dashboard (`cmd/am/web/`).
- **Maintainer/contributor** — single-owner OSS project (module `github.com/RamiAltai/agentman`).

## Core Workflows

1. **Agent task loop:** `am init <tasktype>` (once per session) → `am next` (atomic pick+claim
   of the best ready task; `am claim <id>` for a specific one) → `am note <id> "…"` →
   `am status <id> done` → `am next` again (or `am wait --ready` to block until work exists).
   Evidence: `cmd/am/cli.go`, `cmd/am/wait.go`, `docs/agent-integration.md`.
2. **Human board management:** `am serve` → open `http://127.0.0.1:8787` → create projects/tasks,
   drag cards between status columns, comment, reassign. Evidence: `cmd/am/web/app.js`.
3. **Live monitoring:** the dashboard subscribes to `GET /api/stream` (SSE) and reflects every
   change in real time. Evidence: `cmd/am/server.go` `handleStream`, `cmd/am/hub.go`.
4. **Install / update:** `go install …/cmd/am@latest`; `am update` re-installs; `am serve` logs
   when a newer tag exists. Evidence: `cmd/am/update.go`, `README.md` "Updating".

## Key Features

- Multi-project boards; tasks with status (`todo/doing/blocked/done`), priority (`0–3`),
  assignee, comments. Evidence: `cmd/am/schema.sql`.
- **Category layer** (Phase O): projects are grouped into categories — the hierarchy is
  `instance → category → project → task → comment`. One `am serve` instance and one DB cover
  everything; `-c <cat>` / `AGENTMAN_CATEGORY` scope `am ls`/`am next`/`am wait --ready`, and
  archiving a category hides everything under it (creation into it is blocked). Built for the
  agentic_brain integration, where agents are confined to a category (Phase Q below). Evidence:
  `cmd/am/store.go` (`Category`, migration v4), `cmd/am/cli.go`, `cmd/am/server.go`.
- **Scoped agent identity & enforcement** (Phase Q): an agent can be confined to a category
  (default) or one project (`am init <tasktype> -c CAT [-p PROJ]`). The scope rides as the
  `X-Agent-Scope` header and the server enforces it on every mutation and on named reads — the
  hierarchy is the enforcement axis (`task→project→category` immutability makes the check sound
  outside the store tx). Out of scope → `403 out_of_scope` → CLI exit 8; unfiltered lists are
  silently narrowed; any agent may still file into a **proposals** project (default `meta/proposals`).
  The scope is a client-asserted label (accident prevention, not auth — scope tokens are a later
  phase); `tasks.created_by` (migration v5) backs the "comment on your own proposal" carve-out.
  Built for agentic_brain requirement R4. Evidence: `cmd/am/server.go` (`scopeOf`, `check*`,
  `narrowScope`), `cmd/am/identity.go`, `cmd/am/store.go` (`Scope`, migration v5).
- **Category dashboard + scoped feed** (Phase R): the human dashboard opens to a **category-home**
  view (cards per category with task counts + recently-active agents), drills into a single
  category's board, and exposes an **"All"** cross-category view — driven by linkable URL hashes
  (`#/`, `#/all`, `#/cat/<slug>`). `GET /api/categories` carries the count/active-agent rollups, and
  `GET /api/events` + `GET /api/stream` gain an unscoped `?category=` lens (a human's drill-down
  choice, not the agent identity scope). Built for agentic_brain requirement R6. Evidence:
  `cmd/am/web/app.js` (`route`/`loadOverview`/`viewParams`), `cmd/am/server.go`, `cmd/am/store.go`
  (`ListCategoriesWithStats`, `ProjectIDsInCategory`), `cmd/am/hub.go` (`subFilter`).
- **Scope tokens** (Phase S): turn Phase Q's client-asserted scope into a **server-enforced** boundary.
  A human (unscoped) mints a scope-bound bearer token with `am token new --scope <cat[/proj]>`; the
  agent's CLI then sends it as `Authorization: Bearer` and the server derives the scope from the token
  (it **wins over** the `X-Agent-Scope` header). Minting requires an unscoped caller, so a confined
  agent cannot forge a token for another scope; an invalid/revoked token → `401`/exit 9. Tokens are
  stored as **sha256 hashes** (never plaintext; shown once at mint), emit no event, and ride DB exports
  non-replayably. Built for agentic_brain requirement R5 (the final phase). It is loopback-only — not
  auth against an arbitrary local process (a filesystem read of the identity file = token possession).
  Evidence: `cmd/am/store.go` (`Token`, `CreateToken`/`ResolveToken`/`RevokeToken`, `hashToken`),
  `cmd/am/server.go` (`scopeOf`, `tokenAdminGuard`, `/api/tokens`), `cmd/am/cli.go` (`cmdToken`),
  `cmd/am/identity.go` (`token` field, `AGENTMAN_TOKEN`).
- **Stable IDs + vault binding** (Phase O): categories and projects carry an immutable `uid`
  (`amc_`/`amp_` + 16 hex) that survives slug renames (`am project edit --slug`), and projects
  can store `vault_project_id`/`vault_path` pointers back to the agentic_brain vault
  (`am project edit --vault-id/--vault-path`, `PATCH /api/projects/{slug}`). Evidence:
  `cmd/am/store.go` (`newUID`, `PatchProject`).
- **Atomic task claim** so two agents never grab the same ticket — conditional
  `UPDATE … RETURNING` in `cmd/am/store.go` `ClaimTask`.
- **Stale-claim recovery** so a crashed agent doesn't hold a task forever: `am ls --stale <dur>`
  lists assigned, not-done tasks with no activity for the given window, and
  `am claim <id> --steal-stale <dur>` atomically takes one over (same conditional-UPDATE trick,
  `StealStaleClaim`; exactly one stealer wins; a `task.reclaimed` event records the handoff; the
  dashboard shows a ⏳ stale chip on idle claimed cards). Evidence: `cmd/am/store.go`,
  `cmd/am/cli.go`, `cmd/am/web/app.js`.
- **Agent work loop** so agents need no list-then-claim dance: `am next` atomically picks AND
  claims the highest-priority ready task (FIFO within a priority; same conditional-UPDATE trick,
  `NextTask`), `am wait <id> --done` / `am wait --ready` block on the SSE stream until a task
  finishes or work appears (exit 7 on timeout), and `am status`/`am assign` take multiple ids.
  Evidence: `cmd/am/store.go`, `cmd/am/wait.go`, `cmd/am/cli.go`.
- **Task metadata** (Phase P): free-form `key=value` pairs on tasks (`am new`/`am edit
  --meta k=v`; `--meta k=` removes). Keys are normalized like labels; values are opaque
  (≤ 500 bytes). Key **presence** is filterable on `am ls`/`am next`/`am wait --ready`
  (`--meta KEY`, `?meta_key=`, the `meta_key` next-body field) — the hook an autonomous worker
  loop uses to wait for and pick up exactly the tasks marked for it (e.g. `auto=true`), built for
  the agentic_brain integration (R7). Evidence: `cmd/am/store.go` (`applyMetaTx`,
  `normalizeMetaKey`, `NextFilter`), `cmd/am/cli.go` (`multiFlags`), `cmd/am/server.go`.
- **Findability** so a grown board stays navigable: substring search over task titles and bodies
  (`am ls --grep <text>` / `GET /api/tasks?q=`; a header search box on the dashboard) and
  lightweight free-form labels (`am label <id> +bug -wip`, `am ls --label <l>`; clickable label
  chips + filter on the dashboard). Evidence: `cmd/am/store.go` (`likeEscape`, `normalizeLabel`),
  `cmd/am/cli.go`, `cmd/am/web/app.js`.
- **Live activity feed** backed by an append-only `events` table (also the SSE replay cursor).
- **Per-directory agent identity** that survives the fresh-shell-per-command model agents run in
  (`cmd/am/identity.go`).
- **Embedded dashboard** (no build step, no npm): kanban board, drag-and-drop status changes,
  collapsible/resizable activity panel, keyboard shortcuts, a light/dark theme toggle (system-default,
  then persisted), responsive. **CLI↔GUI parity** (ADR-031): the dashboard can also create/archive
  categories, pick a category when creating a project, edit a project (rename + vault binding), filter
  the board (ready/blocked/stale/assignee/meta via a header popover), edit task meta inline, and
  release a task — all previously CLI-only. Evidence: `cmd/am/web/`.
- **Multi-select project filter** on the dashboard: pick any number of project tabs to scope the
  board/feed at once ("All" clears the selection). Evidence: `cmd/am/web/app.js` `toggleProject`.
- **DB export/import**: `am db export` writes a consistent snapshot (`VACUUM INTO`), and
  `am db import` restores a validated candidate (integrity/FK checks, refuses while a server is
  running, backs up the existing DB first). Evidence: `cmd/am/db.go`.
- **Project archive/hide**: reversible soft-archive (`archived_at`) that hides a project from
  default views across all surfaces — tab bar (`ListProjects`), board (`ListTasks`), and activity
  feed (`ListEvents`/`RecentEvents`). Writing into an archived project is blocked: `CreateTask`
  returns `ErrProjectArchived` → HTTP 400 `{"error":"project_archived"}`. Archive/unarchive is
  accessible from the CLI (`am project archive`/`unarchive`) and from the "Manage" modal in
  the dashboard tab bar (`openManage`); that modal also archives/unarchives **categories**.
  Evidence: `cmd/am/store.go`, `cmd/am/cli.go`,
  `cmd/am/server.go`, `cmd/am/web/app.js`.
- **Hard delete (tasks, comments, projects)**: permanent removal via `DELETE /api/tasks/{id}`,
  `DELETE /api/tasks/{id}/comments/{cid}`, and `DELETE /api/projects/{slug}` (cascade via FK:
  project → tasks → comments). CLI: `am rm <id>` (silent, exit 3 if not found);
  `am project rm <slug> --yes` (requires `--yes`; cascade). The dashboard exposes inline two-step
  delete confirms in the task modal, per-comment, and the Manage modal. Events are never
  deleted — the audit log (including the `*.deleted` events) survives. Evidence: `cmd/am/store.go`
  (`DeleteTask`/`DeleteComment`/`DeleteProject`), `cmd/am/server.go`, `cmd/am/cli.go`,
  `cmd/am/web/app.js`.
- **Self-update**: `am update` + startup "update available" check (`cmd/am/update.go`).

## Domain Concepts

- **Category** — the layer above projects (`instance → category → project → task`): `slug`
  (unique, lowercase), `name`, a stable `uid` (`amc_…`), and the same reversible soft-archive as
  projects — with a cascade that hides everything underneath. Every project belongs to exactly
  one category (the default is `general`).
- **Project** — a named board (`slug` — globally unique across categories, `name`, a stable
  `uid` (`amp_…`), optional `vault_project_id`/`vault_path` binding) grouping tasks. Can be
  archived (hidden from default views, reversible) via `archived_at`.
- **Task** — a ticket. Has a global `id` (`#42`, the cheap wire ref) **and** a per-project `ref`
  (`web-3`, human-friendly). Status + priority + optional assignee.
- **Comment** — a threaded note on a task.
- **Label** — a lightweight free-form tag on a task (normalized lowercase; no separate catalog —
  a label exists iff some task carries it).
- **Meta** — a free-form `key → value` pair on a task (key normalized like a label; value opaque,
  ≤ 500 bytes). Key presence — never the value — is the filterable unit; no separate catalog.
- **Event** — an append-only record of every mutation; powers the activity feed, SSE stream, and
  reconnect replay (`events.id` is the cursor / SSE `Last-Event-ID`).
- **Agent identity** — `{tasktype}_{DDMMYY}_{4 digits}`, attached as the actor on writes; the
  per-directory identity record may also carry an optional scope and (Phase S) a bearer **token**.
- **Token** (Phase S) — a scope-bound bearer credential (`amt_…`) the server binds to a scope. Stored
  only as a sha256 hash; sent as `Authorization: Bearer`; its scope wins over `X-Agent-Scope`. Minted
  by an unscoped human, used by an agent; no separate users.

## Non-Goals

Inferred (Confidence: Medium–High) from `README.md` "Security" and the localhost bind:
- **Not** a multi-tenant / authenticated / internet-facing service. No auth, binds `127.0.0.1`.
- **Not** a heavyweight project manager (no sprints, due dates, or attachments today; the only
  metadata beyond status/priority/assignee is lightweight free-form labels — Phase M — and
  opaque `key=value` meta pairs — Phase P).
- **Not** a hosted SaaS — it's a single local binary; "back up = copy one file."

## Evidence

- `README.md`, `docs/agent-integration.md`
- `cmd/am/main.go` (subcommand dispatch), `cmd/am/cli.go` (CLI surface)
- `cmd/am/schema.sql` (domain model), `cmd/am/server.go` (API), `cmd/am/web/` (dashboard)

## Unknowns

- **Intended scale.** No stated target for concurrent agents / task volume. The single-writer
  SQLite design (`SetMaxOpenConns(1)`) implies modest scale, but this is not documented.
- **Roadmap.** Near-term gap-closing work is tracked in `ROADMAP.md` (repo root). Labels and search
  shipped in Phase M; the agentic_brain foundation (categories, stable IDs, vault binding) in Phase O,
  task metadata in Phase P, scoped agent identity & enforcement in Phase Q, the category dashboard +
  scoped feed in Phase R, and **scope tokens in Phase S** — the final phase. **With Phase S the entire
  agentic_brain integration train (O+P+Q+R+S) is complete: every MUST+SHOULD requirement R1–R8 is
  shipped.** Only NICE-to-have items remain unbuilt (webhook with egress filter, copyable `vault_path`
  in the dashboard, scoped `am db export -c`) along with longer-term ideas (full auth, remote access,
  due dates, prebuilt binaries) — treat those as unconfirmed.
