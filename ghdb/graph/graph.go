package graph

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/DR1N0/github-database/ghdb/base"
)

// graphDB is the concrete implementation of GraphStore (unexported).
type graphDB struct {
	eng      base.Engine
	vertices map[string]*vertexSet
	edges    map[string]map[string]map[string]struct{} // label->from->to
}

// vertexSet holds in-memory vertex records for one labelled vertex type (unexported).
type vertexSet struct {
	db    *graphDB
	label string
	data  map[string]json.RawMessage
}

// New creates a GraphStore and its engine.
func New(
	cfg base.Config,
	verts map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]struct{},
) (GraphDB, base.Engine) {
	eng := base.NewBaseDB(cfg)
	if edges == nil {
		edges = map[string]map[string]map[string]struct{}{}
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
	eng.SetApplyFn(func(r base.MutationRecord) {
		applyToGraphDB(gdb.vertices, gdb.edges, r)
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

		type edgeEntry struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		type edgeFileJSON struct {
			EdgeLabel string      `json:"edgeLabel"`
			Edges     []edgeEntry `json:"edges"`
		}
		for label, fromMap := range gdb.edges {
			ef := edgeFileJSON{EdgeLabel: label, Edges: []edgeEntry{}}
			for from, toSet := range fromMap {
				for to := range toSet {
					ef.Edges = append(ef.Edges, edgeEntry{From: from, To: to})
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
	edges map[string]map[string]map[string]struct{},
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
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] == nil {
		db.edges[label] = map[string]map[string]struct{}{}
	}
	if db.edges[label][from] == nil {
		db.edges[label][from] = map[string]struct{}{}
	}
	db.edges[label][from][to] = struct{}{}
	db.eng.UnlockData()
	db.eng.AppendMutation(base.MutationRecord{TS: time.Now().UTC(), Op: "add_edge", From: from, Label: label, To: to})
	db.eng.Logger().Printf("ghdb: add edge %s: %s→%s", label, from, to)
	return nil
}

// RemoveEdge removes the directed edge from -[label]-> to.
func (db *graphDB) RemoveEdge(label, from, to string) error {
	db.eng.LockCkptRead()
	defer db.eng.UnlockCkptRead()
	db.eng.LockData()
	if db.edges[label] != nil && db.edges[label][from] != nil {
		delete(db.edges[label][from], to)
	}
	db.eng.UnlockData()
	db.eng.AppendMutation(base.MutationRecord{TS: time.Now().UTC(), Op: "remove_edge", From: from, Label: label, To: to})
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
	vs.db.eng.LockCkptRead()
	defer vs.db.eng.UnlockCkptRead()
	vs.db.eng.LockData()
	vs.data[id] = value
	vs.db.eng.UnlockData()
	vs.db.eng.AppendMutation(base.MutationRecord{TS: time.Now().UTC(), Op: "set_vertex", Label: vs.label, ID: id, Value: value})
	vs.db.eng.Logger().Printf("ghdb: set vertex/%s/%s", vs.label, id)
	return nil
}

// Patch merges fields into the existing vertex record (upsert if absent) and buffers a mutation.
func (vs *vertexSet) Patch(id string, fields map[string]json.RawMessage) error {
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
	vs.db.eng.AppendMutation(base.MutationRecord{TS: time.Now().UTC(), Op: "patch_vertex", Label: vs.label, ID: id, Fields: fields})
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
	delete(vs.data, id)
	for edgeLabel, fromMap := range vs.db.edges {
		for toID := range fromMap[id] {
			cascade = append(cascade, base.MutationRecord{TS: now, Op: "delete_edge", From: id, Label: edgeLabel, To: toID})
		}
		delete(fromMap, id)
		for fromID, targets := range fromMap {
			if _, exists := targets[id]; exists {
				cascade = append(cascade, base.MutationRecord{TS: now, Op: "delete_edge", From: fromID, Label: edgeLabel, To: id})
				delete(targets, id)
			}
		}
	}
	vs.db.eng.UnlockData()
	for _, r := range cascade {
		vs.db.eng.AppendMutation(r)
	}
	vs.db.eng.AppendMutation(base.MutationRecord{TS: now, Op: "delete_vertex", Label: vs.label, ID: id})
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
func applyToGraphDB(vertices map[string]*vertexSet, edges map[string]map[string]map[string]struct{}, r base.MutationRecord) {
	switch r.Op {
	case "set_vertex":
		if vs, ok := vertices[r.Label]; ok {
			vs.data[r.ID] = r.Value
		}
	case "patch_vertex":
		if vs, ok := vertices[r.Label]; ok {
			merged, err := base.JSONMerge(vs.data[r.ID], r.Fields)
			if err == nil {
				vs.data[r.ID] = merged
			}
		}
	case "delete_vertex":
		if vs, ok := vertices[r.Label]; ok {
			delete(vs.data, r.ID)
		}
	case "add_edge":
		if edges[r.Label] == nil {
			edges[r.Label] = map[string]map[string]struct{}{}
		}
		if edges[r.Label][r.From] == nil {
			edges[r.Label][r.From] = map[string]struct{}{}
		}
		edges[r.Label][r.From][r.To] = struct{}{}
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
	}
}
