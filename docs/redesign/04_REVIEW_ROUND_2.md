# 04 — Adversarial Review, Round 2: agentman Dashboard Redesign

**Reviewer:** Adversarial Reviewer (lifecycle subagent 4), review round 2.
**Posture:** Assume-flawed-until-proven. The engineer's `gatePassed=true` was NOT
trusted; every claim below was independently re-verified against the actual edited
files and a fresh, uncached gate run.

**Verdict: PASS.** No non-negotiable constraint is violated. The round-1 blocking
issue (WCAG AA contrast on board status pills / priority rank chips / Blocked
colhead) is genuinely fixed in CSS-token values only, with no selector/needle/JS
change. Remaining items are low-risk and explicitly scoped-out by the design docs.

---

## 1. Gate — independently re-run (not trusted from the report)

```
$ go test -count=1 ./cmd/am/...
ok   github.com/RamiAltai/agentman/cmd/am   7.272s   (uncached)
   - PASS TestDashboardNoXSSSinks
   - PASS TestDashboardThemeAssets
   - PASS TestDashboardParityAffordances
$ node --check cmd/am/web/app.js
node: app.js syntax OK
$ go build ./cmd/am/    → build OK
$ go vet ./cmd/am/...   → clean
```

**gatePassed (reviewer-verified): true.**

## 2. XSS-sink guardrail — re-grepped both files

`grep -nE '\.innerHTML|\.outerHTML|\.insertAdjacentHTML|document\.write|eval\('`
over `app.js` and `index.html` → **no hits.** All dynamic DOM is built via `el()` /
`svg()` + `textContent` / `setAttribute` / `classList`. `TestDashboardNoXSSSinks`
enforces this at the Go build level. PASS.

## 3. Every test-locked needle confirmed present (re-checked by hand)

- **index.html:** `localStorage.getItem("am.theme")`, `id="themeToggle"`,
  `id="filterBtn"`, `id="filterPanel"` — all present.
- **app.js (20 needles):** `openNewCategory`, `newCatCard`, `/api/categories`,
  `category: csel.value`, `renderManageCategories`, `/api/categories?archived=true`,
  `/unarchive`, `openEditProject`, `btn-edit-proj`, `vault_project_id`,
  `filterReady`, `filterBlocked`, `filterStale`, `filterMetaKey`,
  `renderFilterPanel`, `patchMeta`, `meta-add-row`, `btn-release`, `"use strict"`,
  `EventSource("/api/stream"` — all present.
- **app.css (6 selectors):** `:root[data-theme="light"]`, `.filter-panel`,
  `.meta-add-row`, `.btn-release`, `.cat-card-add`, `.cat-row` — all present.
- **web_test.go:** `git status` shows it is UNCHANGED (contract left as found). PASS.

## 4. Backend / deploy compatibility — verified, not assumed

- `git status --short`: only `cmd/am/web/{app.css,app.js,index.html}` modified; **no
  `.go` file changed.**
- REST/SSE call targets: `diff` of the sorted unique `api(...)`/`EventSource(...)`
  targets between `HEAD:app.js` and the working copy → **IDENTICAL.** No path,
  method, or query shape changed.
- SSE reconnect core preserved verbatim: `backoff=1000` init, reset to `1000` on
  `onopen`, `setTimeout(connect, backoff + Math.random()*250)`, `backoff =
  Math.min(backoff*2, 10000)`. The only addition is status-text wording
  (`reconnecting…` vs a reserved-channel `offline — retrying…`); timing/cadence
  unchanged.
- Theme via `data-theme`: `applyTheme` still calls
  `documentElement.setAttribute("data-theme", theme)`; FOUC-guard `<head>` script
  intact; dark is the `:root` default, light the single `[data-theme="light"]`
  override.
- Static deploy: no `<link>`/`<script>` to any external origin; only the SVG
  namespace literal appears (not a network fetch). No npm/build/bundler/CDN/font.
  Assets stay `//go:embed web`.

## 5. UX quality vs. the design spec

- **Card hierarchy / scannability:** Fixed three-zone card (`.crow` head → `.ctitle`
  → `.cfoot` / reserved `.ctrouble` / `.ctags`). `.ctrouble:empty{display:none}` and
  `.ctags:empty{display:none}` give the promised non-reshuffling rhythm; trouble
  (Blocked/Ready/Stale) is rendered *before* labels.
- **Status NOT color-only:** status pill carries the status **word**
  (`ST_LABEL[t.status]`) + a dot + soft fill + column position. Priority rank chip
  carries `PRIO_RANK[pr]` text (`P0–P3`) for **all four** levels (the old code showed
  nothing for Normal/Low) plus a `title="Priority: <word>"`. Trouble carries words
  "🔒 Blocked N / ✓ Ready / ⏳ Stale".
- **Board columns:** Blocked column gets a reserved warm tint
  (`.col[data-status="blocked"]`) + amber colhead, so "anything blocked" is readable
  from chrome alone.
- **Feed:** 3-col grid with a per-kind glyph (`EV_GLYPH`, `aria-hidden`), kept `k-*`
  classes color the glyph; `.ev-text` states the event in words → not color-only.
- **Modal:** clearer section eyebrows (`.sheet h3`), `.btn-release` left /
  `.del-task-row` two-step delete; build order unchanged.
- **Graph:** conservative restyle; layout math/pan-zoom/selection-restore untouched;
  non-color cues (dashed-vs-solid edges + arrowheads, 🔒/✓ + detail labels) present.
- **Empty / loading / error states:** `.board-empty`, `.empty-col`, `.feed-empty`
  kept; additive `boardSkeleton()`/`feedSkeleton()` on first paint (replaced via
  `replaceChildren()`); status pill `offline`/`error` + an additive non-blocking
  `.toast` region.
- **Live updates noticeable-not-disruptive:** one-shot `.is-updated` card flash
  (`@keyframes card-flash`, 900ms, single), suppressed under reduced motion; feed
  prepends without scroll theft; debounced 250ms reconcile preserved.

## 6. Accessibility — verified

- **Keyboard parity preserved:** global `/ n g a Esc`, card `Enter/Space` + `[`/`]`,
  modal `trapFocus` + Esc + `lastFocus` restore, graph `graphTrapFocus` + Esc +
  `graphLastFocus` restore, resize-handle Arrow keys — all handlers present and
  unmodified.
- **Escape ordering is correct:** the `#graphOverlay` keydown listener calls
  `ev.stopPropagation()` on Escape before it bubbles to the document `onKey`, so the
  graph closes without double-handling; `trapFocus` early-returns when the modal is
  hidden, so an open graph + hidden modal never conflict.
- **Focus rings:** global `:focus-visible{outline:2px solid var(--accent)}` retained;
  `:focus:not(:focus-visible){outline:none}` retained; SVG nodes use
  `.gnode:focus-visible`.
- **Reduced motion:** global `@media (prefers-reduced-motion: reduce){*{transition:
  none!important;animation:none!important}}` retained; the JS flash also checks
  `prefersReducedMotion()` and never schedules the class toggle; skeleton degrades to
  a static block.
- **Semantics/ARIA:** `header`/`main`/`aside`/`nav`/`section`/`ul`/`button`,
  skip-link to `#board`, `#modal[role=dialog][aria-modal]`,
  `#status[role=status][aria-live=polite]`, filter panel `[role=dialog]`, cards
  `[role=button][tabindex=0][aria-label]`, toast region `[role=status]
  [aria-live=polite]`, decorative glyphs `aria-hidden`.
- **Contrast (round-1 blocker) — fixed at token values only:** light warm/colored
  text darkened (`--warn #8a5a08`, `--ok #0a7350`, `--danger #c43528`, `--st-doing
  #1d54c8`, `--st-todo #565d6b`, `--st-stale/--tag-stale-fg #8f470d`, `--prio-0
  #c43528 / -1 #8a5a08 / -2 #6b7280 / -3 #666d7c`); dark gray/blue pills + Low chip
  lightened (`--st-todo #9aa1b3`, `--st-doing #7aa2ff`, `--prio-3 #8a92a1`). The
  chip's text color resolves from the `.chip-prio.pN{--prio:var(--prio-N)}` rule set
  *on the chip*, so it wins over the card's inline `--prio` rail value — confirming
  no JS change was needed. Board pills/chips/colhead now clear 4.5:1 in both themes.

## 7. Code quality / performance

- **Maintainable CSS:** single token source of truth (color/type/space/radius/
  elevation/motion), dark `:root` default + one light override block, well
  commented. Every JS-referenced inline `var(--token)` (`--accent --faint
  --st-blocked --st-doing --st-done --st-todo --violet --warn`) is defined in
  app.css (verified by script).
- **Understandable JS:** changes are surgical/additive (diffstat: app.js +154/-… ,
  not a rewrite); `el()`/`svg()` convention intact; new consts
  (`PRIO_RANK/PRIO_WORD/EV_GLYPH/ST_LABEL`) are module-scoped, no global pollution.
- **No leaked listeners across reconnect/refresh:** flash listener uses
  `{once:true}` and is gated by reduced-motion; graph pan/zoom + svg click-deselect
  are guarded by `graphPanZoomInstalled` (installed once per overlay open); per-node
  /edge listeners live on fresh SVG nodes recreated each render; filter outside-click
  is wired once via `filterOutsideClickWired`; feed-resize pointer listeners are
  removed on pointerup.
- **SSE perf:** board reconcile stays debounced (250ms) and rebuilds via
  `replaceChildren()` as before; graph refresh debounced (350ms) with selection
  restore; no per-event full reflow churn beyond today's behavior.

---

## 8. Low-risk issues (non-blocking)

1. **Graph-detail status chip background is dead CSS — PRE-EXISTING, not introduced.**
   `app.js:2306` builds `style:"background:" + statusColor + "22; …"` where
   `statusColor` is `var(--st-doing)` etc. Appending `22` to a `var()` reference
   (`"var(--st-doing)22"`) is invalid CSS, so the soft-fill background silently does
   not render (the text color still applies correctly). Byte-identical in
   `HEAD:app.js` — this is legacy, untouched by the redesign, and outside the spec's
   scope. Cosmetic only; the chip text remains legible on `--surface`.
2. **Graph node priority stroke + graph-detail priority chip use the dark `PRIO[]`
   hexes in light theme — PRE-EXISTING and explicitly scoped out** (design spec §2.6
   "graph restyle is conservative", §8 "the inline `PRIO[]` array … is unchanged").
   The node stroke is a 2px decorative border (AA-for-text N/A). The detail chip's
   `P2`/`P3` text (e.g. `#8b93a4`/`#6e7681` on white ≈ 3.0–4.0:1) is below AA, but it
   is a secondary inspection-panel chip, not a board signal, and the brief lists graph
   color encoding as a "could-have". Not a regression.
3. **Design spec file artifact (not an implementation file):**
   `docs/redesign/02_DESIGN_SPEC.md` ends with stray `</content>`/`</invoke>` lines
   (516–517). Doc hygiene only; zero effect on shipped assets or the gate.
4. **`boardSkeleton()` builds a `.colhead` with a trailing empty-string child** —
   harmless (becomes an empty text node) and replaced by the real render on data
   arrival.

## 9. Future improvements

- Migrate the graph-detail priority/status chips to the themed `--prio-N` / `--st-*`
  CSS tokens (or `color-mix`) so light-theme graph inspection also clears AA and the
  status-chip soft fill renders — closing the one remaining (pre-existing) color path
  the board redesign deliberately left untouched.
- Optionally finish migrating action-failure `alert()` calls to the new `showToast()`
  region (the helper is wired but `alert()` is intentionally still used).
- Could-have from the brief: sticky column headers / per-column collapse on long
  lists; saved/quick filters.

## 10. Live-runtime check — attempted, blocked by sandbox (disclosure)

A real browser smoke test was attempted (built `/tmp/am_review`, launched
`am serve` via the preview harness, tried to seed data through the page's
same-origin `fetch`). The sandbox prevented the binary from binding a port (preview
landed on `chrome-error://chromewebdata/`), so DOM-level runtime verification was not
possible here. This is consistent with the project's deliberate no-JS-test-runner
design; verification therefore rests on the Go source-level guardrails (all green,
uncached), `node --check`, `go build`/`go vet`, full diff/needle/token analysis, and
listener-leak tracing above. The launch.json/binary artifacts created for the attempt
were cleaned up.
