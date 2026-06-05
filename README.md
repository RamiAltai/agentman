# agentman (`am`)

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
  database server. Back up = copy one file.
- **Multi-project & multi-agent.** Group tasks into projects; atomic task claims so two
  agents never grab the same ticket.

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

## Using it from agents (Claude Code & others)

Any agent that can run shell commands can use `am`. For **Claude Code**, the one-time setup
(global memory file + permission allowlist) is in **[docs/agent-integration.md](docs/agent-integration.md)**.
The short version — drop this into your `~/.claude/CLAUDE.md` (or a project `CLAUDE.md`):

```md
## Task board (am) — run `am init <tasktype>` once, then:
am ls --status todo   # work to pick up        am ls --mine    # my tasks
am claim <id>         # take it (exit 4 = already claimed)
am show <id> -c       # detail + comments       am note <id> "msg"
am status <id> done   # todo|doing|blocked|done  am new "title" -p <proj>
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
| `am ls [--mine] [--status S] [-p P] [--all]` | list tasks (current project, hides done) |
| `am show <id> [-c]` | task detail; comments with `-c` |
| `am new "title" [--body B] [-p P] [--priority N]` | create a task; prints the new id |
| `am claim <id>` | atomic: assign me + → doing |
| `am status <id> <todo\|doing\|blocked\|done>` | change status |
| `am assign <id> <agent\|me\|->` | reassign (`-` = unassign) |
| `am note <id> "text"` | add a comment (alias: `comment`) |
| `am edit <id> [--title T] [--body B] [--priority N]` | edit fields |
| `am drop <id>` | release: unassign + → todo |
| `am projects` · `am project new <slug> [name]` | list / create projects |
| `am init <tasktype>` · `am whoami` | identity |
| `am serve [--port 8787] [--db PATH]` | run the dashboard + API |

`<id>` accepts a global id (`13`) or a project ref (`web-3`). `--status` accepts a comma
list. Priority is `0` urgent … `3` low (default `2`). Add `--json` to any read to parse.
Exit codes: `0` ok · `3` not found · `4` already claimed · `5` invalid · `6` server down.

## HTTP API

The CLI is a thin client over this (also what the dashboard uses). `X-Agent` header sets the
actor.

```
GET   /api/projects                          GET   /api/tasks/{id}
POST  /api/projects {slug,name}              PATCH /api/tasks/{id} {status?,assignee?,title?,body?,priority?}
GET   /api/tasks?project=&status=&assignee=  POST  /api/tasks/{id}/claim
POST  /api/tasks {project,title,...}         POST  /api/tasks/{id}/comments {body}
GET   /api/events?since=  (poll)             GET   /api/stream  (SSE)
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

## How it works

- **Single writer.** `am serve` is the only process that touches the DB
  (`SetMaxOpenConns(1)`, WAL mode). Claims are atomic via one conditional
  `UPDATE … WHERE assignee IS NULL AND status!='done' RETURNING …`; the loser of a race
  gets `409 already_claimed`.
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
go build -o am ./cmd/am      # build
go vet ./...                 # lint
am serve --db /tmp/dev.db    # run against a throwaway db
```

Layout: `cmd/am/` holds the single `main` package — `server.go` (API + SSE), `hub.go`
(broadcast), `store.go` + `schema.sql` (SQLite), `client.go` + `cli.go` (CLI),
`identity.go`, and `web/` (dashboard).

Contributions welcome — open an issue or PR.

## License

[MIT](LICENSE)
