package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/DR1N0/github-database/ghdb/base"
)

// graphDB is the concrete implementation of GraphStore (unexported).
type graphDB struct {
	eng      base.Engine
	vertices map[string]*vertexSet
	edges    map[string]map[string]map[string]json.RawMessage // label->from->to->props (nil = property-less)
}

// vertexSet holds in-memory vertex records for one labelled vertex type (unexported).
type vertexSet struct {
	db    *graphDB
	label string
	data  map[string]json.RawMessage
}

func validateJSONObject(value json.RawMessage, what string) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		return fmt.Errorf("ghdb: %s must be a JSON object: %w", what, err)
	}
	if obj == nil {
		return fmt.Errorf("ghdb: %s must be a JSON object", what)
	}
	return nil
}

// New creates a GraphStore and its engine.
func New(
	cfg base.Config,
	verts map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]json.RawMessage,
) (GraphDB, base.Engine) {
	eng := base.NewBaseDB(cfg)
	if edges == nil {
		edges = map[string]map[string]map[string]json.RawMessage{}
	}
	gdb := &graphDB{
		eng:      eng,
		edges:    edges,
		vertices: make(map[string]*vertexSet, len(cfg.Vertices)),
	}
	for _, vspec := range cfg.Vertices {
		d := verts[vspec.Label]
		if d == nil {
			d = map[string]json.RawMessage{}
		}
		gdb.vertices[vspec.Label] = &vertexSet{db: gdb, label: vspec.Label, data: d}
	}
	eng.SetApplyFnWithError(func(r base.MutationRecord) error {
		return applyToGraphDB(gdb.vertices, gdb.edges, r)
	})
	eng.SetSnapshotFn(func() (map[string][]byte, error) {
		files := map[string][]byte{}

		for label, vs := range gdb.vertices {
			// Sort by ID and normalize fields for stable diff output.
			ids := make([]string, 0, len(vs.data))
			for id := range vs.data {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			records := make([]map[string]json.RawMessage, 0, len(ids))
			for _, id := range ids {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(vs.data[id], &obj); err != nil {
					return nil, err
				}
				records = append(records, obj)
			}
			data, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				return nil, err
			}
			files["vertices/"+label+".json"] = data
		}

		// Properties holds edge props as a sorted map for deterministic snapshot output.
		// Only JSON objects are supported; SetEdge must validate props is nil or a JSON object.
		type edgeEntry struct {
			From       string                     `json:"from"`
			To         string                     `json:"to"`
			Properties map[string]json.RawMessage `json:"properties,omitempty"`
		}
		type edgeFileJSON struct {
			EdgeLabel string      `json:"edgeLabel"`
			Edges     []edgeEntry `json:"edges"`
		}
		for label, fromMap := range gdb.edges {
			ef := edgeFileJSON{EdgeLabel: label, Edges: []edgeEntry{}}
			for from, toMap := range fromMap {
				for to, props := range toMap {
					entry := edgeEntry{From: from, To: to}
					if len(props) > 0 {
						var obj map[string]json.RawMessage
						if err := json.Unmarshal(props, &obj); err != nil {
							return nil, fmt.Errorf("ghdb: edge %s %s→%s has non-object props: %w", label, from, to, err)
						}
						entry.Properties = obj
					}
					ef.Edges = append(ef.Edges, entry)
				}
			}
			// Sort for stable diff output.
			sort.Slice(ef.Edges, func(i, j int) bool {
				if ef.Edges[i].From != ef.Edges[j].From {
					return ef.Edges[i].From < ef.Edges[j].From
				}
				return ef.Edges[i].To < ef.Edges[j].To
			})
			data, err := json.MarshalIndent(ef, "", "  ")
			if err != nil {
				return nil, err
			}
			files["edges/"+label+".json"] = data
		}

		return files, nil
	})
	return gdb, eng
}

// GetInternal returns the engine for the given GraphStore.
func GetInternal(s GraphDB) base.Engine {
	return s.(*graphDB).eng
}

// newGraphDB is a test helper that returns the concrete *graphDB.
func newGraphDB(
	cfg base.Config,
	verts map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]json.RawMessage,
) *graphDB {
	store, _ := New(cfg, verts, edges)
	return store.(*graphDB)
}

func (db *graphDB) Mode() string   { return "graph" }
func (db *graphDB) IsOnline() bool { return db.eng.IsOnline() }
func (db *graphDB) Schema() GraphSchema {
	cfg := db.eng.GetConfig()
	return GraphSchema{Vertices: cfg.Vertices, Edges: cfg.Edges}
}
func (db *graphDB) Vertex(label string) VertexSet { return db.vertices[label] }

func (db *graphDB) Checkpoint(ctx context.Context) error {
	if !db.eng.IsOnline() {
		return base.ErrOffline
	}
	return db.eng.Checkpoint(ctx)
}

func (db *graphDB) Close(ctx context.Context) error {
	return db.eng.Close(ctx)
}

// AddEdge adds a directed edge from -[label]-> to.
func (db *graphDB) AddEdge(label, from, to string) error {
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "add_edge", From: from, Label: label, To: to}
	if err := db.eng.ValidateMutation(record); err != nil {
		return err
	}
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] == nil {
		db.edges[label] = map[string]map[string]json.RawMessage{}
	}
	if db.edges[label][from] == nil {
		db.edges[label][from] = map[string]json.RawMessage{}
	}
	db.edges[label][from][to] = nil
	db.eng.UnlockData()
	db.eng.AppendMutation(record)
	db.eng.Logger().Printf("ghdb: add edge %s: %s→%s", label, from, to)
	return nil
}

// SetEdge creates or replaces an edge from→to with the given properties.
// props must be nil or a valid JSON object; a non-object value is rejected.
func (db *graphDB) SetEdge(label, from, to string, props json.RawMessage) error {
	if props != nil {
		if err := validateJSONObject(props, "SetEdge props"); err != nil {
			return err
		}
	}
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "set_edge", From: from, Label: label, To: to, Value: props}
	if err := db.eng.ValidateMutation(record); err != nil {
		return err
	}
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] == nil {
		db.edges[label] = map[string]map[string]json.RawMessage{}
	}
	if db.edges[label][from] == nil {
		db.edges[label][from] = map[string]json.RawMessage{}
	}
	db.edges[label][from][to] = props
	db.eng.UnlockData()
	db.eng.AppendMutation(record)
	db.eng.Logger().Printf("ghdb: set edge %s: %s→%s", label, from, to)
	return nil
}

// PatchEdge merges fields into the existing edge properties (upserts if absent).
func (db *graphDB) PatchEdge(label, from, to string, fields map[string]json.RawMessage) error {
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "patch_edge", From: from, Label: label, To: to, Fields: fields}
	if err := db.eng.ValidateMutation(record); err != nil {
		return err
	}
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] == nil {
		db.edges[label] = map[string]map[string]json.RawMessage{}
	}
	if db.edges[label][from] == nil {
		db.edges[label][from] = map[string]json.RawMessage{}
	}
	merged, err := base.JSONMerge(db.edges[label][from][to], fields)
	if err != nil {
		db.eng.UnlockData()
		return err
	}
	db.edges[label][from][to] = merged
	db.eng.UnlockData()
	db.eng.AppendMutation(record)
	db.eng.Logger().Printf("ghdb: patch edge %s: %s→%s", label, from, to)
	return nil
}

// GetEdge returns the properties of the edge from→to.
// Returns nil props for a property-less edge (ok=true). Returns ok=false when the edge does not exist.
func (db *graphDB) GetEdge(label, from, to string) (json.RawMessage, bool) {
	db.eng.RLockData()
	defer db.eng.RUnlockData()
	fromMap, ok := db.edges[label]
	if !ok {
		return nil, false
	}
	toMap, ok := fromMap[from]
	if !ok {
		return nil, false
	}
	props, ok := toMap[to]
	return props, ok
}

// OutEdges returns all outgoing edges from id via label, with their properties.
// Pass label="" to return edges across all labels.
func (db *graphDB) OutEdges(label, id string) []EdgeResult {
	db.eng.RLockData()
	defer db.eng.RUnlockData()
	var labels []string
	if label == "" {
		for l := range db.edges {
			labels = append(labels, l)
		}
	} else {
		labels = []string{label}
	}
	var out []EdgeResult
	for _, l := range labels {
		for toID, props := range db.edges[l][id] {
			out = append(out, EdgeResult{Label: l, From: id, To: toID, Props: props})
		}
	}
	return out
}

// InEdges returns all incoming edges to id via label, with their properties.
// Pass label="" to return edges across all labels.
func (db *graphDB) InEdges(label, id string) []EdgeResult {
	db.eng.RLockData()
	defer db.eng.RUnlockData()
	var labels []string
	if label == "" {
		for l := range db.edges {
			labels = append(labels, l)
		}
	} else {
		labels = []string{label}
	}
	var out []EdgeResult
	for _, l := range labels {
		for fromID, targets := range db.edges[l] {
			if props, ok := targets[id]; ok {
				out = append(out, EdgeResult{Label: l, From: fromID, To: id, Props: props})
			}
		}
	}
	return out
}

// RemoveEdge removes the directed edge from -[label]-> to.
func (db *graphDB) RemoveEdge(label, from, to string) error {
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "remove_edge", From: from, Label: label, To: to}
	if err := db.eng.ValidateMutation(record); err != nil {
		return err
	}
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] != nil && db.edges[label][from] != nil {
		delete(db.edges[label][from], to)
		if len(db.edges[label][from]) == 0 {
			delete(db.edges[label], from)
		}
		if len(db.edges[label]) == 0 {
			delete(db.edges, label)
		}
	}
	db.eng.UnlockData()
	db.eng.AppendMutation(record)
	db.eng.Logger().Printf("ghdb: remove edge %s: %s→%s", label, from, to)
	return nil
}

// OutNeighbors returns the IDs that id points to via the given edge label (all labels if label == "").
func (db *graphDB) OutNeighbors(label, id string) []string {
	db.eng.RLockData()
	defer db.eng.RUnlockData()
	var labels []string
	if label == "" {
		for l := range db.edges {
			labels = append(labels, l)
		}
	} else {
		labels = []string{label}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, l := range labels {
		for toID := range db.edges[l][id] {
			if _, dup := seen[toID]; !dup {
				seen[toID] = struct{}{}
				out = append(out, toID)
			}
		}
	}
	return out
}

// InNeighbors returns the IDs that point to id via the given edge label (all labels if label == "").
func (db *graphDB) InNeighbors(label, id string) []string {
	db.eng.RLockData()
	defer db.eng.RUnlockData()
	var labels []string
	if label == "" {
		for l := range db.edges {
			labels = append(labels, l)
		}
	} else {
		labels = []string{label}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, l := range labels {
		for fromID, targets := range db.edges[l] {
			if _, exists := targets[id]; exists {
				if _, dup := seen[fromID]; !dup {
					seen[fromID] = struct{}{}
					out = append(out, fromID)
				}
			}
		}
	}
	return out
}

// Get returns the raw JSON record for id, and whether it exists.
func (vs *vertexSet) Get(id string) (json.RawMessage, bool) {
	vs.db.eng.RLockData()
	defer vs.db.eng.RUnlockData()
	v, ok := vs.data[id]
	return v, ok
}

// Set writes value under id and buffers a mutation record.
func (vs *vertexSet) Set(id string, value json.RawMessage) error {
	if err := validateJSONObject(value, "vertex value"); err != nil {
		return err
	}
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "set_vertex", Label: vs.label, ID: id, Value: value}
	if err := vs.db.eng.ValidateMutation(record); err != nil {
		return err
	}
	vs.db.eng.LockCkptRead()
	defer vs.db.eng.UnlockCkptRead()
	vs.db.eng.LockData()
	vs.data[id] = value
	vs.db.eng.UnlockData()
	vs.db.eng.AppendMutation(record)
	vs.db.eng.Logger().Printf("ghdb: set vertex/%s/%s", vs.label, id)
	return nil
}

// Patch merges fields into the existing vertex record (upsert if absent) and buffers a mutation.
func (vs *vertexSet) Patch(id string, fields map[string]json.RawMessage) error {
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "patch_vertex", Label: vs.label, ID: id, Fields: fields}
	if err := vs.db.eng.ValidateMutation(record); err != nil {
		return err
	}
	vs.db.eng.LockCkptRead()
	defer vs.db.eng.UnlockCkptRead()
	vs.db.eng.LockData()
	merged, err := base.JSONMerge(vs.data[id], fields)
	if err != nil {
		vs.db.eng.UnlockData()
		return err
	}
	vs.data[id] = merged
	vs.db.eng.UnlockData()
	vs.db.eng.AppendMutation(record)
	vs.db.eng.Logger().Printf("ghdb: patch vertex/%s/%s", vs.label, id)
	return nil
}

// Delete removes a vertex and all edges that reference it, then buffers mutation records.
func (vs *vertexSet) Delete(id string) error {
	vs.db.eng.LockCkptRead()
	defer vs.db.eng.UnlockCkptRead()
	now := time.Now().UTC()
	var cascade []base.MutationRecord
	vs.db.eng.LockData()
	for edgeLabel, fromMap := range vs.db.edges {
		for toID := range fromMap[id] {
			cascade = append(cascade, base.MutationRecord{TS: now, Op: "delete_edge", From: id, Label: edgeLabel, To: toID})
		}
		for fromID, targets := range fromMap {
			if fromID != id {
				if _, exists := targets[id]; exists {
					cascade = append(cascade, base.MutationRecord{TS: now, Op: "delete_edge", From: fromID, Label: edgeLabel, To: id})
				}
			}
		}
	}
	deleteRecord := base.MutationRecord{TS: now, Op: "delete_vertex", Label: vs.label, ID: id}
	for _, r := range append(cascade, deleteRecord) {
		if err := vs.db.eng.ValidateMutation(r); err != nil {
			vs.db.eng.UnlockData()
			return err
		}
	}
	delete(vs.data, id)
	for _, r := range cascade {
		delete(vs.db.edges[r.Label][r.From], r.To)
	}
	vs.db.eng.UnlockData()
	for _, r := range cascade {
		vs.db.eng.AppendMutation(r)
	}
	vs.db.eng.AppendMutation(deleteRecord)
	vs.db.eng.Logger().Printf("ghdb: delete vertex/%s/%s", vs.label, id)
	return nil
}

// All returns a snapshot copy of all vertex records.
func (vs *vertexSet) All() map[string]json.RawMessage {
	vs.db.eng.RLockData()
	defer vs.db.eng.RUnlockData()
	out := make(map[string]json.RawMessage, len(vs.data))
	for k, v := range vs.data {
		out[k] = v
	}
	return out
}

// applyToGraphDB replays a MutationRecord onto the graph stores and edge maps (called under mu.Lock).
// It validates each semantic mutation before allocation or mutation. Edge labels
// intentionally remain dynamic: remote data can use labels absent from the baseline.
func applyToGraphDB(vertices map[string]*vertexSet, edges map[string]map[string]map[string]json.RawMessage, r base.MutationRecord) error {
	switch r.Op {
	case "set_vertex":
		vs, ok := vertices[r.Label]
		if !ok {
			return fmt.Errorf("ghdb: unknown vertex label %q", r.Label)
		}
		if err := validateJSONObject(r.Value, "vertex value"); err != nil {
			return err
		}
		vs.data[r.ID] = r.Value
	case "patch_vertex":
		vs, ok := vertices[r.Label]
		if !ok {
			return fmt.Errorf("ghdb: unknown vertex label %q", r.Label)
		}
		merged, err := base.JSONMerge(vs.data[r.ID], r.Fields)
		if err != nil {
			return fmt.Errorf("ghdb: patch vertex %s/%s: %w", r.Label, r.ID, err)
		}
		vs.data[r.ID] = merged
	case "delete_vertex":
		vs, ok := vertices[r.Label]
		if !ok {
			return fmt.Errorf("ghdb: unknown vertex label %q", r.Label)
		}
		delete(vs.data, r.ID)
	case "add_edge":
		ensureEdge(edges, r.Label, r.From)[r.To] = nil
	case "set_edge":
		if r.Value != nil {
			if err := validateJSONObject(r.Value, "edge properties"); err != nil {
				return err
			}
		}
		ensureEdge(edges, r.Label, r.From)[r.To] = r.Value
	case "patch_edge":
		var existing json.RawMessage
		if edges[r.Label] != nil && edges[r.Label][r.From] != nil {
			existing = edges[r.Label][r.From][r.To]
		}
		merged, err := base.JSONMerge(existing, r.Fields)
		if err != nil {
			return fmt.Errorf("ghdb: patch edge %s %s→%s: %w", r.Label, r.From, r.To, err)
		}
		ensureEdge(edges, r.Label, r.From)[r.To] = merged
	case "remove_edge", "delete_edge":
		if edges[r.Label] != nil && edges[r.Label][r.From] != nil {
			delete(edges[r.Label][r.From], r.To)
			if len(edges[r.Label][r.From]) == 0 {
				delete(edges[r.Label], r.From)
			}
			if len(edges[r.Label]) == 0 {
				delete(edges, r.Label)
			}
		}
	default:
		return fmt.Errorf("ghdb: unknown graph mutation %q", r.Op)
	}
	return nil
}

// ensureEdge initializes the dynamic edge-label and source maps after validation.
func ensureEdge(edges map[string]map[string]map[string]json.RawMessage, label, from string) map[string]json.RawMessage {
	if edges[label] == nil {
		edges[label] = map[string]map[string]json.RawMessage{}
	}
	if edges[label][from] == nil {
		edges[label][from] = map[string]json.RawMessage{}
	}
	return edges[label][from]
}
