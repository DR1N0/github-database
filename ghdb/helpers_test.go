package ghdb_test

import (
	"testing"

	"github.com/DR1N0/github-database/ghdb"
)

// mustTableOf returns a declared table or fails the test.
func mustTableOf(t *testing.T, db ghdb.TableDB, name string) ghdb.Table {
	t.Helper()
	tbl, err := db.Table(name)
	if err != nil {
		t.Fatalf("Table(%q): %v", name, err)
	}
	return tbl
}
