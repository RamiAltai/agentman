package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// openTestStore opens a Store on a throwaway DB under t.TempDir().
func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// uidRe matches the stable-id format for a given prefix: amc_/amp_ + 16 hex.
func uidRe(prefix string) *regexp.Regexp {
	return regexp.MustCompile("^" + prefix + "[0-9a-f]{16}$")
}

// TestOpenStoreFreshSchema: a fresh DB is stamped with currentSchemaVersion and
// seeded with exactly one default category, also after a reopen.
func TestOpenStoreFreshSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	for i := 0; i < 2; i++ {
		st, err := OpenStore(dbPath)
		if err != nil {
			t.Fatalf("OpenStore #%d: %v", i+1, err)
		}
		if v, err := readSchemaVersion(st.db); err != nil || v != currentSchemaVersion {
			t.Fatalf("schema_version = %d (err %v), want %d", v, err, currentSchemaVersion)
		}
		var n int
		var uid, name string
		if err := st.db.QueryRow("SELECT count(*), max(uid), max(name) FROM categories WHERE slug='general'").Scan(&n, &uid, &name); err != nil {
			t.Fatal(err)
		}
		if n != 1 || !uidRe("amc_").MatchString(uid) || name != "General" {
			t.Fatalf("general category: count %d uid %q name %q, want one amc_ row named General", n, uid, name)
		}
		st.Close()
	}
}

// TestOpenStoreRejectsOtherSchemaVersion: schema.sql is the only schema, so a
// DB stamped with any other version (newer or older am) is refused.
func TestOpenStoreRejectsOtherSchemaVersion(t *testing.T) {
	for _, v := range []string{"0", "2", "7", "abc"} {
		dbPath := filepath.Join(t.TempDir(), "other.db")
		st, err := OpenStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec("UPDATE meta SET value=? WHERE key='schema_version'", v); err != nil {
			t.Fatal(err)
		}
		st.Close()
		if _, err := OpenStore(dbPath); err == nil || !strings.Contains(err.Error(), "schema_version") {
			t.Fatalf("schema_version %q: OpenStore err = %v, want a schema_version error", v, err)
		}
	}
}

// TestNoIDReuse: deleting the newest task, project
// or every event never lets the next insert take the freed id.
func TestNoIDReuse(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "web", ""); err != nil {
		t.Fatal(err)
	}
	t1, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTask(t1.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	t2, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if t2.ID == t1.ID {
		t.Fatalf("task id %d reused after delete", t1.ID)
	}
	p1, _, err := st.CreateProject("tmp", "tmp", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteProject("tmp", "", "alice"); err != nil {
		t.Fatal(err)
	}
	p2, _, err := st.CreateProject("tmp2", "tmp2", "")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID == p1.ID {
		t.Fatalf("project id %d reused after delete", p1.ID)
	}
}

// TestEventIndexesUsed: GetTask's per-task history and the category
// stats' recent-activity window are index searches, not event-table scans.
func TestEventIndexesUsed(t *testing.T) {
	st := openTestStore(t)
	plan := func(q string, args ...any) string {
		rows, err := st.db.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, detail)
		}
		return strings.Join(out, " | ")
	}
	if p := plan(`SELECT id FROM events WHERE task_id=? ORDER BY id DESC LIMIT 50`, 1); !strings.Contains(p, "idx_events_task") {
		t.Errorf("GetTask events plan = %q, want idx_events_task", p)
	}
	if p := plan(`SELECT DISTINCT c.id, events.actor FROM events
	      JOIN projects p ON p.id = events.project_id
	      JOIN categories c ON c.id = p.category_id
	      WHERE events.task_id IS NOT NULL AND events.actor != 'human' AND events.created_at > ?
	      ORDER BY c.id, events.actor`, "2026-01-01"); !strings.Contains(p, "idx_events_created") {
		t.Errorf("category stats plan = %q, want idx_events_created", p)
	}
}
