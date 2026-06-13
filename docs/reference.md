# agentman reference

The complete reference for `am` — CLI, HTTP/SSE API, configuration, dashboard, and internals.
For the overview, install, and quickstart, see the [README](../README.md). For wiring agents
up (Claude Code permissions, scoped identities, scope tokens), see
[agent-integration.md](agent-integration.md).

The hierarchy is `instance → category → project → task → comment`. Every project belongs to a
category; every database starts with a default `general` category.

---

## Dashboard

The embedded web UI (no build step, no npm — plain HTML/CSS/vanilla JS via `go:embed`) is a
live kanban board served at `http://127.0.0.1:8787`. Layout is a collapsible **left rail**
(navigation) beside a lean top bar; a bold violet theme runs through both light and dark.

- **Left rail (navigation)** — the brand, an **Overview** item (returns to the category home),
  an **All tasks** item (cross-category board), then a **category → project tree** with per-item
  open-task counts, plus **＋ New project** and **Manage** as rail actions at the foot. Clicking a
  project selects **that one project** (single-select); a category row opens its board. The rail
  collapses to icons, and becomes an off-canvas drawer on small screens.
- **Category home (landing view)** — a grid of **category cards**, each showing the category's
  task counts (todo / doing / blocked / done) and the agents active in it in the last 30 minutes.
  Click a card to **drill into that category's board**. A dashed **＋ New category** add-card opens
  a modal (name + auto-derived slug) that creates the category. Views are linkable and the
  **browser back button works** — the URL hash is `#/` (home), `#/all` (cross-category board), or
  `#/cat/<slug>` (one category).
- **Columns** — Todo / In Progress / Blocked / Done, with counts. A category board shows only that
  category's projects; the **All tasks** view shows every project. Selecting a project from the rail
  filters the board to it.
- **Drag a card** between columns to change its status; click a card to open a wide, **resizable**
  ticket with description, comments, and full history.
- **Activity feed** you can **collapse** or **drag-resize** (an overlay drawer on small screens);
  task `#refs` in the feed are clickable.
- **Responsive** from desktop down to mobile — columns stack and the panel overlays.
- **Light & dark themes** — a top-bar theme toggle (after **Graph** in the utility cluster) switches
  between them. First load follows your OS appearance (`prefers-color-scheme`); once you pick a theme
  it persists across reloads (stored in the browser). No keyboard shortcut.
- **Keyboard:** `n` new task · `a` toggle the activity panel · `/` focus search · `g` toggle the
  dependency graph · `Enter`/`Space` open a focused card · `[` / `]` move a focused card between
  statuses · `Esc` close a dialog.
- **Manage (categories & projects):** the **Manage** rail action opens the **Manage** modal with
  two sections:
  - **Categories** — every category (including archived ones), each with its open-task count and an
    **Archive** / **Unarchive** toggle. (There is no category delete — there is no delete API for
    categories.)
  - **Projects** — every project, each with an **Edit** button, an **Archive** / **Unarchive**
    toggle, and a two-step-confirm **Delete** (removes the project and all its tasks/comments).
    Archiving hides a project from the rail, task list, and feed; creating a task into an archived
    project is blocked.
  - **Edit project** opens a sub-modal to **rename** the project (its name and slug — the rename is
    safe, the project's stable id is unchanged) and set its **vault binding** (vault project id /
    vault path).
- **Create a project with a category:** the **＋ New project** rail action's modal has a required
  **Category** picker (defaulting to the category you're currently viewing, else `general`), so a
  project lands in the right category without the CLI.
- **Dependencies:** the task modal has a **Dependencies** section — prerequisite chips with status
  dots and ✕ remove buttons, an **"Add prerequisite…"** dropdown of same-project tasks, and a
  read-only **Blocks** list. Cards show a **🔒 Blocked** tag with unfinished prerequisites or a
  **✓ Ready** tag when all are done. Starting/claiming a blocked task is rejected (409 naming the
  open prerequisites) and the dashboard reverts the change.
- **Dependency graph:** the **"Graph"** button (or `g`) opens a per-project full-screen DAG of the
  task dependencies. Click a task to highlight its full upstream prerequisite path and downstream
  subtree, with a side panel (status, priority, assignee, clickable Prerequisites/Unblocks, and an
  "Open task" button). Nodes are colored by priority; edges are green-solid (cleared) or
  amber-dashed (still blocking). Pan, zoom, reset freely.
- **Search & labels:** a header **search box** (`/`) filters the board by a substring of any task's
  title *or* description (server-side via `?q=`, so it survives live SSE reloads). Cards show up to
  3 **label chips** (then a `+N` overflow) — click a chip to filter by that label (a header chip
  with ✕ clears it). The task modal has a **Labels** section.
- **Board filters:** a header **Filter** button opens a popover with **Ready** / **Blocked** /
  **Stale** toggles, an **Assignee** field (with a **Mine** shortcut = your `human` actor), and a
  **Meta key** field, plus **Clear all**. The button shows a count chip while any filter is active.
  Filters compose with the search box, label filter, and project/category scope and survive live
  reloads. (Status is not a filter — the four columns already are the status axis.)
- **Task metadata:** the modal's **Meta** section is editable — each `key=value` pair has a ✕ to
  remove it, and an add-row (key + value, **Add** or Enter) creates one.
- **Release a task:** the task modal's **Release** button (shown when a task is claimed or not in
  Todo) unassigns it and returns it to Todo in one click — the `am drop` equivalent.
- **Stale claims:** an *In Progress* card with an assignee and no activity for 30+ minutes shows an
  amber **⏳ stale** chip; a takeover (`am claim --steal-stale`) shows as *"X reclaimed #N from Y"*.

---

## Identity & scope

Agents need an identity to claim/own tasks. Because agent runtimes spawn a fresh shell per command
(so `export` doesn't persist), `am init` writes a **per-directory** identity that the CLI reads
automatically:

```sh
am init refactor     # → refactor_130626_3391, remembered for this working directory
am whoami            # show current identity (+ scope / token state)
```

Format: `{tasktype}_{DDMMYY}_{4 digits}`. `AGENTMAN_AGENT` overrides it (useful for several agents
in one directory).

**Scoped identity.** `am init <tasktype> -c <category> [-p <project>]` confines an agent to a
category (or a single project): the scope is recorded in the identity file (JSON) and sent on every
request as `X-Agent-Scope`. The server rejects out-of-scope mutations and named reads with
`403 out_of_scope` → **exit 8**, and silently narrows unfiltered lists (`am ls`/`am next`) to the
scope. One carve-out: any agent may file tasks into the **proposals project** (default
`meta/proposals`, set with `am serve --proposals`). `AGENTMAN_SCOPE` overrides the file. A scope is
a **client-asserted label** — accident prevention for a config-following agent, not authentication.

**Scope tokens (server-enforced).** To make a scope a real boundary, a human mints a scope-bound
**bearer token** with `am token new --scope <cat[/proj]>` (an unscoped operation) — it prints the
token once and stores it in this directory's identity. The agent then sends it as
`Authorization: Bearer` automatically; its scope **wins over** any header, and an invalid/revoked
token fails with **exit 9**. `am whoami` shows `token: set` (never the value); `AGENTMAN_TOKEN`
overrides the file. The board stores only the token's sha256 hash.

---

## CLI reference

| Command | What it does |
|---|---|
| `am ls [--mine] [--status S] [-p P] [-c CAT] [--all] [--ready] [--blocked] [--stale D] [--grep TEXT] [--label L] [--meta KEY]` | list tasks (hides done; `-c` = category scope; `--ready` = todo with no open prereqs; `--blocked` = ≥1 open prereq; `--stale D` = assigned, not done, no activity for D — Go duration, e.g. `30m`, `48h`; `--grep` = substring match on title or body, ASCII-case-insensitive; `--label`/`-l` = tasks carrying that label; `--meta KEY` = tasks carrying that meta key) |
| `am show <id> [-c]` | task detail + `depends on:` / `blocks:` / `meta:` lines; comments with `-c` (for `show` only, `-c` means comments, not category) |
| `am new "title" [--body B] [-p P] [--priority N] [--meta k=v]...` | create a task; prints the new id (the project determines the category; `--meta` is repeatable) |
| `am claim <id> [--steal-stale D]` | atomic: assign me + → doing (exit 4 if already taken **or** has open prereqs); `--steal-stale D` takes over a claim idle for ≥ D (exit 4 with `not stale yet` if still fresh) |
| `am next [-p P] [-c CAT] [--meta KEY]` | atomic pick + claim of the best ready task (priority, then FIFO; `--meta` = only tasks carrying that key); prints its id; exit 3 if nothing is ready |
| `am wait <id> --done [--timeout D]` | block until the task is done (exit 7 on timeout; default 10m; D is a Go duration or seconds) |
| `am wait --ready [-p P] [-c CAT] [--meta KEY] [--timeout D]` | block until some ready task exists (in scope); prints its id |
| `am status <id...> <todo\|doing\|blocked\|done>` | change status — several ids at once is fine (blocked → 409 if doing/done with open prereqs) |
| `am assign <id...> <agent\|me\|->` | reassign one or more tasks (`-` = unassign) |
| `am note <id> "text"` | add a comment (alias: `comment`) |
| `am edit <id> [--title T] [--body B] [--priority N] [--meta k=v]...` | edit fields; `--meta` is repeatable and applies in one atomic edit — `--meta k=` (empty value) removes the key |
| `am drop <id>` | release: unassign + → todo |
| `am rm <id>` | hard-delete a task (permanent; cascades its comments + dep edges); exit 3 if not found |
| `am dep add <id> <prereq> [prereq…]` | add one or more prerequisites (same project; rejects cycles) |
| `am dep rm <id> <prereq>` | remove a prerequisite edge |
| `am label <id> [+l …] [-l …]` | with no args: print the task's labels; `+foo` (or bare `foo`) adds, `-bar` removes. Labels are lowercased, 1–50 chars of `a-z 0-9 . _ -` |
| `am projects [--all]` · `am project new <slug> [name] -c <category>` | list (`--all` includes archived) / create projects — category required (`-c` or `AGENTMAN_CATEGORY`) |
| `am project edit <slug> [--slug NEW] [--name N] [--vault-id X] [--vault-path Y]` | rename a project (its stable `uid` never changes) / set the vault binding (an empty value clears it) |
| `am project archive <slug>` · `am project unarchive <slug>` | soft-archive (hide) / restore a project |
| `am project rm <slug> --yes` | hard-delete a project **and ALL its tasks/comments** (permanent; `--yes` required) |
| `am categories [--all]` · `am category new <slug> [name]` | list (`--all` includes archived) / create categories |
| `am category archive <slug>` · `am category unarchive <slug>` | soft-archive a category (hides its projects/tasks; blocks new tasks/projects under it) / restore it |
| `am init <tasktype> [-c CAT [-p PROJ]]` · `am whoami` | identity (optionally **scoped** to a category or one project; out-of-scope ops exit 8); `whoami` adds a `scope:` line when scoped and a `token: set` line when a token is configured |
| `am token new --scope <cat[/proj]>` · `am token ls [--json]` · `am token revoke <id>` | **scope tokens** (the human mints, the agent uses): `new` mints a scope-bound bearer token, prints it once, and stores it in this directory's identity; `ls` lists tokens (id / scope / created / [revoked], never the value); `revoke` revokes one. Minting requires an **unscoped** caller. An invalid/revoked token → exit 9 |
| `am serve [--port 8787] [--db PATH] [--log] [--proposals CAT/PROJ]` | run the dashboard + API (`--proposals` = the scope carve-out project; default `meta/proposals`) |
| `am db export [path] [--db PATH]` | write a consistent DB snapshot (prints the path) |
| `am db import <path> [--db PATH] [--yes]` | restore a snapshot (stop `am serve` first; backs up current DB) |
| `am db prune (--before <YYYY-MM-DD> \| --keep <N>) [--db PATH] [--yes]` | trim old events from the DB (offline; events only; stop `am serve` first) |
| `am version` · `am update [version]` | print version · reinstall the latest (or a given) version |

`<id>` accepts a global id (`13`) or a project ref (`web-3`). `--status` accepts a comma list.
Priority is `0` urgent … `3` low (default `2`). Durations use Go syntax (`30m`, `48h` — not `2d`).
Add `--json` to any read to parse.

**Exit codes:** `0` ok · `3` not found · `4` already claimed, blocked, or not stale yet · `5` invalid ·
`6` server down · `7` wait timed out · `8` out of scope · `9` bad token (invalid or revoked).

---

## HTTP API

The CLI is a thin client over this (also what the dashboard uses). The `X-Agent` header sets the
actor; the optional `X-Agent-Scope` header (`category[/project]`) confines the caller — out-of-scope
mutations and named reads return `403 {"error":"out_of_scope"}` (CLI exit 8). A **scope token**
(`Authorization: Bearer <tok>`) carries a server-bound scope that **wins over** `X-Agent-Scope`; an
invalid/revoked token → `401 {"error":"unauthorized"}` (CLI exit 9).

```
GET    /api/categories?archived=true             GET    /api/tasks/{id}          (returns depends_on + blocks)
       (+ per-category counts & active_agents)
POST   /api/categories {slug,name?}              PATCH  /api/tasks/{id} {status?,assignee?,title?,body?,priority?,meta?}
POST   /api/categories/{slug}/archive            POST   /api/tasks/{id}/claim    (409 if open prereqs; body {"steal_stale":"<dur>"} = stale takeover, 409 not_stale if fresh)
POST   /api/categories/{slug}/unarchive          POST   /api/tasks/next         {project?,category?,meta_key?} atomic pick+claim of the best ready task (404 if none)
GET    /api/projects?category=<slug>             POST   /api/tasks/{id}/comments {body}
POST   /api/projects {slug,name,category?}       DELETE /api/tasks/{id}/comments/{cid}
PATCH  /api/projects/{slug} {slug?,name?,         POST   /api/tasks/{id}/deps {depends_on:<id-or-ref>}
       vault_project_id?,vault_path?}            DELETE /api/tasks/{id}/deps/{depId}
DELETE /api/projects/{slug}                      POST   /api/tasks/{id}/labels {label}
POST   /api/projects/{slug}/archive              DELETE /api/tasks/{id}/labels/{label}
POST   /api/projects/{slug}/unarchive
GET    /api/tasks?project=&category=&status=&assignee=
       &ready=true|&blocked=true|&stale=<dur>|&q=<text>|&label=<l>|&meta_key=<k>
POST   /api/tasks {project,title,meta?,...}      DELETE /api/tasks/{id}
GET    /api/events?since=|?tail=|?before=        GET    /api/stream  (SSE)
       [&project=][&category=]                          [?project=|?category= scope]
GET    /api/projects/{slug}/graph               {nodes,edges}; read-only DAG (no events)
POST   /api/tokens {scope:"cat[/proj]"}          GET    /api/tokens   (list; never the plaintext/hash)
       201 {id,scope,token,...} — plaintext once  POST   /api/tokens/{id}/revoke
       (all three require an UNSCOPED caller — mint-requires-unscoped)
```

Category and project payloads carry a **stable id** (`uid`: `amc_…` / `amp_…`) that never changes
across slug renames — bind external systems to it, not the slug. Projects also carry optional
`vault_project_id` / `vault_path` binding fields. Creating into an archived category fails with
`400 {"error":"category_archived"}`.

`GET /api/categories` returns each category augmented with `counts` (todo/doing/blocked/done over its
non-archived projects) and `active_agents` (non-human actors active in the last 30 minutes) — what
the dashboard's category-home view renders. `GET /api/events` and `GET /api/stream` accept a
`?category=<slug>` lens that scopes the feed/stream to that category's projects' events. This is an
unscoped query-param choice — distinct from the agent `X-Agent-Scope` identity scope.

Tasks carry optional free-form **metadata** (`"meta": {"k":"v", …}`): keys are normalized like
labels (lowercase, 1–50 chars of `a-z 0-9 . _ -`), values are opaque strings up to 500 bytes. Set
pairs on create or PATCH (an empty-string value removes the key); filter by key **presence** with
`?meta_key=` / the `meta_key` next-body field.

```sh
curl -s 127.0.0.1:8787/api/tasks?project=web
curl -s -H 'X-Agent: claude-1' -X POST 127.0.0.1:8787/api/tasks/13/claim
```

---

## Configuration

| | |
|---|---|
| `AGENTMAN_URL` | server the CLI talks to (default `http://127.0.0.1:8787`) |
| `AGENTMAN_PROJECT` | default project for `am ls` / `am new` |
| `AGENTMAN_CATEGORY` | default category scope for `am ls` / `am next` / `am wait --ready` / `am project new` |
| `AGENTMAN_SCOPE` | override the identity file's confinement scope sent as `X-Agent-Scope` (e.g. `work` or `work/api`) |
| `AGENTMAN_TOKEN` | override the identity file's bearer token (sent as `Authorization: Bearer`; a token's scope wins over `X-Agent-Scope`) |
| `AGENTMAN_AGENT` | identity override (else the `am init` file) |
| `AGENTMAN_PROPOSALS` / `--proposals` | (serve) the scope carve-out project any agent may file into (default `meta/proposals`) |
| `AGENTMAN_PORT` / `--port` | server port (default `8787`) |
| `AGENTMAN_DB` / `--db` | database path (default `~/.agentman/agentman.db`) |
| `AGENTMAN_NO_UPDATE_CHECK` | set to `1` to disable the startup "update available" check |
| `AGENTMAN_LOG` / `--log` | set to `1` (or pass `--log`) for per-request logging to stderr: `METHOD PATH STATUS LATENCY ACTOR` |

---

## Backups

The whole board is one SQLite file, so backing up is copying it. For a guaranteed-consistent
snapshot (even while `am serve` is running), use `am db export`:

```sh
am db export                     # writes a timestamped snapshot in the cwd, prints the path
am db export /backups/board.db   # or pick the path
am db import /backups/board.db   # restore — stop `am serve` first; backs up the current DB
```

`am db import` validates the snapshot, refuses to run while a server is up, and backs up your
existing DB before swapping it in.

To trim the event log on a long-running instance, use `am db prune` (stop `am serve` first):

```sh
am db prune --before 2026-01-01   # delete events older than 2026-01-01 (same-day events kept)
am db prune --keep 10000          # keep only the newest 10 000 events
am db prune --keep 10000 --yes    # skip the confirmation prompt
```

`am db prune` deletes **events only** (not tasks, comments, or projects), then runs `VACUUM`. The
dashboard's activity feed also has a **"Load older activity"** button to page back through history.

---

## Updating

```sh
am update            # reinstalls the latest release (runs `go install …@latest` for you)
# or directly:  go install github.com/RamiAltai/agentman/cmd/am@latest
```

Then **restart any running `am serve`** — the dashboard is embedded in the binary, so a running
server keeps serving the old UI until you restart it (hard-refresh the browser tab too). `am serve`
also logs `update available — vX.Y.Z` on startup when you're behind; disable that with
`AGENTMAN_NO_UPDATE_CHECK=1`.

> **Maintainers:** `…@latest` resolves to the highest **git tag**, so publish each release as a
> semver tag — `git tag v0.6.0 && git push origin v0.6.0`.

---

## How it works

- **Single writer.** `am serve` is the only process that touches the DB (`SetMaxOpenConns(1)`, WAL
  mode). Claims are atomic via one conditional `UPDATE … WHERE assignee IS NULL AND status!='done'
  RETURNING …`; the loser of a race gets `409 already_claimed`. Stale-claim takeover
  (`am claim --steal-stale <dur>`) uses the same trick with a staleness predicate, so a crashed
  agent's task can be recovered — exactly one stealer wins, the rest get `409 not_stale`, and a
  `task.reclaimed` event records the handoff.
- **Live updates.** Every mutation appends to an append-only `events` table in the same transaction,
  then broadcasts over SSE after commit. That table is also the durable cursor used to replay missed
  events on reconnect.
- **Embedded dashboard.** Plain HTML/CSS/vanilla JS, embedded via `go:embed` — no build step, no
  npm. Agent-supplied text is rendered with `textContent` (never `innerHTML`), so a malicious task
  title can't inject markup.

---

## Security

`am serve` binds to `127.0.0.1` with **no authentication** — it's a personal, local board. Don't
expose the port to untrusted networks. If you need remote/multi-user access, put it behind a reverse
proxy with auth.

Agent **scopes** (`X-Agent-Scope`, `am init -c …`) confine a *config-following* agent to its slice of
the board. On their own they are **client-asserted labels** — any local caller can forge or omit the
header. **Scope tokens** (`am token new --scope …`) upgrade this: a token is server-minted and bound
to a scope, its scope **wins over** the header, minting requires an unscoped caller, and a
bad/revoked token hard-fails (`401`/exit 9) — so a config-following agent that holds only its own
token **cannot forge another scope's token**. Tokens are stored as sha256 hashes (never plaintext).
This is still **loopback-only** and **not** authentication against an arbitrary local process: a
process that can read the identity file holds the token. See `architecture/security.md` for the full
threat model.

---

## Development

```sh
go build -o am ./cmd/am                       # build
go vet ./... && go test ./...                 # lint + tests
am serve --db /tmp/dev.db                     # run against a throwaway db
go build -ldflags "-X main.injectedVersion=v0.6.0" -o am ./cmd/am   # version-stamped build
```

Layout: `cmd/am/` holds the single `main` package — `server.go` (API + SSE), `hub.go` (broadcast),
`store.go` + `schema.sql` (SQLite), `client.go` + `cli.go` (CLI), `db.go` (`am db` export/import),
`identity.go`, `version.go`, `update.go`, `wait.go`, and `web/` (dashboard). The `architecture/`
directory holds ADRs and design docs.

CI runs `go build`, `go vet`, `gofmt -l`, `go test -race`, a JS syntax check, and `govulncheck` on
every push to `main` and every PR.
