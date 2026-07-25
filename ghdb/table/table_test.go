package table

import (
	"encoding/json"
	"testing"

	"github.com/DR1N0/github-database/ghdb/base"
)

func makeTableDB(t *testing.T) *tableDB {
	t.Helper()
	data := map[string]map[string]json.RawMessage{
		"components": {
			"svc-a": json.RawMessage(`{"name":"svc-a","version":"1.0"}`),
			"svc-b": json.RawMessage(`{"name":"svc-b","version":"2.0"}`),
		},
	}
	cfg := base.Config{Tables: []base.TableSpec{
		{Name: "components", Key: "name"},
		{Name: "imagerepos", Key: "host",
			Columns: []base.ColumnSpec{{Name: "host", Required: true}, {Name: "tag"}}},
	}}
	return newTableDB(cfg, data)
}

func TestTableGet(t *testing.T) {
	db := makeTableDB(t)
	rec, ok := db.Table("components").Get("svc-a")
	if !ok {
		t.Fatal("expected svc-a")
	}
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["version"] != "1.0" {
		t.Errorf("version=%q want 1.0", m["version"])
	}
}

func TestTableSet(t *testing.T) {
	db := makeTableDB(t)
	tbl := db.Table("components")
	if err := tbl.Set("svc-c", json.RawMessage(`{"name":"svc-c","version":"3.0"}`)); err != nil {
		t.Fatal(err)
	}
	rec, ok := tbl.Get("svc-c")
	if !ok {
		t.Fatal("expected svc-c after Set")
	}
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["version"] != "3.0" {
		t.Errorf("version=%q want 3.0", m["version"])
	}
}

func TestTablePatchMerges(t *testing.T) {
	db := makeTableDB(t)
	tbl := db.Table("components")
	if err := tbl.Patch("svc-a", map[string]json.RawMessage{"version": json.RawMessage(`"9.0"`)}); err != nil {
		t.Fatal(err)
	}
	rec, _ := tbl.Get("svc-a")
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["version"] != "9.0" {
		t.Errorf("version=%q want 9.0", m["version"])
	}
	if m["name"] != "svc-a" {
		t.Errorf("name should be unchanged, got %q", m["name"])
	}
}

func TestTablePatchUpsert(t *testing.T) {
	db := makeTableDB(t)
	if err := db.Table("components").Patch("new-svc", map[string]json.RawMessage{"name": json.RawMessage(`"new-svc"`)}); err != nil {
		t.Fatal(err)
	}
	_, ok := db.Table("components").Get("new-svc")
	if !ok {
		t.Error("Patch on missing key should upsert")
	}
}

func TestTableDelete(t *testing.T) {
	db := makeTableDB(t)
	if err := db.Table("components").Delete("svc-a"); err != nil {
		t.Fatal(err)
	}
	_, ok := db.Table("components").Get("svc-a")
	if ok {
		t.Error("expected svc-a to be deleted")
	}
}

func TestTableAll(t *testing.T) {
	db := makeTableDB(t)
	all := db.Table("components").All()
	if len(all) != 2 {
		t.Errorf("All() len=%d want 2", len(all))
	}
}

func TestTableSchema(t *testing.T) {
	db := makeTableDB(t)
	schema := db.Schema()
	if len(schema) != 2 {
		t.Errorf("Schema() len=%d want 2", len(schema))
	}
	if schema[1].Columns[0].Name != "host" {
		t.Errorf("Columns[0].Name=%q want host", schema[1].Columns[0].Name)
	}
}

func TestTableSetPopulatesWriteBuffer(t *testing.T) {
	db := makeTableDB(t)
	db.Table("components").Set("svc-c", json.RawMessage(`{"name":"svc-c"}`))
	n := base.EngineWbufLen(db.eng)
	op := base.EngineWbufOp(db.eng, 0)
	if n != 1 {
		t.Errorf("wbuf len=%d want 1", n)
	}
	if op != "set" {
		t.Errorf("op=%q want set", op)
	}
}

func TestTableMode(t *testing.T) {
	db := makeTableDB(t)
	if db.Mode() != "table" {
		t.Errorf("Mode=%q want table", db.Mode())
	}
	if db.IsOnline() {
		t.Error("offline DB should not be online")
	}
}

func TestTableSetRequiredMissing(t *testing.T) {
	cfg := base.Config{Tables: []base.TableSpec{
		{Name: "things", Key: "id", Columns: []base.ColumnSpec{
			{Name: "id", Required: true},
			{Name: "name", Required: true},
		}},
	}}
	db := newTableDB(cfg, nil)
	err := db.Table("things").Set("x", json.RawMessage(`{"id":"x"}`)) // name missing
	if err == nil {
		t.Fatal("expected error for missing required column")
	}
}

func TestTableSetRequiredPresent(t *testing.T) {
	cfg := base.Config{Tables: []base.TableSpec{
		{Name: "things", Key: "id", Columns: []base.ColumnSpec{
			{Name: "id", Required: true},
			{Name: "name", Required: true},
		}},
	}}
	db := newTableDB(cfg, nil)
	if err := db.Table("things").Set("x", json.RawMessage(`{"id":"x","name":"Xander"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTablePatchSkipsRequiredCheck(t *testing.T) {
	cfg := base.Config{Tables: []base.TableSpec{
		{Name: "things", Key: "id", Columns: []base.ColumnSpec{
			{Name: "id", Required: true},
			{Name: "name", Required: true},
		}},
	}}
	db := newTableDB(cfg, map[string]map[string]json.RawMessage{
		"things": {"x": json.RawMessage(`{"id":"x","name":"old"}`)},
	})
	// Patch with only one field — should not fail required check
	if err := db.Table("things").Patch("x", map[string]json.RawMessage{
		"name": json.RawMessage(`"new"`),
	}); err != nil {
		t.Fatalf("Patch should not enforce required: %v", err)
	}
}
