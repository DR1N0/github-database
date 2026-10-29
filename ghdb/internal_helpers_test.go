package ghdb

import "testing"

// mustTableOf returns a declared table or fails the test.
func mustTableOf(t *testing.T, db TableDB, name string) Table {
	t.Helper()
	tbl, err := db.Table(name)
	if err != nil {
		t.Fatalf("Table(%q): %v", name, err)
	}
	return tbl
}
