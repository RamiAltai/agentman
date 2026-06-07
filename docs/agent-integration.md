# Integrating agentman with your agents

`am` is just a CLI + HTTP API, so any agent that can run shell commands (or make HTTP
requests) can use the board. This guide covers **Claude Code** in detail, then other setups.

First, make sure the server is running on the machine your agents use:

```sh
am serve     # http://127.0.0.1:8787
```

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

    am ls --status todo        # unclaimed work to pick up      am ls --mine   # my tasks
    am claim <id>              # take a task (exit 4 = already claimed by another agent)
    am show <id> -c            # full detail + comments
    am note <id> "progress"    # leave a short comment as you work
    am status <id> done        # todo | doing | blocked | done
    am new "title" -p <proj>   # create a task (prints its id); exits non-zero with
                               #   `project_archived` if the target project is archived
    am projects --all          # list projects, incl. archived (marked "(archived)")
    am project archive <slug>  # hide a project (exit 3 if not found)
    am project unarchive <slug>

Choose the project with `-p <slug>` (or set AGENTMAN_PROJECT). Output is terse text — add
`--json` to parse. Silence = success. Exit codes: 0 ok · 3 not found · 4 already claimed ·
6 server down.

Typical flow: claim (or create then claim) a task before substantial work, post brief
`am note` updates at milestones, and `am status <id> done` when finished — so the human can
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

### Updating

Update the binary with `am update` (or `go install …@latest`), then restart the server so it
serves the new embedded dashboard:

```sh
am update
launchctl kickstart -k "gui/$(id -u)/com.agentman"   # macOS, if using the launchd unit above
systemctl --user restart agentman                     # Linux, if using the systemd unit above
```

See the [README](../README.md#updating) for the full update flow.
