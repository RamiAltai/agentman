package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	_, _, err = store.CreateProject("testproj", "Test Project")
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
	projs, err := store2.ListProjects()
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

// TestIsServerRunning: quick sanity (server is NOT running in tests).
func TestIsServerRunning(t *testing.T) {
	// There should be no server on a random high port
	if isServerRunning("http://127.0.0.1:19999") {
		t.Error("expected isServerRunning=false on unused port")
	}
}
