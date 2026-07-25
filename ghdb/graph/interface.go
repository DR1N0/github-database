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

type GraphDB interface {
	base.DB
	Schema() GraphSchema
	Vertex(label string) VertexSet
	AddEdge(label, from, to string) error
	RemoveEdge(label, from, to string) error
	OutNeighbors(label, id string) []string
	InNeighbors(label, id string) []string
}

type VertexSet interface {
	Get(id string) (json.RawMessage, bool)
	Set(id string, value json.RawMessage) error
	Patch(id string, fields map[string]json.RawMessage) error
	Delete(id string) error
	All() map[string]json.RawMessage
}
