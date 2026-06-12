package main

import (
	"database/sql"
	"path/filepath"
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

func TestReadSchemaVersionFresh(t *testing.T) {
	st := openTestStore(t)
	v, err := readSchemaVersion(st.db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	// After OpenStore, the real migrations have run, so the version is currentSchemaVersion.
	if v != currentSchemaVersion {
		t.Fatalf("schema_version after OpenStore = %d, want %d", v, currentSchemaVersion)
	}
}

func TestRunMigrationsApplyAndBump(t *testing.T) {
	st := openTestStore(t)
	var applied []int
	// Use version numbers above currentSchemaVersion to avoid collision with real migrations.
	steps := []migration{
		{version: currentSchemaVersion + 1, apply: func(tx *sql.Tx) error {
			applied = append(applied, currentSchemaVersion+1)
			_, err := tx.Exec("CREATE TABLE m_test1 (x INTEGER)")
			return err
		}},
		{version: currentSchemaVersion + 2, apply: func(tx *sql.Tx) error {
			applied = append(applied, currentSchemaVersion+2)
			_, err := tx.Exec("CREATE TABLE m_test2 (x INTEGER)")
			return err
		}},
	}
	target := currentSchemaVersion + 2
	if err := runMigrations(st.db, target, steps); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if len(applied) != 2 || applied[0] != currentSchemaVersion+1 || applied[1] != currentSchemaVersion+2 {
		t.Fatalf("applied = %v, want [%d %d]", applied, currentSchemaVersion+1, currentSchemaVersion+2)
	}
	if v, _ := readSchemaVersion(st.db); v != target {
		t.Fatalf("schema_version after migrate = %d, want %d", v, target)
	}
	// Side effects present.
	for _, tbl := range []string{"m_test1", "m_test2"} {
		var n int
		if err := st.db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatalf("expected table %s to exist: %v", tbl, err)
		}
	}
}

func TestRunMigrationsSkipsLowerOrEqual(t *testing.T) {
	st := openTestStore(t)
	// DB is already at currentSchemaVersion after OpenStore. Versions at or below are skipped.
	var applied []int
	steps := []migration{
		{version: currentSchemaVersion - 1, apply: func(*sql.Tx) error {
			applied = append(applied, currentSchemaVersion-1)
			return nil
		}},
		{version: currentSchemaVersion, apply: func(*sql.Tx) error {
			applied = append(applied, currentSchemaVersion)
			return nil
		}},
		{version: currentSchemaVersion + 1, apply: func(tx *sql.Tx) error {
			applied = append(applied, currentSchemaVersion+1)
			_, err := tx.Exec("CREATE TABLE m_skip_test (x INTEGER)")
			return err
		}},
	}
	target := currentSchemaVersion + 1
	if err := runMigrations(st.db, target, steps); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if len(applied) != 1 || applied[0] != currentSchemaVersion+1 {
		t.Fatalf("applied = %v, want only [%d] (lower versions skipped)", applied, currentSchemaVersion+1)
	}
	if v, _ := readSchemaVersion(st.db); v != target {
		t.Fatalf("schema_version = %d, want %d", v, target)
	}
}

func TestRunMigrationsIdempotent(t *testing.T) {
	st := openTestStore(t)
	calls := 0
	target := currentSchemaVersion + 1
	steps := []migration{
		{version: target, apply: func(tx *sql.Tx) error {
			calls++
			// INSERT not CREATE-IF-NOT-EXISTS so a duplicate apply would be observable.
			if _, err := tx.Exec("CREATE TABLE once_idem (n INTEGER)"); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO once_idem(n) VALUES(1)")
			return err
		}},
	}
	if err := runMigrations(st.db, target, steps); err != nil {
		t.Fatalf("first runMigrations: %v", err)
	}
	// Re-run: must be a no-op.
	if err := runMigrations(st.db, target, steps); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
	if calls != 1 {
		t.Fatalf("apply called %d times, want 1 (idempotent)", calls)
	}
	var rows int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM once_idem").Scan(&rows); err != nil {
		t.Fatalf("count once_idem: %v", err)
	}
	if rows != 1 {
		t.Fatalf("once_idem row count = %d, want 1 (side effect not duplicated)", rows)
	}
	if v, _ := readSchemaVersion(st.db); v != target {
		t.Fatalf("schema_version = %d, want %d (unchanged on re-run)", v, target)
	}
}

func TestRunMigrationsRollbackLeavesPriorVersion(t *testing.T) {
	st := openTestStore(t)
	target := currentSchemaVersion + 1
	steps := []migration{
		{version: target, apply: func(*sql.Tx) error { return sql.ErrConnDone }},
	}
	if err := runMigrations(st.db, target, steps); err == nil {
		t.Fatal("runMigrations: want error from failing step, got nil")
	}
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after failed migration = %d, want %d (rolled back)", v, currentSchemaVersion)
	}
}

func TestMigrationV2AddsArchivedAt(t *testing.T) {
	st := openTestStore(t)
	// Confirm archived_at column exists by running a query that uses it.
	_, err := st.db.Exec("UPDATE projects SET archived_at=NULL WHERE 1=0")
	if err != nil {
		t.Fatalf("archived_at column missing after migration: %v", err)
	}
}

func TestMigrationV3AddsClaimedAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v3.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	// Confirm claimed_at column exists by running a query that uses it.
	if _, err := st.db.Exec("UPDATE tasks SET claimed_at=NULL WHERE 1=0"); err != nil {
		t.Fatalf("claimed_at column missing after migration: %v", err)
	}
	// A fresh OpenStore DB ends at version 3.
	if v, _ := readSchemaVersion(st.db); v != 3 {
		t.Fatalf("schema_version = %d, want 3", v)
	}
	st.Close()

	// Reopen — v3 must not double-apply (duplicate ALTER would error).
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	defer st2.Close()
	if v, _ := readSchemaVersion(st2.db); v != 3 {
		t.Fatalf("schema_version after reopen = %d, want 3", v)
	}
}
