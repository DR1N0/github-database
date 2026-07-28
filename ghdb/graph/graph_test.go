package graph

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/DR1N0/github-database/ghdb/base"
)

func makeGraphDB(t *testing.T) *graphDB {
	t.Helper()
	verts := map[string]map[string]json.RawMessage{
		"component": {
			"svc-a": json.RawMessage(`{"id":"svc-a","name":"svc-a"}`),
			"svc-b": json.RawMessage(`{"id":"svc-b","name":"svc-b"}`),
		},
	}
	edges := map[string]map[string]map[string]struct{}{
		"dep":  {"svc-a": {"svc-b": {}}},
		"uses": {"svc-a": {"lib-x": {}}},
	}
	cfg := base.Config{
		Vertices: []base.VertexSpec{{Label: "component", Properties: []base.PropertySpec{{Name: "name"}}}},
		Edges:    []base.EdgeSpec{{Label: "dep"}, {Label: "uses"}},
	}
	return newGraphDB(cfg, verts, edges)
}

func TestVertexStoreGet(t *testing.T) {
	db := makeGraphDB(t)
	rec, ok := db.Vertex("component").Get("svc-a")
	if !ok {
		t.Fatal("expected svc-a")
	}
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["name"] != "svc-a" {
		t.Errorf("name=%q want svc-a", m["name"])
	}
}

func TestVertexStoreSet(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.Vertex("component").Set("svc-c", json.RawMessage(`{"id":"svc-c","name":"svc-c"}`)); err != nil {
		t.Fatal(err)
	}
	_, ok := db.Vertex("component").Get("svc-c")
	if !ok {
		t.Error("expected svc-c after Set")
	}
}

func TestVertexStorePatch(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.Vertex("component").Patch("svc-a", map[string]json.RawMessage{"version": json.RawMessage(`"9.0"`)}); err != nil {
		t.Fatal(err)
	}
	rec, _ := db.Vertex("component").Get("svc-a")
	var m map[string]string
	json.Unmarshal(rec, &m)
	if m["version"] != "9.0" {
		t.Errorf("version=%q want 9.0", m["version"])
	}
	if m["name"] != "svc-a" {
		t.Errorf("name unchanged, got %q", m["name"])
	}
}

func TestVertexStoreDeleteCascade(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.Vertex("component").Delete("svc-a"); err != nil {
		t.Fatal(err)
	}

	if _, ok := db.Vertex("component").Get("svc-a"); ok {
		t.Error("svc-a should be gone")
	}
	if len(db.edges["dep"]["svc-a"]) != 0 {
		t.Error("outgoing dep edges should be gone")
	}
	if len(db.edges["uses"]["svc-a"]) != 0 {
		t.Error("outgoing uses edges should be gone")
	}

	// write buffer: 2 delete_edge then 1 delete_vertex
	n := base.EngineWbufLen(db.eng)
	if n != 3 {
		t.Errorf("wbuf len=%d want 3 (2 delete_edge + 1 delete_vertex)", n)
	}
	if op := base.EngineWbufOp(db.eng, 0); op != "delete_edge" {
		t.Errorf("wbuf[0].Op=%q want delete_edge", op)
	}
}

func TestVertexStoreDeleteIncomingEdgeCascade(t *testing.T) {
	verts := map[string]map[string]json.RawMessage{
		"component": {
			"svc-a": json.RawMessage(`{"id":"svc-a"}`),
			"svc-b": json.RawMessage(`{"id":"svc-b"}`),
		},
	}
	edges := map[string]map[string]map[string]struct{}{
		"dep": {"svc-b": {"svc-a": {}}},
	}
	cfg := base.Config{Vertices: []base.VertexSpec{{Label: "component"}}, Edges: []base.EdgeSpec{{Label: "dep"}}}
	db := newGraphDB(cfg, verts, edges)
	db.Vertex("component").Delete("svc-a")
	if _, exists := db.edges["dep"]["svc-b"]["svc-a"]; exists {
		t.Error("incoming edge svc-b->svc-a should be removed on svc-a delete")
	}
}

func TestAddRemoveEdge(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.AddEdge("dep", "svc-b", "lib-y"); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.edges["dep"]["svc-b"]["lib-y"]; !ok {
		t.Error("edge svc-b->lib-y missing")
	}

	if err := db.RemoveEdge("dep", "svc-b", "lib-y"); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.edges["dep"]["svc-b"]["lib-y"]; ok {
		t.Error("edge should be removed")
	}
}

func TestOutNeighbors(t *testing.T) {
	db := makeGraphDB(t)
	n := db.OutNeighbors("dep", "svc-a")
	if len(n) != 1 || n[0] != "svc-b" {
		t.Errorf("OutNeighbors(dep)=%v want [svc-b]", n)
	}
	all := db.OutNeighbors("", "svc-a")
	if len(all) != 2 {
		t.Errorf("OutNeighbors(all)=%v want 2 results", all)
	}
}

func TestInNeighbors(t *testing.T) {
	db := makeGraphDB(t)
	n := db.InNeighbors("dep", "svc-b")
	if len(n) != 1 || n[0] != "svc-a" {
		t.Errorf("InNeighbors(dep)=%v want [svc-a]", n)
	}
}

func TestSnapshotVerticesAndEdgesSorted(t *testing.T) {
	verts := map[string]map[string]json.RawMessage{
		"svc": {
			// IDs deliberately out of alphabetical order; fields deliberately reversed.
			"z-svc": json.RawMessage(`{"name":"z","id":"z-svc"}`),
			"a-svc": json.RawMessage(`{"name":"a","id":"a-svc"}`),
		},
	}
	edges := map[string]map[string]map[string]struct{}{
		"dep": {
			"z-svc": {"a-svc": {}},
			"a-svc": {"z-svc": {}},
		},
	}
	cfg := base.Config{
		Vertices: []base.VertexSpec{{Label: "svc"}},
		Edges:    []base.EdgeSpec{{Label: "dep"}},
	}
	db := newGraphDB(cfg, verts, edges)

	files, err := base.EngineSnapshot(db.eng)
	if err != nil {
		t.Fatal(err)
	}

	// Vertices: a-svc must come before z-svc; fields within each sorted.
	wantVerts := `[
  {
    "id": "a-svc",
    "name": "a"
  },
  {
    "id": "z-svc",
    "name": "z"
  }
]`
	if !bytes.Equal(files["vertices/svc.json"], []byte(wantVerts)) {
		t.Errorf("vertices not sorted:\ngot:\n%s\nwant:\n%s", files["vertices/svc.json"], wantVerts)
	}

	// Edges: a-svc→z-svc must come before z-svc→a-svc.
	wantEdges := `{
  "edgeLabel": "dep",
  "edges": [
    {
      "from": "a-svc",
      "to": "z-svc"
    },
    {
      "from": "z-svc",
      "to": "a-svc"
    }
  ]
}`
	if !bytes.Equal(files["edges/dep.json"], []byte(wantEdges)) {
		t.Errorf("edges not sorted:\ngot:\n%s\nwant:\n%s", files["edges/dep.json"], wantEdges)
	}
}

func TestGraphSchema(t *testing.T) {
	db := makeGraphDB(t)
	s := db.Schema()
	if len(s.Vertices) != 1 {
		t.Errorf("Vertices len=%d want 1", len(s.Vertices))
	}
	if s.Vertices[0].Properties[0].Name != "name" {
		t.Error("expected property name=name")
	}
	if len(s.Edges) != 2 {
		t.Errorf("Edges len=%d want 2", len(s.Edges))
	}
}
