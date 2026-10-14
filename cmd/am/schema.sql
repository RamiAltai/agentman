-- AgentMan schema: the single, current schema (no migrations — there is no
-- older data to upgrade). Pragmas are set on the DSN (see store.go), not here,
-- so journal_mode=WAL is applied at connection open (never inside a tx).
-- projects, tasks and events use AUTOINCREMENT so a deleted row's id is never
-- handed out again (ids are wire refs, scope-check keys and the SSE cursor).

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- categories: the layer above projects (instance → category → project → task).
-- OpenStore seeds the default 'general' category.
CREATE TABLE IF NOT EXISTS categories (
  id          INTEGER PRIMARY KEY,
  uid         TEXT NOT NULL UNIQUE,   -- stable id, amc_<16 hex>, never changes
  slug        TEXT NOT NULL UNIQUE,   -- lowercase handle
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);

-- projects: named boards that group tasks.
CREATE TABLE IF NOT EXISTS projects (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  slug             TEXT NOT NULL UNIQUE,   -- short handle agents pass (token-cheap)
  name             TEXT NOT NULL,
  category_id      INTEGER NOT NULL REFERENCES categories(id),
  uid              TEXT NOT NULL UNIQUE,   -- stable id, amp_<16 hex>, survives slug renames
  vault_project_id TEXT,                   -- optional external vault binding
  vault_path       TEXT,
  created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_projects_category ON projects(category_id);

-- tasks: the tickets.
CREATE TABLE IF NOT EXISTS tasks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT, -- global id, the cheap wire ref ("#42")
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ref        INTEGER NOT NULL,            -- per-project number for humans ("web-3")
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'todo'
             CHECK (status IN ('todo','doing','blocked','done')),
  assignee   TEXT,                        -- agent id; NULL = unclaimed
  priority   INTEGER NOT NULL DEFAULT 2,  -- 0=urgent..3=low
  created_by TEXT,                        -- creating actor (proposals carve-out: own-task comments)
  claimed_at TEXT,                        -- set on (re)assign, cleared on unassign
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (project_id, ref)
);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
-- Partial: every assignee= and stale filter implies assignee IS NOT NULL; a full
-- index lured the planner into using it for NextTask's assignee IS NULL.
CREATE INDEX IF NOT EXISTS idx_tasks_assignee ON tasks(assignee) WHERE assignee IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_updated ON tasks(updated_at);
-- NextTask's candidate predicate, so `am next` reads the first eligible row in
-- (priority, id) order instead of sorting the backlog.
CREATE INDEX IF NOT EXISTS idx_tasks_next ON tasks(priority, id) WHERE status='todo' AND assignee IS NULL;

-- task_deps: prerequisite edges between tasks (many-to-many, same-project only).
-- Both FKs cascade on delete so removing a task cleans up its edges automatically.
CREATE TABLE IF NOT EXISTS task_deps (
  task_id       INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,  -- dependent
  depends_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,  -- prerequisite
  PRIMARY KEY (task_id, depends_on_id)
);
CREATE INDEX IF NOT EXISTS idx_task_deps_prereq ON task_deps(depends_on_id);

-- task_labels: free-form tags on tasks (many-to-many, label stored inline —
-- no separate labels catalog; a label exists iff some task carries it).
CREATE TABLE IF NOT EXISTS task_labels (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  label   TEXT NOT NULL,
  PRIMARY KEY (task_id, label)
);
CREATE INDEX IF NOT EXISTS idx_task_labels_label ON task_labels(label);

-- task_meta: key→value pairs on tasks (values are opaque; key PRESENCE is the
-- filterable unit). Like task_labels there is no separate catalog — a key
-- exists iff some task carries it.
CREATE TABLE IF NOT EXISTS task_meta (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL,
  PRIMARY KEY (task_id, key)
);
CREATE INDEX IF NOT EXISTS idx_task_meta_key ON task_meta(key);

-- tokens: scope-bound bearer credentials. A token confines an agent to one
-- scope server-side: the server derives the scope from the presented token and
-- ignores the client-asserted X-Agent-Scope header. Only the sha256 HASH of the
-- plaintext is stored — the plaintext (amt_<32 hex>) is shown once at mint and
-- never persisted, so a stolen DB row cannot be replayed as a credential.
CREATE TABLE IF NOT EXISTS tokens (
  id          TEXT PRIMARY KEY,          -- tk_<16 hex>, shown in ls/revoke
  token_hash  TEXT NOT NULL UNIQUE,      -- sha256(plaintext) hex; plaintext never stored
  category    TEXT NOT NULL,             -- bound scope category slug
  project     TEXT,                      -- bound scope project slug (NULL = category-wide)
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at  TEXT                       -- NULL = active
);

-- comments: discussion thread on a task.
CREATE TABLE IF NOT EXISTS comments (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_comments_task ON comments(task_id, id);

-- events: append-only log = activity feed + SSE backbone + monotonic cursor.
CREATE TABLE IF NOT EXISTS events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT, -- ?since= cursor and SSE Last-Event-ID; never reused
  project_id INTEGER,
  task_id    INTEGER,
  actor      TEXT NOT NULL,
  kind       TEXT NOT NULL,               -- task.created|claimed|reclaimed|status|assign|patched|deleted|dep_added|dep_removed|labeled|unlabeled|comment.added|comment.deleted|project.created|archived|unarchived|patched|deleted|category.created|archived|unarchived
  data       TEXT NOT NULL DEFAULT '{}',  -- compact JSON delta, e.g. {"status":["todo","doing"]}
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_events_task    ON events(task_id, id);     -- GetTask's history
CREATE INDEX IF NOT EXISTS idx_events_project ON events(project_id, id);  -- project-filtered replay
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);      -- category stats' activity window

INSERT OR IGNORE INTO meta(key, value) VALUES ('schema_version', '1');
