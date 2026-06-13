# 02 — Design Specification: agentman Dashboard Redesign

**Author:** Senior UI/UX Designer (lifecycle subagent 2)
**Inputs:** `01_PRODUCT_BRIEF.md`, current `index.html` / `app.css` / `app.js`, and the
`web_test.go` compatibility contract.
**Status:** Design direction, ready to implement. **No code changed in this stage.**
**Hard rule:** every token, component, and interaction below is achievable in vanilla
HTML/CSS/JS with **zero build step, zero CDN, zero framework**, building all dynamic
DOM through the existing `el()` / `svg()` helpers + `textContent`. Anything that would
require `innerHTML`, a renamed test-locked selector, or a backend change is explicitly
out of scope and is **not** specified here.

This spec **restyles existing classes** and **adds** new opt-in classes / data
attributes / a handful of structural child elements built via `el()`. It never renames a
class `app.js` emits and never removes a frozen id/selector.

---

## 1. Design principles

1. **Clarity over decoration.** Every pixel of ink should encode operational state.
   Borders, fills, and chips earn their place by carrying meaning (status, priority,
   trouble); nothing is added purely to look "designed." Calm by default so the loud
   things stay loud.
2. **Live changes noticeable, not distracting.** A changed card gets a brief, single,
   reduced-motion-safe highlight — never a sustained pulse, never a reflow animation on
   every debounced reconcile. The feed prepends without yanking scroll. Motion informs;
   it never competes for attention with itself.
3. **Status readable four ways: color + text + icon + layout.** No signal is
   color-only. Status = column position (layout) + a labeled swatch (text) + a colored
   accent (color). Priority = a left rail (color) + an always-present rank token like
   `P1` (text) for every level. Trouble (blocked/stale) = an icon + a word + a reserved
   warm color + a position at the front of the meta row.
4. **Balanced density — dense but not cramped.** Target ~7–9 cards visible per column
   at a 900px viewport height. Whitespace is tuned to *separate* without *spreading*;
   radii are modest; the card foot has a fixed, predictable two-row rhythm that never
   reshuffles on re-render.
5. **Modal preserves context.** Opening a task dims (not destroys) the board behind it;
   closing restores focus to the originating card. Live updates keep flowing into the
   open modal. Filter/scroll/selection state survives every modal and graph round-trip.

---

## 2. Information architecture

### 2.1 Header / nav (unchanged structure, restyled)
The single fixed header keeps its left-to-right order: **brand → breadcrumb → project
tabs → actions cluster**. The actions cluster groups into three visual segments
separated by hairline dividers (pure CSS `::before` on segment wrappers — no new DOM
required, or one optional `el("span",{class:"actions-sep"})` if a real element reads
cleaner):

- **Find segment:** search box + label-filter chip + Filter button/popover.
- **View segment:** theme toggle + Graph.
- **Act/Status segment:** `+ Task` (primary) + live-status pill + Activity toggle.

The **"currently filtered" state** is unified: the Filter button gains its existing
`.has-filters` treatment and count chip, AND the search box and label chip read as part
of the same filter family (shared accent border when active). One glance answers "what
is filtered right now."

### 2.2 Board layout
Three views as today (`#/`, `#/all`, `#/cat/<slug>`), unchanged routing. The board is
the same four columns (`COLS`). Changes are presentational:

- **Column = a titled region**, not a bare flex child. Clearer `.colhead` with a
  labeled swatch, the column name, and a right-aligned count pill.
- **Calm column surface, loud trouble.** The Blocked column header carries a subtle warm
  tint so "is anything blocked" is answerable from the column chrome alone.
- Columns cap at `max-width` and the group centers on wide screens (preserve existing
  `justify-content: safe center`).

### 2.3 Task-card hierarchy (the headline change)
A card reads top-to-bottom in a **fixed three-zone** layout that never reshuffles:

```
┌─ priority rail (left border, color = priority) ──────────────┐
│ ROW 1 (head):  #id     [P1 HIGH]              ● Doing        │  ← status pill (text+icon+color)
│ ROW 2 (title): up to 3 lines, clamped                        │
│ ROW 3 (meta):  primary line — assignee · project             │
│                trouble line — 🔒 Blocked / ✓ Ready / ⏳ Stale  │  ← reserved "trouble" sub-row
│                tags line — label chips · 💬 count             │
└──────────────────────────────────────────────────────────────┘
```

Visual priority order (top of the brief's hierarchy goal): **status → priority →
blockers/stale → assignee/project → labels/comments.** The trouble sub-row is a
dedicated, reserved slot rendered *before* labels so blocked/stale never sink beneath
decorative chips. When there is no trouble, the slot collapses (no empty gap). This is
the key fix for "operationally critical signals don't reliably win attention."

### 2.4 Activity feed
The drawer keeps its structure (`.feed-head`, `#feedList`, load-older button outside the
list). Each `.ev` becomes a 3-column grid: **typed icon glyph** (not a color-only dot) ·
**event text** · **time**. Event kinds get distinct glyph + accent (claimed/status/done/
blocked/comment/other), so a `done` is distinguishable from a `blocked` from a `comment`
without relying on hue alone. Empty, paginated, and start-of-activity states get clearer
typographic treatment.

### 2.5 Modal
The `.sheet` editor keeps every section and the same DOM-build order, regrouped for
scan-ability: **header meta → title → status/assignee/priority row → description →
Dependencies → Labels → Meta → Comments → History → footer (Release / Delete).** Section
`h3`s become clearer "section eyebrows." The footer row keeps `.btn-release` pushed left
and Delete right. Backdrop dims the board; `.sheet` floats with elevation.

### 2.6 Dependency-graph behavior
Unchanged layout math, pan/zoom, selection-restore. Restyle nodes/edges and **add
non-color cues**: edges keep dashed-vs-solid (blocking vs cleared) AND gain the arrow
marker distinction already present; nodes keep the status dot but the ready/blocked
indicator stays text/glyph (`🔒` / `✓`) plus a same-meaning label in the detail panel.
Legend contrast fixed in both themes.

### 2.7 Empty / loading / error states
- **Empty board:** existing `.board-empty` (headline + sentence + CTA), restyled.
- **Empty column:** existing `.empty-col` dashed placeholder.
- **Empty feed:** existing `.feed-empty`.
- **Loading:** NEW lightweight **skeleton** state (pure CSS shimmer, reduced-motion
  safe) rendered via `el()` into `#board` / `#feedList` during the first load, replaced
  on data arrival. Optional/additive — no selector the JS already queries is affected.
- **Error:** status pill already shows `error: …`; ADD an inline, dismissible
  `.toast` region (new, additive) for transient action failures so we can begin
  migrating away from `alert()` where desired (non-blocking; `alert()` may remain).

---

## 3. Visual system — concrete token set

All tokens live as CSS custom properties. **Dark is the `:root` default; light is the
single `:root[data-theme="light"]` override** (test-locked). The tables below give the
new/retained values. Names that already exist are kept verbatim so `app.js`'s inline
`style:"background:var(--st-done)"` references keep working; new names are additive.

### 3.1 Color palette — DARK (`:root` default)

| Token | Value | Role |
|---|---|---|
| `--bg` | `#0e1014` | app background |
| `--surface` | `#161a21` | header, drawer, modal, popovers |
| `--surface-2` | `#1b1f28` | raised surface (new alias of card bg) |
| `--col-bg` | `#14171e` | board column body |
| `--card-bg` | `#1b1f28` | cards, inputs |
| `--card-hover` | `#212632` | card/input hover |
| `--line` | `#2a2f3a` | hairline borders |
| `--line-strong` | `#3a4250` | emphasized borders / scrollbar |
| `--fg` | `#e9ecf3` | primary text (≈13:1 on `--bg`) |
| `--muted` | `#aeb4c2` | secondary text (≈7:1 on `--bg`) |
| `--faint` | `#828a99` | tertiary text (≈4.6:1 on `--bg` — AA floor) |
| `--accent` | `#5b8cff` | brand / interactive |
| `--accent-strong` | `#7aa2ff` | accent text on dark surfaces (new) |
| `--accent-weak` | `rgba(91,140,255,.16)` | accent fills |
| `--ok` | `#35d6a4` | success / done |
| `--warn` | `#f8b738` | caution / blocked |
| `--danger` | `#f4756b` | destructive / urgent |
| `--violet` | `#a98bf0` | comment / downstream |

### 3.2 Status colors (semantic — DARK)
Kept names (JS reads `--st-*` and `ST[]` directly). Plus new optional "soft" fills for
backgrounds and "on" text colors so status pills can hit AA without per-element math.

| Token | Dark | Role |
|---|---|---|
| `--st-todo` | `#8b93a4` | todo accent |
| `--st-doing` | `#5b8cff` | in-progress accent |
| `--st-blocked` | `#f8b738` | blocked accent |
| `--st-done` | `#35d6a4` | done accent |
| `--st-stale` | `#f0883e` | stale accent (new; was `--tag-stale-fg`) |
| `--st-todo-soft` | `rgba(139,147,164,.16)` | todo pill bg (new) |
| `--st-doing-soft` | `rgba(91,140,255,.16)` | doing pill bg (new) |
| `--st-blocked-soft` | `rgba(248,183,56,.15)` | blocked pill bg (new) |
| `--st-done-soft` | `rgba(53,214,164,.14)` | done pill bg (new) |
| `--st-blocked-col` | `rgba(248,183,56,.05)` | blocked-column tint (new) |

> **review / failed:** the backend's status enum is fixed at todo/doing/blocked/done
> (see `COLS`); there is **no** review or failed state, so no tokens are minted for them.
> The "trouble channel" (blocked + stale) is the operational-alarm equivalent.

### 3.3 Priority colors
JS holds `PRIO = ["#f4756b","#f8b738","#8b93a4","#6e7681"]` inline. We **mirror** these as
tokens so CSS (left rail, chip) and JS (graph stroke) agree, and so light theme can
adjust them. JS keeps its array (no rename); CSS adds:

| Token | Dark | Light | Rank |
|---|---|---|---|
| `--prio-0` | `#f4756b` | `#d94436` | Urgent |
| `--prio-1` | `#f8b738` | `#b9770a` | High |
| `--prio-2` | `#8b93a4` | `#6b7280` | Normal |
| `--prio-3` | `#6e7681` | `#9aa3b2` | Low |

### 3.4 Color palette — LIGHT (`:root[data-theme="light"]`)

| Token | Value |
|---|---|
| `--bg` | `#f5f6f8` |
| `--surface` | `#ffffff` |
| `--surface-2` | `#ffffff` |
| `--col-bg` | `#eef0f4` |
| `--card-bg` | `#ffffff` |
| `--card-hover` | `#f0f2f6` |
| `--line` | `#d8dce3` |
| `--line-strong` | `#b9c0cc` |
| `--fg` | `#1a1d24` (≈14:1 on `--bg`) |
| `--muted` | `#4a515e` (≈7:1) |
| `--faint` | `#5f6775` (≈4.6:1 — AA floor; darker than today's `#6b7280` to guarantee AA on `--card-bg`) |
| `--accent` | `#2f6df0` |
| `--accent-strong` | `#2456c8` |
| `--accent-weak` | `rgba(47,109,240,.12)` |
| `--ok` | `#0f9d75` |
| `--warn` | `#b9770a` (amber darkened for AA on white — the brief's #1 risk) |
| `--danger` | `#d94436` |
| `--violet` | `#7c4ddb` |
| `--st-todo` | `#6b7280` · `--st-doing` `#2f6df0` · `--st-blocked` `#b9770a` · `--st-done` `#0f9d75` · `--st-stale` `#b25a12` |
| `--st-*-soft` | same rgba pattern at .10–.14 alpha over white |
| `--st-blocked-col` | `rgba(185,119,10,.05)` |

> **Light-theme amber is the chief AA hazard.** All warm text (`--warn`, `--st-blocked`,
> `--st-stale`) is darkened to ≥4.5:1 on `--card-bg`/`--bg`; warm chips use a soft tint
> *fill* with the darkened amber *text*, never amber text on a light amber fill without
> verification.

### 3.5 Component / overlay tokens (retain all existing names)
Keep every existing component token verbatim (`--on-accent`, `--tab-active-badge-bg`,
`--chip-accent-bg/-border`, `--pulse-ring`/`-fade`, `--backdrop*`, `--danger-weak`,
`--tag-blocked-*`, `--tag-ready-*`, `--tag-stale-*`, `--tag-label-*`, `--dep-open-border`,
`--legend-bg`, `--scrollbar-thumb-hover`) — `app.css` and a few inline JS styles depend on
them. New additive tokens: `--st-*-soft`, `--st-stale`, `--st-blocked-col`,
`--accent-strong`, `--surface-2`, `--prio-0..3`, plus the elevation/space/type tokens
below.

### 3.6 Typography scale
System UI stack stays (no web fonts — non-goal). Tokenize the scale:

| Token | Value | Use |
|---|---|---|
| `--fs-2xs` | `10px` | priority rank, badges |
| `--fs-xs` | `11px` | ids, times, eyebrows |
| `--fs-sm` | `12px` | chips, meta, column heads |
| `--fs-base` | `13px` | body/controls |
| `--fs-card` | `13.5px` | card title |
| `--fs-md` | `15px` | brand, category name |
| `--fs-lg` | `16px` | empty-state headline |
| `--fs-xl` | `21px` | modal title |
| `--lh-tight` | `1.32` · `--lh-normal` `1.45` · `--lh-relaxed` `1.55` | line heights |
| weights | `400` body · `600` emphasis · `700` strong/numeric | |

Numeric fields use `font-variant-numeric: tabular-nums` (ids, counts, times).

### 3.7 Spacing scale (4px base)

| Token | Value |
|---|---|
| `--sp-1` | `4px` |
| `--sp-2` | `6px` |
| `--sp-3` | `8px` |
| `--sp-4` | `10px` |
| `--sp-5` | `12px` |
| `--sp-6` | `14px` |
| `--sp-7` | `18px` |
| `--sp-8` | `22px` |

Card padding `var(--sp-4) var(--sp-5)`; column gap `var(--sp-6)`; card gap `var(--sp-3)`
(slightly tighter than today's 9px to protect density).

### 3.8 Radius scale

| Token | Value | Use |
|---|---|---|
| `--r-xs` | `5px` | inner swatches, small badges |
| `--r-sm` | `8px` | buttons, inputs |
| `--r-md` | `9px` | cards |
| `--r-lg` | `12px` | columns, category cards |
| `--r-xl` | `14px` | modal sheet |
| `--r-pill` | `999px` | chips, tabs, count pills |

### 3.9 Elevation / shadow

| Token | Dark | Light | Use |
|---|---|---|---|
| `--elev-0` | `none` | `none` | flush surfaces |
| `--elev-1` | `0 1px 2px rgba(0,0,0,.4)` | `0 1px 2px rgba(20,26,40,.06)` | hover card lift |
| `--elev-2` | `0 6px 18px rgba(0,0,0,.45)` | `0 6px 16px rgba(20,26,40,.12)` | popover/toast |
| `--shadow` (kept) | `0 16px 40px rgba(0,0,0,.5)` | `0 14px 36px rgba(20,26,40,.18)` | modal/graph |

### 3.10 Focus-ring system
Keep the global rule and strengthen for visibility:
`:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: var(--r-xs); }`
plus an additive `--focus-ring: 0 0 0 3px var(--accent-weak)` available for elements
where an outline clips (SVG nodes use the existing `.gnode:focus-visible` outline). Never
remove focus styling; `:focus:not(:focus-visible){outline:none}` stays.

### 3.11 Motion / transition rules

| Token | Value | Use |
|---|---|---|
| `--dur-fast` | `120ms` | hover color/border |
| `--dur-base` | `180ms` | drawer width, popover |
| `--dur-flash` | `900ms` | one-shot "updated" highlight |
| `--ease` | `cubic-bezier(.2,.6,.2,1)` | standard |

Allowed motion: hover transitions, drawer slide, status-pulse (existing), and a **single**
`@keyframes card-flash` that fades a faint accent background once on a changed card.
**Forbidden:** per-reconcile reflow/translate animations, looping card pulses, feed-row
enter slides that fight scroll. The existing global
`@media (prefers-reduced-motion: reduce){*{transition:none!important;animation:none!important}}`
**must remain** and disables all of the above.

---

## 4. Component design

- **App shell.** `header` (fixed `--header-h`) / `main` (flex) / `#feed` drawer /
  `#feedResize` / `#feedBackdrop`. Restyle only; structure frozen.
- **Header.** Brand keeps `.brand-accent`. Tabs are pills with active = accent fill +
  `--on-accent` text + light badge. Actions grouped into the three segments (§2.1).
- **Summary metrics.** No new "stat bar" element (would be new DOM the JS doesn't manage
  on reconcile); instead the existing **column count pills** and **overview category
  cards** are the metric surface. Overview numbers enlarged for wallboard read
  (should-have): `.cat-card` counts use `--fs-md`, `--fs-lg` weight 700.
- **Board columns.** `.col` = `--col-bg`, `--r-lg`, `1px --line`. `.colhead` = labeled
  `.swatch` (status color) + name (uppercase, `--fs-sm`, letter-spacing) + right
  `.count` pill. Blocked column body gets `--st-blocked-col` tint. `.col.drag-over`
  keeps accent-weak fill + accent border (frozen interaction).
- **Task cards.** `.card` with `border-left: 3px solid var(--prio,var(--faint))` (kept —
  JS sets `--prio` inline). Three-zone layout (§2.3). Hover = `--card-hover` +
  `--elev-1`. `.dragging` = `opacity:.4`. NEW additive class `.card.is-updated` triggers
  the one-shot flash (added/removed by JS via `classList`, no innerHTML).
- **Status badges.** NEW: a labeled status pill in the card head — `el("span",{class:
  "status-pill st-"+t.status})` containing a dot + the status word (e.g. "● Doing").
  Backed by `--st-*` + `--st-*-soft`. This is the redundant, non-color status cue the
  brief demands. (New class only; JS builds it with `el()`/`textContent`.)
- **Priority indicators.** Left rail (color) PLUS an always-present rank token for **all
  four** levels: `el("span",{class:"chip-prio p"+t.priority}, PRIO_RANK[t.priority])`
  where a new `PRIO_RANK = ["P0","P1","P2","P3"]` (or "Urgent/High/Normal/Low"). The
  existing `.chip-prio` is restyled and now shown for every priority (today it's hidden
  for Normal/Low). Color from `--prio-0..3`.
- **Agent / assignee indicators.** `.who` + `.avatar` (initials, color `--accent` on
  `--accent-weak`), `.who.unassigned` in `--faint` with the word "Unassigned" (text, not
  color-only). Names ellipsis-clamped.
- **Activity-feed items.** `.ev` grid → glyph cell `.ev-icon` (per-kind glyph, e.g.
  claimed ▸, status ⇄, done ✓, blocked ⏸, comment 💬) + `.ev-text` + `.ev-time`. Keep
  `.ev-dot`/`k-*` classes (JS may still set them) but the new glyph carries the
  non-color meaning. `.ref` links stay accent + underline-on-hover.
- **Task-detail modal.** `.sheet` (`--surface`, `--r-xl`, `--shadow`, resizable). `.x`
  close top-right. `.mtitle` borderless textarea → bottom-border on focus. `.mrow`
  field cluster. Section `h3` eyebrows. Dependency chips: `.dep-chip.dep-open` =
  `--dep-open-border`, `.dep-done` = `opacity:.7`, status dot per `--st-*`. Footer
  `.del-task-row` with `.btn-release` (margin-right:auto) and two-step `.btn-danger-task`.
- **Dependency-graph overlay.** `.graph-shell` full-bleed. Restyle `.gnode-rect`
  (`--card-bg`, priority stroke), edges (`--st-done` solid / `--warn` dashed + arrow
  markers), `.gnode-sel` drop-shadow focus, dimming via `.gnode-dim`/`.gedge-dim`
  (`opacity`). Legend (`--legend-bg`) contrast-fixed.
- **Toasts / inline notifications.** NEW additive: a `#toast-region`-style container
  appended by JS (`el()` only) at body level, `role="status" aria-live="polite"`,
  positioned bottom-center, `--elev-2`, auto-dismiss + manual close. Non-blocking
  alternative to `alert()`; adoption optional and incremental.
- **Loading skeletons.** NEW additive `.skeleton` / `.skel-card` / `.skel-line` with a
  CSS `@keyframes skeleton-shimmer` (disabled under reduced-motion → static muted block).
  Rendered by JS into `#board`/`#feedList` before first paint of real data.
- **Empty states.** `.board-empty` (headline `--fs-lg`, sentence `--muted`, CTA `.save`),
  `.empty-col`, `.feed-empty` — all restyled, classes kept.
- **Error states.** Status pill `error: …` (kept). Optional toast for action errors.

---

## 5. Interaction design

- **Hover.** Cards/inputs/buttons transition `background`/`border-color` over
  `--dur-fast`; cards add `--elev-1`. No layout shift on hover.
- **Focus.** `:focus-visible` ring on every interactive element (cards have
  `tabindex=0`, `role=button`). Graph nodes use their existing outline rule.
- **Keyboard nav (ALL preserved).** Global: `/` focus search, `n` new task, `g` toggle
  graph, `a` toggle activity, `Esc` (closes filter panel → else modal). Cards: `Enter`/
  `Space` open, `[`/`]` move status. Modal: `Tab` focus-trap (`trapFocus`), `Esc` close.
  Graph: `Tab` trap (`graphTrapFocus`), `Esc` close, Enter/Space on nodes. Resize handle:
  `ArrowLeft`/`ArrowRight` resize. **None of these handlers may be re-wired or
  reordered** — restyling must not change DOM focus order within `.sheet`/`.col`/`.card`.
- **Modal open/close.** Open: store `lastFocus`, show `#modal`, focus `.sheet`. Close:
  hide, restore `lastFocus`. Backdrop click on `#modal` (target check) closes. All
  existing.
- **Escape / click-outside.** Filter popover: outside-click + `Esc` close (existing
  `closeFilterPanel`). Modal/graph: `Esc` close, backdrop click close. Preserve.
- **SSE live-update indication.** Status pill: `connecting…` → `live` (ok, pulsing dot) →
  `reconnecting…` (warn). A changed card briefly gets `.is-updated` (one-shot flash) so
  live moves are *noticed* without motion spam. Feed prepends newest at top; trim at 200
  unless paginated. **No scroll/focus theft on `renderBoard`** — the redesign must not
  introduce focus-grabbing elements or auto-scroll.
- **Reconnect / offline.** Existing exponential backoff + jitter (`connect`); the
  `warn` pill state communicates degraded liveness. No design change beyond styling the
  pill states for AA in both themes.
- **Reduced motion.** Global reduce rule disables flash, shimmer, pulse, slides.
- **Responsive.**
  - **Desktop (>1024px):** multi-column board centered + capped; feed docked, resizable.
  - **Tablet (721–1024px):** board scrolls horizontally; feed defaults collapsed
    (existing `initFeed` collapses ≤1024 on first visit).
  - **Mobile (≤720px):** board stacks to single column (`flex-direction:column`), each
    column `max-height:78vh`; feed becomes an off-canvas overlay with `#feedBackdrop`;
    `.sheet` goes full-width, `resize:none`. All existing in the `max-width:720px` block;
    restyle only. No horizontal-scroll traps.

---

## 6. Accessibility requirements

- **Contrast ≥ 4.5:1 for text** in **both** themes. `--fg`/`--muted`/`--faint` chosen
  to clear AA on their backgrounds; light `--faint` darkened to `#5f6775`, light amber
  family darkened (≥4.5:1). Verify every status/priority text token on its actual
  surface (card, soft-fill chip, column).
- **Visible focus** via `:focus-visible` on all interactive elements; never removed.
- **ARIA preserved/used appropriately:** `#modal[role=dialog][aria-modal][aria-label]`
  (label updated per task), `#status[role=status][aria-live=polite]`,
  `#feed[aria-label]`, `#graphDetail[aria-live=polite]`, filter panel
  `[role=dialog]`, tabs `[aria-pressed]`, cards `[role=button][tabindex=0][aria-label]`
  with the full "press Enter to open, [ and ] to change status" hint. New status pill
  text is real text (SR-readable); new feed glyphs are decorative
  (`aria-hidden="true"`) since `.ev-text` already states the event in words.
- **Semantic HTML:** `header`/`main`/`aside`/`nav`/`section`/`ul`/`li`/`button` as today;
  skip-link to `#board` retained. New toast container is a `[role=status]` region.
- **SR-friendly modal:** focus moves in on open, trapped while open, restored on close;
  `aria-modal="true"`; descriptive `aria-label`.
- **No color-only status.** Every status/priority/trouble signal carries text + icon +
  position in addition to color (status pill word, `P0–P3` rank, "Blocked/Ready/Stale"
  words, graph detail labels). Graph edges use solid-vs-dashed + arrowheads, not hue
  alone.
- **prefers-reduced-motion: reduce** disables all transitions/animations (global rule
  retained; new flash/shimmer authored to be no-ops under it).

---

## 7. Implementation notes

**Pure CSS (no JS change needed):**
- The entire token system (§3) — rewrite `:root` and `:root[data-theme="light"]` blocks.
- Restyle of every existing class: header, tabs, `.col`/`.colhead`/`.cards`, `.card` and
  its children, `.ev*`, modal `.sheet`/`.mrow`/`.lbl`/`h3`/deps/labels/meta/comments,
  graph, empty states, responsive block, scrollbars, focus ring.
- New keyframes `card-flash`, `skeleton-shimmer` (both reduced-motion-guarded).
- New additive classes' styling: `.status-pill`/`.st-*`, `.is-updated`, `.skeleton`/
  `.skel-*`, `.toast`/toast region, `.ev-icon`, optional `.actions-sep`.

**DOM / selectors that MUST be preserved (no rename, no removal):**
- **IDs:** `board, overview, tabs, breadcrumb, searchBox, labelFilterChip, filterBtn,
  filterPanel, themeToggle, graphBtn, newBtn, status, feedToggle, feed, feedResize,
  feedClose, feedList, feedBackdrop, modal, sheet, graphOverlay, graphTitle,
  graphProjectSel, graphReset, graphClose, graphSvg, graphDetail, graphLegend`.
- **Body classes:** `view-overview, feed-collapsed, resizing`.
- **Data attributes:** `data-theme` (html), `data-status` (col), `data-id` (card/gnode),
  `data-from`/`data-to` (edges).
- **Structural classes app.js queries / emits:** `.card, .col, .col.drag-over, .cards,
  .status-text, .theme-toggle-icon, .filter-count, .filter-wrap, .feed-empty, .mtitle,
  .gnode[data-id], .gedge, .btn-cancel-del, .graph-pan-surface`.
- **Test-locked CSS selectors:** `:root[data-theme="light"]`, `.filter-panel`,
  `.meta-add-row`, `.btn-release`, `.cat-card-add`, `.cat-row`.
- **Test-locked index.html:** inline `localStorage.getItem("am.theme")` FOUC script,
  `id="themeToggle"`, `id="filterBtn"`, `id="filterPanel"`.
- **Test-locked app.js strings (verbatim):** `openNewCategory, newCatCard,
  /api/categories, "category: csel.value", renderManageCategories,
  /api/categories?archived=true, /unarchive, openEditProject, btn-edit-proj,
  vault_project_id, filterReady, filterBlocked, filterStale, filterMetaKey,
  renderFilterPanel, patchMeta, meta-add-row, btn-release`, plus `"use strict"`, all
  `/api/*` calls, `EventSource("/api/stream"...)` + backoff, `data-theme` toggling.

**Classes / data-attributes that MAY be ADDED (built only via `el()`/`svg()` +
`textContent`/`classList`/`setAttribute`):**
`status-pill`, `st-todo|doing|blocked|done`, `is-updated`, `skeleton`, `skel-card`,
`skel-line`, `toast`, `ev-icon`, `actions-sep`, and the priority-rank text. Plus
optional tokens in §3 (additive only).

**Where JS changes ARE needed (small, surgical, still `el()`-only):**
1. `card(t)` — add the status pill (`status-pill st-<status>`), show the priority rank
   chip for **all** priorities (add `PRIO_RANK`), move blocked/ready/stale into a
   dedicated reserved "trouble" sub-row built with `el()`. No new sinks; all text via
   `textContent`/`el()` children.
2. `feedItem(ev)` — prepend an `el("span",{class:"ev-icon","aria-hidden":"true"}, glyph)`
   keyed off `evKind(ev)`; keep `.ev-dot`/`k-*`.
3. *(optional, incremental)* one-shot `.is-updated` flash on reconciled cards; a
   `showToast()` helper appended to body via `el()` to supplement `alert()`.
4. *(optional)* skeleton render on first `loadBoard`/`loadFeed`, replaced on data.

Every JS edit must keep the `el()`/`svg()` convention — **no `.innerHTML`, `.outerHTML`,
`.insertAdjacentHTML`, `document.write`, `eval(`** — so `TestDashboardNoXSSSinks`,
`TestDashboardThemeAssets`, and `TestDashboardParityAffordances` stay green.

---

## 8. Tailwind decision (required)

**Verdict: NO Tailwind. Handcrafted CSS custom properties + modern CSS only.**

- **(a) Can the aesthetic be achieved with handcrafted CSS custom properties + modern
  CSS?** Yes, completely. The design is a token-driven system (color/space/type/radius/
  elevation/motion), dual-theme via a single `[data-theme]` override, `:focus-visible`,
  `color-mix()`, container-free responsive `@media` blocks, and `@keyframes`. Every
  requirement maps to plain CSS the project already uses. Tailwind would add nothing the
  custom-property system can't express.
- **(b) Would Tailwind require a build step?** Yes — the real Tailwind (JIT/CLI/PostCSS)
  needs a Node build to scan classes and emit CSS. That directly violates the explicit
  non-goals: "no build step, no npm, no bundler."
- **(c) Is CDN Tailwind acceptable for this deployment/security model?** No. The CDN
  build (`cdn.tailwindcss.com`) is an external network dependency and ships a runtime
  compiler — it breaks the **offline static-embed** model (assets are `embed`-ed in the
  Go binary and served locally), adds an external origin (security/supply-chain
  surface), and is explicitly disallowed ("no external CDN / web fonts").
- **(d) Maintainability vs. speed?** It would only speed *initial* styling. Long-term it
  *hurts* maintainability here: utility soup in `el()`-built DOM means class strings
  scattered through JS, and a parallel token source of truth competing with the CSS
  custom properties the test contract and theme system already depend on. A documented,
  commented custom-property layer is easier to audit and theme.
- **(e) Conflict with the embedded static-binary goal?** Yes — fundamentally. CDN form
  needs the network; build form needs npm/Node and emits artifacts. Both contradict the
  single-file, no-build, embedded-binary architecture that is a hard requirement.

**Reasoning summary:** Tailwind buys speed we don't need and costs us the two things we
can't give up — the no-build static-embed model and the single CSS-custom-property token
source of truth. Hand-authored tokens win on every axis that matters here.
