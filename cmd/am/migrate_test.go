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

// buildDBAtVersion hand-builds the db at path at schema version n: schema.sql
// plus every schemaMigrations step up to n, opened with OpenStore's DSN so the
// fixture runs under the production pragmas. The caller seeds version-n data,
// closes db and reopens with OpenStore to run the remaining migrations.
func buildDBAtVersion(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	var steps []migration
	for _, m := range schemaMigrations {
		if m.version <= n {
			steps = append(steps, m)
		}
	}
	if len(steps) == 0 || steps[len(steps)-1].version != n {
		t.Fatalf("schemaMigrations has no step for v%d", n)
	}
	db, err := sql.Open("sqlite", storeDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if err := runMigrations(db, n, steps); err != nil {
		t.Fatalf("migrate to v%d: %v", n, err)
	}
	if v, _ := readSchemaVersion(db); v != n {
		t.Fatalf("hand-built DB version = %d, want %d", v, n)
	}
	return db
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
	// The step does real DDL before failing, so a missing rollback would leave
	// m_partial behind (or the connection wedged in an open tx).
	steps := []migration{
		{version: target, apply: func(tx *sql.Tx) error {
			if _, err := tx.Exec("CREATE TABLE m_partial (x INTEGER)"); err != nil {
				return err
			}
			return sql.ErrConnDone
		}},
	}
	if err := runMigrations(st.db, target, steps); err == nil {
		t.Fatal("runMigrations: want error from failing step, got nil")
	}
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after failed migration = %d, want %d (rolled back)", v, currentSchemaVersion)
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='m_partial'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("m_partial count = %d (%v), want 0: the failed step's DDL was not rolled back", n, err)
	}
	// The connection is usable afterwards: a good step applies and bumps.
	ok := []migration{{version: target, apply: func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE m_after (x INTEGER)")
		return err
	}}}
	if err := runMigrations(st.db, target, ok); err != nil {
		t.Fatalf("runMigrations after a rolled-back step: %v", err)
	}
	if v, _ := readSchemaVersion(st.db); v != target {
		t.Fatalf("schema_version = %d, want %d", v, target)
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

	// Hand-build a v3 DB (v4 must not have run yet).
	db := buildDBAtVersion(t, dbPath, 3)
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
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", v, currentSchemaVersion)
	}
	// created_by column exists (probe pattern from the V2/V3 tests).
	if _, err := st.db.Exec("UPDATE tasks SET created_by=NULL WHERE 1=0"); err != nil {
		t.Fatalf("tasks.created_by column missing after migration: %v", err)
	}
}

func TestMigrationV5ExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v4.db")

	// Hand-build a v4 DB (v5 must not have run yet).
	db := buildDBAtVersion(t, dbPath, 4)
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
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after migrate = %d, want %d", v, currentSchemaVersion)
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
	if v, _ := readSchemaVersion(st2.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after reopen = %d, want %d", v, currentSchemaVersion)
	}
}

// TestMigrationV6ExistingDB: v6 rebuilds projects/tasks/events as
// AUTOINCREMENT without cascading into child rows (runMigrations turns
// foreign_keys off), and seeds each sequence past every id the event log has
// seen, so ids deleted BEFORE the migration are not reissued either.
func TestMigrationV6ExistingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v5.db")
	db := buildDBAtVersion(t, dbPath, 5)
	for _, q := range []string{
		`INSERT INTO projects(slug,name,category_id,uid) VALUES
		   ('alpha','alpha',(SELECT id FROM categories WHERE slug='general'),'amp_00000000000000a1'),
		   ('beta','beta',(SELECT id FROM categories WHERE slug='general'),'amp_00000000000000b2')`,
		`INSERT INTO tasks(project_id,ref,title,created_by) SELECT id, 1, 't1', 'alice' FROM projects WHERE slug='alpha'`,
		`INSERT INTO tasks(project_id,ref,title) SELECT id, 2, 't2' FROM projects WHERE slug='alpha'`,
		`INSERT INTO tasks(project_id,ref,title) SELECT id, 3, 't3' FROM projects WHERE slug='alpha'`,
		`INSERT INTO comments(task_id,author,body) SELECT id,'bob','hi' FROM tasks WHERE title='t2'`,
		`INSERT INTO task_labels(task_id,label) SELECT id,'bug' FROM tasks WHERE title='t2'`,
		`INSERT INTO task_meta(task_id,key,value) SELECT id,'k','v' FROM tasks WHERE title='t2'`,
		`INSERT INTO task_deps(task_id,depends_on_id) SELECT a.id, b.id FROM tasks a, tasks b WHERE a.title='t2' AND b.title='t1'`,
		`INSERT INTO events(project_id,task_id,actor,kind) SELECT project_id, id, 'alice', 'task.created' FROM tasks`,
		`INSERT INTO events(project_id,actor,kind) SELECT id, 'alice', 'project.deleted' FROM projects WHERE slug='beta'`,
		// Highest task and highest project deleted; only the event log remembers their ids.
		`DELETE FROM tasks WHERE title='t3'`,
		`DELETE FROM projects WHERE slug='beta'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	var maxTask, maxProject, maxEvent int64
	db.QueryRow("SELECT MAX(task_id), MAX(project_id), MAX(id) FROM events").Scan(&maxTask, &maxProject, &maxEvent)
	db.Close()

	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore on v5 DB: %v", err)
	}
	defer st.Close()
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", v, currentSchemaVersion)
	}
	for _, tbl := range []string{"projects", "tasks", "events"} {
		var ddl string
		st.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&ddl)
		if !strings.Contains(ddl, "AUTOINCREMENT") {
			t.Errorf("%s DDL lacks AUTOINCREMENT: %s", tbl, ddl)
		}
	}
	// Child rows survived the parent rebuild (no cascade).
	for q, want := range map[string]int{
		"SELECT COUNT(*) FROM tasks":       2,
		"SELECT COUNT(*) FROM comments":    1,
		"SELECT COUNT(*) FROM task_labels": 1,
		"SELECT COUNT(*) FROM task_meta":   1,
		"SELECT COUNT(*) FROM task_deps":   1,
		"SELECT COUNT(*) FROM events":      4,
	} {
		var n int
		if err := st.db.QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", q, n, err, want)
		}
	}
	var fk int
	st.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if fk != 1 {
		t.Fatalf("foreign_keys = %d after migration, want 1 (re-enabled)", fk)
	}
	var createdBy string
	st.db.QueryRow("SELECT created_by FROM tasks WHERE title='t1'").Scan(&createdBy)
	if createdBy != "alice" {
		t.Fatalf("t1 created_by = %q after rebuild, want alice", createdBy)
	}

	// New rows get ids past every id the log has seen.
	tk, _, err := st.CreateTask(CreateTaskInput{Project: "alpha", Title: "t4"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID <= maxTask {
		t.Errorf("new task id %d reuses a deleted task's id (max seen %d)", tk.ID, maxTask)
	}
	p, _, err := st.CreateProject("gamma", "gamma", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID <= maxProject {
		t.Errorf("new project id %d reuses a deleted project's id (max seen %d)", p.ID, maxProject)
	}
	if _, err := st.db.Exec("DELETE FROM events"); err != nil { // `am db prune --keep 0`
		t.Fatal(err)
	}
	_, ev, err := st.CreateTask(CreateTaskInput{Project: "alpha", Title: "t5"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID <= maxEvent {
		t.Errorf("event id %d after emptying the log restarts below the old cursor %d", ev.ID, maxEvent)
	}
}

// TestMigrationV6NoIDReuse: on a fresh DB, deleting the newest task, project
// or every event never lets the next insert take the freed id.
func TestMigrationV6NoIDReuse(t *testing.T) {
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

// TestMigrationV6EventIndexes: GetTask's per-task history and the category
// stats' recent-activity window are index searches, not event-table scans.
func TestMigrationV6EventIndexes(t *testing.T) {
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
