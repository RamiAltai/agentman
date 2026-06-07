package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pruneEvents deletes old events rows from dbPath. Exactly one of before or keep
// must be supplied (before as a YYYY-MM-DD string, keep as a positive count).
// Returns the number of rows deleted. VACUUM is run afterwards on a best-effort
// basis — a VACUUM error does NOT fail the prune.
func pruneEvents(dbPath, before string, keep int) (int64, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(1)&_pragma=synchronous(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
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
		dbPath := a.flag("db")
		if dbPath == "" {
			dbPath = defaultDBPath()
		}
		if err := exportDB(dbPath, outPath); err != nil {
			fail(1, "am db export: %v", err)
		}
		fmt.Println(outPath)
	case "import":
		srcPath := a.at(1)
		if srcPath == "" {
			fail(1, "am db import: missing source path")
		}
		dbPath := a.flag("db")
		if dbPath == "" {
			dbPath = defaultDBPath()
		}
		backupDir := filepath.Dir(dbPath)
		assumeYes := a.has("yes")
		if err := importDB(srcPath, dbPath, backupDir, assumeYes); err != nil {
			fail(1, "am db import: %v", err)
		}
	case "prune":
		beforeDate := a.flag("before")
		keepStr := a.flag("keep")
		dbPath := a.flag("db")
		if dbPath == "" {
			dbPath = defaultDBPath()
		}
		assumeYes := a.has("yes")

		// Require exactly one of --before / --keep.
		if (beforeDate == "") == (keepStr == "") {
			fail(1, "am db prune: provide exactly one of --before <YYYY-MM-DD> or --keep <N>")
		}
		var keepN int
		if keepStr != "" {
			v, err := strconv.Atoi(keepStr)
			if err != nil || v < 0 {
				fail(1, "am db prune: --keep must be a non-negative integer")
			}
			keepN = v
		}

		// Refuse while a server is running.
		if isServerRunning(envOr("AGENTMAN_URL", "http://127.0.0.1:8787")) {
			fail(1, "am db prune: stop `am serve` before pruning")
		}

		// Confirm unless --yes.
		if !assumeYes {
			if beforeDate != "" {
				fmt.Fprintf(os.Stderr, "This will delete events before %s from %s. Continue? [y/N] ", beforeDate, dbPath)
			} else {
				fmt.Fprintf(os.Stderr, "This will delete all but the newest %d events from %s. Continue? [y/N] ", keepN, dbPath)
			}
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			line := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if line != "y" && line != "yes" {
				fail(1, "aborted")
			}
		}

		n, err := pruneEvents(dbPath, beforeDate, keepN)
		if err != nil {
			fail(1, "am db prune: %v", err)
		}
		fmt.Fprintf(os.Stderr, "pruned %d events\n", n)
	default:
		fail(1, "usage: am db <export|import|prune> ...")
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

	// required tables
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

// importDB validates, optionally prompts, backs up, and replaces destPath with srcPath.
func importDB(srcPath, destPath, backupDir string, assumeYes bool) error {
	// 1. Validate the candidate
	if err := validateImportCandidate(srcPath); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// 2. Refuse if server is running
	if isServerRunning(envOr("AGENTMAN_URL", "http://127.0.0.1:8787")) {
		return errors.New("stop `am serve` before importing")
	}

	// 3. Confirm with user unless --yes
	if !assumeYes {
		fmt.Fprintf(os.Stderr, "This will replace %s with %s. Continue? [y/N] ", destPath, srcPath)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if line != "y" && line != "yes" {
			return errors.New("aborted")
		}
	}

	// 4. Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return err
	}

	// 5. Backup existing dest DB if it exists
	if _, err := os.Stat(destPath); err == nil {
		ts := strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339), ":", "-")
		backupPath := filepath.Join(backupDir, "agentman-backup-"+ts+".db")
		if err := copyFile(destPath, backupPath); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		// Remove stale WAL/SHM from dest
		os.Remove(destPath + "-wal")
		os.Remove(destPath + "-shm")
	}

	// 6. Copy src to dest
	if err := copyFile(srcPath, destPath); err != nil {
		return fmt.Errorf("copy failed: %w", err)
	}

	// 7. Remove WAL/SHM that may have been copied alongside dest
	os.Remove(destPath + "-wal")
	os.Remove(destPath + "-shm")

	return nil
}

// copyFile copies src to dst with 0o600 permissions.
func copyFile(src, dst string) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	_, retErr = io.Copy(out, in)
	return retErr
}
