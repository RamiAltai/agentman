# Changes draft — dashboard CLI↔GUI parity

Frontend-only feature. Closes seven gaps where the `am` CLI could do something the
web dashboard could not. The HTTP API and store already supported every operation;
this change only touches `cmd/am/web/{index.html,app.js,app.css}` plus a build-level
guard test in `cmd/am/web_test.go`. No Go API/store/CLI code was modified.

## Externally visible changes (one entry per gap)

### Gap 1 — Create a category from the GUI
- The category overview grid (the landing view, `#/`) gains a dashed **＋ New
  category** add-card after the existing category cards. Clicking it (or Enter/Space
  when focused) opens a "New category" modal with a name field and an auto-derived
  slug (same name→slug behavior as New project). Submitting POSTs
  `/api/categories` `{slug,name}`, reloads the overview, and closes the modal.
- Conflict error surfaces as: `a category with slug "<slug>" already exists`.

### Gap 2 — Pick a category when creating a project
- The "New project" modal gains a required **Category** `<select>`. It is populated
  from `GET /api/categories` (fetched lazily if the overview hasn't loaded it yet)
  and defaults to the current view's category when on a category board, otherwise
  `general` (falling back to the first known category if `general` is absent). The
  create POST now includes `category: <selected slug>` in the body.
- If the category list can't be fetched, the select falls back to a single
  `general` option so project creation still works.

### Gap 3 — Archive / unarchive a category from the GUI
- The tab-bar "⋯" button is relabeled **Manage** (title/aria-label updated) and its
  handler renamed `openManageProjects` → `openManage` (a back-compat alias
  `openManageProjects = openManage` is kept). The modal title is now "Manage".
- The Manage modal gains a **Categories** section above the existing **Projects**
  section. Each category row shows name, slug, open-task count, an Archived pill when
  archived, and an Archive/Unarchive toggle that POSTs
  `/api/categories/<slug>/{archive,unarchive}`, then refreshes projects, the overview
  (when visible), and the category list in place.
- Decision: archive/unarchive lives in the Manage modal (not per-card buttons on the
  overview) to keep the overview clean and co-locate all lifecycle management.
- No category **delete** control: there is no category-delete API.

### Gap 4 — Edit a project from the GUI
- Each project row in the Manage modal gains an **Edit** button (before
  Archive/Delete) opening an "Edit project · <slug>" sub-modal with: Name, Slug
  (editable — a safe uid-keyed rename; NOT auto-derived), Vault project id, Vault
  path. Save PATCHes `/api/projects/<slug>` with only the changed fields. A no-op
  edit just closes the modal. On a slug change, if the project was selected the
  selection follows the new slug.
- Errors: conflict → `slug "<slug>" is taken`; validation →
  `check name/slug (no spaces or /)`.

### Gap 5 — Board filters in the GUI
- The header gains a single **Filter** button (`#filterBtn`) with a popover panel
  (`#filterPanel`) instead of a persistent filter row. The panel offers:
  - **Ready** / **Blocked** / **Stale** checkboxes → `?ready=true`, `?blocked=true`,
    `?stale=30m` (the `30m` matches the existing `STALE_MS` stale-badge threshold).
  - **Assignee** text input → `?assignee=<value>`, with a **Mine** fill-button that
    sets it to `human` (the dashboard's fixed actor, the `X-Agent` it sends).
  - **Meta key** text input → `?meta_key=<value>`.
  - **Clear all** resets every filter.
- Decision: status is intentionally NOT a filter — the board columns already are the
  status axis.
- Filters fold into `loadBoard()`'s query string, so they compose with the existing
  project/category scope, search box, and label filter, and survive SSE-driven live
  reloads automatically (no `onEvent`/`renderBoard` changes needed).
- The button shows a count chip and a highlighted state while any filter is active.
  The panel closes on outside click, on Escape (returning focus to the button), and
  stays open while toggling so several filters can be set in a row.

### Gap 6 — Edit task meta from the GUI
- The task modal's **Meta** section is now editable (previously read-only). Each
  existing pair gets a ✕ remove button; an add-row with **key** + **value** inputs
  and an **Add** button creates a pair (Enter in either input also adds). Removing
  sends `PATCH /api/tasks/<id>` `{meta:{<key>:""}}` (empty value deletes the pair);
  adding sends `{meta:{<key>:<value>}}`.
- Decision: meta editing uses the raw `api()` call (not the shared `patch()` helper),
  because `patch()`'s success path would refresh the modal and wipe the inline error
  / in-progress inputs. The SSE `task.patched` echo re-renders the section from
  server truth on its own.
- Validation error surfaces as: `meta keys are 1-50 chars of a-z 0-9 . _ -`.

### Gap 7 — Release a task from the GUI (one click)
- The task modal's delete row gains a **Release** button (shown only when the task
  has an assignee or isn't in `todo`). It PATCHes
  `/api/tasks/<id>` `{assignee:"", status:"todo"}`, returning the task to the
  unclaimed pool. The Delete button is pushed to the right edge of the row.

## Design decisions / rationale
- Filter as a button + popover (not a persistent row): keeps the header compact and
  matches the existing label-filter-chip idiom.
- Mine = `assignee=human`: the dashboard always sends `X-Agent: human`, so "my"
  tasks are the human actor's.
- Category management in an extended Manage modal rather than per-card overview
  buttons: one place for all project/category lifecycle, overview stays a clean grid.
- New-project category picker (no inline new-category): avoids nesting a creation
  flow inside another; categories are created from the overview's add-card.
- Slug rename is safe because projects/categories are uid-keyed in the store.

## Deviations from the implementation map
- None of substance. One clarifying note: in `openEditProject` the `name` field is
  placed only inside the Slug+Name `.mrow` (a DOM node can live in exactly one
  parent), matching the map's stated "Layout: .mrow Slug+Name". The map's prose also
  listed `name .mtitle field (p.name)` as a field definition; it is defined there but
  rendered in the mrow.

## Tests
- `cmd/am/web_test.go`: added `TestDashboardParityAffordances`, mirroring the
  existing `TestDashboardThemeAssets` `[]struct{src,needle,why}` pattern. It reads
  the embedded `web/app.js`, `web/index.html`, and `web/app.css` and asserts the
  presence of every parity affordance's marker (e.g. `openNewCategory`,
  `/api/categories`, `newCatCard`, `category: csel.value`, `renderManageCategories`,
  `/api/categories?archived=true`, `/unarchive`, `openEditProject`, `btn-edit-proj`,
  `vault_project_id`, `id="filterBtn"`, `id="filterPanel"`, `filterReady`,
  `filterBlocked`, `filterStale`, `filterMetaKey`, `renderFilterPanel`, `patchMeta`,
  `meta-add-row`, `btn-release`, and CSS classes `.filter-panel`, `.meta-add-row`,
  `.btn-release`, `.cat-card-add`, `.cat-row`). This locks the affordances in at the
  Go build level (the project has no JS test runner, per the no-npm ethos).
- `TestDashboardNoXSSSinks` still passes: all new DOM is built via `el()` /
  `textContent`; no `innerHTML`-family sinks were introduced.

## Verification performed
Built `go build -o am ./cmd/am` (embeds the web assets) and started a throwaway
server (`AGENTMAN_DB=/tmp/parity_demo.db ./am serve --port 8802`) on a spare port
with a temp DB, then:
- Confirmed the rebuilt binary serves the new code: `curl /app.js` contains
  `openNewCategory`, `renderFilterPanel`, `openEditProject`, `newCatCard`,
  `patchMeta`, `btn-release`, `renderManageCategories`, `category: csel.value`;
  `curl /` contains `filterBtn`; `curl /app.css` contains `.filter-panel`,
  `.meta-add-row`, `.btn-release`, `.cat-card-add`, `.cat-row`.
- Exercised the flows via curl: created a category (`platform`); created a project
  under it (`edge`); renamed the project slug `edge`→`edge-svc` and set
  `vault_project_id` (uid unchanged, confirming a safe rename); added meta
  `env=prod,tier=gold` and deleted `env` via an empty value (detail GET confirmed
  `{tier:gold}` remained); claimed (`bot_1`/`doing`) then released (`""`/`todo`) a
  task; and confirmed the board filters `?ready=true` and `?meta_key=tier` returned
  the expected counts. Archived and unarchived the `platform` category and confirmed
  `?archived=true` lists it.
- Tore down the server and removed the temp DB. Did not touch `:8555` or the user's
  Chrome.

## Gates run (all green)
- `gofmt -l .` → empty
- `go build ./...` → ok
- `go vet ./...` → ok
- `go test -race -count=1 ./...` → ok (`TestDashboardParityAffordances`,
  `TestDashboardThemeAssets`, `TestDashboardNoXSSSinks` all pass)
- `node --check cmd/am/web/app.js` → ok
- `govulncheck ./...` → no vulnerabilities in called code
