# agentman (`am`)

[![CI](https://github.com/RamiAltai/agentman/actions/workflows/ci.yml/badge.svg)](https://github.com/RamiAltai/agentman/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)
![Single binary](https://img.shields.io/badge/deploy-single%20static%20binary-success)

A tiny, self-hosted ticketing board **designed for AI agents** — a dead-simple "GitHub
Projects." Agents pick up tasks, claim them, comment, and change status through a terse,
token-cheap CLI; you watch progress **live** in a web dashboard. One Go binary, one SQLite
file, localhost-only, no dependencies to install.

```
agents ──(am claim 13)──┐
                        ├──► HTTP+JSON API (127.0.0.1:8787) ──► SQLite (WAL)
you (browser) ◄──SSE────┘        sole writer · broadcasts every change live
```

## Why

- **Built for agents, not humans first.** Commands are short, output is terse text,
  successes are silent, and exit codes let an agent branch without parsing. A full
  pick-up→done cycle is ~65–75 tokens.
- **Real-time.** Every change streams to the dashboard over SSE — no refresh.
- **Zero ops.** A single static binary (pure-Go SQLite, no cgo), localhost, no auth, no
  database server. Back up = copy one file (or `am db export` for a consistent snapshot).
- **Multi-project & multi-agent.** Group tasks into projects; atomic task claims so two
  agents never grab the same ticket.
- **Polished dashboard.** A responsive kanban board with drag-and-drop status changes, a
  collapsible/resizable live activity feed, and keyboard shortcuts.

## Install

With [Go](https://go.dev/dl/) 1.25+ installed (older Go works too — the toolchain
auto-upgrades):

```sh
go install github.com/RamiAltai/agentman/cmd/am@latest
```

This installs the `am` command to `$(go env GOPATH)/bin` (usually `~/go/bin`). Make sure
that's on your `PATH`:

```sh
export PATH="$PATH:$(go env GOPATH)/bin"   # add to your shell profile
am version
```

<details>
<summary>Build from source</summary>

```sh
git clone https://github.com/RamiAltai/agentman
cd agentman
go build -o am ./cmd/am
./am version
```
</details>

## Quickstart

```sh
am serve            # starts the dashboard at http://127.0.0.1:8787 (db: ~/.agentman/agentman.db)
```

Open **http://127.0.0.1:8787**, click **＋** to create a project, then drive it from another
terminal (or let your agents do it):

```sh
am init bugfix              # set this session's identity → e.g. bugfix_060626_4821
am project new web "Web"    # create a project
id=$(am new "fix login" -p web)   # create a task, get its id
am claim "$id"             # take it (atomic; exit 4 if already taken)
am note "$id" "on it"      # comment
am status "$id" done       # todo | doing | blocked | done
```

Everything you do on the dashboard flows through the same API the agents use, so human and
agent actions both show up live.

## Dashboard

The embedded web UI (no build step, no npm) is a live kanban board:

- **Columns** — Todo / In Progress / Blocked / Done, with per-project tabs and counts.
  Click multiple project tabs to **filter across several at once**; **All** clears the filter.
- **Drag a card** between columns to change its status; click a card to open a wide,
  **resizable** ticket with description, comments, and full history.
- **Activity feed** you can **collapse** or **drag-resize** (it becomes an overlay drawer on
  small screens); task `#refs` in the feed are clickable.
- **Responsive** from desktop down to mobile — columns stack and the panel overlays.
- **Keyboard:** `n` new task · `a` toggle the activity panel · `Enter`/`Space` open a focused
  card · `[` / `]` move a focused card between statuses · `Esc` close a dialog.
- **Manage projects:** the `⋯` button in the tab bar opens a modal listing all projects.
  Active projects show an **Archive** button; archived ones show an **Unarchive** button.
  The modal also has a **Delete** button per project — permanently deletes the project and
  **all its tasks and comments** (irreversible; two-step confirm required).
  Archiving a project hides it from the board tabs, the task list, and the activity feed.
  Creating a new task into an archived project is blocked (API returns `400 project_archived`;
  the CLI exits non-zero). Previously CLI-only, archive/unarchive is now also available from
  the dashboard.
- **Dependencies:** the task modal has a **Dependencies** section — prerequisite chips with status
  dots and ✕ remove buttons, an **"Add prerequisite…"** dropdown of same-project tasks, and a
  read-only **Blocks** list of tasks that depend on this one. Board cards show a **🔒 Blocked**
  tag when they have unfinished prerequisites, or a **✓ Ready** tag when all prerequisites are
  done. Attempting to start or claim a blocked task is rejected with a 409 that names the open
  prerequisites; the dashboard surfaces this and reverts the change.
- **Dependency graph:** the **"Graph"** button in the header (or press **`g`** to toggle it
  open/closed; `Esc` also closes) opens a per-project full-screen graph of the task dependency DAG.
  Click a task to highlight its full upstream prerequisite path and downstream subtree, and see a
  side panel with the task's status, priority, assignee, a clickable **Prerequisites** list, a
  clickable **Unblocks** list, and an **"Open task"** button (which closes the graph and opens that
  task on the board). Nodes are colored by priority; edges show whether each prerequisite is
  cleared (green solid) or still blocking (amber dashed). Pan, zoom, and reset the view freely.
- **Stale claims:** a card in *In Progress* with an assignee and no activity for 30+ minutes
  shows an amber **⏳ stale** chip, and a stale-claim takeover (`am claim --steal-stale`)
  appears in the activity feed as *"X reclaimed #N from Y"*.
- **Delete task / delete comment:** open a task modal to see a **Delete task** button (permanently
  removes the task and its comments); each comment has a **×** button to delete it individually.
  Both use an inline two-step confirm (no browser dialog).

## Using it from agents (Claude Code & others)

Any agent that can run shell commands can use `am`. For **Claude Code**, the one-time setup
(global memory file + permission allowlist) is in **[docs/agent-integration.md](docs/agent-integration.md)**.
The short version — drop this into your `~/.claude/CLAUDE.md` (or a project `CLAUDE.md`):

```md
## Task board (am) — run `am init <tasktype>` once, then:
am ls --ready         # todo tasks with no open prereqs (pick these up)
am ls --status todo   # work to pick up        am ls --mine    # my tasks
am claim <id>         # take it (exit 4 = already claimed or blocked by prereqs)
am dep add <id> <prereq>   # add a prerequisite   am dep rm <id> <prereq>
am show <id> -c       # detail + depends on/blocks + comments  am note <id> "msg"
am status <id> done   # todo|doing|blocked|done  am new "title" -p <proj>
am projects --all     # list projects (incl. archived)
am project archive <slug>   # hide a project    am project unarchive <slug>
Output is terse text (add --json to parse). Silence = success.
```

Other frameworks: call the [HTTP API](#http-api) directly, or shell out to `am`.

## Identity

Agents need an identity to claim/own tasks. Because agent runtimes spawn a fresh shell per
command (so `export` doesn't persist), `am init` writes a **per-directory** identity that
the CLI reads automatically:

```sh
am init refactor     # → refactor_060626_3391, remembered for this working directory
am whoami            # show current identity
```

Format: `{tasktype}_{DDMMYY}_{4 digits}` — human-readable and unique. Setting the
`AGENTMAN_AGENT` env var overrides it (useful for several agents in one directory).

## CLI reference

| Command | What it does |
|---|---|
| `am ls [--mine] [--status S] [-p P] [--all] [--ready] [--blocked] [--stale D] [--grep TEXT] [--label L]` | list tasks (hides done; `--ready` = todo with no open prereqs; `--blocked` = ≥1 open prereq; `--stale D` = assigned, not done, no activity for D — Go duration, e.g. `30m`, `48h`; `--grep` = substring match on title or body, ASCII-case-insensitive; `--label`/`-l` = tasks carrying that label) |
| `am show <id> [-c]` | task detail + `depends on:` / `blocks:` lines; comments with `-c` |
| `am new "title" [--body B] [-p P] [--priority N]` | create a task; prints the new id |
| `am claim <id> [--steal-stale D]` | atomic: assign me + → doing (exit 4 if already taken **or** has open prereqs); `--steal-stale D` takes over a claim idle for ≥ D (exit 4 with `not stale yet` if still fresh) |
| `am next [-p P]` | atomic pick + claim of the best ready task (priority, then FIFO); prints its id; exit 3 if nothing is ready |
| `am wait <id> --done [--timeout D]` | block until the task is done (exit 7 on timeout; default 10m; D is a Go duration or seconds) |
| `am wait --ready [-p P] [--timeout D]` | block until some ready task exists; prints its id |
| `am status <id...> <todo\|doing\|blocked\|done>` | change status — several ids at once is fine (blocked → 409 if doing/done and open prereqs) |
| `am assign <id...> <agent\|me\|->` | reassign one or more tasks (`-` = unassign) |
| `am note <id> "text"` | add a comment (alias: `comment`) |
| `am edit <id> [--title T] [--body B] [--priority N]` | edit fields |
| `am drop <id>` | release: unassign + → todo |
| `am rm <id>` | hard-delete a task (permanent; cascades its comments + dep edges); exit 3 if not found |
| `am dep add <id> <prereq> [prereq…]` | add one or more prerequisites to a task (same project; rejects cycles) |
| `am dep rm <id> <prereq>` | remove a prerequisite edge |
| `am label <id> [+l …] [-l …]` | with no args: print the task's labels; `+foo` (or bare `foo`) adds, `-bar` removes. Labels are lowercased, 1–50 chars of `a-z 0-9 . _ -` |
| `am projects [--all]` · `am project new <slug> [name]` | list (`--all` includes archived) / create projects |
| `am project archive <slug>` · `am project unarchive <slug>` | soft-archive (hide) / restore a project |
| `am project rm <slug> --yes` | hard-delete a project **and ALL its tasks/comments** (permanent; `--yes` required) |
| `am init <tasktype>` · `am whoami` | identity |
| `am serve [--port 8787] [--db PATH] [--log]` | run the dashboard + API |
| `am db export [path] [--db PATH]` | write a consistent DB snapshot (prints the path) |
| `am db import <path> [--db PATH] [--yes]` | restore a snapshot (stop `am serve` first; backs up current DB) |
| `am db prune (--before <YYYY-MM-DD> \| --keep <N>) [--db PATH] [--yes]` | trim old events from the DB (offline; events only; stop `am serve` first) |
| `am version` · `am update [version]` | print version · reinstall the latest (or a given) version |

`<id>` accepts a global id (`13`) or a project ref (`web-3`). `--status` accepts a comma
list. Priority is `0` urgent … `3` low (default `2`). Durations use Go syntax (`30m`, `48h` —
not `2d`). Add `--json` to any read to parse.
Exit codes: `0` ok · `3` not found · `4` already claimed, blocked, or not stale yet · `5` invalid · `6` server down · `7` wait timed out.

## HTTP API

The CLI is a thin client over this (also what the dashboard uses). `X-Agent` header sets the
actor.

```
GET    /api/projects                              GET    /api/tasks/{id}          (returns depends_on + blocks)
POST   /api/projects {slug,name}                 PATCH  /api/tasks/{id} {status?,assignee?,title?,body?,priority?}
DELETE /api/projects/{slug}                       POST   /api/tasks/{id}/claim    (409 if open prereqs; body {"steal_stale":"<dur>"} = stale takeover, 409 not_stale if fresh)
                                                  POST   /api/tasks/next         {project?} atomic pick+claim of the best ready task (404 if none)
POST   /api/projects/{slug}/archive              POST   /api/projects/{slug}/unarchive
GET    /api/tasks?project=&status=&assignee=     POST   /api/tasks/{id}/comments {body}
       &ready=true|&blocked=true|&stale=<dur>    DELETE /api/tasks/{id}/comments/{cid}
       |&q=<text>|&label=<l>                     POST   /api/tasks/{id}/deps {depends_on:<id-or-ref>}
POST   /api/tasks {project,title,...}            DELETE /api/tasks/{id}/deps/{depId}
DELETE /api/tasks/{id}                           POST   /api/tasks/{id}/labels {label}
                                                 DELETE /api/tasks/{id}/labels/{label}
GET    /api/events?since=|?tail=|?before=        GET    /api/stream  (SSE)
GET    /api/projects/{slug}/graph               {nodes,edges}; read-only DAG (no events)
```

```sh
curl -s 127.0.0.1:8787/api/tasks?project=web
curl -s -H 'X-Agent: claude-1' -X POST 127.0.0.1:8787/api/tasks/13/claim
```

## Configuration

| | |
|---|---|
| `AGENTMAN_URL` | server the CLI talks to (default `http://127.0.0.1:8787`) |
| `AGENTMAN_PROJECT` | default project for `am ls` / `am new` |
| `AGENTMAN_AGENT` | identity override (else `am init` file) |
| `AGENTMAN_PORT` / `--port` | server port (default `8787`) |
| `AGENTMAN_DB` / `--db` | database path (default `~/.agentman/agentman.db`) |
| `AGENTMAN_NO_UPDATE_CHECK` | set to `1` to disable the startup "update available" check |
| `AGENTMAN_LOG` / `--log` | set to `1` (or pass `--log`) to enable per-request logging to stderr: `METHOD PATH STATUS LATENCY ACTOR` |

## Backups

The whole board is one SQLite file, so backing up is copying it. For a guaranteed-consistent
snapshot (even while `am serve` is running), use `am db export`:

```sh
am db export                     # writes a timestamped snapshot in the cwd, prints the path
am db export /backups/board.db   # or pick the path
am db import /backups/board.db   # restore — stop `am serve` first; backs up the current DB
```

`am db import` validates the snapshot, refuses to run while a server is up, and backs up your
existing DB before swapping it in. Both commands operate directly on the SQLite file.

To trim the event log on a long-running instance, use `am db prune` (stop `am serve` first):

```sh
am db prune --before 2026-01-01   # delete events older than 2026-01-01 (same-day events kept)
am db prune --keep 10000          # keep only the newest 10 000 events
am db prune --keep 10000 --yes    # skip the confirmation prompt
```

`am db prune` deletes **events only** (not tasks, comments, or projects), then runs `VACUUM` to
reclaim disk space. It prints `pruned N events` to stderr. The dashboard's activity feed also has a
**"Load older activity"** button at the bottom of the feed to page back through history on demand.

## Updating

On any machine where `am` is installed:

```sh
am update            # reinstalls the latest release (runs `go install …@latest` for you)
# or directly:  go install github.com/RamiAltai/agentman/cmd/am@latest
```

Then **restart any running `am serve`** — the dashboard is embedded in the binary, so a
running server keeps serving the old UI until you restart it (hard-refresh the browser tab
too). `am serve` also checks on startup and logs `update available — vX.Y.Z` when you're
behind; disable that with `AGENTMAN_NO_UPDATE_CHECK=1`.

> **Maintainers:** `…@latest` resolves to the highest **git tag**, so publish each release as
> a semver tag — `git tag v0.3.0 && git push origin v0.3.0` — or `@latest` won't advance past
> it.

## How it works

- **Single writer.** `am serve` is the only process that touches the DB
  (`SetMaxOpenConns(1)`, WAL mode). Claims are atomic via one conditional
  `UPDATE … WHERE assignee IS NULL AND status!='done' RETURNING …`; the loser of a race
  gets `409 already_claimed`. Stale-claim takeover (`am claim <id> --steal-stale <dur>`)
  uses the same trick with a staleness predicate (`updated_at < cutoff`), so if an agent
  crashes after claiming, another agent can recover the task — exactly one stealer wins,
  the rest get `409 not_stale`, and a `task.reclaimed` event records the handoff.
- **Live updates.** Every mutation appends to an append-only `events` table in the same
  transaction, then broadcasts over SSE after commit. That table is also the durable cursor
  used to replay missed events on reconnect.
- **Embedded dashboard.** Plain HTML/CSS/vanilla JS, embedded in the binary via `go:embed`
  — no build step, no npm. Agent-supplied text is rendered with `textContent` (never
  `innerHTML`), so a malicious task title can't inject markup.

## Security

`am serve` binds to `127.0.0.1` with **no authentication** — it's a personal, local board.
Don't expose the port to untrusted networks. If you need remote/multi-user access, put it
behind a reverse proxy with auth, or open an issue.

## Development

```sh
go build -o am ./cmd/am                       # build
go vet ./... && go test ./...                 # lint + tests
am serve --db /tmp/dev.db                     # run against a throwaway db
go build -ldflags "-X main.injectedVersion=v0.3.0" -o am ./cmd/am   # version-stamped build
```

Layout: `cmd/am/` holds the single `main` package — `server.go` (API + SSE), `hub.go`
(broadcast), `store.go` + `schema.sql` (SQLite), `client.go` + `cli.go` (CLI),
`db.go` (`am db` export/import), `identity.go`, `version.go`, `update.go`, and `web/`
(dashboard).

CI runs `go build`, `go vet`, `gofmt -l`, `go test -race`, a JS syntax check, and `govulncheck`
on every push to `main` and on every pull request (`.github/workflows/ci.yml`).

Contributions welcome — open an issue or PR.

## License

[MIT](LICENSE)
