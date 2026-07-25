package base

import (
	"encoding/json"
	"testing"
	"testing/fstest"
)

func TestLoadJSONTable(t *testing.T) {
	fsys := fstest.MapFS{
		"tables/components.json": {Data: []byte(`{"svc-a":{"name":"svc-a","version":"1.0"},"svc-b":{"name":"svc-b","version":"2.0"}}`)},
	}
	cfg := Config{Tables: []TableSpec{{Name: "components", Key: "name"}}}
	tables, err := loadTableState(fsys, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := tables["components"]["svc-a"]
	if !ok {
		t.Fatal("expected record for svc-a")
	}
	var m map[string]string
	if err := json.Unmarshal(rec, &m); err != nil {
		t.Fatal(err)
	}
	if m["version"] != "1.0" {
		t.Errorf("version=%q want 1.0", m["version"])
	}
	if len(tables["components"]) != 2 {
		t.Errorf("expected 2 records, got %d", len(tables["components"]))
	}
}

func TestLoadTableMissingFileIsEmpty(t *testing.T) {
	fsys := fstest.MapFS{} // no files
	cfg := Config{Tables: []TableSpec{{Name: "missing", Key: "id"}}}
	tables, err := loadTableState(fsys, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables["missing"]) != 0 {
		t.Errorf("expected empty table, got %d records", len(tables["missing"]))
	}
}

func TestLoadMultipleTables(t *testing.T) {
	fsys := fstest.MapFS{
		"tables/a.json": {Data: []byte(`{"k1":{"id":"k1"}}`)},
		"tables/b.json": {Data: []byte(`{"k2":{"id":"k2"}}`)},
	}
	cfg := Config{Tables: []TableSpec{
		{Name: "a", Key: "id"},
		{Name: "b", Key: "id"},
	}}
	tables, err := loadTableState(fsys, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tables["a"]["k1"]; !ok {
		t.Error("expected a/k1")
	}
	if _, ok := tables["b"]["k2"]; !ok {
		t.Error("expected b/k2")
	}
}

func TestLoadGraphVertices(t *testing.T) {
	fsys := fstest.MapFS{
		"vertices/component.json": {Data: []byte(
			`[{"id":"svc-a","name":"svc-a","version":"1.0"},{"id":"svc-b","name":"svc-b","version":"2.0"}]`,
		)},
	}
	cfg := Config{
		Vertices: []VertexSpec{{Label: "component"}},
		Edges:    []EdgeSpec{},
	}
	verts, edges, err := loadGraphState(fsys, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(verts["component"]) != 2 {
		t.Errorf("got %d vertices, want 2", len(verts["component"]))
	}
	rec, ok := verts["component"]["svc-a"]
	if !ok {
		t.Fatal("expected vertex svc-a")
	}
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["name"] != "svc-a" {
		t.Errorf("name=%q want svc-a", m["name"])
	}
	_ = edges
}

func TestLoadGraphEdges(t *testing.T) {
	fsys := fstest.MapFS{
		"edges/sourcerepo.json": {Data: []byte(
			`{"edgeLabel":"sourcerepo","edges":[{"from":"svc-a","to":"repo-x"},{"from":"svc-b","to":"repo-x"}]}`,
		)},
	}
	cfg := Config{
		Vertices: []VertexSpec{},
		Edges:    []EdgeSpec{{Label: "sourcerepo"}},
	}
	_, edges, err := loadGraphState(fsys, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges["sourcerepo"]["svc-a"]) != 1 {
		t.Errorf("expected 1 edge from svc-a")
	}
	if _, ok := edges["sourcerepo"]["svc-a"]["repo-x"]; !ok {
		t.Error("expected edge svc-a->repo-x")
	}
	if _, ok := edges["sourcerepo"]["svc-b"]["repo-x"]; !ok {
		t.Error("expected edge svc-b->repo-x")
	}
}

func TestLoadGraphMissingFilesAreEmpty(t *testing.T) {
	fsys := fstest.MapFS{}
	cfg := Config{
		Vertices: []VertexSpec{{Label: "component"}},
		Edges:    []EdgeSpec{{Label: "dep"}},
	}
	verts, edges, err := loadGraphState(fsys, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(verts["component"]) != 0 {
		t.Error("expected empty vertex store")
	}
	if len(edges["dep"]) != 0 {
		t.Error("expected empty edge set")
	}
}
