package main

import (
	"database/sql"
	"net"
	"strings"
	"testing"
)

// cwfDbNoServer points AGENTMAN_URL at a loopback port that was free a moment
// ago, so the liveness probe finds no server, and returns that URL.
func cwfDbNoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	l.Close()
	t.Setenv("AGENTMAN_URL", url)
	return url
}

// cwfDbCount runs a single-integer query and fails the test on any error.
func cwfDbCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// cwfDbV3SchemaSQL is a frozen schema_version 3 database: schema.sql as of the
// last v3 release (2045343) with migrations v2 (projects.archived_at) and v3
// (tasks.claimed_at) folded in. It must never follow schema.sql.
const cwfDbV3SchemaSQL = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE projects (
  id INTEGER PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  ref INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo','doing','blocked','done')),
  assignee TEXT,
  priority INTEGER NOT NULL DEFAULT 2,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  claimed_at TEXT,
  UNIQUE (project_id, ref)
);
CREATE INDEX idx_tasks_project_status ON tasks(project_id, status);
CREATE INDEX idx_tasks_assignee ON tasks(assignee);
CREATE INDEX idx_tasks_updated ON tasks(updated_at);
CREATE TABLE task_deps (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  depends_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  PRIMARY KEY (task_id, depends_on_id)
);
CREATE INDEX idx_task_deps_prereq ON task_deps(depends_on_id);
CREATE TABLE task_labels (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  PRIMARY KEY (task_id, label)
);
CREATE INDEX idx_task_labels_label ON task_labels(label);
CREATE TABLE comments (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  author TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_comments_task ON comments(task_id, id);
CREATE TABLE events (
  id INTEGER PRIMARY KEY,
  project_id INTEGER,
  task_id INTEGER,
  actor TEXT NOT NULL,
  kind TEXT NOT NULL,
  data TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_events_since ON events(id);
INSERT INTO meta(key, value) VALUES ('schema_version', '3');
`

// FIND-0099: pruneEvents itself enforces "exactly one of before / keep" with a
// positive keep; keep==0 (the "unused" value) must not delete every event.
func TestCwfDbPruneRejectsAmbiguousArgs(t *testing.T) {
	p := cwfDbStore(t, t.TempDir(), "args.db", "p")
	total := cwfDbEventCount(t, p)
	for _, tc := range []struct {
		before string
		keep   int
	}{
		{"", 0},
		{"", -1},
		{"2024-01-01", 5},
		{"2024-01-01", -1},
	} {
		if _, err := pruneEvents(p, tc.before, tc.keep); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("pruneEvents(before=%q, keep=%d) err = %v, want 'exactly one'", tc.before, tc.keep, err)
		}
	}
	if got := cwfDbEventCount(t, p); got != total {
		t.Fatalf("events = %d after rejected prunes, want %d", got, total)
	}
}
