package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPruneEventsKeep(t *testing.T) {
	cwfDbNoServer(t)

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
	total := cwfDbCount(t, st.db, "SELECT COUNT(*) FROM events")
	if total < 3 {
		t.Fatalf("expected >=3 events seeded, got %d", total)
	}

	// Collect newest event IDs so we can verify them after prune.
	rows, err := st.db.Query("SELECT id FROM events ORDER BY id DESC LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	var newestIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		newestIDs = append(newestIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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
	if remaining := cwfDbCount(t, st2.db, "SELECT COUNT(*) FROM events"); remaining != 2 {
		t.Fatalf("expected 2 events after keep prune, got %d", remaining)
	}
	for _, nid := range newestIDs {
		if cwfDbCount(t, st2.db, "SELECT COUNT(*) FROM events WHERE id=?", nid) != 1 {
			t.Errorf("newest event id=%d should survive keep prune, but not found", nid)
		}
	}
}

func TestPruneEventsBefore(t *testing.T) {
	cwfDbNoServer(t)

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
	for _, ev := range []struct {
		kind, at string
		n        int
	}{{"test.old", oldDate, 3}, {"test.new", newDate, 2}} {
		for i := 0; i < ev.n; i++ {
			if _, err := st.db.Exec("INSERT INTO events(project_id, actor, kind, data, created_at) SELECT id, 'tester', ?, '{}', ? FROM projects WHERE slug='bproj'", ev.kind, ev.at); err != nil {
				t.Fatalf("seed %s: %v", ev.kind, err)
			}
		}
	}
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
	if remaining := cwfDbCount(t, st2.db, "SELECT COUNT(*) FROM events WHERE kind='test.new'"); remaining != 2 {
		t.Fatalf("expected 2 'test.new' events to survive, got %d", remaining)
	}
	if oldRemaining := cwfDbCount(t, st2.db, "SELECT COUNT(*) FROM events WHERE kind='test.old'"); oldRemaining != 0 {
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
	cwfDbNoServer(t)

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
	cwfDbNoServer(t)

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

// TestImportCreatesBackup: importing over an existing DB backs up the OLD
// contents (0600) and leaves the NEW contents at dest. Both DBs carry a
// distinct project so a swapped or empty backup cannot pass.
func TestImportCreatesBackup(t *testing.T) {
	cwfDbNoServer(t)

	srcDB := filepath.Join(t.TempDir(), "src.db")
	store, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateProject("newproj", "New", ""); err != nil {
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
	if _, _, err := store2.CreateProject("oldproj", "Old", ""); err != nil {
		t.Fatal(err)
	}
	store2.Close()

	backupDir := t.TempDir()
	if err := importDB(exportPath, destDB, backupDir, true); err != nil {
		t.Fatal(err)
	}

	// Exactly one backup file, 0600, holding the pre-import data.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 backup file in backupDir, got %d", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup %s perm = %o, want 0o600", entries[0].Name(), info.Mode().Perm())
	}
	if got := cwfDbProjects(t, filepath.Join(backupDir, entries[0].Name())); len(got) != 1 || got[0] != "oldproj" {
		t.Errorf("backup projects = %v, want [oldproj]", got)
	}
	if got := cwfDbProjects(t, destDB); len(got) != 1 || got[0] != "newproj" {
		t.Errorf("dest projects after import = %v, want [newproj]", got)
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
	if err := validateImportCandidate(f.Name()); err == nil || !strings.Contains(err.Error(), "integrity_check") {
		t.Errorf("validating garbage file: err = %v, want an integrity_check error", err)
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
	if isServerRunning(cwfDbNoServer(t)) {
		t.Error("expected isServerRunning=false on unused port")
	}
}

// ---------- Phase O: snapshots across the v4 boundary ----------

// TestExportContainsCategories: a snapshot of a current DB carries the
// categories table (VACUUM INTO copies everything).
func TestExportContainsCategories(t *testing.T) {
	cwfDbNoServer(t)

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

// TestImportRejectsOtherSchema: a snapshot stamped with any schema_version
// other than this am's is refused (there are no migrations to bridge it).
func TestImportRejectsOtherSchema(t *testing.T) {
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
	if err := validateImportCandidate(srcDB); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("schema_version %d snapshot: err = %v, want a schema_version error", currentSchemaVersion+1, err)
	}
}

// TestImportRejectsOtherSchemaHash: a snapshot made from a different
// schema.sql is refused even though its schema_version matches.
func TestImportRejectsOtherSchemaHash(t *testing.T) {
	srcDB := filepath.Join(t.TempDir(), "stale.db")
	st, err := OpenStore(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("UPDATE meta SET value='0000000000000000' WHERE key='schema_hash'"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := validateImportCandidate(srcDB); err == nil || !strings.Contains(err.Error(), "schema_hash") {
		t.Fatalf("snapshot from another schema: err = %v, want a schema_hash error", err)
	}
}

// TestExportImportWithTokens: a DB carrying tokens round-trips through
// export/import. The tokens table rides the VACUUM INTO whole-file snapshot
// (only sha256 hashes travel, never plaintext). After import the token still
// resolves.
func TestExportImportWithTokens(t *testing.T) {
	cwfDbNoServer(t)

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
