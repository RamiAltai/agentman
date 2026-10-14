package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cwfDbSnapshot returns a valid import snapshot holding one project slug.
func cwfDbSnapshot(t *testing.T, slug string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src.db")
	st, err := OpenStore(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject(slug, slug, ""); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out := filepath.Join(t.TempDir(), "snap.db")
	if err := exportDB(src, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// cwfDbProjects lists project slugs in dbPath.
func cwfDbProjects(t *testing.T, dbPath string) []string {
	t.Helper()
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, err := st.ListProjects(false, "")
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range ps {
		slugs = append(slugs, p.Slug)
	}
	return slugs
}

func cwfDbCopy(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// FIND-0083: pruning a mistyped path must not create an empty database there.
func TestCwfDbPruneMissingDBCreatesNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "typo.db")
	if _, err := pruneEvents(dbPath, "", 10); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("pruneEvents on missing DB err = %v, want 'not found'", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("pruneEvents created %s (stat err %v)", dbPath, err)
	}
}

// FIND-0476: importing the live DB (directly or via a symlink) onto itself is refused.
func TestCwfDbImportRefusesSameFile(t *testing.T) {
	cwfDbNoServer(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "live.db")
	st, err := OpenStore(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("keepme", "Keep", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(dest, link); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{dest, link} {
		if err := importDB(src, dest, t.TempDir(), true); err == nil {
			t.Fatalf("importDB(%s onto itself) = nil, want error", src)
		}
	}
	if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "keepme" {
		t.Fatalf("live DB projects after refused self-import = %v, want [keepme]", got)
	}
}

// FIND-0096: a sidecar that cannot be removed fails the import before the copy.
func TestCwfDbImportFailsOnUnremovableSidecar(t *testing.T) {
	cwfDbNoServer(t)
	snap := cwfDbSnapshot(t, "p")
	dest := filepath.Join(t.TempDir(), "dest.db")
	// A non-empty directory named like the WAL: os.Remove fails with ENOTEMPTY.
	if err := os.MkdirAll(filepath.Join(dest+"-wal", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := importDB(snap, dest, t.TempDir(), true); err == nil {
		t.Fatal("importDB with unremovable -wal = nil, want error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest was written despite the sidecar failure (stat err %v)", err)
	}
}

// FIND-0081: commits left only in the WAL by an unclean shutdown survive in the backup.
func TestCwfDbBackupKeepsWALCommits(t *testing.T) {
	cwfDbNoServer(t)
	live := filepath.Join(t.TempDir(), "live.db")
	st, err := OpenStore(live)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("walonly", "WAL", ""); err != nil {
		t.Fatal(err)
	}
	// Freeze the on-disk state of a crashed server: main file + un-checkpointed WAL.
	crashDir := t.TempDir()
	dest := filepath.Join(crashDir, "crashed.db")
	cwfDbCopy(t, live, dest)
	if _, err := os.Stat(live + "-wal"); err != nil {
		t.Fatalf("expected a live WAL to copy: %v", err)
	}
	cwfDbCopy(t, live+"-wal", dest+"-wal")
	st.Close()

	backupDir := t.TempDir()
	if err := importDB(cwfDbSnapshot(t, "new"), dest, backupDir, true); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(backupDir, "agentman-backup-*.db"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
	if got := cwfDbProjects(t, backups[0]); len(got) != 1 || got[0] != "walonly" {
		t.Fatalf("backup projects = %v, want [walonly] (WAL commits lost)", got)
	}
	if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "new" {
		t.Fatalf("imported projects = %v, want [new]", got)
	}
}

// FIND-0095: the guard is the DB file itself, not the HTTP probe — an open
// store blocks import and prune even when AGENTMAN_URL points elsewhere.
func TestCwfDbGuardRefusesOpenDB(t *testing.T) {
	cwfDbNoServer(t)
	dest := filepath.Join(t.TempDir(), "busy.db")
	st, err := OpenStore(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateProject("live", "Live", ""); err != nil {
		t.Fatal(err)
	}
	if err := importDB(cwfDbSnapshot(t, "other"), dest, t.TempDir(), true); err == nil {
		t.Error("importDB onto an open DB = nil, want 'in use' error")
	}
	if _, err := pruneEvents(dest, "", 1); err == nil {
		t.Error("pruneEvents on an open DB = nil, want 'in use' error")
	}
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil || n == 0 {
		t.Errorf("events after refused prune = %d (err %v), want >0", n, err)
	}
	st.Close()

	// Once the store is closed the same calls go through.
	if err := ensureDBNotInUse(dest); err != nil {
		t.Fatalf("ensureDBNotInUse after Close: %v", err)
	}
	if got := cwfDbProjects(t, dest); len(got) != 1 || got[0] != "live" {
		t.Fatalf("projects after refused import = %v, want [live]", got)
	}
}

// FIND-0094: a copy that fails mid-stream leaves dst intact and no temp file;
// back-to-back imports keep separate backups.
func TestCwfDbCopyFileAtomicAndUniqueBackups(t *testing.T) {
	cwfDbNoServer(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.db")
	if err := os.WriteFile(dst, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Opening a directory succeeds; reading it fails, after dst would be opened.
	if err := copyFile(t.TempDir(), dst); err == nil {
		t.Fatal("copyFile from a directory = nil, want error")
	}
	if b, _ := os.ReadFile(dst); string(b) != "original" {
		t.Fatalf("dst after failed copy = %q, want %q", b, "original")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("dir after failed copy has %d entries, want 1 (temp file left behind)", len(entries))
	}

	dest := filepath.Join(t.TempDir(), "dest.db")
	backupDir := t.TempDir()
	for _, slug := range []string{"a", "b", "c"} {
		if err := importDB(cwfDbSnapshot(t, slug), dest, backupDir, true); err != nil {
			t.Fatal(err)
		}
	}
	if backups, _ := filepath.Glob(filepath.Join(backupDir, "agentman-backup-*.db")); len(backups) != 2 {
		t.Fatalf("backups after 3 imports = %v, want 2 distinct files", backups)
	}
}

// FIND-0098: `am db` honors AGENTMAN_DB like `am serve`; --db still wins.
func TestCwfDbPathFromArgsHonorsEnv(t *testing.T) {
	t.Setenv("AGENTMAN_DB", "/data/board.db")
	if got := dbPathFromArgs(parse([]string{"prune"})); got != "/data/board.db" {
		t.Errorf("dbPathFromArgs with AGENTMAN_DB = %q, want /data/board.db", got)
	}
	if got := dbPathFromArgs(parse([]string{"prune", "--db", "/x.db"})); got != "/x.db" {
		t.Errorf("dbPathFromArgs with --db = %q, want /x.db", got)
	}
	t.Setenv("AGENTMAN_DB", "")
	if got := dbPathFromArgs(parse([]string{"prune"})); got != defaultDBPath() {
		t.Errorf("dbPathFromArgs default = %q, want %q", got, defaultDBPath())
	}
}
