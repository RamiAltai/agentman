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
  agentic_brain integration, where agents will later be confined to a category. Evidence:
  `cmd/am/store.go` (`Category`, migration v4), `cmd/am/cli.go`, `cmd/am/server.go`.
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
- **Findability** so a grown board stays navigable: substring search over task titles and bodies
  (`am ls --grep <text>` / `GET /api/tasks?q=`; a header search box on the dashboard) and
  lightweight free-form labels (`am label <id> +bug -wip`, `am ls --label <l>`; clickable label
  chips + filter on the dashboard). Evidence: `cmd/am/store.go` (`likeEscape`, `normalizeLabel`),
  `cmd/am/cli.go`, `cmd/am/web/app.js`.
- **Live activity feed** backed by an append-only `events` table (also the SSE replay cursor).
- **Per-directory agent identity** that survives the fresh-shell-per-command model agents run in
  (`cmd/am/identity.go`).
- **Embedded dashboard** (no build step, no npm): kanban board, drag-and-drop status changes,
  collapsible/resizable activity panel, keyboard shortcuts, responsive. Evidence: `cmd/am/web/`.
- **Multi-select project filter** on the dashboard: pick any number of project tabs to scope the
  board/feed at once ("All" clears the selection). Evidence: `cmd/am/web/app.js` `toggleProject`.
- **DB export/import**: `am db export` writes a consistent snapshot (`VACUUM INTO`), and
  `am db import` restores a validated candidate (integrity/FK checks, refuses while a server is
  running, backs up the existing DB first). Evidence: `cmd/am/db.go`.
- **Project archive/hide**: reversible soft-archive (`archived_at`) that hides a project from
  default views across all surfaces — tab bar (`ListProjects`), board (`ListTasks`), and activity
  feed (`ListEvents`/`RecentEvents`). Writing into an archived project is blocked: `CreateTask`
  returns `ErrProjectArchived` → HTTP 400 `{"error":"project_archived"}`. Archive/unarchive is
  accessible from the CLI (`am project archive`/`unarchive`) and from a "Manage projects" modal in
  the dashboard tab bar (`openManageProjects`). Evidence: `cmd/am/store.go`, `cmd/am/cli.go`,
  `cmd/am/server.go`, `cmd/am/web/app.js`.
- **Hard delete (tasks, comments, projects)**: permanent removal via `DELETE /api/tasks/{id}`,
  `DELETE /api/tasks/{id}/comments/{cid}`, and `DELETE /api/projects/{slug}` (cascade via FK:
  project → tasks → comments). CLI: `am rm <id>` (silent, exit 3 if not found);
  `am project rm <slug> --yes` (requires `--yes`; cascade). The dashboard exposes inline two-step
  delete confirms in the task modal, per-comment, and the Manage-projects modal. Events are never
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
- **Event** — an append-only record of every mutation; powers the activity feed, SSE stream, and
  reconnect replay (`events.id` is the cursor / SSE `Last-Event-ID`).
- **Agent identity** — `{tasktype}_{DDMMYY}_{4 digits}`, attached as the actor on writes.

## Non-Goals

Inferred (Confidence: Medium–High) from `README.md` "Security" and the localhost bind:
- **Not** a multi-tenant / authenticated / internet-facing service. No auth, binds `127.0.0.1`.
- **Not** a heavyweight project manager (no sprints, due dates, or attachments today; the only
  metadata beyond status/priority/assignee is lightweight free-form labels, added in Phase M).
- **Not** a hosted SaaS — it's a single local binary; "back up = copy one file."

## Evidence

- `README.md`, `docs/agent-integration.md`
- `cmd/am/main.go` (subcommand dispatch), `cmd/am/cli.go` (CLI surface)
- `cmd/am/schema.sql` (domain model), `cmd/am/server.go` (API), `cmd/am/web/` (dashboard)

## Unknowns

- **Intended scale.** No stated target for concurrent agents / task volume. The single-writer
  SQLite design (`SetMaxOpenConns(1)`) implies modest scale, but this is not documented.
- **Roadmap.** Near-term gap-closing work is now tracked in `ROADMAP.md` (repo root). Labels and
  search shipped in Phase M; the agentic_brain foundation (categories, stable IDs, vault binding)
  shipped in Phase O, with task metadata (P), scoping enforcement (Q), the category dashboard (R),
  and scope tokens (S) to follow; longer-term ideas (auth, remote access, due dates, prebuilt
  binaries) remain discussion-only — treat those as unconfirmed.
