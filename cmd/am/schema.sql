-- AgentMan schema. Pragmas are set on the DSN (see store.go), not here,
-- so journal_mode=WAL is applied at connection open (never inside a tx).

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- projects: named boards that group tasks.
-- NOTE: this CREATE TABLE is the frozen v1 baseline — category_id, uid, and the
-- vault binding columns are added by migration v4 (see store.go), never here.
CREATE TABLE IF NOT EXISTS projects (
  id         INTEGER PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,        -- short handle agents pass (token-cheap)
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- categories: the layer above projects (instance → category → project → task).
-- projects.category_id is added by migration v4, not here — the projects CREATE
-- TABLE above is the frozen v1 baseline that old snapshots are validated against.
CREATE TABLE IF NOT EXISTS categories (
  id          INTEGER PRIMARY KEY,
  uid         TEXT NOT NULL UNIQUE,   -- stable id, amc_<16 hex>, never changes
  slug        TEXT NOT NULL UNIQUE,   -- lowercase handle
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);

-- tasks: the tickets.
CREATE TABLE IF NOT EXISTS tasks (
  id         INTEGER PRIMARY KEY,         -- global id, the cheap wire ref ("#42")
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ref        INTEGER NOT NULL,            -- per-project number for humans ("web-3")
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'todo'
             CHECK (status IN ('todo','doing','blocked','done')),
  assignee   TEXT,                        -- agent id; NULL = unclaimed
  priority   INTEGER NOT NULL DEFAULT 2,  -- 0=urgent..3=low
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (project_id, ref)
);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_assignee       ON tasks(assignee);
CREATE INDEX IF NOT EXISTS idx_tasks_updated        ON tasks(updated_at);

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
  id         INTEGER PRIMARY KEY,         -- doubles as ?since= cursor and SSE Last-Event-ID
  project_id INTEGER,
  task_id    INTEGER,
  actor      TEXT NOT NULL,
  kind       TEXT NOT NULL,               -- task.created|claimed|reclaimed|status|assign|patched|deleted|dep_added|dep_removed|labeled|unlabeled|comment.added|comment.deleted|project.created|archived|unarchived|patched|deleted|category.created|archived|unarchived
  data       TEXT NOT NULL DEFAULT '{}',  -- compact JSON delta, e.g. {"status":["todo","doing"]}
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_events_since ON events(id);

INSERT OR IGNORE INTO meta(key, value) VALUES ('schema_version', '1');
