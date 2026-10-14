package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// validDate reports whether s is a real calendar date in YYYY-MM-DD form.
func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// serverURL is the agentman server base URL: AGENTMAN_URL or the default.
func serverURL() string {
	return envOr("AGENTMAN_URL", "http://127.0.0.1:8787")
}

// confirm writes prompt to stderr and reports whether the next stdin line is
// "y" or "yes" (case-insensitive). EOF or anything else is "no".
func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt+" [y/N] ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	line := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return line == "y" || line == "yes"
}

// pruneEvents deletes old events rows from dbPath. Exactly one of before or keep
// must be supplied (before as a YYYY-MM-DD string, keep as a positive count; the
// unused one is "" or 0). Returns the number of rows deleted. VACUUM is run
// afterwards on a best-effort basis — a VACUUM error does NOT fail the prune.
func pruneEvents(dbPath, before string, keep int) (int64, error) {
	// keep==0 doubles as "not supplied", so it can never mean "keep nothing":
	// a caller passing a zero default must not wipe the whole events log.
	if (before == "") == (keep == 0) || keep < 0 {
		return 0, errors.New("provide exactly one of before (YYYY-MM-DD) or keep (a positive count)")
	}
	// Guard here too (not just in cmdDB): `before` feeds a string comparison
	// against ISO-8601 timestamps, which is only correct for well-formed dates.
	if before != "" && !validDate(before) {
		return 0, fmt.Errorf("--before must be a valid YYYY-MM-DD date (got %q)", before)
	}
	// sql.Open would silently create an empty database at a mistyped path.
	if _, err := os.Stat(dbPath); err != nil {
		return 0, fmt.Errorf("source database not found: %w", err)
	}
	if err := ensureDBNotInUse(dbPath); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", storeDSN(dbPath))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var res sql.Result
	if before != "" {
		res, err = db.Exec("DELETE FROM events WHERE created_at < ?", before)
	} else {
		// Delete all rows NOT in the newest keep rows.
		res, err = db.Exec("DELETE FROM events WHERE id NOT IN (SELECT id FROM events ORDER BY id DESC LIMIT ?)", keep)
	}
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// VACUUM to reclaim disk space; best-effort — don't fail if it errors.
	db.Exec("VACUUM")
	return n, nil
}

// dbPathFromArgs resolves the database path the way `am serve` does:
// --db, then AGENTMAN_DB, then the default.
func dbPathFromArgs(a Args) string {
	if p := a.flag("db"); p != "" {
		return p
	}
	return envOr("AGENTMAN_DB", defaultDBPath())
}

// cmdDB dispatches the "am db" subcommand.
func cmdDB(a Args) {
	sub := a.at(0)
	switch sub {
	case "export":
		outPath := a.at(1)
		if outPath == "" {
			ts := strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339), ":", "-")
			outPath = "agentman-export-" + ts + ".db"
		}
		dbPath := dbPathFromArgs(a)
		if err := exportDB(dbPath, outPath); err != nil {
			fail(1, "am db export: %v", err)
		}
		fmt.Println(outPath)
	case "import":
		srcPath := a.at(1)
		if srcPath == "" {
			usageErr("am db import: missing source path")
		}
		dbPath := dbPathFromArgs(a)
		backupDir := filepath.Dir(dbPath)
		assumeYes := a.has("yes")
		if err := importDB(srcPath, dbPath, backupDir, assumeYes); err != nil {
			fail(1, "am db import: %v", err)
		}
	case "prune":
		beforeDate := a.flag("before")
		keepStr := a.flag("keep")
		dbPath := dbPathFromArgs(a)
		assumeYes := a.has("yes")

		// Require exactly one of --before / --keep.
		if (beforeDate == "") == (keepStr == "") {
			usageErr("am db prune: provide exactly one of --before <YYYY-MM-DD> or --keep <N>")
		}
		// Validate the date up front (before the confirmation prompt); a typo
		// like 2026-13-99 would otherwise silently prune the wrong rows.
		if beforeDate != "" && !validDate(beforeDate) {
			usageErr("am db prune: --before must be a valid YYYY-MM-DD date (got %q)", beforeDate)
		}
		var keepN int
		if keepStr != "" {
			v, err := strconv.Atoi(keepStr)
			if err != nil || v < 1 {
				usageErr("am db prune: --keep must be a positive integer")
			}
			keepN = v
		}

		// Refuse while a server is running.
		if isServerRunning(serverURL()) {
			fail(1, "am db prune: stop `am serve` before pruning")
		}

		// Confirm unless --yes.
		if !assumeYes {
			prompt := fmt.Sprintf("This will delete events before %s from %s. Continue?", beforeDate, dbPath)
			if beforeDate == "" {
				prompt = fmt.Sprintf("This will delete all but the newest %d events from %s. Continue?", keepN, dbPath)
			}
			if !confirm(prompt) {
				fail(1, "aborted")
			}
		}

		n, err := pruneEvents(dbPath, beforeDate, keepN)
		if err != nil {
			fail(1, "am db prune: %v", err)
		}
		fmt.Fprintf(os.Stderr, "pruned %d events\n", n)
	default:
		usageErr("usage: am db <export|import|prune> ...")
	}
}

// exportDB creates a snapshot of dbPath at outPath using VACUUM INTO.
func exportDB(dbPath, outPath string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("source database not found: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	escaped := strings.ReplaceAll(outPath, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return err
	}

	return os.Chmod(outPath, 0o600)
}

// validateImportCandidate sanity-checks a DB file before importing.
func validateImportCandidate(srcPath string) error {
	db, err := sql.Open("sqlite", srcPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	// integrity_check
	var ic string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ic); err != nil {
		return fmt.Errorf("integrity_check failed: %w", err)
	}
	if ic != "ok" {
		return fmt.Errorf("integrity_check: %s", ic)
	}

	// foreign_key_check — must return 0 rows
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check failed: %w", err)
	}
	hasRows := rows.Next()
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check scan: %w", err)
	}
	if hasRows {
		return errors.New("foreign key violations found")
	}

	// required tables — deliberately the v1 baseline set, NOT the current
	// schema: later tables (task_deps, task_labels, categories) are created by
	// schema.sql/migrations on the next OpenStore, so pre-migration snapshots
	// stay importable. Do not add new tables here.
	required := map[string]bool{
		"projects": false,
		"tasks":    false,
		"comments": false,
		"events":   false,
		"meta":     false,
	}
	trows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return fmt.Errorf("sqlite_master query failed: %w", err)
	}
	for trows.Next() {
		var name string
		if err := trows.Scan(&name); err != nil {
			trows.Close()
			return err
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return fmt.Errorf("sqlite_master scan: %w", err)
	}
	for tbl, present := range required {
		if !present {
			return fmt.Errorf("missing required table: %s", tbl)
		}
	}

	// schema_version check
	var raw string
	verr := db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&raw)
	if verr == nil {
		v, perr := strconv.Atoi(strings.TrimSpace(raw))
		if perr == nil && v > currentSchemaVersion {
			return fmt.Errorf("schema_version %d is newer than supported %d", v, currentSchemaVersion)
		}
	}
	// missing or unparseable → treat as version 1 (compatible)

	return nil
}

// isServerRunning returns true if the agentman server responds with HTTP 200.
func isServerRunning(url string) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(url + "/api/projects")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ensureDBNotInUse fails if another connection holds dbPath — notably a
// running `am serve`, whose WAL-mode store keeps a SHARED lock on the file for
// as long as it is open, whatever URL it listens on. It takes and releases an
// exclusive lock without waiting. A missing file is not in use.
func ensureDBNotInUse(dbPath string) error {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=locking_mode(EXCLUSIVE)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("BEGIN EXCLUSIVE"); err != nil {
		return fmt.Errorf("%s is in use — stop `am serve` first (%w)", dbPath, err)
	}
	_, err = db.Exec("ROLLBACK")
	return err
}

// removeSidecars deletes dbPath's -wal and -shm files. A leftover WAL would be
// replayed into whatever main file sits at dbPath next, so only "not exist" is
// tolerated.
func removeSidecars(dbPath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// importDB validates, optionally prompts, backs up, and replaces destPath with srcPath.
func importDB(srcPath, destPath, backupDir string, assumeYes bool) error {
	// 1. Validate the candidate
	if err := validateImportCandidate(srcPath); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	// Importing the live DB onto itself would back it up, drop its WAL and
	// replace it with its own stale main file.
	if si, err := os.Stat(srcPath); err == nil {
		if di, err := os.Stat(destPath); err == nil && os.SameFile(si, di) {
			return fmt.Errorf("%s is the database being replaced", srcPath)
		}
	}

	// 2. Refuse if server is running
	if isServerRunning(serverURL()) {
		return errors.New("stop `am serve` before importing")
	}

	// 3. Confirm with user unless --yes
	if !assumeYes && !confirm(fmt.Sprintf("This will replace %s with %s. Continue?", destPath, srcPath)) {
		return errors.New("aborted")
	}

	// 4. Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return err
	}
	// Re-checked on the file itself: the HTTP probe only knows AGENTMAN_URL.
	if err := ensureDBNotInUse(destPath); err != nil {
		return err
	}

	// 5. Backup existing dest DB if it exists. VACUUM INTO (exportDB) folds in
	// commits still sitting in the WAL after an unclean shutdown, which a raw
	// copy of the main file would lose.
	backupNote := ""
	if _, err := os.Stat(destPath); err == nil {
		ts := strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "-")
		backupPath := filepath.Join(backupDir, "agentman-backup-"+ts+".db")
		if err := exportDB(destPath, backupPath); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "backed up %s to %s\n", destPath, backupPath)
		backupNote = " (previous database backed up to " + backupPath + ")"
	}

	// 6. Remove stale WAL/SHM from dest so they cannot be replayed into the
	// imported file.
	if err := removeSidecars(destPath); err != nil {
		return fmt.Errorf("remove stale WAL/SHM: %w", err)
	}

	// 7. Copy src to dest
	if err := copyFile(srcPath, destPath); err != nil {
		return fmt.Errorf("copy failed%s: %w", backupNote, err)
	}
	return nil
}

// copyFile copies src to dst with 0o600 permissions. It writes a temp file in
// dst's directory, syncs it and renames it over dst, so an interrupted copy
// never leaves dst truncated.
func copyFile(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
		if retErr == nil {
			retErr = os.Rename(out.Name(), dst)
		}
		if retErr != nil {
			os.Remove(out.Name())
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
