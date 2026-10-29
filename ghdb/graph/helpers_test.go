package graph

import "testing"

// mustVertex returns a declared vertex set or fails the test.
func mustVertex(t *testing.T, db *graphDB, label string) VertexSet {
	t.Helper()
	vs, err := db.Vertex(label)
	if err != nil {
		t.Fatalf("Vertex(%q): %v", label, err)
	}
	return vs
}
