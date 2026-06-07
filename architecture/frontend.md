# Frontend Architecture

There **is** a frontend: a small single-page dashboard in `cmd/am/web/`
(`index.html` 50 lines, `app.css` 415 lines, `app.js` 957 lines), embedded into the binary via
`//go:embed web` (`cmd/am/server.go`) and served at `/`. It is the human-facing view; agents do
not use it.

## Framework

**None — intentional.** Vanilla HTML/CSS/JavaScript, no framework, no bundler, no npm, no build
step. The only DOM construction helper is `el(tag, props, ...kids)` in `app.js`, which appends
string children as **text nodes** (never `innerHTML`) so agent-supplied text can't inject markup.

## Routing

No client-side router. It's a single page; "navigation" is toggling project tabs on/off
(`toggleProject`) and opening/closing a modal. The filter is **multi-select** — several projects
can be active at once, and "All" clears the selection. No URL/history manipulation except the
implicit single document.

## Pages and Components

All built imperatively in `app.js` (no component framework):
- **Header / tabs** — `renderTabs`, `tab()`: project tabs with open-count badges + an "All" tab + a
  "＋" new-project button + a "⋯" **Manage projects** button (after the ＋). Clicking "⋯" calls
  `openManageProjects`, which opens the reused `#sheet` modal (same focus-trap / Esc-to-close
  infrastructure as the task modal). Inside, `renderManageList` fetches all projects including
  archived ones via `GET /api/projects?archived=true` and builds a list with `el()` (no
  `innerHTML`): active projects show an **Archive** button; archived projects show an **Archived**
  badge and an **Unarchive** button. Both buttons call `POST /api/projects/{slug}/archive|unarchive`
  via `api()`, then refresh the list. If the just-archived project was selected, the tab bar and
  board reload automatically.
- **Board** — `renderBoard`, `card(t)`: four status columns (`COLS`), priority via card left-border
  + chip, avatar initials, project tag (shown when `selected.size !== 1`, i.e. only when the board
  isn't already scoped to a single project), comment count. Cards now also show a dependency tag in
  the card footer: **🔒 Blocked** (`.tag-blocked`, shown when `t.nopen > 0`) or **✓ Ready**
  (`.tag-ready`, shown when `t.nprereq > 0 && t.nopen === 0`). These are derived from server-side
  counts (`nprereq`/`nopen` on the task object); there is no stored "ready" status field.
- **Activity feed** — `feedItem`, `evText`, `evKind`: color-coded events with clickable `#refs`.
  Event kinds include the project lifecycle: `project.created`, `project.archived`,
  `project.unarchived` (render via `evText`/`describeText`; `evKind` colors them as generic "other"),
  and the new delete kinds: `task.deleted`, `comment.deleted`, `project.deleted`. The feed supports
  **backward pagination** via a "Load older activity" button appended **outside** `#feedList` (so
  `trimFeed` can't remove it); clicking it fetches `GET /api/events?before=<oldest-loaded-id>` and
  appends the results. `feedOldest` tracks the lowest event id currently in the feed; `feedPaginated`
  is set to `true` on the first paginated fetch, which causes `trimFeed` to skip its cap so the user's
  loaded history is not silently discarded. Trade-off: a long-running tab that paginates can grow
  the feed unbounded until the next page reload. When `?before=` returns no events, the button is
  replaced by a `"— start of activity —"` end-marker. All DOM via `el()` (no `innerHTML`).
- **Detail modal** — `renderModal`, plus `openNew` (new task) and `openNewProject`: one reused
  `#sheet` element; auto-growing title `<textarea>`; status/assignee/priority controls; comments;
  history. The modal includes a **Delete task** button (inline two-step confirm — see below) and
  each comment has a **× delete** button (also two-step). The modal also has a **Dependencies**
  section (`depsSection`):
  - **"Depends on"** — one chip per prerequisite showing a status dot, `project-ref` (clickable
    link to open that task), title, status text, and a **✕ remove** button
    (`DELETE /api/tasks/{id}/deps/{depId}`). Open prereqs get class `dep-open`; done ones get
    `dep-done`. If none, shows "None".
  - **"Add prerequisite…"** — a `<select>` dropdown lazily populated with same-project tasks
    (excludes self and already-linked tasks). Selecting a candidate calls
    `POST /api/tasks/{id}/deps {depends_on: id}` and refreshes the modal; an inline error element
    shows the rejection message (e.g. "would create a dependency cycle").
  - **"Blocks"** — a read-only list of tasks that depend on this one (`t.blocks`); each row shows
    the ref link, title, and status.
  Hard-block UX: if a claim or status-change to `doing`/`done` is rejected with a 409 `blocked`
  response, the dashboard surfaces the blocking prereq ids (e.g. "blocked by #1 #2 (prereq not
  done)") and reverts the card/modal to its previous state.
- **Delete affordances** — three inline two-step confirms (no native `confirm()`/`prompt()` — they
  are blocked in webviews; all DOM built via `el()`, no `innerHTML`):
  1. **Delete task** (`btn-danger-task`) in the task modal — on first click shows "Confirm delete?";
     a 4-second timeout resets it; second click calls `DELETE /api/tasks/{id}`.
  2. **Per-comment ×** (`btn-del-cm`) on each comment row — same two-step flow; calls
     `DELETE /api/tasks/{id}/comments/{cid}`.
  3. **Delete project** (`btn-danger-proj`) in the Manage-projects modal — distinct from the Archive
     button; two-step with a 5-second timeout; calls `DELETE /api/projects/{slug}`.
  All three are irreversible hard deletes (cascade for projects/tasks).

## State Management

Module-level mutable variables in `app.js` (no store/framework):
`projects` (array), `selected` (`Set<slug>` of active project filters, empty=all), `tasks`
(`Map<id,task>`), `cursor` (highest seen `events.id` for SSE `since=`), `es` (EventSource),
`openTaskId`, `dragId`, `lastFocus`, `feedOldest` (lowest event id currently in `#feedList`; `0`
if none loaded), `feedPaginated` (`true` once the user has paginated; disables `trimFeed` cap),
`loadOlderBtn` (reference to the "Load older" button outside `#feedList`). Reconciliation is
**snapshot-based**: on each SSE event the feed updates immediately and a **debounced (250 ms) full
`loadBoard()`** re-fetches and re-renders (`onEvent`). Simple and correct over clever diffing.

## API Integration

- **`api(method, path, body)`** — `fetch` wrapper; always sends `X-Agent: human`; throws on non-2xx
  with the server's `error` field.
- **SSE** — `connect()` opens `EventSource('/api/stream?since=<cursor>')`, with `&project=<slug>`
  appended by `qstr()` **only when exactly one project is selected** (`selected.size === 1`); for 0
  or 2+ selected it streams/loads everything and `renderBoard()` filters client-side
  (`selected.has(t.project)`). `onmessage` → `onEvent`; `onerror` → close + reconnect with
  exponential backoff (1s→10s) and a "reconnecting…" status. `loadFeed()` bootstraps from
  `/api/events?tail=50` (same `qstr` rule). `onEvent` handles the three delete kinds:
  `task.deleted` removes the card from `tasks` map and closes the modal if it was open;
  `comment.deleted` refreshes the open modal; `project.deleted` drops the slug from `selected` and
  reloads the board/feed. For `task.dep_added` and `task.dep_removed`, `onEvent` refreshes the
  open modal if either the task or the referenced prereq is currently open (so both sides of the
  edge see the update), then triggers the debounced board reload.
- Same-origin only; no CORS, no auth token (the API is unauthenticated).

## Styling and Design System

`app.css` defines a **CSS-variable design system** (`:root` tokens): background/surface levels,
`--line`, text `--fg`/`--muted`/`--faint`, `--accent`, status colors (`--st-todo/doing/blocked/
done`), radii, `--feed-w`, `--header-h`. Dark theme only (`color-scheme: dark`). Thin custom
scrollbars. Status and priority each have a consistent color language across board, cards, and feed.

The board uses `justify-content: safe center` on `#board` so columns are centered on wide/ultrawide
screens. The `safe` keyword falls back to `flex-start` when columns overflow their container, so
horizontal scrolling on narrow screens never clips the leftmost column. New CSS classes support the
Manage-projects modal: `.proj-list`, `.proj-row`, `.badge-archived`, `.btn-archive` (and
`.btn-archive.unarchive`).

## Forms

Plain inputs/selects/textareas inside the modal; changes **auto-save** via `onchange` →
`PATCH`/`POST` (no submit button for edits). New task / new project use a "Create" button with
inline `.ferr` error text. Slug auto-derives from project name (`slugify`).

## UI States

- **Empty states** — `boardEmpty()` (no projects / no tasks, with a CTA), `.empty-col` per empty
  column, "No comments yet" / "No activity yet".
- **Connection state** — `setStatus()` shows `live` (green pulse) / `reconnecting…` / `connecting…`.
- **Loading** — minimal; localhost fetches are instant. No spinners.
- **Done column** capped at 50 rendered cards (`+N more`); feed capped at ~200 nodes (`trimFeed`) —
  cap is skipped once the user has paginated (`feedPaginated = true`) to preserve loaded history.

## Accessibility

Deliberately addressed in this codebase (see `decision-records.md` IADR / UX history):
- Skip link (`index.html`), global `:focus-visible` ring, `prefers-reduced-motion` reset.
- Modal: `role="dialog"`, `aria-modal`, dynamic `aria-label`, a **focus trap** (`trapFocus`) and
  **focus restore** to the trigger (`lastFocus`).
- Cards are `role="button"`, `tabindex=0`, openable with Enter/Space; status moves via `[` / `]`.
- Keyboard shortcuts (`onKey`): `n` new task, `a` toggle activity, `Esc` close.
- `aria-pressed` on tabs, `aria-expanded` on the activity toggle, labels on all fields.
- Drawer resize handle is a `role="separator"` with arrow-key support.

## Testing

**No JS test runner** — by deliberate decision (Phase E4 / ADR-018). Adding npm/jsdom would break
the no-npm/single-binary/no-build-step ethos that is a core project invariant.

**XSS-sink guard (Go level):** `cmd/am/web_test.go` `TestDashboardNoXSSSinks` reads the embedded
`web/app.js` + `web/index.html` via the `webFS` embed.FS at Go test time and asserts that none of
`.innerHTML`/`.outerHTML`/`.insertAdjacentHTML`/`document.write`/`eval(` appear. This locks in the
`el()`/`textContent` convention at `go test` time — an accidental sink assignment will fail the
build before it ships.

**Remaining gap:** behavioral dashboard JS — the "Manage projects" modal, the delete confirm flows
(task/comment/project), the feed pagination button, the dependency section (prereq chips, add-prereq
dropdown, blocks list), multi-select filter logic, and SSE reconciliation paths — is not
automatically tested. The new dependency UI is additional un-runner-tested JS; XSS safety is
guarded by `TestDashboardNoXSSSinks`. All frontend behavior in these docs is from source reading
and manual verification. (Gap; see `known-risks-and-gaps.md`.)

## Where to Add New Features

- All UI changes go in `cmd/am/web/` (`index.html`/`app.css`/`app.js`). **Rebuild the binary**
  after editing (`go build -o am ./cmd/am`) — assets are embedded, so a running server serves the
  old UI until restarted.
- New card field → extend `card()` + `renderModal()`. New event type → `evKind`/`evText`/
  `describeText`. New board affordance → `renderBoard()`/`moveTask()`.

## Risks and Gaps

- **Native HTML5 drag-and-drop doesn't fire on touch** → mobile relies on the status dropdown /
  `[ ]` keys (documented fallback in code comments).
- **Full board re-render per event batch** (debounced) — fine at small scale, O(n) at large scale.
- Single 957-line `app.js`, no module split, no minification. Behavioral JS logic is not
  automatically tested (deliberate no-JS-runner decision); XSS-sink safety is enforced by the
  `TestDashboardNoXSSSinks` Go guard. The delete confirm flows, feed pagination, and dependency
  UI are still untested at the behavioral level.
- `localStorage` access is wrapped (`lsGet`/`lsSet`) so a sandboxed/Private-mode browser won't
  break the app — keep that pattern if you add persistence.
