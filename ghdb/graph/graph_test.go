package graph

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DR1N0/github-database/ghdb/base"
)

func TestOversizedMutationRecordRejectedBeforeGraphStateChange(t *testing.T) {
	db := makeGraphDB(t)
	payload, err := json.Marshal(map[string]string{"id": "svc-a", "payload": string(bytes.Repeat([]byte("x"), base.MaxSingleMutationBytes))})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Vertex("component").Set("svc-a", payload)
	var tooLarge *base.ErrMutationTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Vertex.Set error = %v, want ErrMutationTooLarge", err)
	}
	if got := base.EngineWbufLen(db.eng); got != 0 {
		t.Fatalf("write buffer length = %d, want 0", got)
	}
}

func TestOversizedGraphOperationsLeaveStateAndBufferUnchanged(t *testing.T) {
	big := string(bytes.Repeat([]byte("x"), base.MaxSingleMutationBytes))
	assertTooLarge := func(t *testing.T, err error) {
		t.Helper()
		var target *base.ErrMutationTooLarge
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want ErrMutationTooLarge", err)
		}
	}
	db := makeGraphDB(t)
	assertTooLarge(t, db.Vertex("component").Set(big, json.RawMessage(`{}`)))
	assertTooLarge(t, db.Vertex("component").Patch(big, map[string]json.RawMessage{}))
	assertTooLarge(t, db.SetEdge("dep", big, "to", json.RawMessage(`{}`)))
	assertTooLarge(t, db.PatchEdge("dep", big, "to", map[string]json.RawMessage{}))
	assertTooLarge(t, db.AddEdge("dep", big, "to"))
	assertTooLarge(t, db.RemoveEdge("dep", big, "to"))
	// Seed a cascade whose constructed delete-edge record exceeds the ceiling.
	db.edges[big] = map[string]map[string]json.RawMessage{"svc-a": {"svc-b": nil}}
	assertTooLarge(t, db.Vertex("component").Delete("svc-a"))
	if _, ok := db.Vertex("component").Get("svc-a"); !ok {
		t.Fatal("vertex changed after rejected cascade")
	}
	if _, ok := db.GetEdge(big, "svc-a", "svc-b"); !ok {
		t.Fatal("edge changed after rejected cascade")
	}
	if got := base.EngineWbufLen(db.eng); got != 0 {
		t.Fatalf("write buffer length = %d, want 0", got)
	}
}

func TestVertexDeleteSelfLoopEmitsOneCascadeMutation(t *testing.T) {
	db := makeGraphDB(t)
	db.edges = map[string]map[string]map[string]json.RawMessage{"dep": {"svc-a": {"svc-a": nil}}}
	if err := db.Vertex("component").Delete("svc-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Vertex("component").Get("svc-a"); ok {
		t.Fatal("vertex remains after delete")
	}
	if _, ok := db.GetEdge("dep", "svc-a", "svc-a"); ok {
		t.Fatal("self-loop remains after delete")
	}
	if got := base.EngineWbufLen(db.eng); got != 2 {
		t.Fatalf("queued records = %d, want one delete_edge and delete_vertex", got)
	}
	if got := base.EngineWbufOp(db.eng, 0); got != "delete_edge" {
		t.Fatalf("first queued op = %q, want delete_edge", got)
	}
	if got := base.EngineWbufOp(db.eng, 1); got != "delete_vertex" {
		t.Fatalf("second queued op = %q, want delete_vertex", got)
	}
}

func makeGraphDB(t *testing.T) *graphDB {
	t.Helper()
	verts := map[string]map[string]json.RawMessage{
		"component": {
			"svc-a": json.RawMessage(`{"id":"svc-a","name":"svc-a"}`),
			"svc-b": json.RawMessage(`{"id":"svc-b","name":"svc-b"}`),
		},
	}
	edges := map[string]map[string]map[string]json.RawMessage{
		"dep":  {"svc-a": {"svc-b": nil}},
		"uses": {"svc-a": {"lib-x": nil}},
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
	edges := map[string]map[string]map[string]json.RawMessage{
		"dep": {"svc-b": {"svc-a": nil}},
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
	edges := map[string]map[string]map[string]json.RawMessage{
		"dep": {
			"z-svc": {"a-svc": nil},
			"a-svc": {"z-svc": nil},
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

func TestSetEdgeCreatesEdgeWithProperties(t *testing.T) {
	db := makeGraphDB(t)
	props := json.RawMessage(`{"weight":5,"optional":false}`)
	if err := db.SetEdge("dep", "svc-a", "new-svc", props); err != nil {
		t.Fatal(err)
	}
	got, ok := db.GetEdge("dep", "svc-a", "new-svc")
	if !ok {
		t.Fatal("expected edge after SetEdge")
	}
	var m map[string]json.RawMessage
	json.Unmarshal(got, &m)
	var w int
	json.Unmarshal(m["weight"], &w)
	if w != 5 {
		t.Errorf("weight=%d want 5", w)
	}
}

func TestSetEdgeReplacesProperties(t *testing.T) {
	db := makeGraphDB(t)
	db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`{"weight":1}`))
	db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`{"weight":9}`))
	got, _ := db.GetEdge("dep", "svc-a", "svc-b")
	var m map[string]json.RawMessage
	json.Unmarshal(got, &m)
	var w int
	json.Unmarshal(m["weight"], &w)
	if w != 9 {
		t.Errorf("weight=%d want 9 after second SetEdge", w)
	}
}

func TestPatchEdgeMergesProperties(t *testing.T) {
	db := makeGraphDB(t)
	db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`{"weight":3,"label":"prod"}`))
	if err := db.PatchEdge("dep", "svc-a", "svc-b", map[string]json.RawMessage{
		"weight": json.RawMessage(`7`),
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetEdge("dep", "svc-a", "svc-b")
	var m map[string]json.RawMessage
	json.Unmarshal(got, &m)
	var w int
	var label string
	json.Unmarshal(m["weight"], &w)
	json.Unmarshal(m["label"], &label)
	if w != 7 {
		t.Errorf("weight=%d want 7", w)
	}
	if label != "prod" {
		t.Errorf("label=%q want prod (preserved by patch)", label)
	}
}

func TestPatchEdgeUpserts(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.PatchEdge("dep", "svc-a", "brand-new", map[string]json.RawMessage{
		"weight": json.RawMessage(`2`),
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := db.GetEdge("dep", "svc-a", "brand-new")
	if !ok {
		t.Fatal("PatchEdge should upsert a missing edge")
	}
	var m map[string]json.RawMessage
	json.Unmarshal(got, &m)
	var w int
	json.Unmarshal(m["weight"], &w)
	if w != 2 {
		t.Errorf("weight=%d want 2", w)
	}
}

func TestSetEdgeRejectsNonObjectProps(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`[1,2,3]`)); err == nil {
		t.Error("expected error for non-object props")
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

func TestGetEdgePropertyLessEdge(t *testing.T) {
	db := makeGraphDB(t)
	// "svc-a"→"svc-b" created by makeGraphDB via the edge map (nil props = property-less).
	props, ok := db.GetEdge("dep", "svc-a", "svc-b")
	if !ok {
		t.Fatal("expected edge to exist (created in makeGraphDB)")
	}
	if props != nil {
		t.Errorf("expected nil props for property-less edge, got %s", props)
	}
}

func TestGetEdgeMissingEdge(t *testing.T) {
	db := makeGraphDB(t)
	_, ok := db.GetEdge("dep", "svc-a", "does-not-exist")
	if ok {
		t.Error("expected ok=false for a missing edge")
	}
}

func TestOutEdgesWithProperties(t *testing.T) {
	db := makeGraphDB(t)
	db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`{"weight":4}`))
	results := db.OutEdges("dep", "svc-a")
	if len(results) != 1 {
		t.Fatalf("OutEdges len=%d want 1", len(results))
	}
	r := results[0]
	if r.Label != "dep" || r.From != "svc-a" || r.To != "svc-b" {
		t.Errorf("unexpected EdgeResult fields: %+v", r)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(r.Props, &m)
	var w int
	json.Unmarshal(m["weight"], &w)
	if w != 4 {
		t.Errorf("Props weight=%d want 4", w)
	}
}

func TestInEdgesWithProperties(t *testing.T) {
	db := makeGraphDB(t)
	db.SetEdge("dep", "svc-a", "svc-b", json.RawMessage(`{"weight":6}`))
	results := db.InEdges("dep", "svc-b")
	if len(results) != 1 {
		t.Fatalf("InEdges len=%d want 1", len(results))
	}
	r := results[0]
	if r.Label != "dep" || r.From != "svc-a" || r.To != "svc-b" {
		t.Errorf("unexpected EdgeResult fields: %+v", r)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(r.Props, &m)
	var w int
	json.Unmarshal(m["weight"], &w)
	if w != 6 {
		t.Errorf("Props weight=%d want 6", w)
	}
}

func TestSnapshotEdgePropertiesSorted(t *testing.T) {
	verts := map[string]map[string]json.RawMessage{
		"svc": {
			"a": json.RawMessage(`{"id":"a"}`),
			"b": json.RawMessage(`{"id":"b"}`),
		},
	}
	edges := map[string]map[string]map[string]json.RawMessage{
		"dep": {
			"a": {"b": json.RawMessage(`{"z":"last","weight":2}`)},
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
	want := "{\n  \"edgeLabel\": \"dep\",\n  \"edges\": [\n    {\n      \"from\": \"a\",\n      \"to\": \"b\",\n      \"properties\": {\n        \"weight\": 2,\n        \"z\": \"last\"\n      }\n    }\n  ]\n}"
	got := string(files["edges/dep.json"])
	if got != want {
		t.Errorf("edge snapshot mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddEdgeBackwardCompat(t *testing.T) {
	db := makeGraphDB(t)
	if err := db.AddEdge("dep", "svc-b", "svc-a"); err != nil {
		t.Fatal(err)
	}
	neighbors := db.OutNeighbors("dep", "svc-b")
	if len(neighbors) != 1 || neighbors[0] != "svc-a" {
		t.Errorf("OutNeighbors after AddEdge = %v, want [svc-a]", neighbors)
	}
	props, ok := db.GetEdge("dep", "svc-b", "svc-a")
	if !ok {
		t.Fatal("expected edge to exist after AddEdge")
	}
	if props != nil {
		t.Errorf("AddEdge should create property-less edge, got props %s", props)
	}
}

func TestOutEdgesAllLabels(t *testing.T) {
	// makeGraphDB has "dep": svc-a→svc-b and "uses": svc-a→lib-x.
	// OutEdges("", "svc-a") must return both.
	db := makeGraphDB(t)
	results := db.OutEdges("", "svc-a")
	if len(results) != 2 {
		t.Errorf("OutEdges(all labels) len=%d want 2", len(results))
	}
}

func TestInEdgesAllLabels(t *testing.T) {
	// makeGraphDB has "dep": svc-a→svc-b. Add a second label edge pointing at svc-b.
	db := makeGraphDB(t)
	db.SetEdge("uses", "svc-a", "svc-b", nil)
	results := db.InEdges("", "svc-b")
	if len(results) != 2 {
		t.Errorf("InEdges(all labels) len=%d want 2", len(results))
	}
}
