# 04 — Adversarial Review, Round 1: agentman Dashboard Redesign

**Author:** Adversarial Reviewer (lifecycle subagent 4)
**Inputs:** `01_PRODUCT_BRIEF.md`, `02_DESIGN_SPEC.md`, `03_ENGINEERING_REPORT.md`, and the
actual edited `cmd/am/web/{index.html,app.css,app.js}` + the `cmd/am/web_test.go` contract.
**Verdict:** **REJECT** (1 blocking accessibility defect). Gate itself passes; the rejection
is on a stated non-negotiable acceptance criterion, not on the build gate.

---

## 1. Gate — independently re-run (NOT trusted from the report)

```
$ go test -count=1 -run TestDashboard -v ./cmd/am/...
=== RUN   TestDashboardNoXSSSinks
--- PASS: TestDashboardNoXSSSinks (0.00s)
=== RUN   TestDashboardThemeAssets
--- PASS: TestDashboardThemeAssets (0.00s)
=== RUN   TestDashboardParityAffordances
--- PASS: TestDashboardParityAffordances (0.00s)
PASS
ok   github.com/RamiAltai/agentman/cmd/am  0.431s

$ node --check cmd/am/web/app.js
node: app.js syntax OK

$ go build ./cmd/am/        → build OK
```

**Gate passed = TRUE** (verified, not taken on faith). The engineer's `gatePassed=true`
claim is accurate for the machine gate.

### XSS-sink grep (the most load-bearing constraint) — independently run
```
$ grep -nE '\.innerHTML|\.outerHTML|\.insertAdjacentHTML|document\.write|eval\(' \
        cmd/am/web/app.js cmd/am/web/index.html
app.js:    NO SINKS
index.html: NO SINKS
```
All dynamic DOM is built via `el()` + `textContent`/`setAttribute`/`classList`. New nodes
(status pill, priority chip, trouble row, feed glyph, toast, skeleton) all follow the
convention. **No regression.**

### Frozen-needle inventory — every test needle present (counts)
- **app.js:** `openNewCategory`(4), `newCatCard`(3), `/api/categories`(6),
  `category: csel.value`(1), `renderManageCategories`(4), `/api/categories?archived=true`(1),
  `/unarchive`(2), `openEditProject`(3), `btn-edit-proj`(1), `vault_project_id`(2),
  `filterReady`(5), `filterBlocked`(5), `filterStale`(5), `filterMetaKey`(6),
  `renderFilterPanel`(3), `patchMeta`(4), `meta-add-row`(1), `btn-release`(1).
- **index.html:** `id="filterBtn"`(1), `id="filterPanel"`(1), `id="themeToggle"`(1),
  `localStorage.getItem("am.theme")`(1).
- **app.css:** `:root[data-theme="light"]`(2 — dark+light), `.filter-panel`(2),
  `.meta-add-row`(1), `.btn-release`(2), `.cat-card-add`(3), `.cat-row`(5).
- **app.js line 1:** `"use strict";` present.

### Backend / deploy compatibility — verified
- REST paths unchanged; board still `GET /api/tasks?…limit=500` with filters folded into
  the query string (`loadBoard`, app.js:192-205). Modal CRUD, deps, labels, meta, comments,
  release, project/category endpoints untouched.
- SSE: `es = new EventSource("/api/stream" + qstr({ since: cursor }))` (app.js:1412);
  `if (es) es.close()` before reconnect (1410) and `es.close()` in `onerror` (1416) — **no
  listener/connection leak across reconnect**. Backoff `Math.min(backoff*2, 10000)` + jitter
  `Math.random()*250` (1426-1427) intact. Dedupe `if (ev.id <= cursor) return` (1433).
  Debounced board reconcile at 250ms (1496). Only status-pill *wording* was added — no
  timing/logic change.
- Theme via `document.documentElement.setAttribute("data-theme", theme)` (app.js:1694);
  inline FOUC script in `<head>` untouched; dark is `:root` default, light is the single
  `[data-theme="light"]` override, absent/unknown theme → dark.
- Pure static: only the three embedded files changed; the sole `<link>`/`<script>` are the
  local `app.css`/`app.js`. **No CDN, web font, npm, build step, or bundler.** No backend
  change required.

---

## 2. UX / design-spec conformance — verified against the actual files

- **Card three-zone layout** (app.js:339-400) matches §2.3 exactly: ROW1 `#id` +
  `chip-prio pN` (all four levels) + labeled `status-pill st-<status>`; ROW2 clamped title
  (3 lines, app.css:414-417); ROW3 `.cfoot` (assignee · project) → reserved `.ctrouble`
  (Blocked/Ready/Stale, **before** labels) → `.ctags` (labels · 💬). `.ctrouble:empty {
  display:none }` (app.css:434) collapses the slot cleanly — no empty gap, no reshuffle.
- **Status is NOT color-only:** column position (layout) + `.colhead` swatch + the
  `status-pill` *word* ("Doing"/"Blocked"/…) as real `textContent` (SR-readable). The pill
  `::before` dot is `aria-hidden` decoration via CSS content. **Good.**
- **Priority readable at all four levels:** `PRIO_RANK = ["P0".."P3"]` shown for every
  priority (was previously hidden for Normal/Low), + left rail color. **Good.**
- **Feed glyphs** carry per-kind meaning via `EV_GLYPH` (app.js:18, 1500-1508), decorative
  (`aria-hidden="true"`) over the word-bearing `.ev-text`. `.ev-icon` colored per kind.
- **Trouble channel** (blocked/stale/ready) reserved before labels — the brief's #1 fix.
- **Live update** = single one-shot `.is-updated` flash (`@keyframes card-flash`,
  app.css:377-381), JS-guarded by `prefersReducedMotion()` (app.js:311-313) **and** the
  global reduce rule (app.css:998-1001). `{ once: true }` listener — no leak. `renderBoard`
  uses `replaceChildren()` (app.js:261) like before — no new scroll/focus theft.
- **Modal** focus trap (`trapFocus`, app.js:738-746), focus store/restore (`lastFocus`,
  727-735), Escape + backdrop close — all preserved, DOM build order unchanged.
- **Semantic landmarks / ARIA** (index.html): skip-link, `header`/`main`/`section`/`aside`/
  `nav`, `role=dialog aria-modal` on modal+graph, `role=status aria-live=polite` on the
  status pill, `aria-live=polite` graph detail, `role=group` actions segments. Strong.
- **Motion discipline** (app.css:325/377/965): only the existing status pulse, one-shot
  flash, and loading shimmer — no per-reconcile reflow/translate. All reduce-safe.
- **Graph:** conservatively restyled through retained tokens; layout/pan-zoom/selection
  math untouched per the report (consistent with no diff to those functions).

This is a faithful, disciplined implementation of the spec. The one place it does **not**
deliver on a stated acceptance criterion is contrast — below.

---

## 3. BLOCKING ISSUE

### B1 — New status-pill / priority-rank / blocked-colhead text fails WCAG 2.1 AA (4.5:1) for normal text, in BOTH themes

The brief (§7 "Accessibility": *"WCAG 2.1 AA contrast for text and meaningful UI in **both**
themes"*) and the design spec (§3.4: *"All warm text … darkened to ≥4.5:1 on
`--card-bg`/`--bg`"*; §6: *"Verify every status/priority text token on its actual surface
(card, soft-fill chip, column)"*) make AA-in-both-themes a non-negotiable acceptance
criterion. The engineering report §2 and the token comments claim it holds. **It does not.**
These are all **newly introduced** colored-text elements at 10–12px, weight 600–700 —
**normal text** by WCAG (large = ≥18.66px bold / ≥24px), so the 4.5:1 threshold applies (not
3:1). Computed contrast (sRGB WCAG formula; soft fills composited over their actual surface):

**LIGHT theme (`--card-bg`/`--col-bg`):**
| Element | Color on surface | Ratio | AA(4.5) |
|---|---|---|---|
| `status-pill.st-done` text on `--st-done-soft`/card | `#0f9d75` | **3.00** | FAIL |
| `status-pill.st-blocked` on `--st-blocked-soft`/card | `#b9770a` | **3.22** | FAIL |
| `status-pill.st-doing` on `--st-doing-soft`/card | `#2f6df0` | **4.03** | FAIL |
| `status-pill.st-todo` on `--st-todo-soft`/card | `#6b7280` | **4.16** | FAIL |
| `chip-prio.p3` (Low) text on card | `#9aa3b2` | **2.54** | FAIL |
| `chip-prio.p1` (High) text on card | `#b9770a` | **3.68** | FAIL |
| `chip-prio.p0` (Urgent) text on card | `#d94436` | **4.35** | FAIL (marginal) |
| `.colhead` "BLOCKED" on `--col-bg` | `#b9770a` | **3.23** | FAIL |
| `tag-stale` on `--tag-stale-bg`/card | `#b25a12` | 4.10 | FAIL (marginal) |

**DARK theme (`--card-bg #1b1f28`):**
| Element | Color | Ratio | AA(4.5) |
|---|---|---|---|
| `status-pill.st-todo` on todo-soft/card | `#8b93a4` | **4.17** | FAIL |
| `status-pill.st-doing` on doing-soft/card | `#5b8cff` | **4.15** | FAIL |
| `chip-prio.p3` (Low) text on card | `#6e7681` | **3.59** | FAIL |

(Dark `st-blocked`/`st-done` pills, all dark trouble tags, dark blocked-colhead, and feed
glyphs DO pass — 5.2–10.1:1. The failures cluster on the two *new* card chips and the light
theme generally.)

- **Evidence (CSS):** `app.css:391-411` (chip-prio + status-pill color/size), `app.css:361`
  (blocked colhead `color: var(--st-blocked)`), token values `app.css:35-51` (dark),
  `app.css:156-170` (light). Sizes: `--fs-2xs:10px` / `--fs-xs:11px` (`app.css:54-55`),
  colhead `--fs-sm:12px`/600 (`app.css:350`). Verified by re-deriving each ratio with the
  WCAG relative-luminance formula over the composited backgrounds.
- **Why it blocks:** "AA in both themes" and "verify every status/priority text token on its
  surface" are explicit acceptance criteria the report asserts are met. Redundancy of the
  signal (column/rail/word) does **not** exempt visible text from 1.4.3; and the worst case
  (`chip-prio.p3` at 2.54:1, `status-pill.st-done` 3.00:1) is genuinely hard to read, not
  just a spec-letter miss.
- **Required fix:** Darken (light) / lighten (dark) the offending text tokens until each
  clears 4.5:1 on its **actual composited surface**, OR raise the soft-fill opacity / give the
  pills a solid readable fill, OR for the pill specifically rely on a non-text-color treatment
  (e.g. keep the colored dot + use `--fg`/`--muted` for the pill word so the word always
  clears AA while the dot/border carries hue). Concretely: light `--st-done` ≈ `#0a7d5c`,
  light `--st-blocked`/`--warn` ≈ `#8a5a08`, light `--st-doing` ≈ `#1d54c8`, light
  `--st-todo` ≈ `#565d6b` (target ≥4.5 on soft fill), light `--prio-3` ≈ `#6b7280`,
  light `--prio-1`/`--prio-0` darkened to ≥4.5 on white, light blocked-colhead use a darker
  amber or `--muted`; dark `--prio-3` ≈ `#9aa3b2`, dark `--st-todo`/`--st-doing` pill text
  bumped (or pill word switched to `--fg`). Re-measure every status/priority text token on
  card, soft-fill chip, and column in BOTH themes.
- **Owner:** UI/UX Designer (token values) with Frontend Engineer (apply + re-measure).
- **Regression tests to rerun:** `go test -count=1 ./cmd/am/...` (must stay green —
  contrast fix is CSS-token-only, touches no selector/needle); `node --check cmd/am/web/app.js`;
  then re-derive the contrast table above for all status/priority/colhead text tokens in
  both themes and confirm every value ≥4.5:1; manual spot-check the light theme on a white
  card (the spec's named #1 hazard).

---

## 4. Low-risk issues (do not block, should be tracked)

- **L1 — `.ev-dot` CSS is dead** (`app.css:533-537`): retained but no longer emitted by JS
  (`feedItem` builds `.ev-icon` instead, app.js:1500-1508). The report calls this
  intentional back-compat, but the parity test does **not** check `.ev-dot`, so it is simply
  unused CSS. Harmless; consider removing or annotating to avoid a future "why is this here."
- **L2 — Skeleton/toast helpers are present but not load-bearing** (`boardSkeleton`/
  `feedSkeleton`/`showToast`, app.js:84-176). They exist, are reduced-motion-safe, and are
  replaced on data via `replaceChildren()`, but `alert()` is still the live error path. Fine
  per the spec's "incremental" framing; just confirm the skeletons actually mount on first
  paint (only wired on initial load) and don't flash on every reconnect.
- **L3 — `chip-prio.p0` (4.35 light) and `tag-stale` (4.10 light) are marginal** — fold into
  B1's re-measure; they are close enough that a small token nudge fixes them.
- **L4 — Tablet (721–1024px) horizontal-scroll behavior** relies on JS `initFeed` collapsing
  the drawer; only one `@media (max-width:720px)` block exists in CSS. Matches the spec's
  description but worth a manual check that the board doesn't create a horizontal-scroll trap
  at ~900px width with the feed open.

## 5. Future improvements (out of scope for this round)

- Migrate the live action-error path from `alert()` to the new `showToast()` (non-blocking).
- Add an automated contrast check (even a tiny Go test that parses the token hex values and
  asserts ratios) so AA can't silently regress again — the project's no-JS-runner ethos makes
  a Go-level luminance assertion a natural fit alongside the existing source-level guardrails.
- Consider a "new since you looked" affordance (brief Could-have) once B1 is resolved.

---

## 6. Verdict

**REJECT.** The machine gate is genuinely green and the implementation is otherwise a
faithful, well-disciplined realization of the spec (no XSS sinks, all frozen selectors/needles
intact, backend/SSE/theme/static-deploy contract preserved, no listener leaks, restrained
reduced-motion-safe motion, strong semantics/ARIA, correct focus trap). But **B1** is a
violation of an explicit, repeated, non-negotiable acceptance criterion — *WCAG 2.1 AA text
contrast in both themes* — on elements the redesign itself introduced, and the engineering
report claims compliance that independent measurement disproves. That gap must be closed
before this ships. Fix is confined to CSS token values (no selector/needle/DOM churn), so the
gate should stay green through the correction.
