# 03 — Engineering Report: agentman Dashboard Redesign

**Author:** Senior Frontend Engineer (lifecycle subagent 3)
**Inputs:** `01_PRODUCT_BRIEF.md`, `02_DESIGN_SPEC.md`, current `index.html` / `app.css`
/ `app.js`, and the `web_test.go` compatibility contract.
**Status:** Implemented in place. Gate green (`go test ./cmd/am/...` + `node --check`).

This stage restyles existing classes through a token-based CSS system, makes
additive ARIA/structure improvements to `index.html`, and applies surgical,
`el()`-only changes to `app.js`. No backend, build step, npm, bundler, or CDN was
introduced. The DOM stays builder-based (no `innerHTML`/`outerHTML`/
`insertAdjacentHTML`/`document.write`/`eval(`).

---

## 1. Changed files

- `cmd/am/web/app.css` — primary work: full design-token system (color, type,
  spacing, radius, elevation, focus, motion) for both themes; restyle of every
  existing class; new additive component styles (status pill, priority rank chip,
  reserved trouble sub-row, feed glyph cell, one-shot card flash, loading
  skeletons, toast region).
- `cmd/am/web/index.html` — additive only: grouped the actions cluster into
  `Find` / `View` / `Act` ARIA segments with hairline `.actions-sep` dividers.
  Every id, the inline theme-init FOUC script, `#themeToggle`, `#filterBtn`,
  `#filterPanel`, and the rest of the structure preserved verbatim.
- `cmd/am/web/app.js` — surgical, additive: card three-zone layout (status pill +
  priority rank for all four levels + reserved trouble sub-row), per-kind feed
  glyph, one-shot `.is-updated` flash on reconciled cards (reduced-motion
  guarded), clearer SSE offline/connecting state in `#status`, search-box
  active-filter affordance, and loading-skeleton + non-blocking toast helpers.
- `cmd/am/web_test.go` — **unchanged** (the contract; left exactly as found).

---

## 2. Compatibility checklist (each contract item: PASS + how)

1. **No XSS sinks in app.js / index.html** — PASS. `grep -nE
   '\.innerHTML|\.outerHTML|\.insertAdjacentHTML|document\.write|eval\('` over
   both files returns nothing. Every new node is built with `el()` +
   `textContent`/`setAttribute`/`classList`. `TestDashboardNoXSSSinks` passes.

2. **app.css keeps `:root[data-theme="light"]` and `.filter-panel`,
   `.meta-add-row`, `.btn-release`, `.cat-card-add`, `.cat-row`** — PASS. All five
   class selectors and the light-theme block are present and restyled, not
   removed. `TestDashboardThemeAssets` + `TestDashboardParityAffordances` pass.

3. **index.html keeps the inline `localStorage.getItem("am.theme")` theme-init
   script, `id="themeToggle"`, `id="filterBtn"`, `id="filterPanel"`** — PASS. The
   `<head>` FOUC-guard script is untouched; the three ids are intact (the filter
   button/panel simply moved inside the new `Find` segment wrapper, ids unchanged).

4. **app.js keeps all verbatim identifiers/strings** — PASS. Confirmed by grep:
   `openNewCategory`, `newCatCard`, `/api/categories`, `category: csel.value`,
   `renderManageCategories`, `/api/categories?archived=true`, `/unarchive`,
   `openEditProject`, `btn-edit-proj`, `vault_project_id`, `filterReady`,
   `filterBlocked`, `filterStale`, `filterMetaKey`, `renderFilterPanel`,
   `patchMeta`, `meta-add-row`, `btn-release` all still present. None renamed.

5. **REST calls, SSE `EventSource("/api/stream"...)` + reconnect/backoff, theme
   toggling via `data-theme`, leading `"use strict"`** — PASS. No `/api/*` path was
   changed. `connect()` retains the same `EventSource`, exponential backoff
   (`backoff = Math.min(backoff * 2, 10000)`) and jitter; I only added status-text
   wording for the offline/connecting state (no logic change to the timing or
   reconnect cadence). `applyTheme` still calls
   `document.documentElement.setAttribute("data-theme", theme)`. `"use strict"` is
   line 1.

6. **No backend / npm / build / bundler / CDN; no global pollution; no-build
   single-file static deploy** — PASS. Only the three embedded static files were
   edited. `go build ./cmd/am/` succeeds. No new top-level globals beyond a few
   module-scoped `const`/`let` (e.g. `PRIO_RANK`, `EV_GLYPH`, `flashIds`,
   `toastRegion`) in the existing single-IIFE-free module scope, consistent with
   the file's existing pattern. No external `<link>`/`<script>`/font added.

---

## 3. Preserved backend assumptions

- Identical REST surface: every `/api/*` request/response shape, query param, and
  method is unchanged (board still `GET /api/tasks?…limit=500`, filters folded
  into the query string exactly as before; modal CRUD, deps, labels, meta,
  comments, release, project/category endpoints all untouched).
- SSE stream: same `EventSource("/api/stream" + qstr({ since: cursor }))`, same
  dedupe-by-id (`ev.id <= cursor`), feed prepend + `trimFeed` at 200 (pagination
  guard preserved), debounced board reconcile (250ms), and modal/graph/overview
  refresh on the same event kinds.
- Embed model: assets remain `//go:embed web` static files; no network
  dependency introduced, so the offline single-binary deployment is intact.
- Theme contract: dark is the `:root` default; light is the single
  `[data-theme="light"]` override; an unknown/absent theme renders dark.

---

## 4. New CSS tokens / classes

**New additive tokens** (all in `:root` + `:root[data-theme="light"]`):
`--surface-2`, `--accent-strong`, `--st-stale`, `--st-todo-soft`,
`--st-doing-soft`, `--st-blocked-soft`, `--st-done-soft`, `--st-blocked-col`,
`--prio-0..3`, the type scale (`--fs-2xs`…`--fs-xl`, `--lh-tight/normal/relaxed`),
the spacing scale (`--sp-1`…`--sp-8`), the radius scale (`--r-xs`…`--r-pill`),
elevation (`--elev-0/1/2`), `--focus-ring`, and motion
(`--dur-fast/base/flash`, `--ease`). Every pre-existing token name was kept
verbatim (the JS still reads `--st-*`, `--prio` inline, `--on-accent`,
`--tag-*`, `--dep-open-border`, `--legend-bg`, etc.).

**New additive classes:** `.status-pill` + `.st-todo|doing|blocked|done`,
`.chip-prio.p0..p3` (the rank chip, now shown for all four levels),
`.ctrouble` (reserved trouble sub-row), `.ctags` (labels/comments line),
`.card.is-updated` + `@keyframes card-flash`, `.ev-icon`, `.actions-seg` /
`.actions-sep`, `.search-input.has-query`, `.status.err`,
`.skeleton`/`.skel-card`/`.skel-line`/`.skel-feed` + `@keyframes
skeleton-shimmer`, `.toast-region`/`.toast`/`.toast-ok`/`.toast-msg`/`.toast-x`,
and `.col[data-status="blocked"]` warm tint. The `.ev-dot`/`k-*` rules are kept
for documented back-compat per the spec (the new `.ev-icon` carries the
non-color cue).

---

## 5. JS behavior changes (all additive, `el()`-only)

- `card(t)` rebuilt into a fixed three-zone layout: ROW 1 head (`#id` + `chip-prio
  pN` rank for **every** priority + a labeled `status-pill st-<status>`), ROW 2
  title, ROW 3 a predictable meta rhythm: `.cfoot` (assignee · project), a
  reserved `.ctrouble` sub-row (🔒 Blocked N / ✓ Ready / ⏳ Stale, before labels),
  then `.ctags` (label chips · 💬 count). New consts `PRIO_RANK`, `PRIO_WORD`,
  `ST_LABEL`. The card's inline `--prio` rail is unchanged.
- `feedItem(ev)` prepends a per-kind decorative glyph (`EV_GLYPH`,
  `aria-hidden="true"`) keyed off the existing `evKind(ev)`; the `k-<kind>` class
  is kept.
- One-shot live-update highlight: `onEvent` records touched task ids in `flashIds`
  (board views only, `task.*`, not deletes); `renderBoard` → `applyFlash()` adds
  `.is-updated` once and self-clears on `animationend`. Suppressed entirely under
  `prefersReducedMotion()`.
- SSE state surfacing: `connect()` shows `connecting…` on (re)connect, `live` on
  open, `reconnecting…` while backing off, and a loud `offline — retrying…` (new
  `.err` channel) once backoff has climbed or `navigator.onLine === false`. Timing
  and reconnect logic unchanged.
- Search box toggles `.has-query` so search reads as part of the active-filter
  family.
- Additive helpers (not yet load-bearing): `boardSkeleton()`/`feedSkeleton()`
  render reduced-motion-safe loading placeholders on first paint (replaced by the
  real render via `replaceChildren()`); `showToast()` provides a non-blocking
  `role="status"` toast to supplement `alert()`.

No rendering/data-layer or SSE/REST rewrite. Focus traps (`trapFocus`,
`graphTrapFocus`), focus restore (`lastFocus`, `graphLastFocus`), Escape-to-close,
and all global/card/resize keybindings were already present and were left
untouched (so keyboard parity is preserved).

---

## 6. Manual test steps

1. `go test ./cmd/am/...` → ok. `node --check cmd/am/web/app.js` → OK.
2. `go run ./cmd/am serve`, open `http://127.0.0.1:8787`.
3. Board: each card shows `#id`, a `P0–P3` rank chip (all priorities), and a
   labeled status pill. Blocked/Stale/Ready appear on their own reserved row
   above labels and don't reshuffle when labels wrap. Blocked column has a faint
   warm tint.
4. Move a card (`[`/`]` or drag) and watch a second tab: the changed card flashes
   once. Enable OS "reduce motion" → no flash/shimmer/pulse.
5. Filter: type in search (box gains accent), open the Filter popover (Ready /
   Blocked / Stale / Assignee / Meta), confirm the count chip and `has-filters`
   state; `Esc` closes the popover, then `Esc` closes the modal.
6. Activity feed: each row shows a typed glyph (claim ▸, status ⇄, done ✓, blocked
   ⏸, comment 💬) distinct without relying on color.
7. Toggle theme (☀/☾) — verify both themes; check amber (blocked/stale) text
   contrast on light. Reload to confirm no FOUC.
8. Stop the server → status pill goes `reconnecting…` then `offline — retrying…`;
   restart → returns to `live`.
9. Open a task modal, edit fields, deps, labels, meta, comments; Release; Delete
   (two-step). Open the dependency graph (`g`), pan/zoom/focus, Reset, Esc.

---

## 7. Known limitations

- **Skeletons / toasts are wired-but-light.** `boardSkeleton`/`feedSkeleton` show
  on first paint and are replaced on data arrival; `showToast()` exists but
  `alert()` is intentionally still used for action failures (incremental
  migration per the spec). No selector the JS queries is affected by either.
- **`.ev-dot` CSS is retained but no longer emitted.** Kept deliberately for the
  back-compat the design spec calls out; not a live selector.
- **Graph overlay restyle is conservative.** Colors already flow through retained
  tokens (`--st-done`, `--warn`, `--accent`, `--violet`, `--card-bg`,
  `--legend-bg`); layout math, pan/zoom, and selection-restore were left
  untouched to avoid regressing the stateful graph. Node/edge non-color cues
  (dashed-vs-solid edges + arrow markers, `🔒`/`✓` glyphs, detail-panel labels)
  were already present and preserved.
- **No automated JS test runner** (by project design); verification is the Go
  source-level guardrails plus `node --check` and manual steps above.

---

## 8. Fix round 2 — WCAG AA contrast on status / priority / colhead text

Adversarial review round 1 returned **REJECT** with one blocking issue: several
small-text color tokens used for the new status pills, the always-present priority
rank chip, and the Blocked column header failed WCAG 2.1 AA (≥ 4.5:1 for normal
text) on their *actual composited* surfaces in one or both themes. The fix is
**CSS-token-only** — no selector, class, DOM node, JS identifier, REST/SSE path, or
needle was touched. Only the *values* of text-color custom properties changed; every
soft-fill `rgba()` and border value was left as-is.

### What was failing (measured over composited surfaces)

| Theme | Token (old) | Surface | Old ratio |
|---|---|---|---|
| light | `--st-done`/`--ok` `#0f9d75` | pill text on done-soft over `--card-bg` | 3.00 |
| light | `--st-blocked`/`--warn` `#b9770a` | blocked-soft pill / blocked colhead on `--col-bg` | 3.21 / 3.23 |
| light | `--st-doing` `#2f6df0` | pill text on doing-soft over card | 4.04 |
| light | `--st-todo` `#6b7280` | pill text on todo-soft over card | 4.17 |
| light | `--prio-3` `#9aa3b2` | "Low" chip text on white card | 2.54 |
| light | `--prio-1` `#b9770a` | "High" chip text on white card | 3.68 |
| light | `--prio-0`/`--danger` `#d94436` | "Urgent" chip text on white card | 4.35 |
| light | `--st-stale`/`--tag-stale-fg` `#b25a12` | tag text on stale-bg over card | 4.11 |
| dark | `--st-todo` `#8b93a4` | pill text on todo-soft over card | 4.19 |
| dark | `--st-doing` `#5b8cff` | pill text on doing-soft over card | 4.13 |
| dark | `--prio-3` `#6e7681` | "Low" chip text on dark card | 3.59 |

### The fix — token value changes only

**Light theme (`:root[data-theme="light"]`)** — warm/colored text darkened toward
the dark end so it clears AA on white and on the lighter soft fills:

- `--ok` / `--st-done`: `#0f9d75` → `#0a7350`
- `--warn` / `--st-blocked` / `--prio-1`: `#b9770a` → `#8a5a08` (chosen to also pass
  the *hardest* warm surface — the Blocked colhead on `--col-bg`)
- `--st-doing`: `#2f6df0` → `#1d54c8`
- `--st-todo`: `#6b7280` → `#565d6b` (the *chip* `--prio-2` stays `#6b7280`, which
  already passes at 4.83:1 on bare white; the *pill* needs more because it sits on a
  soft fill)
- `--st-stale` / `--tag-stale-fg`: `#b25a12` → `#8f470d`
- `--prio-0` / `--danger`: `#d94436` → `#c43528`
- `--prio-3`: `#9aa3b2` → `#666d7c`

**Dark theme (`:root` default)** — the two trouble pills already passed (6.7:1); the
gray/blue pills and the Low chip were *lightened* toward white:

- `--st-todo`: `#8b93a4` → `#9aa1b3`
- `--st-doing`: `#7aa2ff` (lightened from `#5b8cff`; note this is intentionally
  decoupled from `--accent`, which is unchanged so brand fills/borders stay on-brand)
- `--prio-3`: `#6e7681` → `#8a92a1`

`--st-blocked`/`--st-done` (dark), `--prio-0/1/2` (dark) and the Blocked colhead
(dark, `#f8b738` on `#14171e` = 10.1:1) were already compliant and were not touched.

### Why no JS change was needed

The card priority *rail* uses the JS-injected inline `--prio` (the raw `PRIO[]` hex)
on the `.card` element, but that is a 3px decorative left border, not text — AA-for-
text does not apply. The priority **rank chip's** text color resolves from the CSS
token: `.chip-prio.pN { --prio: var(--prio-N); }` sets `--prio` *on the chip element
itself*, which wins over the value inherited from the card, so the chip word/`PN`
renders in the fixed `--prio-N` token. The status pill (`.status-pill.st-*`) and the
Blocked colhead (`.col[data-status="blocked"] .colhead`) likewise read `--st-*`
directly. All three flagged surfaces are therefore driven entirely by the tokens
changed above, so `PRIO`/`PRIO_RANK`/`card()` in `app.js` were left untouched (the
inline `PRIO[]` array — used for the graph node stroke and graph-detail chip, which
are outside this blocking issue's scope — is unchanged).

### Re-measured result (every flagged token, both themes)

All status / priority / colhead text tokens now clear 4.5:1 on their actual
composited surface (card, soft-fill chip, and column), lowest margin 4.83:1:

- **Light pills:** todo 5.70, doing 5.85, blocked 5.17, done 5.12.
- **Light chips:** P0 5.40, P1 5.92, P2 4.83, P3 5.19.
- **Light colhead** (blocked on `--col-bg`): 5.19. **Light trouble tags:** blocked
  5.17, ready 5.12, stale 5.83. **Light `--warn`/`--ok`/`--danger` on `--surface`:**
  5.92 / 5.86 / 5.40.
- **Dark pills:** todo 5.00, doing 5.24 (blocked 6.75, done 6.67 unchanged).
- **Dark chips:** P0 5.97, P1 9.28, P2 5.35, P3 5.27. **Dark colhead:** 10.10. **Dark
  stale tag:** 5.20.

Ratios computed with the WCAG 2.1 sRGB relative-luminance formula, compositing each
soft-fill `rgba()` over its opaque base surface before measuring the text against it.

### Gate (re-run, round 2)

- `go test ./cmd/am/...` → `ok` (incl. `TestDashboardNoXSSSinks`,
  `TestDashboardThemeAssets`, `TestDashboardParityAffordances`).
- `node --check cmd/am/web/app.js` → syntax OK.
- Full compatibility contract re-verified by grep: no XSS sinks; all five test-locked
  CSS selectors, the three index.html ids + theme-init script, and all 18 verbatim
  app.js identifiers/strings (plus `"use strict"` and `EventSource("/api/stream"`)
  still present.
