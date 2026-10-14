package main

import "testing"

// cwfStoreCount runs a COUNT query and fails the test if it errors, so a
// broken query cannot pass a "count == 0" assertion with the zero value.
func cwfStoreCount(t *testing.T, st *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
