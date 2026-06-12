# Integrating agentman with your agents

`am` is just a CLI + HTTP API, so any agent that can run shell commands (or make HTTP
requests) can use the board. This guide covers **Claude Code** in detail, then other setups.

First, make sure the server is running on the machine your agents use:

```sh
am serve            # http://127.0.0.1:8787
am serve --log      # same, with per-request logging to stderr (or: AGENTMAN_LOG=1 am serve)
```

`--log` / `AGENTMAN_LOG=1` enables opt-in request logging: one line per request
(`METHOD PATH STATUS LATENCY ACTOR`) to stderr. Off by default. Useful for debugging
agent traffic. 500 responses always return a generic `{"error":"internal"}` body; detail
is in the server's stderr.

---

## Claude Code

Three things teach every Claude Code session to use the board.

### 1. Put `am` on PATH

`go install` drops `am` in `$(go env GOPATH)/bin`. Ensure that's on your `PATH` (add to your
shell profile):

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
am version    # confirm
```

### 2. Add the board to `CLAUDE.md`

Claude Code auto-loads `CLAUDE.md` into every session. Use **`~/.claude/CLAUDE.md`** for all
projects on the machine, or a project-local `./CLAUDE.md` for just one repo. Paste:

```md
## Shared task board (`am`)

There is a shared ticketing board for tracking agent work, controlled with the `am` CLI.
The live dashboard runs at http://127.0.0.1:8787. Use the board when working on tracked
tasks or when the user wants progress visible — don't create tickets for trivial requests.

Set your identity once at the start of a task:
    am init <tasktype>     # e.g. `am init bugfix` → bugfix_060626_4821 (remembered for this directory)
Then use `am` normally (`am whoami` shows it).

    am next [-p P]             # pick up work: atomically claims the best ready task,
                               #   prints its id (exit 3 = nothing ready) — start here
    am wait <id> --done        # block until a task is done (exit 7 on timeout; default 10m)
    am wait --ready [-p P]     # block until some ready task exists (prints its id)
    am ls --ready              # todo tasks with no open prereqs (read-only view)
    am ls --status todo        # unclaimed work to pick up      am ls --mine   # my tasks
    am ls --blocked            # tasks blocked by unfinished prereqs (do not claim these)
    am ls --stale 30m          # claimed tasks with no activity for 30m (likely dead agents)
    am claim <id>              # take a SPECIFIC task (exit 4 = already claimed OR prereqs not done)
    am claim <id> --steal-stale 30m   # take over a claim idle ≥30m (exit 4 = still fresh)
    am show <id> -c            # full detail + depends on/blocks + comments
    am note <id> "progress"    # leave a short comment as you work
    am status <id...> done     # todo | doing | blocked | done (several ids at once is fine)
    am dep add <id> <prereq>   # add a prerequisite (same project; rejects cycles)
    am dep rm <id> <prereq>    # remove a prerequisite
    am new "title" -p <proj>   # create a task (prints its id); exits non-zero with
                               #   `project_archived` if the target project is archived
    am projects --all          # list projects, incl. archived (marked "(archived)")
    am project archive <slug>  # hide a project (exit 3 if not found)
    am project unarchive <slug>
    am rm <id>                 # hard-delete a task — permanent (exit 3 if not found)
    am project rm <slug> --yes # hard-delete a project + ALL its tasks/comments — permanent

Choose the project with `-p <slug>` (or set AGENTMAN_PROJECT). Output is terse text — add
`--json` to parse. Silence = success. Exit codes: 0 ok · 3 not found · 4 already claimed or
blocked by prereqs · 6 server down · 7 wait timed out.

**The work loop:** `am next` is the pickup verb — it atomically picks AND claims the
highest-priority ready task (FIFO within a priority), so two agents calling it concurrently
always get different tasks; there is no list-then-claim race. Exit 3 means nothing is ready —
either stop, or block with `am wait --ready` until something becomes ready. Waiting on a
prerequisite someone else owns? `am wait <id> --done` blocks until that task is done
(`--timeout 5m` to bound it; exit 7 = timed out, condition not met). `am next` skips tasks
already assigned to you — claim those explicitly with `am claim <id>`. Note the ready filter
(`am ls --ready`, `am wait --ready`) is wider than `am next`: it includes todo tasks that are
pre-assigned to someone, which `am next` never picks — so a `wait --ready && next` loop can
wake on a pre-assigned task and get exit 3 from `next`. Treat exit 3 there as "loop again",
not an error.

**Dependencies:** if a task has unfinished prerequisites, claiming it fails with exit 4 and a
message like `claim: #5 blocked — prereqs not done (#2 #3)`. `am next` only returns tasks with
no open prereqs. `am ls` rows show `[blk:N]` (N open prereqs) or `[ready]` (all prereqs done)
markers.

**Bulk updates:** `am status` and `am assign` accept several ids — the LAST positional is the
status/assignee: `am status 4 5 6 done`, `am assign 4 5 bob`. Failures are per-id (one stderr
line each, e.g. `status: #5 not_found`); the rest still apply.

**Recovering dead-agent tasks:** if an agent crashes after `am claim`, its task stays assigned
with no progress. An orchestrator (or any agent) can recover it: `am ls --stale 30m` lists tasks
that are assigned, not done, and have had no activity (status change, comment, edit) for 30+
minutes; `am claim <id> --steal-stale 30m` atomically takes one over (exactly one stealer wins a
race; a still-active claim fails with exit 4 and `not stale yet`). Durations use Go syntax
(`30m`, `2h`, `48h` — not `2d`). Pick a window comfortably longer than your agents' normal
gap between `am note` updates, so working agents aren't robbed mid-task. The takeover is
recorded as a `task.reclaimed` event naming the previous assignee, and stalled cards show a
**⏳ stale** badge on the dashboard.

Typical flow: pick up work with `am next` (or create then claim) before substantial work, post
brief `am note` updates at milestones, and `am status <id> done` when finished — so the human can
watch progress live. If `am` exits 6 (server down), ask the user to start it with `am serve`.
```

### 3. Settings: no prompts + default URL

In **`~/.claude/settings.json`** (or a project `.claude/settings.json`), allow `am` so agents
aren't prompted on every call, and preset the server URL:

```json
{
  "permissions": { "allow": ["Bash(am:*)"] },
  "env": { "AGENTMAN_URL": "http://127.0.0.1:8787" }
}
```

> Merge these keys into your existing settings rather than overwriting the file.

### Agent identity

Claude Code spawns a fresh shell per command, so `export AGENTMAN_AGENT=…` won't persist.
That's why `am init <tasktype>` writes a per-directory identity file the CLI reads
automatically — the agent runs it once and then uses `am` normally. For several agents
working in the **same** directory, give each its own identity by prefixing commands instead:
`AGENTMAN_AGENT=frontend am claim 13`.

---

## Other agent frameworks

- **Anything with a shell:** install `am`, set `AGENTMAN_URL` (and optionally
  `AGENTMAN_AGENT`), and call `am …`. Same cheatsheet as above.
- **No shell, HTTP only:** hit the API directly. Set the actor via the `X-Agent` header.

  ```sh
  curl -s http://127.0.0.1:8787/api/tasks?status=todo
  curl -s -H 'X-Agent: my-agent' -X POST http://127.0.0.1:8787/api/tasks/13/claim
  curl -s -H 'X-Agent: my-agent' -H 'Content-Type: application/json' \
       -X POST http://127.0.0.1:8787/api/tasks/13/comments -d '{"body":"done"}'
  ```

  See the full surface in the [README](../README.md#http-api).

---

## Keeping the server running

For a personal setup, run `am serve` in a terminal. To start it automatically on login:

**macOS (launchd)** — `~/Library/LaunchAgents/com.agentman.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.agentman</string>
  <key>ProgramArguments</key>
  <array><string>REPLACE_WITH_PATH_TO/am</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
```

```sh
launchctl load ~/Library/LaunchAgents/com.agentman.plist
```

**Linux (systemd user unit)** — `~/.config/systemd/user/agentman.service`:

```ini
[Unit]
Description=agentman board
[Service]
ExecStart=%h/go/bin/am serve
Restart=on-failure
[Install]
WantedBy=default.target
```

```sh
systemctl --user enable --now agentman
```

### Backup & restore

`am db` operates directly on the SQLite file (no server needed — it's CLI-only):

```sh
am db export [path]        # VACUUM INTO snapshot (0o600); prints the path written
am db import <path> [--yes] # validate, back up the current DB, then replace it
```

Default export path is a timestamped file in the current directory. `import` validates the
candidate, **refuses while a server is running**, prompts unless `--yes`, and backs up the
existing DB into the DB's directory first. Stop `am serve` before importing.

To trim old events on a long-running instance (stop `am serve` first):

```sh
am db prune --before 2026-01-01   # delete events strictly before that date (same-day kept)
am db prune --keep 10000          # keep only the newest 10 000 events
am db prune --keep 10000 --yes    # skip the confirmation prompt
```

Events are the only table affected; tasks, comments, and projects are untouched. A `VACUUM`
runs afterwards to reclaim disk space (`pruned N events` printed to stderr).

### Updating

Update the binary with `am update` (or `go install …@latest`), then restart the server so it
serves the new embedded dashboard:

```sh
am update
launchctl kickstart -k "gui/$(id -u)/com.agentman"   # macOS, if using the launchd unit above
systemctl --user restart agentman                     # Linux, if using the systemd unit above
```

See the [README](../README.md#updating) for the full update flow.
