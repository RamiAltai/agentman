package main

import (
	"database/sql"
	"net"
	"strings"
	"testing"
)

// cwfDbNoServer points AGENTMAN_URL at a loopback port that was free a moment
// ago, so the liveness probe finds no server, and returns that URL.
func cwfDbNoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	l.Close()
	t.Setenv("AGENTMAN_URL", url)
	return url
}

// cwfDbCount runs a single-integer query and fails the test on any error.
func cwfDbCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// FIND-0099: pruneEvents itself enforces "exactly one of before / keep" with a
// positive keep; keep==0 (the "unused" value) must not delete every event.
func TestCwfDbPruneRejectsAmbiguousArgs(t *testing.T) {
	p := cwfDbStore(t, t.TempDir(), "args.db", "p")
	total := cwfDbEventCount(t, p)
	for _, tc := range []struct {
		before string
		keep   int
	}{
		{"", 0},
		{"", -1},
		{"2024-01-01", 5},
		{"2024-01-01", -1},
	} {
		if _, err := pruneEvents(p, tc.before, tc.keep); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("pruneEvents(before=%q, keep=%d) err = %v, want 'exactly one'", tc.before, tc.keep, err)
		}
	}
	if got := cwfDbEventCount(t, p); got != total {
		t.Fatalf("events = %d after rejected prunes, want %d", got, total)
	}
}
