package table

import (
	"encoding/json"
	"testing"
)

// TestTableValuesAreIndependentCopies guards the public contract that Get, All,
// and Set never alias caller-owned or database-owned byte slices. Before this
// was enforced, mutating a value returned by Get corrupted the table and could
// make a later checkpoint fail to serialize.
func TestTableValuesAreIndependentCopies(t *testing.T) {
	db := makeTableDB(t)
	tbl := db.Table("components")

	src := json.RawMessage(`{"name":"svc-c","version":"3.0"}`)
	if err := tbl.Set("svc-c", src); err != nil {
		t.Fatalf("Set: %v", err)
	}
	src[0] = 'X'
	got, ok := tbl.Get("svc-c")
	if !ok {
		t.Fatal("Get(svc-c) = missing")
	}
	if got[0] != '{' {
		t.Fatalf("stored value aliases the caller's buffer: %s", got)
	}

	got[0] = 'X'
	again, _ := tbl.Get("svc-c")
	if again[0] != '{' {
		t.Fatalf("Get returned bytes that alias database state: %s", again)
	}

	all := tbl.All()
	all["svc-c"][0] = 'X'
	third, _ := tbl.Get("svc-c")
	if third[0] != '{' {
		t.Fatalf("All returned bytes that alias database state: %s", third)
	}
}

// TestPatchCopiesFieldsBeforeBuffering ensures a caller that reuses its fields
// map after Patch cannot rewrite the buffered mutation record.
func TestPatchCopiesFieldsBeforeBuffering(t *testing.T) {
	db := makeTableDB(t)
	tbl := db.Table("components")

	fields := map[string]json.RawMessage{"version": json.RawMessage(`"9.9"`)}
	if err := tbl.Patch("svc-a", fields); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	fields["version"][0] = 'X'

	got, _ := tbl.Get("svc-a")
	var row map[string]string
	if err := json.Unmarshal(got, &row); err != nil {
		t.Fatalf("stored row is not valid JSON after caller mutation: %v (%s)", err, got)
	}
	if row["version"] != "9.9" {
		t.Fatalf("version = %q, want 9.9", row["version"])
	}
}
