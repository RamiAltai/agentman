package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func cwfStoreNewTask(t *testing.T, st *Store, project, title string) *Task {
	t.Helper()
	tk, _, err := st.CreateTask(CreateTaskInput{Project: project, Title: title})
	if err != nil {
		t.Fatalf("CreateTask(%s): %v", title, err)
	}
	return tk
}

func cwfStoreMeta(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%d", i)] = "v"
	}
	return m
}

// FIND-0273: a task holds at most maxMetaKeys meta keys, on create and patch.
func TestCwfStoreMetaKeyCap(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tooMany := map[string]string{}
	for k := range cwfStoreMeta(maxMetaKeys + 1) {
		tooMany[k] = "v"
	}
	if _, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "t", Meta: tooMany}); !errors.Is(err, ErrValidation) {
		t.Fatalf("CreateTask with %d meta keys err = %v, want ErrValidation", len(tooMany), err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": cwfStoreMeta(maxMetaKeys + 1)}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("patch with %d meta keys err = %v, want ErrValidation", maxMetaKeys+1, err)
	}
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": cwfStoreMeta(maxMetaKeys)}, "a"); err != nil {
		t.Fatalf("patch with exactly %d meta keys: %v", maxMetaKeys, err)
	}
	// The cap is on the task's total, not just one patch.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"extra": "v"}}, "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("patch past the per-task total err = %v, want ErrValidation", err)
	}
	// Updating or removing existing keys at the cap still works.
	if _, _, err := st.PatchTask(tk.ID, map[string]any{"meta": map[string]any{"k0": "new", "k1": ""}}, "a"); err != nil {
		t.Fatalf("update/remove at the cap: %v", err)
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM task_meta WHERE task_id=?", tk.ID).Scan(&n); err != nil || n != maxMetaKeys-1 {
		t.Fatalf("meta rows = %d, %v; want %d", n, err, maxMetaKeys-1)
	}
}

// FIND-0277: racing creates of one slug yield one success and ErrConflict for
// the rest, never a raw UNIQUE error.
func TestCwfStoreConcurrentSlugCreate(t *testing.T) {
	st := openTestStore(t)
	for name, create := range map[string]func() error{
		"category": func() error { _, _, err := st.CreateCategory("dupcat", ""); return err },
		"project":  func() error { _, _, err := st.CreateProject("dupproj", "", ""); return err },
	} {
		const n = 16
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) { defer wg.Done(); errs[i] = create() }(i)
		}
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, ErrConflict):
				t.Errorf("%s: concurrent create err = %v, want nil or ErrConflict", name, err)
			}
		}
		if wins != 1 {
			t.Errorf("%s: %d creates succeeded, want 1", name, wins)
		}
	}
}

// FIND-0287: a failed updated_at bump aborts the comment mutation instead of
// committing a comment that leaves the claim looking stale.
func TestCwfStoreCommentBumpFailureAborts(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	kept, _, err := st.AddComment(tk.ID, "a", "kept")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER cwf_fail BEFORE UPDATE OF updated_at ON tasks
	       BEGIN SELECT RAISE(ABORT, 'bump failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddComment(tk.ID, "a", "lost"); err == nil {
		t.Fatal("AddComment succeeded although the updated_at bump failed")
	}
	if _, err := st.DeleteComment(tk.ID, kept.ID, "a"); err == nil {
		t.Fatal("DeleteComment succeeded although the updated_at bump failed")
	}
	var bodies []string
	rows, err := st.db.Query("SELECT body FROM comments WHERE task_id=? ORDER BY id", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		rows.Scan(&b)
		bodies = append(bodies, b)
	}
	if strings.Join(bodies, ",") != "kept" {
		t.Fatalf("comments = %v, want [kept] (both failed mutations rolled back)", bodies)
	}
}

// FIND-0450: a next request whose context is gone before commit claims nothing.
func TestCwfStoreNextTaskCanceledContext(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := st.NextTaskContext(ctx, NextFilter{}, "agent-x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("NextTaskContext(canceled) err = %v, want context.Canceled", err)
	}
	got, err := st.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "todo" || got.Assignee != "" {
		t.Fatalf("task after abandoned next = %s/%q, want todo/unassigned", got.Status, got.Assignee)
	}
}

// FIND-0471: '?', '#' and '%' in the db path name the literal file and keep
// the DSN pragmas.
func TestCwfStoreOpenStoreURIChars(t *testing.T) {
	for _, dir := range []string{"a#b", "c?d", "e%20f"} {
		path := filepath.Join(t.TempDir(), dir, "am.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		st, err := OpenStore(path)
		if err != nil {
			t.Fatalf("OpenStore(%q): %v", path, err)
		}
		var fk int
		var jm string
		st.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
		st.db.QueryRow("PRAGMA journal_mode").Scan(&jm)
		st.Close()
		if fk != 1 || jm != "wal" {
			t.Errorf("%q: foreign_keys=%d journal_mode=%q, want 1 and wal", dir, fk, jm)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%q: db not created at the literal path: %v", dir, err)
		}
	}
}

// FIND-0473: a failed migration names the step and the version it started from.
func TestCwfStoreMigrationErrorNamesStep(t *testing.T) {
	db, err := sql.Open("sqlite", storeDSN(filepath.Join(t.TempDir(), "m.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	steps := []migration{
		{version: 2, apply: func(*sql.Tx) error { return nil }},
		{version: 3, apply: func(*sql.Tx) error { return boom }},
	}
	err = runMigrations(db, 3, steps)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "v2 -> v3") {
		t.Fatalf("runMigrations err = %v, want boom wrapped with \"v2 -> v3\"", err)
	}
	if v, _ := readSchemaVersion(db); v != 2 {
		t.Fatalf("schema_version after failed v3 = %d, want 2", v)
	}
}

// FIND-0474: the v5 created_by backfill finds each task's creation event
// through an index rather than scanning the event log.
func TestCwfStoreV5BackfillIndexed(t *testing.T) {
	db := buildDBAtVersion(t, filepath.Join(t.TempDir(), "v5.db"), 5)
	defer db.Close()
	var plan strings.Builder
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT e.actor FROM events e
	       WHERE e.task_id=1 AND e.kind='task.created' ORDER BY e.id DESC LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		rows.Scan(&id, &parent, &unused, &detail)
		plan.WriteString(detail + "; ")
	}
	rows.Close()
	if !strings.Contains(plan.String(), "idx_events_task") {
		t.Fatalf("v5 backfill lookup plan = %q, want idx_events_task", plan.String())
	}
	// v6 rebuilds events and recreates the index under the same name.
	if err := runMigrations(db, currentSchemaVersion, schemaMigrations); err != nil {
		t.Fatalf("migrate v5 -> current: %v", err)
	}
}

// FIND-0485: the owner's re-claim stays idempotent after an open prerequisite
// is added to its started task; a claim that would change state stays blocked.
func TestCwfStoreOwnerReclaimWithNewPrereq(t *testing.T) {
	st := openTestStore(t)
	if _, _, err := st.CreateProject("web", "", ""); err != nil {
		t.Fatal(err)
	}
	tk := cwfStoreNewTask(t, st, "web", "t")
	pre := cwfStoreNewTask(t, st, "web", "prereq")
	if _, _, err := st.ClaimTask(tk.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDep(tk.ID, pre.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if got, ev, err := st.ClaimTask(tk.ID, "a"); err != nil || ev != nil || got.Assignee != "a" {
		t.Fatalf("owner re-claim = %+v, %v, %v; want task, nil event, nil", got, ev, err)
	}
	if got, ev, err := st.StealStaleClaim(tk.ID, "a", time.Hour); err != nil || ev != nil || got.Assignee != "a" {
		t.Fatalf("owner steal = %+v, %v, %v; want task, nil event, nil", got, ev, err)
	}
	var be *BlockedError
	if _, _, err := st.ClaimTask(tk.ID, "b"); !errors.As(err, &be) {
		t.Fatalf("other agent's claim err = %v, want BlockedError", err)
	}
	// A pre-assigned todo task would be started by the claim: still blocked.
	todo, _, err := st.CreateTask(CreateTaskInput{Project: "web", Title: "pre-assigned", Assignee: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDep(todo.ID, pre.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ClaimTask(todo.ID, "a"); !errors.As(err, &be) {
		t.Fatalf("claim starting a blocked pre-assigned task err = %v, want BlockedError", err)
	}
}
