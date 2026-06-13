package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPruneEventsKeep(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	dbPath := filepath.Join(t.TempDir(), "prune_keep.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Seed: create a project and several tasks (each generates events).
	if _, _, err := st.CreateProject("keepproj", "Keep", ""); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"t1", "t2", "t3", "t4", "t5"} {
		if _, _, err := st.CreateTask(CreateTaskInput{Project: "keepproj", Title: title}); err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
	}
	// Count total events.
	var total int
	st.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&total)
	if total < 3 {
		t.Fatalf("expected >=3 events seeded, got %d", total)
	}

	// Collect newest event IDs so we can verify them after prune.
	type evRow struct {
		id int64
	}
	rows, err := st.db.Query("SELECT id FROM events ORDER BY id DESC LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	var newestIDs []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		newestIDs = append(newestIDs, id)
	}
	rows.Close()

	st.Close()

	// Prune keeping only 2.
	deleted, err := pruneEvents(dbPath, "", 2)
	if err != nil {
		t.Fatalf("pruneEvents(keep=2): %v", err)
	}
	expected := int64(total - 2)
	if deleted != expected {
		t.Fatalf("pruneEvents(keep=2) deleted=%d, want %d", deleted, expected)
	}

	// Verify exactly 2 events remain and they are the newest two.
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var remaining int
	st2.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("expected 2 events after keep prune, got %d", remaining)
	}
	for _, nid := range newestIDs {
		var cnt int
		st2.db.QueryRow("SELECT COUNT(*) FROM events WHERE id=?", nid).Scan(&cnt)
		if cnt != 1 {
			t.Errorf("newest event id=%d should survive keep prune, but not found", nid)
		}
	}
}

func TestPruneEventsBefore(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	dbPath := filepath.Join(t.TempDir(), "prune_before.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("bproj", "Before", ""); err != nil {
		t.Fatal(err)
	}

	// Insert events with explicit created_at so we control the date boundary.
	oldDate := "2020-06-01T12:00:00.000Z"
	newDate := "2025-06-01T12:00:00.000Z"
	for i := 0; i < 3; i++ {
		st.db.Exec("INSERT INTO events(project_id, actor, kind, data, created_at) SELECT id, 'tester', 'test.old', '{}', ? FROM projects WHERE slug='bproj'", oldDate)
	}
	for i := 0; i < 2; i++ {
		st.db.Exec("INSERT INTO events(project_id, actor, kind, data, created_at) SELECT id, 'tester', 'test.new', '{}', ? FROM projects WHERE slug='bproj'", newDate)
	}

	var total int
	st.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&total)
	st.Close()

	// Prune before 2023-01-01 — only the "old" rows should be deleted.
	deleted, err := pruneEvents(dbPath, "2023-01-01", 0)
	if err != nil {
		t.Fatalf("pruneEvents(before=2023-01-01): %v", err)
	}
	if deleted != 3 {
		t.Fatalf("pruneEvents(before=2023-01-01) deleted=%d, want 3", deleted)
	}

	// Verify only the "new" rows remain.
	st2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var remaining int
	st2.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='test.new'").Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("expected 2 'test.new' events to survive, got %d", remaining)
	}
	var oldRemaining int
	st2.db.QueryRow("SELECT COUNT(*) FROM events WHERE kind='test.old'").Scan(&oldRemaining)
	if oldRemaining != 0 {
		t.Fatalf("expected 0 'test.old' events after prune, got %d", oldRemaining)
	}

	// Prune before a date before all events — should delete 0.
	st2.Close()
	deleted2, err := pruneEvents(dbPath, "2019-01-01", 0)
	if err != nil {
		t.Fatalf("pruneEvents(before=2019-01-01): %v", err)
	}
	if deleted2 != 0 {
		t.Fatalf("pruneEvents(before=2019-01-01) deleted=%d, want 0", deleted2)
	}
}

// TestPruneEventsBeforeSameDayBoundary locks down the subtle lexical boundary:
// a bare YYYY-MM-DD cutoff must KEEP same-day events (their ISO timestamp
// "...T..." sorts AFTER the date-only string) and only the NEXT day deletes them.
func TestPruneEventsBeforeSameDayBoundary(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	dbPath := filepath.Join(t.TempDir(), "prune_boundary.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("dproj", "Day", ""); err != nil {
		t.Fatal(err)
	}
	// One event timestamped midday on 2024-03-15.
	if _, err := st.db.Exec(
		"INSERT INTO events(project_id, actor, kind, data, created_at) SELECT id, 'tester', 'test.sameday', '{}', ? FROM projects WHERE slug='dproj'",
		"2024-03-15T12:00:00.000Z"); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	st.Close()

	// --before the SAME day must NOT delete the same-day event.
	deleted, err := pruneEvents(dbPath, "2024-03-15", 0)
	if err != nil {
		t.Fatalf("pruneEvents(before=2024-03-15): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("same-day prune deleted=%d, want 0 (same-day events must be kept)", deleted)
	}

	// --before the NEXT day deletes it.
	deleted2, err := pruneEvents(dbPath, "2024-03-16", 0)
	if err != nil {
		t.Fatalf("pruneEvents(before=2024-03-16): %v", err)
	}
	if deleted2 != 1 {
		t.Fatalf("next-day prune deleted=%d, want 1", deleted2)
	}
}

// TestExportImportRoundtrip: create a store, export it, validate the export, import it back.
func TestExportImportRoundtrip(t *testing.T) {
	// Point away from any live server so isServerRunning returns false.
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	// 1. Create a source DB with a project and task
	srcDB := filepath.Join(t.TempDir(), "src.db")
	store, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateProject("testproj", "Test Project", "")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// 2. Export it
	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := exportDB(srcDB, exportPath); err != nil {
		t.Fatal(err)
	}

	// 3. Check file perms
	info, err := os.Stat(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("export perm = %o, want 0o600", info.Mode().Perm())
	}

	// 4. Validate the export
	if err := validateImportCandidate(exportPath); err != nil {
		t.Fatal(err)
	}

	// 5. Import into a fresh destination
	destDB := filepath.Join(t.TempDir(), "dest.db")
	backupDir := t.TempDir()
	if err := importDB(exportPath, destDB, backupDir, true); err != nil {
		t.Fatal(err)
	}

	// 6. Verify dest DB has the project
	store2, err := OpenStore(destDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	projs, err := store2.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projs) != 1 || projs[0].Slug != "testproj" {
		t.Errorf("want 1 project 'testproj', got %+v", projs)
	}
}

// TestImportCreatesBackup: importing over an existing DB creates a backup.
func TestImportCreatesBackup(t *testing.T) {
	// Point away from any live server so isServerRunning returns false.
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	srcDB := filepath.Join(t.TempDir(), "src.db")
	store, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := exportDB(srcDB, exportPath); err != nil {
		t.Fatal(err)
	}

	// Pre-existing dest DB
	destDB := filepath.Join(t.TempDir(), "dest.db")
	store2, err := OpenStore(destDB)
	if err != nil {
		t.Fatal(err)
	}
	store2.Close()

	backupDir := t.TempDir()
	if err := importDB(exportPath, destDB, backupDir, true); err != nil {
		t.Fatal(err)
	}

	// A backup file should exist in backupDir
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("expected backup file in backupDir, got none")
	}
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("backup %s perm = %o, want 0o600", e.Name(), info.Mode().Perm())
		}
	}
}

// TestValidateImportCandidateRejectsGarbage: a non-SQLite file fails validation.
func TestValidateImportCandidateRejectsGarbage(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "garbage*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("this is not a sqlite database at all")
	f.Close()
	if err := validateImportCandidate(f.Name()); err == nil {
		t.Error("expected error validating garbage file, got nil")
	}
}

// TestPruneEventsRejectsBadDate: malformed --before dates must error out
// instead of silently string-comparing against ISO-8601 timestamps.
func TestPruneEventsRejectsBadDate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad_date.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, bad := range []string{"2026-13-99", "garbage", "2026-1-1", "2026/01/01"} {
		if _, err := pruneEvents(dbPath, bad, 0); err == nil {
			t.Errorf("pruneEvents(--before %q): expected error, got nil", bad)
		}
	}
}

// TestIsServerRunning: quick sanity (server is NOT running in tests).
func TestIsServerRunning(t *testing.T) {
	// There should be no server on a random high port
	if isServerRunning("http://127.0.0.1:19999") {
		t.Error("expected isServerRunning=false on unused port")
	}
}

// ---------- Phase O: snapshots across the v4 boundary ----------

// TestExportContainsCategories: a snapshot of a current DB carries the
// categories table (VACUUM INTO copies everything).
func TestExportContainsCategories(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	srcDB := filepath.Join(t.TempDir(), "src.db")
	st, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateCategory("work", "Work"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := exportDB(srcDB, exportPath); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", exportPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM categories").Scan(&n); err != nil {
		t.Fatalf("categories table missing from export: %v", err)
	}
	if n != 2 { // general (seeded) + work
		t.Fatalf("exported categories = %d, want 2", n)
	}
}

// TestImportPreCategorySnapshot: a v3-shaped DB (no categories table) passes
// validation — the required-table set is the v1 baseline on purpose — imports
// cleanly, and migrates to the current version on the next OpenStore.
func TestImportPreCategorySnapshot(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	// Hand-build a v3 DB (schema.sql baseline + v2/v3 only), with one project.
	srcDB := filepath.Join(t.TempDir(), "v3src.db")
	db, err := sql.Open("sqlite", "file:"+srcDB+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db, 3, schemaMigrations[:2]); err != nil {
		t.Fatal(err)
	}
	// Make it truly pre-category: drop the table schema.sql just created.
	if _, err := db.Exec("DROP TABLE categories"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO projects(slug,name) VALUES('oldproj','Old')"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := validateImportCandidate(srcDB); err != nil {
		t.Fatalf("v3 snapshot rejected: %v", err)
	}

	destDB := filepath.Join(t.TempDir(), "dest.db")
	if err := importDB(srcDB, destDB, t.TempDir(), true); err != nil {
		t.Fatalf("importDB: %v", err)
	}

	// OpenStore migrates the imported DB to the current version.
	st, err := OpenStore(destDB)
	if err != nil {
		t.Fatalf("OpenStore after import: %v", err)
	}
	defer st.Close()
	if v, _ := readSchemaVersion(st.db); v != currentSchemaVersion {
		t.Fatalf("schema_version after import+open = %d, want %d", v, currentSchemaVersion)
	}
	ps, err := st.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Slug != "oldproj" || ps[0].Category != "general" {
		t.Fatalf("imported project = %+v, want oldproj in general", ps)
	}
}

// TestImportRejectsNewerSchema: a snapshot stamped with a future schema_version
// must be refused.
func TestImportRejectsNewerSchema(t *testing.T) {
	srcDB := filepath.Join(t.TempDir(), "future.db")
	st, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version', ?)",
		strconv.Itoa(currentSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := validateImportCandidate(srcDB); err == nil {
		t.Fatalf("schema_version %d snapshot accepted, want rejection", currentSchemaVersion+1)
	}
}

// TestExportImportWithTokens: a DB carrying tokens round-trips through
// export/import. The tokens table rides the VACUUM INTO whole-file snapshot
// (only sha256 hashes travel, never plaintext), and validateImportCandidate is
// unchanged — its required-table set stays the v1 baseline, so the new table is
// neither required nor rejected. After import the token still resolves.
func TestExportImportWithTokens(t *testing.T) {
	t.Setenv("AGENTMAN_URL", "http://127.0.0.1:19999")

	srcDB := filepath.Join(t.TempDir(), "src.db")
	store, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateCategory("work", "Work"); err != nil {
		t.Fatal(err)
	}
	plain, _, err := store.CreateToken(Scope{Category: "work"})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := exportDB(srcDB, exportPath); err != nil {
		t.Fatal(err)
	}
	if err := validateImportCandidate(exportPath); err != nil {
		t.Fatalf("export with tokens rejected: %v", err)
	}

	destDB := filepath.Join(t.TempDir(), "dest.db")
	if err := importDB(exportPath, destDB, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}

	store2, err := OpenStore(destDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	toks, err := store2.ListTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 1 || toks[0].Category != "work" {
		t.Fatalf("imported tokens = %+v, want 1 bound to work", toks)
	}
	sc, err := store2.ResolveToken(plain)
	if err != nil || sc.Category != "work" {
		t.Fatalf("ResolveToken after import = (%+v, %v), want work", sc, err)
	}
}
