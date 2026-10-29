package table

import "testing"

// mustTable returns a declared table or fails the test.
func mustTable(t *testing.T, db *tableDB, name string) Table {
	t.Helper()
	tbl, err := db.Table(name)
	if err != nil {
		t.Fatalf("Table(%q): %v", name, err)
	}
	return tbl
}
