package graph

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DR1N0/github-database/ghdb/base"
)

// TestVertexWritesRequireMatchingID covers the contract that makes checkpoint
// output loadable: vertex files are flat arrays, so the "id" field is the only
// key available when the snapshot is read back.
func TestVertexWritesRequireMatchingID(t *testing.T) {
	db := makeGraphDB(t)
	vs := mustVertex(t, db, "component")

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"missing id", `{"name":"svc-x"}`},
		{"mismatched id", `{"id":"other","name":"svc-x"}`},
	} {
		if err := vs.Set("svc-x", json.RawMessage(tc.value)); !errors.Is(err, ErrVertexIDMismatch) {
			t.Errorf("%s: Set error = %v, want ErrVertexIDMismatch", tc.name, err)
		}
	}
	if err := vs.Set("svc-x", json.RawMessage(`{"id":1}`)); err == nil {
		t.Error("non-string id: Set error = nil, want rejection")
	}
	if got := base.EngineWbufLen(db.eng); got != 0 {
		t.Fatalf("write buffer length = %d, want 0 after rejected writes", got)
	}

	if err := vs.Set("svc-x", json.RawMessage(`{"id":"svc-x"}`)); err != nil {
		t.Fatalf("Set with matching id: %v", err)
	}
	if err := vs.Patch("svc-x", map[string]json.RawMessage{"id": json.RawMessage(`"other"`)}); !errors.Is(err, ErrVertexIDMismatch) {
		t.Errorf("Patch with mismatched id: error = %v, want ErrVertexIDMismatch", err)
	}
	if err := vs.Patch("svc-x", map[string]json.RawMessage{"name": json.RawMessage(`"renamed"`)}); err != nil {
		t.Errorf("Patch without id field: %v", err)
	}
}

// TestCheckpointSnapshotReloadsVertices is the regression test for the defect
// where a checkpointed graph could not be opened again.
func TestCheckpointSnapshotReloadsVertices(t *testing.T) {
	db := makeGraphDB(t)
	if err := mustVertex(t, db, "component").Set("svc-c", json.RawMessage(`{"id":"svc-c","name":"svc-c"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	files, err := base.EngineSnapshot(db.eng)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: content}
	}

	cfg := base.Config{
		Vertices: []base.VertexSpec{{Label: "component"}},
		Edges:    []base.EdgeSpec{{Label: "dep"}, {Label: "uses"}},
	}
	verts, edges, err := base.LoadGraphState(fsys, cfg)
	if err != nil {
		t.Fatalf("checkpoint snapshot cannot be reloaded: %v", err)
	}
	if _, ok := verts["component"]["svc-c"]; !ok {
		t.Fatalf("reloaded vertices = %v, want svc-c present", verts["component"])
	}
	if len(edges["dep"]["svc-a"]) != 1 {
		t.Fatalf("reloaded edges = %v, want dep svc-a -> svc-b", edges["dep"])
	}
}

// TestGraphValuesAreIndependentCopies mirrors the table-mode test for vertices
// and edge properties.
func TestGraphValuesAreIndependentCopies(t *testing.T) {
	db := makeGraphDB(t)
	vs := mustVertex(t, db, "component")

	src := json.RawMessage(`{"id":"svc-c","name":"svc-c"}`)
	if err := vs.Set("svc-c", src); err != nil {
		t.Fatalf("Set: %v", err)
	}
	src[0] = 'X'
	got, _ := vs.Get("svc-c")
	if got[0] != '{' {
		t.Fatalf("stored vertex aliases the caller's buffer: %s", got)
	}
	got[0] = 'X'
	again, _ := vs.Get("svc-c")
	if again[0] != '{' {
		t.Fatalf("Get returned bytes that alias database state: %s", again)
	}
	all := vs.All()
	all["svc-c"][0] = 'X'
	third, _ := vs.Get("svc-c")
	if third[0] != '{' {
		t.Fatalf("All returned bytes that alias database state: %s", third)
	}

	props := json.RawMessage(`{"weight":1}`)
	if err := db.SetEdge("dep", "svc-a", "svc-c", props); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	props[0] = 'X'
	stored, ok := db.GetEdge("dep", "svc-a", "svc-c")
	if !ok {
		t.Fatal("GetEdge = missing")
	}
	if stored[0] != '{' {
		t.Fatalf("stored edge properties alias the caller's buffer: %s", stored)
	}
	results := db.OutEdges("dep", "svc-a")
	for _, r := range results {
		if r.To == "svc-c" {
			if len(r.Props) == 0 || r.Props[0] != '{' {
				t.Fatalf("OutEdges returned aliased properties: %s", r.Props)
			}
			r.Props[0] = 'X'
		}
	}
	if again, _ := db.GetEdge("dep", "svc-a", "svc-c"); again[0] != '{' {
		t.Fatalf("OutEdges mutated database state: %s", again)
	}
}

// TestApplyRejectsRemotelyCorruptedVertex keeps a malformed remote mutation from
// poisoning state instead of being skipped.
func TestApplyRejectsRemotelyCorruptedVertex(t *testing.T) {
	db := makeGraphDB(t)
	err := applyToGraphDB(db.vertices, db.edges, base.MutationRecord{
		Op: "set_vertex", Label: "component", ID: "svc-z", Value: json.RawMessage(`{"name":"svc-z"}`),
	})
	if !errors.Is(err, ErrVertexIDMismatch) {
		t.Fatalf("apply error = %v, want ErrVertexIDMismatch", err)
	}
	if _, ok := mustVertex(t, db, "component").Get("svc-z"); ok {
		t.Fatal("corrupted remote vertex was applied")
	}
}

// TestLoadRejectsVertexWithoutID documents the load-side error a malformed
// baseline or hand-edited data file produces.
func TestLoadRejectsVertexWithoutID(t *testing.T) {
	fsys := fstest.MapFS{
		"vertices/component.json": {Data: []byte(`[{"name":"svc-a"}]`)},
	}
	_, _, err := base.LoadGraphState(fsys, base.Config{Vertices: []base.VertexSpec{{Label: "component"}}})
	if err == nil {
		t.Fatal("LoadGraphState error = nil, want missing-id error")
	}
	if !strings.Contains(err.Error(), "missing id field") {
		t.Fatalf("error = %v, want mention of missing id field", err)
	}
}
