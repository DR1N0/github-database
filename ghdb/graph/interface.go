package graph

import (
	"encoding/json"

	"github.com/DR1N0/github-database/ghdb/base"
)

// GraphSchema describes the vertex labels and edge labels of a graph DB.
type GraphSchema struct {
	Vertices []base.VertexSpec
	Edges    []base.EdgeSpec
}

// EdgeResult holds one edge and its properties.
// Label, From, and To are always populated.
type EdgeResult struct {
	Label string          `json:"label"`
	From  string          `json:"from"`
	To    string          `json:"to"`
	Props json.RawMessage `json:"props,omitempty"`
}

type GraphDB interface {
	base.DB
	Schema() GraphSchema
	Vertex(label string) VertexSet
	AddEdge(label, from, to string) error
	RemoveEdge(label, from, to string) error
	OutNeighbors(label, id string) []string
	InNeighbors(label, id string) []string

	// SetEdge creates or replaces an edge from→to with the given properties.
	// Pass nil props to create a property-less edge (equivalent to AddEdge).
	SetEdge(label, from, to string, props json.RawMessage) error

	// PatchEdge merges fields into the existing edge properties.
	// If the edge does not exist, it is created with fields as its initial properties.
	PatchEdge(label, from, to string, fields map[string]json.RawMessage) error

	// GetEdge returns the properties of the edge from→to.
	// Returns nil props for a property-less edge (ok=true).
	// Returns ok=false when the edge does not exist.
	GetEdge(label, from, to string) (props json.RawMessage, ok bool)

	// OutEdges returns all outgoing edges from id via label, with their properties.
	// Pass label="" to return edges across all labels.
	OutEdges(label, id string) []EdgeResult

	// InEdges returns all incoming edges to id via label, with their properties.
	// Pass label="" to return edges across all labels.
	InEdges(label, id string) []EdgeResult
}

type VertexSet interface {
	Get(id string) (json.RawMessage, bool)
	Set(id string, value json.RawMessage) error
	Patch(id string, fields map[string]json.RawMessage) error
	Delete(id string) error
	All() map[string]json.RawMessage
}
