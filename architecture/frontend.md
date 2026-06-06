# Frontend Architecture

There **is** a frontend: a small single-page dashboard in `cmd/am/web/`
(`index.html` 50 lines, `app.css` 304 lines, `app.js` 591 lines), embedded into the binary via
`//go:embed web` (`cmd/am/server.go`) and served at `/`. It is the human-facing view; agents do
not use it.

## Framework

**None — intentional.** Vanilla HTML/CSS/JavaScript, no framework, no bundler, no npm, no build
step. The only DOM construction helper is `el(tag, props, ...kids)` in `app.js`, which appends
string children as **text nodes** (never `innerHTML`) so agent-supplied text can't inject markup.

## Routing

No client-side router. It's a single page; "navigation" is changing the selected project
(`selectProject`) and opening/closing a modal. No URL/history manipulation except the implicit
single document.

## Pages and Components

All built imperatively in `app.js` (no component framework):
- **Header / tabs** — `renderTabs`, `tab()`: project tabs with open-count badges + an "All" tab + a
  "＋" new-project button.
- **Board** — `renderBoard`, `card(t)`: four status columns (`COLS`), priority via card left-border
  + chip, avatar initials, project tag, comment count.
- **Activity feed** — `feedItem`, `evText`, `evKind`: color-coded events with clickable `#refs`.
- **Detail modal** — `renderModal`, plus `openNew` (new task) and `openNewProject`: one reused
  `#sheet` element; auto-growing title `<textarea>`; status/assignee/priority controls; comments;
  history.

## State Management

Module-level mutable variables in `app.js` (no store/framework):
`projects` (array), `current` (selected slug, `""`=all), `tasks` (`Map<id,task>`), `cursor`
(highest seen `events.id` for SSE `since=`), `es` (EventSource), `openTaskId`, `dragId`,
`lastFocus`. Reconciliation is **snapshot-based**: on each SSE event the feed updates immediately
and a **debounced (250 ms) full `loadBoard()`** re-fetches and re-renders (`onEvent`). Simple and
correct over clever diffing.

## API Integration

- **`api(method, path, body)`** — `fetch` wrapper; always sends `X-Agent: human`; throws on non-2xx
  with the server's `error` field.
- **SSE** — `connect()` opens `EventSource('/api/stream?since=<cursor>&project=<current>')`;
  `onmessage` → `onEvent`; `onerror` → close + reconnect with exponential backoff (1s→10s) and a
  "reconnecting…" status. `loadFeed()` bootstraps from `/api/events?tail=50`.
- Same-origin only; no CORS, no auth token (the API is unauthenticated).

## Styling and Design System

`app.css` defines a **CSS-variable design system** (`:root` tokens): background/surface levels,
`--line`, text `--fg`/`--muted`/`--faint`, `--accent`, status colors (`--st-todo/doing/blocked/
done`), radii, `--feed-w`, `--header-h`. Dark theme only (`color-scheme: dark`). Thin custom
scrollbars. Status and priority each have a consistent color language across board, cards, and feed.

## Forms

Plain inputs/selects/textareas inside the modal; changes **auto-save** via `onchange` →
`PATCH`/`POST` (no submit button for edits). New task / new project use a "Create" button with
inline `.ferr` error text. Slug auto-derives from project name (`slugify`).

## UI States

- **Empty states** — `boardEmpty()` (no projects / no tasks, with a CTA), `.empty-col` per empty
  column, "No comments yet" / "No activity yet".
- **Connection state** — `setStatus()` shows `live` (green pulse) / `reconnecting…` / `connecting…`.
- **Loading** — minimal; localhost fetches are instant. No spinners.
- **Done column** capped at 50 rendered cards (`+N more`); feed capped at ~200 nodes (`trimFeed`).

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

**None.** No JS test runner, no Playwright/Cypress, no snapshot tests. All frontend behavior in
these docs is from source reading and manual verification, not automated tests. (Gap.)

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
- Single 591-line `app.js`, no module split, no minification, no tests → refactors are unguarded.
- `localStorage` access is wrapped (`lsGet`/`lsSet`) so a sandboxed/Private-mode browser won't
  break the app — keep that pattern if you add persistence.
