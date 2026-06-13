# Changes draft — dark/light theme toggle (dashboard)

Frontend-only change to the embedded dashboard (`cmd/am/web/`). No Go API, schema, or
CLI surface changed. This file is the docs-sync stage's input — it should let someone
update README/docs/CHANGELOG without re-reading the diff.

## Externally visible changes

- **New theme toggle button.** A ghost icon button (`#themeToggle`) now sits in the
  header `.actions` row, immediately before the **Graph** button. It shows the theme you
  would switch TO: `☀` (sun) while in dark mode, `☾` (moon) while in light mode. Click to
  flip. `aria-label`/`title` read "Switch to light theme" / "Switch to dark theme" and
  `aria-pressed` reflects whether light is active. No keyboard shortcut is bound (by
  design — the existing a/n/g// shortcuts are unchanged).
- **System-default-then-persist behavior.** On first load the dashboard follows the OS
  `prefers-color-scheme`. Once the user clicks the toggle, their explicit choice is saved
  and from then on the dashboard ignores OS changes. While no explicit choice is stored,
  the dashboard live-follows OS theme changes (e.g. macOS auto night switch) without a
  reload.
- **New localStorage key `am.theme`.** Values: `"light"` or `"dark"`. Unset means "follow
  the system." This sits alongside the existing `am.feedW` / `am.feedCollapsed` keys.
- **Light theme across all surfaces.** Board, columns, cards, header, tabs, search, the
  task modal, manage-projects list, dependency tags/chips, activity feed, modal/feed/graph
  backdrops, and the dependency-graph overlay (nodes, edges, detail panel, legend) all have
  light variants. Dark remains the default look.
- **`color-scheme` meta tag** changed from `content="dark"` to `content="dark light"` so
  the browser renders native form controls / scrollbars correctly in both themes.

## Design decisions (ADR-style)

- **`data-theme` attribute on `<html>`.** Theme is selected by a single
  `data-theme="light"` attribute on the root element. Dark is the bare `:root` default; an
  unknown or absent `data-theme` renders dark.
- **Single light override block.** `app.css` keeps the existing dark `:root` as the
  default and adds one `:root[data-theme="light"]{…}` block that re-defines the color
  tokens. To make this possible, ~20 previously-inline color literals (backdrops, tag
  pill backgrounds/borders, the status pulse ring, `#fff` on-accent text, the scrollbar
  hover, the graph legend background, etc.) were tokenized into new CSS custom properties
  on `:root` and overridden in the light block. No visual change in dark mode — the new
  tokens hold the exact previous literal values.
- **Inline `<head>` FOUC-prevention script.** A tiny IIFE in `index.html` reads
  `localStorage["am.theme"]` (falling back to `prefers-color-scheme`, then `"dark"` on any
  error) and sets `data-theme` before the stylesheet loads, so there is no flash of the
  wrong theme. It is static markup, not `innerHTML`, and contains none of the XSS-sink
  substrings the source-level guardrail test forbids.
- **No theme-swap animation.** The global `@media (prefers-reduced-motion: reduce)` reset
  already disables transitions; the theme swap intentionally adds none.
- **PRIO color literals left untouched.** The `PRIO` JS array in `app.js` (priority
  border colors) is unchanged per scope.

## Planner-flagged risks

- **Color literals must be tokenized exhaustively** or dark mode would visually regress.
  Mitigation: every tokenized literal keeps its original value in the dark `:root`; a grep
  for the original literal patterns outside `--var` definitions returns empty after the
  edits.
- **FOUC** if the theme were applied only by `app.js` after stylesheet load. Mitigated by
  the inline head script that runs before the stylesheet.
- **localStorage unavailable** (private mode / sandboxed iframe). Both the inline script
  (try/catch → `"dark"`) and `app.js` (`lsGet`/`lsSet` swallow exceptions) degrade
  gracefully; the toggle still works for the session.
- **`matchMedia` unavailable.** `initTheme` wraps the media-query listener in try/catch and
  uses `addListener` as a fallback for older `addEventListener`-less `MediaQueryList`.

## Deviations from the implementation map

- Added one small CSS rule `.theme-toggle-icon { font-size: 14px; line-height: 1; }` next
  to the `.iconbtn` rules. The map said add a sizing rule "only if needed / if the glyph
  looks off"; this locks the glyph to a sensible size and crisp vertical centering inside
  the ghost button. Low-risk, explicitly permitted by the map.
- The `am` task board server was down (`am` exited 6, "cannot reach server"), so no board
  ticket was created/updated for this work.

## Tests added

- `cmd/am/web_test.go`: `TestDashboardThemeAssets` — asserts `app.css` contains the
  `:root[data-theme="light"]` block and `index.html` contains both the inline
  `localStorage.getItem("am.theme")` init script and the `id="themeToggle"` button.
  (Same source-level / no-JS-runner guardrail pattern as the existing
  `TestDashboardNoXSSSinks`.)

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `go test -race -count=1 ./...`,
  `node --check cmd/am/web/app.js` — all green (see commit message / report).
- Rebuilt the embedded binary (`go build -o am ./cmd/am`) and started a throwaway server
  on a spare port (8801) with a temp DB (`/tmp/theme_demo.db`), then curled the served
  assets to confirm the rebuilt binary serves the new code:
  - `curl -s 127.0.0.1:8801/app.css` contains `:root[data-theme="light"]` — FOUND.
  - `curl -s 127.0.0.1:8801/app.js` contains `function initTheme` — FOUND.
  - `curl -s 127.0.0.1:8801/` (the dashboard root) contains the
    `localStorage.getItem("am.theme")` init script, `id="themeToggle"`, and
    `content="dark light"` — all FOUND.
  - Note: hitting `/index.html` directly returns a 301 redirect to `/` (Go's
    `http.FileServer` canonical-path behavior), so the served HTML must be checked at `/`,
    not `/index.html`. Server killed and temp DB removed afterward.
</content>
</invoke>
