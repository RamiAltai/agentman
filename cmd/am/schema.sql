-- AgentMan schema. Pragmas are set on the DSN (see store.go), not here,
-- so journal_mode=WAL is applied at connection open (never inside a tx).

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- projects: named boards that group tasks.
CREATE TABLE IF NOT EXISTS projects (
  id         INTEGER PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,        -- short handle agents pass (token-cheap)
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
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
  kind       TEXT NOT NULL,               -- task.created|claimed|reclaimed|status|assign|patched|deleted|dep_added|dep_removed|comment.added|comment.deleted|project.created|archived|unarchived|deleted
  data       TEXT NOT NULL DEFAULT '{}',  -- compact JSON delta, e.g. {"status":["todo","doing"]}
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_events_since ON events(id);

INSERT OR IGNORE INTO meta(key, value) VALUES ('schema_version', '1');
