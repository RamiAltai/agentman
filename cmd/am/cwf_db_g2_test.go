package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cwfDbStdin feeds s to os.Stdin for the rest of the test (the confirm prompt
// reads os.Stdin directly).
func cwfDbStdin(t *testing.T, s string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig; f.Close() })
}

// cwfDbLiveServer points AGENTMAN_URL at a server answering /api/projects with status.
func cwfDbLiveServer(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTMAN_URL", srv.URL)
	return srv.URL
}

// cwfDbStore creates a store at dir/name holding one project slug.
func cwfDbStore(t *testing.T, dir, name, slug string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	st, err := OpenStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject(slug, slug, ""); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return p
}

func cwfDbRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cwfDbExec runs stmts on dbPath with foreign keys off, so tests can build
// deliberately broken snapshots.
func cwfDbExec(t *testing.T, dbPath string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func cwfDbEventCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// FIND-0090: isServerRunning is true only for a 200 from /api/projects.
func TestCwfDbIsServerRunningStatus(t *testing.T) {
	if url := cwfDbLiveServer(t, http.StatusOK); !isServerRunning(url) {
		t.Error("isServerRunning(200) = false, want true")
	}
	if url := cwfDbLiveServer(t, http.StatusInternalServerError); isServerRunning(url) {
		t.Error("isServerRunning(500) = true, want false")
	}
}

// FIND-0090: every refusal path in importDB returns an error and leaves the
// existing destination byte-for-byte unchanged.
func TestCwfDbImportRefusalsLeaveDestUnchanged(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(garbage, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, want string
		setup      func(t *testing.T) (src, backupDir string)
		yes        bool
	}{
		{"invalid source", "validation failed", func(t *testing.T) (string, string) {
			cwfDbNoServer(t)
			return garbage, t.TempDir()
		}, true},
		{"server running", "stop `am serve`", func(t *testing.T) (string, string) {
			cwfDbLiveServer(t, http.StatusOK)
			return cwfDbSnapshot(t, "new"), t.TempDir()
		}, true},
		{"user aborts", "aborted", func(t *testing.T) (string, string) {
			cwfDbNoServer(t)
			cwfDbStdin(t, "n\n")
			return cwfDbSnapshot(t, "new"), t.TempDir()
		}, false},
		{"backup fails", "backup failed", func(t *testing.T) (string, string) {
			cwfDbNoServer(t)
			return cwfDbSnapshot(t, "new"), filepath.Join(t.TempDir(), "missing", "dir")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := cwfDbStore(t, t.TempDir(), "dest.db", "old")
			before := cwfDbRead(t, dest)
			src, backupDir := tc.setup(t)
			var err error
			captureStderr(t, func() { err = importDB(src, dest, backupDir, tc.yes) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("importDB err = %v, want %q", err, tc.want)
			}
			if !bytes.Equal(cwfDbRead(t, dest), before) {
				t.Fatal("destination changed after a refused import")
			}
			if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "old" {
				t.Fatalf("dest projects = %v, want [old]", got)
			}
		})
	}
}

// FIND-0090: answering "y" at the prompt goes through; a dest directory that
// cannot be created fails before anything is written.
func TestCwfDbImportConfirmAndMkdirFailure(t *testing.T) {
	cwfDbNoServer(t)
	dest := cwfDbStore(t, t.TempDir(), "dest.db", "old")
	cwfDbStdin(t, "yes\n")
	var err error
	captureStderr(t, func() { err = importDB(cwfDbSnapshot(t, "new"), dest, t.TempDir(), false) })
	if err != nil {
		t.Fatalf("importDB after 'yes' = %v", err)
	}
	if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "new" {
		t.Fatalf("dest projects after confirmed import = %v, want [new]", got)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	badDest := filepath.Join(file, "sub", "dest.db")
	if err := importDB(cwfDbSnapshot(t, "new"), badDest, t.TempDir(), true); err == nil {
		t.Fatal("importDB under a regular file = nil, want mkdir error")
	}
}

// FIND-0091: each rejection branch of validateImportCandidate fires and says which check failed.
func TestCwfDbValidateRejections(t *testing.T) {
	for _, tbl := range []string{"projects", "tasks", "comments", "events", "meta"} {
		t.Run("missing "+tbl, func(t *testing.T) {
			p := cwfDbStore(t, t.TempDir(), "src.db", "p")
			cwfDbExec(t, p, "DROP TABLE "+tbl)
			err := validateImportCandidate(p)
			if err == nil || !strings.Contains(err.Error(), "missing required table: "+tbl) {
				t.Fatalf("validate without %s: err = %v", tbl, err)
			}
		})
	}
	t.Run("foreign key violation", func(t *testing.T) {
		p := cwfDbStore(t, t.TempDir(), "src.db", "p")
		cwfDbExec(t, p, "INSERT INTO comments(task_id, author, body) VALUES (9999, 'x', 'orphan')")
		err := validateImportCandidate(p)
		if err == nil || !strings.Contains(err.Error(), "foreign key violations") {
			t.Fatalf("validate with orphan comment: err = %v", err)
		}
	})
	t.Run("integrity check", func(t *testing.T) {
		p := cwfDbStore(t, t.TempDir(), "src.db", "p")
		// Re-declare an existing index over a different column: its stored
		// entries no longer match the table, which integrity_check reports.
		cwfDbExec(t, p, "PRAGMA writable_schema=ON",
			"UPDATE sqlite_master SET sql='CREATE INDEX idx_events_since ON events(actor)' WHERE name='idx_events_since'")
		err := validateImportCandidate(p)
		if err == nil || !strings.Contains(err.Error(), "integrity_check") {
			t.Fatalf("validate corrupt index: err = %v", err)
		}
	})
}

// FIND-0105: keep boundaries — >= count deletes nothing, an empty events table
// is fine. keep 0 is rejected (FIND-0099, TestCwfDbPruneRejectsAmbiguousArgs).
func TestCwfDbPruneKeepBoundaries(t *testing.T) {
	cwfDbNoServer(t)
	p := cwfDbStore(t, t.TempDir(), "count.db", "p")
	total := cwfDbEventCount(t, p)
	if total == 0 {
		t.Fatal("expected seeded events")
	}
	for _, tc := range []struct {
		name       string
		keep       int
		empty      bool
		wantDel    int
		wantRemain int
	}{
		{"keep greater than count", total + 5, false, 0, total},
		{"keep equal to count", total, false, 0, total},
		{"empty table", 3, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := cwfDbStore(t, t.TempDir(), "e.db", "p")
			if tc.empty {
				cwfDbExec(t, db, "DELETE FROM events")
			}
			n, err := pruneEvents(db, "", tc.keep)
			if err != nil {
				t.Fatal(err)
			}
			if n != int64(tc.wantDel) {
				t.Errorf("deleted = %d, want %d", n, tc.wantDel)
			}
			if got := cwfDbEventCount(t, db); got != tc.wantRemain {
				t.Errorf("remaining = %d, want %d", got, tc.wantRemain)
			}
		})
	}
}

// FIND-0105: exportDB fails for a missing source or an existing output, and
// writes a path containing a single quote verbatim.
func TestCwfDbExportErrorsAndQuoting(t *testing.T) {
	dir := t.TempDir()
	src := cwfDbStore(t, dir, "src.db", "p")

	missing := filepath.Join(dir, "nope.db")
	if err := exportDB(missing, filepath.Join(dir, "out.db")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("exportDB(missing) err = %v, want 'not found'", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("exportDB created the missing source (stat err %v)", err)
	}

	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exportDB(src, existing); err == nil {
		t.Error("exportDB onto an existing file = nil, want error")
	}
	if b := cwfDbRead(t, existing); string(b) != "keep" {
		t.Errorf("existing output overwritten: %q", b)
	}

	quoted := filepath.Join(dir, "it's.db")
	if err := exportDB(src, quoted); err != nil {
		t.Fatalf("exportDB to %q: %v", quoted, err)
	}
	if got := cwfDbProjects(t, quoted); len(got) != 1 || got[0] != "p" {
		t.Fatalf("quoted export projects = %v, want [p]", got)
	}
}

// FIND-0101: cmdDB argument validation, liveness refusal, prompt and abort,
// driven through the osExit hook.
func TestCwfDbCmdDB(t *testing.T) {
	cwfDbNoServer(t)
	t.Setenv("AGENTMAN_DB", "")
	db := cwfDbStore(t, t.TempDir(), "board.db", "p")
	cwfDbExec(t, db, "INSERT INTO events(actor, kind) VALUES ('t', 'test'), ('t', 'test')")
	total := cwfDbEventCount(t, db)

	run := func(argv ...string) (code int, stderr string) {
		stderr = captureStderr(t, func() {
			code = captureExit(t, func() { cmdDB(parse(argv)) })
		})
		return code, stderr
	}

	for _, tc := range []struct {
		name string
		argv []string
		want string
		code int // usage errors exit 5 like every other verb (FIND-0055); I/O errors exit 1
	}{
		{"no subcommand", nil, "usage: am db", 5},
		{"unknown subcommand", []string{"nuke"}, "usage: am db", 5},
		{"import without source", []string{"import", "--db", db}, "missing source path", 5},
		{"prune neither flag", []string{"prune", "--db", db}, "exactly one of", 5},
		{"prune both flags", []string{"prune", "--db", db, "--keep", "1", "--before", "2024-01-01"}, "exactly one of", 5},
		{"prune bad date", []string{"prune", "--db", db, "--before", "2026-13-99", "--yes"}, "valid YYYY-MM-DD", 5},
		{"prune zero keep", []string{"prune", "--db", db, "--keep", "0", "--yes"}, "positive integer", 5},
		{"prune negative keep", []string{"prune", "--db", db, "--keep=-1", "--yes"}, "positive integer", 5},
		{"prune non-numeric keep", []string{"prune", "--db", db, "--keep", "abc", "--yes"}, "positive integer", 5},
		{"export missing source", []string{"export", filepath.Join(t.TempDir(), "o.db"), "--db", filepath.Join(t.TempDir(), "nope.db")}, "not found", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stderr := run(tc.argv...)
			if code != tc.code || !strings.Contains(stderr, tc.want) {
				t.Fatalf("cmdDB(%q) = exit %d, stderr %q; want exit %d with %q", tc.argv, code, stderr, tc.code, tc.want)
			}
			if got := cwfDbEventCount(t, db); got != total {
				t.Fatalf("events = %d after a refused command, want %d", got, total)
			}
		})
	}

	t.Run("prune refused while server runs", func(t *testing.T) {
		cwfDbLiveServer(t, http.StatusOK)
		if code, stderr := run("prune", "--db", db, "--keep", "1", "--yes"); code != 1 || !strings.Contains(stderr, "stop `am serve`") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := cwfDbEventCount(t, db); got != total {
			t.Fatalf("events = %d, want %d", got, total)
		}
	})

	t.Run("prune aborted at prompt", func(t *testing.T) {
		cwfDbStdin(t, "n\n")
		if code, stderr := run("prune", "--db", db, "--keep", "1"); code != 1 || !strings.Contains(stderr, "aborted") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := cwfDbEventCount(t, db); got != total {
			t.Fatalf("events = %d, want %d", got, total)
		}
	})

	t.Run("prune confirmed at prompt", func(t *testing.T) {
		cwfDbStdin(t, "y\n")
		code, stderr := run("prune", "--db", db, "--keep", "1")
		if code != -1 || !strings.Contains(stderr, "newest 1 events") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := cwfDbEventCount(t, db); got != 1 {
			t.Fatalf("events after keep=1 = %d, want 1", got)
		}
	})

	t.Run("export then import", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "snap.db")
		var code int
		stdout := captureStdout(t, func() {
			code = captureExit(t, func() { cmdDB(parse([]string{"export", out, "--db", db})) })
		})
		if code != -1 || strings.TrimSpace(stdout) != out {
			t.Fatalf("export: exit %d, stdout %q", code, stdout)
		}
		dest := cwfDbStore(t, t.TempDir(), "dest.db", "old")
		if code, stderr := run("import", out, "--db", dest, "--yes"); code != -1 {
			t.Fatalf("import: exit %d, stderr %q", code, stderr)
		}
		if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "p" {
			t.Fatalf("dest projects = %v, want [p]", got)
		}
		// The backup lands next to the database (backupDir = dir of --db).
		if b, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "agentman-backup-*.db")); len(b) != 1 {
			t.Fatalf("backups next to dest = %v, want 1", b)
		}
	})
}

// FIND-0092: the dest's stale -wal/-shm from an unclean shutdown are gone after
// import, so they cannot be replayed into the imported file.
func TestCwfDbImportRemovesStaleSidecars(t *testing.T) {
	cwfDbNoServer(t)
	live := filepath.Join(t.TempDir(), "live.db")
	st, err := OpenStore(live)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("walonly", "WAL", ""); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "crashed.db")
	cwfDbCopy(t, live, dest)
	cwfDbCopy(t, live+"-wal", dest+"-wal")
	st.Close()

	captureStderr(t, func() { err = importDB(cwfDbSnapshot(t, "new"), dest, t.TempDir(), true) })
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dest + suffix); !os.IsNotExist(err) {
			t.Errorf("%s left behind after import (stat err %v)", suffix, err)
		}
	}
	if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "new" {
		t.Fatalf("imported projects = %v, want [new]", got)
	}
}
