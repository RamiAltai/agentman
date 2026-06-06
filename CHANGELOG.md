# Changelog

All notable changes to **agentman** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

When cutting a release, rename the `[Unreleased]` heading to the version + date and start a
fresh `[Unreleased]` section.

## [Unreleased]

_Target: **v0.4.0** — not yet tagged._

### Added

- **DB export / import** — `am db export [path] [--db PATH]` writes a consistent `VACUUM INTO`
  snapshot (chmod `0o600`, prints the path); `am db import <path> [--db PATH] [--yes]` validates
  the candidate (integrity + foreign-key checks, required tables, schema version), **refuses to
  run while a server is live**, backs up the current DB, then atomically swaps it in. CLI-only —
  there is no HTTP route; it operates directly on the SQLite file. (`cmd/am/db.go`)
- **Project archive / hide** — `am project archive <slug>` / `am project unarchive <slug>`, plus
  `am projects --all` and `GET /api/projects?archived=true` / `POST /api/projects/{slug}/archive`
  and `…/unarchive`. Backed by the first real schema migration (**v2**, adding
  `projects.archived_at`), which exercises the Phase-0 forward-only migration runner end-to-end.
- **Multi-select project filter** on the dashboard — click several project tabs to view their
  boards together; the **All** tab clears the selection.

### Fixed

- **Archived projects' tasks were still shown on the board.** Archiving hid a project's tab and
  column header (`ListProjects` filters archived) but `ListTasks` had no archived filter, so the
  tickets kept rendering in the board's "All" view and in `am ls`. `ListTasks` now excludes tasks
  belonging to archived projects when **no explicit project is requested**; an explicit
  `?project=<slug>` / `am ls -p <slug>` still returns them for direct inspection. Regression test:
  `TestListTasksHidesArchivedProjectTasks`. (`cmd/am/store.go`, `cmd/am/store_test.go`)
- **The board clung to the left edge on wide / ultrawide screens.** The status columns cap at
  `max-width: 480px`, so beyond ~1990px of width the leftover space piled up on the right. The
  board now centers with `justify-content: safe center`; the `safe` keyword falls back to
  `flex-start` when columns overflow, so horizontal scrolling on narrow screens never clips the
  first column. The mobile (≤720px) vertical stack stays top-aligned. (`cmd/am/web/app.css`)
- **Review hardening for DB export/import** (caught during the Phase 1 tester pass): `exportDB`
  now fails fast on a missing source DB instead of silently writing an empty snapshot;
  `validateImportCandidate` checks `rows.Err()` after iterating; `copyFile` propagates the file
  close error via a named return rather than swallowing it on a double `Close`. (`cmd/am/db.go`)

### Changed

- **Documentation brought current** with the shipped features across `README.md`,
  `docs/agent-integration.md`, and the `architecture/` set — new commands, routes, event kinds
  (`project.archived` / `project.unarchived`), schema v2, and the now-exercised migration runner.

## [0.3.0] and earlier

Predate this changelog — see the git history (`v0.1.0` – `v0.3.0`). Highlights: the single-binary
CLI + HTTP/SSE server + embedded dashboard, atomic claim, per-directory agent identity,
`am update` + startup version check, the Phase-0 migration-runner foundation, and the localhost
HTTP guardrails (Host allowlist + write-CSRF guard + CSP).
