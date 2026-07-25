package base

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
)

func LoadTableState(fsys fs.FS, cfg Config) (map[string]map[string]json.RawMessage, error) {
	return loadTableState(fsys, cfg)
}

func LoadGraphState(fsys fs.FS, cfg Config) (
	vertices map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]struct{},
	err error,
) {
	return loadGraphState(fsys, cfg)
}

func loadTableState(fsys fs.FS, cfg Config) (map[string]map[string]json.RawMessage, error) {
	out := make(map[string]map[string]json.RawMessage, len(cfg.Tables))
	for _, spec := range cfg.Tables {
		data, err := loadJSONTable(fsys, spec)
		if err != nil {
			return nil, err
		}
		out[spec.Name] = data
	}
	return out, nil
}

func loadJSONTable(fsys fs.FS, spec TableSpec) (map[string]json.RawMessage, error) {
	path := "tables/" + spec.Name + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Printf("ghdb: %s not found, using empty table", path)
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("ghdb: open %s: %w", path, err)
	}
	defer f.Close()
	var data map[string]json.RawMessage
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, fmt.Errorf("ghdb: decode %s: %w", path, err)
	}
	if data == nil {
		data = map[string]json.RawMessage{}
	}
	return data, nil
}

type edgeFile struct {
	EdgeLabel string `json:"edgeLabel"`
	Edges     []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"edges"`
}

func loadGraphState(fsys fs.FS, cfg Config) (
	vertices map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]struct{},
	err error,
) {
	vertices = make(map[string]map[string]json.RawMessage, len(cfg.Vertices))
	edges = make(map[string]map[string]map[string]struct{}, len(cfg.Edges))

	for _, vspec := range cfg.Vertices {
		data, e := loadVertexFile(fsys, vspec.Label)
		if e != nil {
			return nil, nil, e
		}
		vertices[vspec.Label] = data
	}
	for _, espec := range cfg.Edges {
		adj, e := loadEdgeFile(fsys, espec.Label)
		if e != nil {
			return nil, nil, e
		}
		edges[espec.Label] = adj
	}
	return vertices, edges, nil
}

func loadVertexFile(fsys fs.FS, label string) (map[string]json.RawMessage, error) {
	path := "vertices/" + label + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Printf("ghdb: %s not found, using empty vertex store", path)
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("ghdb: open %s: %w", path, err)
	}
	defer f.Close()

	var records []json.RawMessage
	if err := json.NewDecoder(f).Decode(&records); err != nil {
		return nil, fmt.Errorf("ghdb: decode %s: %w", path, err)
	}
	result := make(map[string]json.RawMessage, len(records))
	for _, raw := range records {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("ghdb: parse vertex in %s: %w", path, err)
		}
		var id string
		if err := json.Unmarshal(obj["id"], &id); err != nil {
			return nil, fmt.Errorf("ghdb: vertex in %s has missing/invalid id: %w", path, err)
		}
		result[id] = raw
	}
	return result, nil
}

func loadEdgeFile(fsys fs.FS, label string) (map[string]map[string]struct{}, error) {
	path := "edges/" + label + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Printf("ghdb: %s not found, using empty edge set", path)
			return map[string]map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("ghdb: open %s: %w", path, err)
	}
	defer f.Close()

	var ef edgeFile
	if err := json.NewDecoder(f).Decode(&ef); err != nil {
		return nil, fmt.Errorf("ghdb: decode %s: %w", path, err)
	}
	adj := make(map[string]map[string]struct{}, len(ef.Edges))
	for _, e := range ef.Edges {
		if adj[e.From] == nil {
			adj[e.From] = map[string]struct{}{}
		}
		adj[e.From][e.To] = struct{}{}
	}
	return adj, nil
}
