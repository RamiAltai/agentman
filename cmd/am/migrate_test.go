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
	if v != 1 {
		t.Fatalf("fresh schema_version = %d, want 1", v)
	}
}

func TestRunMigrationsApplyAndBump(t *testing.T) {
	st := openTestStore(t)
	var applied []int
	steps := []migration{
		{version: 2, apply: func(tx *sql.Tx) error {
			applied = append(applied, 2)
			_, err := tx.Exec("CREATE TABLE m2 (x INTEGER)")
			return err
		}},
		{version: 3, apply: func(tx *sql.Tx) error {
			applied = append(applied, 3)
			_, err := tx.Exec("CREATE TABLE m3 (x INTEGER)")
			return err
		}},
	}
	if err := runMigrations(st.db, 3, steps); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if len(applied) != 2 || applied[0] != 2 || applied[1] != 3 {
		t.Fatalf("applied = %v, want [2 3]", applied)
	}
	if v, _ := readSchemaVersion(st.db); v != 3 {
		t.Fatalf("schema_version after migrate = %d, want 3", v)
	}
	// Side effects present.
	for _, tbl := range []string{"m2", "m3"} {
		var n int
		if err := st.db.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatalf("expected table %s to exist: %v", tbl, err)
		}
	}
}

func TestRunMigrationsSkipsLowerOrEqual(t *testing.T) {
	st := openTestStore(t)
	// Pretend the DB is already at version 2.
	if _, err := st.db.Exec(
		"INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version','2')"); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	var applied []int
	steps := []migration{
		{version: 1, apply: func(*sql.Tx) error { applied = append(applied, 1); return nil }},
		{version: 2, apply: func(*sql.Tx) error { applied = append(applied, 2); return nil }},
		{version: 3, apply: func(tx *sql.Tx) error {
			applied = append(applied, 3)
			_, err := tx.Exec("CREATE TABLE m3only (x INTEGER)")
			return err
		}},
	}
	if err := runMigrations(st.db, 3, steps); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if len(applied) != 1 || applied[0] != 3 {
		t.Fatalf("applied = %v, want only [3] (1 and 2 skipped)", applied)
	}
	if v, _ := readSchemaVersion(st.db); v != 3 {
		t.Fatalf("schema_version = %d, want 3", v)
	}
}

func TestRunMigrationsIdempotent(t *testing.T) {
	st := openTestStore(t)
	calls := 0
	steps := []migration{
		{version: 2, apply: func(tx *sql.Tx) error {
			calls++
			// INSERT not CREATE-IF-NOT-EXISTS so a duplicate apply would be observable.
			if _, err := tx.Exec("CREATE TABLE once (n INTEGER)"); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO once(n) VALUES(1)")
			return err
		}},
	}
	if err := runMigrations(st.db, 2, steps); err != nil {
		t.Fatalf("first runMigrations: %v", err)
	}
	// Re-run: must be a no-op.
	if err := runMigrations(st.db, 2, steps); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
	if calls != 1 {
		t.Fatalf("apply called %d times, want 1 (idempotent)", calls)
	}
	var rows int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM once").Scan(&rows); err != nil {
		t.Fatalf("count once: %v", err)
	}
	if rows != 1 {
		t.Fatalf("once row count = %d, want 1 (side effect not duplicated)", rows)
	}
	if v, _ := readSchemaVersion(st.db); v != 2 {
		t.Fatalf("schema_version = %d, want 2 (unchanged on re-run)", v)
	}
}

func TestRunMigrationsRollbackLeavesPriorVersion(t *testing.T) {
	st := openTestStore(t)
	steps := []migration{
		{version: 2, apply: func(*sql.Tx) error { return sql.ErrConnDone }},
	}
	if err := runMigrations(st.db, 2, steps); err == nil {
		t.Fatal("runMigrations: want error from failing step, got nil")
	}
	if v, _ := readSchemaVersion(st.db); v != 1 {
		t.Fatalf("schema_version after failed migration = %d, want 1 (rolled back)", v)
	}
}
