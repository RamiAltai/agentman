package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
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

func TestOpenStoreRejectsNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "newer.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	// Simulate a DB migrated by a newer am.
	if _, err := st.db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version', ?)",
		strconv.Itoa(currentSchemaVersion+1)); err != nil {
		t.Fatalf("bump schema_version: %v", err)
	}
	st.Close()

	_, err = OpenStore(dbPath)
	if err == nil {
		t.Fatal("OpenStore on newer-schema DB: want error, got nil")
	}
	want := fmt.Sprintf("schema_version %d is newer than supported %d", currentSchemaVersion+1, currentSchemaVersion)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to mention %q", err, want)
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
	// A fresh OpenStore DB ends at the current schema version.
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", v, currentSchemaVersion)
	}
	st.Close()

	// Reopen — v3 must not double-apply (duplicate ALTER would error).
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	defer st2.Close()
	if v, _ := readSchemaVersion(st2.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after reopen = %d, want %d", v, currentSchemaVersion)
	}
}

// uidRe matches the stable-id format for a given prefix: amc_/amp_ + 16 hex.
func uidRe(prefix string) *regexp.Regexp {
	return regexp.MustCompile("^" + prefix + "[0-9a-f]{16}$")
}

func TestMigrationV4Fresh(t *testing.T) {
	st := openTestStore(t)
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", v, currentSchemaVersion)
	}
	// categories table exists, seeded with the default "general" category.
	var uid, name string
	if err := st.db.QueryRow("SELECT uid, name FROM categories WHERE slug='general'").Scan(&uid, &name); err != nil {
		t.Fatalf("default category missing: %v", err)
	}
	if !uidRe("amc_").MatchString(uid) {
		t.Fatalf("general uid = %q, want amc_<16 hex>", uid)
	}
	if name != "General" {
		t.Fatalf("general name = %q, want General", name)
	}
	// New projects columns exist (probe pattern from the V2/V3 tests).
	for _, col := range []string{"category_id", "uid", "vault_project_id", "vault_path"} {
		if _, err := st.db.Exec(fmt.Sprintf("UPDATE projects SET %s=NULL WHERE 1=0", col)); err != nil {
			t.Fatalf("projects.%s column missing after migration: %v", col, err)
		}
	}
}

func TestMigrationV4ExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v3.db")

	// Hand-build a v3 DB: seed schema.sql, then apply ONLY v2+v3 (the first two
	// entries of schemaMigrations — v4 must not have run yet).
	if schemaMigrations[0].version != 2 || schemaMigrations[1].version != 3 {
		t.Fatalf("schemaMigrations layout changed; first two steps = v%d, v%d, want v2, v3",
			schemaMigrations[0].version, schemaMigrations[1].version)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(1)&_pragma=synchronous(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if err := runMigrations(db, 3, schemaMigrations[:2]); err != nil {
		t.Fatalf("migrate to v3: %v", err)
	}
	if v, _ := readSchemaVersion(db); v != 3 {
		t.Fatalf("hand-built DB version = %d, want 3", v)
	}
	// Seed v3-era data: two projects, tasks with refs/claimed_at, a label.
	for _, slug := range []string{"alpha", "beta"} {
		if _, err := db.Exec("INSERT INTO projects(slug,name) VALUES(?,?)", slug, slug); err != nil {
			t.Fatalf("insert project %s: %v", slug, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id,ref,title,claimed_at)
	       SELECT id, 1, 'a-one', '2026-01-01T00:00:00.000Z' FROM projects WHERE slug='alpha'`); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id,ref,title)
	       SELECT id, 1, 'b-one' FROM projects WHERE slug='beta'`); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.Exec("INSERT INTO task_labels(task_id,label) SELECT id, 'bug' FROM tasks WHERE title='a-one'"); err != nil {
		t.Fatalf("insert label: %v", err)
	}
	var taskID int64
	if err := db.QueryRow("SELECT id FROM tasks WHERE title='a-one'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// OpenStore runs migration v4 (and everything after it).
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore on v3 DB: %v", err)
	}
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after migrate = %d, want %d", v, currentSchemaVersion)
	}
	// Both projects landed in general with distinct amp_ uids.
	rows, err := st.db.Query(`SELECT p.slug, p.uid, c.slug FROM projects p
	       JOIN categories c ON c.id=p.category_id ORDER BY p.id`)
	if err != nil {
		t.Fatal(err)
	}
	uids := map[string]string{}
	for rows.Next() {
		var slug, uid, cat string
		if err := rows.Scan(&slug, &uid, &cat); err != nil {
			t.Fatal(err)
		}
		if cat != "general" {
			t.Errorf("project %s category = %q, want general", slug, cat)
		}
		if !uidRe("amp_").MatchString(uid) {
			t.Errorf("project %s uid = %q, want amp_<16 hex>", slug, uid)
		}
		uids[slug] = uid
	}
	rows.Close()
	if len(uids) != 2 || uids["alpha"] == uids["beta"] {
		t.Fatalf("backfilled uids = %v, want 2 distinct", uids)
	}
	// Task ids/refs/claimed_at/labels untouched.
	var ref int64
	var claimedAt string
	if err := st.db.QueryRow("SELECT ref, COALESCE(claimed_at,'') FROM tasks WHERE id=?", taskID).
		Scan(&ref, &claimedAt); err != nil {
		t.Fatal(err)
	}
	if ref != 1 || claimedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("task ref/claimed_at = %d/%q, want 1/2026-01-01T00:00:00.000Z", ref, claimedAt)
	}
	var label string
	if err := st.db.QueryRow("SELECT label FROM task_labels WHERE task_id=?", taskID).Scan(&label); err != nil || label != "bug" {
		t.Fatalf("label after migrate = %q, %v; want bug", label, err)
	}
	st.Close()

	// Reopen — uids unchanged, no double-apply (duplicate ALTER would error).
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	defer st2.Close()
	var alphaUID string
	if err := st2.db.QueryRow("SELECT uid FROM projects WHERE slug='alpha'").Scan(&alphaUID); err != nil {
		t.Fatal(err)
	}
	if alphaUID != uids["alpha"] {
		t.Fatalf("alpha uid changed on reopen: %q → %q", uids["alpha"], alphaUID)
	}
	if v, _ := readSchemaVersion(st2.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after reopen = %d, want %d", v, currentSchemaVersion)
	}
}

func TestMigrationV5Fresh(t *testing.T) {
	st := openTestStore(t)
	if v, _ := readSchemaVersion(st.db); v != 5 {
		t.Fatalf("schema_version = %d, want 5", v)
	}
	// created_by column exists (probe pattern from the V2/V3 tests).
	if _, err := st.db.Exec("UPDATE tasks SET created_by=NULL WHERE 1=0"); err != nil {
		t.Fatalf("tasks.created_by column missing after migration: %v", err)
	}
}

func TestMigrationV5ExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v4.db")

	// Hand-build a v4 DB: seed schema.sql, then apply ONLY v2-v4 (the first
	// three entries of schemaMigrations — v5 must not have run yet).
	if schemaMigrations[0].version != 2 || schemaMigrations[1].version != 3 || schemaMigrations[2].version != 4 {
		t.Fatalf("schemaMigrations layout changed; first three steps = v%d, v%d, v%d, want v2, v3, v4",
			schemaMigrations[0].version, schemaMigrations[1].version, schemaMigrations[2].version)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(1)&_pragma=synchronous(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if err := runMigrations(db, 4, schemaMigrations[:3]); err != nil {
		t.Fatalf("migrate to v4: %v", err)
	}
	if v, _ := readSchemaVersion(db); v != 4 {
		t.Fatalf("hand-built DB version = %d, want 4", v)
	}
	// Seed v4-era data: one project, a task WITH a task.created event (the
	// backfill source) and a task WITHOUT one (events pruned).
	if _, err := db.Exec("INSERT INTO projects(slug,name,category_id,uid) VALUES('web','web',(SELECT id FROM categories WHERE slug='general'),'amp_0000000000000001')"); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	for _, title := range []string{"with-event", "pruned"} {
		if _, err := db.Exec(`INSERT INTO tasks(project_id,ref,title)
		       SELECT id, (SELECT COALESCE(MAX(ref),0)+1 FROM tasks), ? FROM projects WHERE slug='web'`, title); err != nil {
			t.Fatalf("insert task %s: %v", title, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO events(project_id,task_id,actor,kind,data)
	       SELECT t.project_id, t.id, 'bugfix_010101_0001', 'task.created', '{}'
	       FROM tasks t WHERE t.title='with-event'`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	// Rowid-reuse case: tasks.id has no AUTOINCREMENT, so deleting the max-id
	// task frees its id for the next insert, while the deleted task's events
	// survive (DeleteTask removes only the tasks row). The backfill must pick
	// the LATEST task.created event — the current incarnation's creator
	// ('bob'), not the deleted predecessor's ('alice').
	if _, err := db.Exec(`INSERT INTO tasks(project_id,ref,title)
	       SELECT id, (SELECT COALESCE(MAX(ref),0)+1 FROM tasks), 'old-incarnation' FROM projects WHERE slug='web'`); err != nil {
		t.Fatalf("insert old incarnation: %v", err)
	}
	var oldID int64
	if err := db.QueryRow("SELECT id FROM tasks WHERE title='old-incarnation'").Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events(project_id,task_id,actor,kind,data)
	       SELECT t.project_id, t.id, 'alice', 'task.created', '{}'
	       FROM tasks t WHERE t.title='old-incarnation'`); err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	if _, err := db.Exec("DELETE FROM tasks WHERE id=?", oldID); err != nil {
		t.Fatalf("delete old incarnation: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(project_id,ref,title)
	       SELECT id, (SELECT COALESCE(MAX(ref),0)+1 FROM tasks), 'reused' FROM projects WHERE slug='web'`); err != nil {
		t.Fatalf("insert reused task: %v", err)
	}
	var reusedID int64
	if err := db.QueryRow("SELECT id FROM tasks WHERE title='reused'").Scan(&reusedID); err != nil {
		t.Fatal(err)
	}
	if reusedID != oldID {
		t.Fatalf("new task id = %d, want reused %d (rowid-reuse setup assumption broken)", reusedID, oldID)
	}
	if _, err := db.Exec(`INSERT INTO events(project_id,task_id,actor,kind,data)
	       SELECT t.project_id, t.id, 'bob', 'task.created', '{}'
	       FROM tasks t WHERE t.title='reused'`); err != nil {
		t.Fatalf("insert reused event: %v", err)
	}
	db.Close()

	// OpenStore runs migration v5: column added, backfill applied.
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore on v4 DB: %v", err)
	}
	if v, _ := readSchemaVersion(st.db); v != 5 {
		t.Fatalf("schema_version after migrate = %d, want 5", v)
	}
	var creator sql.NullString
	if err := st.db.QueryRow("SELECT created_by FROM tasks WHERE title='with-event'").Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if !creator.Valid || creator.String != "bugfix_010101_0001" {
		t.Fatalf("created_by backfill = %v, want bugfix_010101_0001", creator)
	}
	// The pruned-events task stays NULL — never matches the own-proposal rule.
	if err := st.db.QueryRow("SELECT created_by FROM tasks WHERE title='pruned'").Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if creator.Valid {
		t.Fatalf("created_by for pruned-events task = %q, want NULL", creator.String)
	}
	// The reused id backfills from the LATEST creation event, not the deleted
	// predecessor's.
	if err := st.db.QueryRow("SELECT created_by FROM tasks WHERE title='reused'").Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if !creator.Valid || creator.String != "bob" {
		t.Fatalf("created_by for reused-id task = %v, want bob (not the deleted predecessor's alice)", creator)
	}
	st.Close()

	// Reopen — no double-apply (duplicate ALTER would error), version stable.
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	defer st2.Close()
	if v, _ := readSchemaVersion(st2.db); v != 5 {
		t.Fatalf("schema_version after reopen = %d, want 5", v)
	}
}
