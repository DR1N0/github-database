package base

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
)

// loggerOr returns logger, or the process-wide default when none was supplied.
func loggerOr(logger *log.Logger) *log.Logger {
	if logger != nil {
		return logger
	}
	return log.Default()
}

// LoadTableState reads the baseline table files using log.Default().
func LoadTableState(fsys fs.FS, cfg Config) (map[string]map[string]json.RawMessage, error) {
	return loadTableState(fsys, cfg, nil)
}

// LoadTableStateWithLogger is LoadTableState with an explicit logger, so a
// caller-supplied Option.Logger receives baseline messages instead of the
// process-wide default logger.
func LoadTableStateWithLogger(fsys fs.FS, cfg Config, logger *log.Logger) (map[string]map[string]json.RawMessage, error) {
	return loadTableState(fsys, cfg, logger)
}

// LoadGraphState reads the baseline graph files using log.Default().
func LoadGraphState(fsys fs.FS, cfg Config) (
	vertices map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]json.RawMessage,
	err error,
) {
	return loadGraphState(fsys, cfg, nil)
}

// LoadGraphStateWithLogger is LoadGraphState with an explicit logger.
func LoadGraphStateWithLogger(fsys fs.FS, cfg Config, logger *log.Logger) (
	vertices map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]json.RawMessage,
	err error,
) {
	return loadGraphState(fsys, cfg, logger)
}

func loadTableState(fsys fs.FS, cfg Config, logger *log.Logger) (map[string]map[string]json.RawMessage, error) {
	out := make(map[string]map[string]json.RawMessage, len(cfg.Tables))
	for _, spec := range cfg.Tables {
		data, err := loadJSONTable(fsys, spec, logger)
		if err != nil {
			return nil, err
		}
		out[spec.Name] = data
	}
	return out, nil
}

func loadJSONTable(fsys fs.FS, spec TableSpec, logger *log.Logger) (map[string]json.RawMessage, error) {
	path := "tables/" + spec.Name + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			loggerOr(logger).Printf("ghdb: %s not found, using empty table", path)
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
		From       string          `json:"from"`
		To         string          `json:"to"`
		Properties json.RawMessage `json:"properties,omitempty"`
	} `json:"edges"`
}

func loadGraphState(fsys fs.FS, cfg Config, logger *log.Logger) (
	vertices map[string]map[string]json.RawMessage,
	edges map[string]map[string]map[string]json.RawMessage,
	err error,
) {
	vertices = make(map[string]map[string]json.RawMessage, len(cfg.Vertices))
	edges = make(map[string]map[string]map[string]json.RawMessage, len(cfg.Edges))

	for _, vspec := range cfg.Vertices {
		data, e := loadVertexFile(fsys, vspec.Label, logger)
		if e != nil {
			return nil, nil, e
		}
		vertices[vspec.Label] = data
	}
	for _, espec := range cfg.Edges {
		adj, e := loadEdgeFile(fsys, espec.Label, logger)
		if e != nil {
			return nil, nil, e
		}
		edges[espec.Label] = adj
	}
	return vertices, edges, nil
}

func loadVertexFile(fsys fs.FS, label string, logger *log.Logger) (map[string]json.RawMessage, error) {
	path := "vertices/" + label + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			loggerOr(logger).Printf("ghdb: %s not found, using empty vertex store", path)
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
		// Every vertex file record is a flat array entry, so the id field is the
		// only key available at load time. Vertex writes enforce that id: see
		// graph.VertexSet.Set.
		idRaw, ok := obj["id"]
		if !ok {
			return nil, fmt.Errorf("ghdb: vertex in %s has missing id field: every vertex value must contain an \"id\" string equal to its vertex id", path)
		}
		var id string
		if err := json.Unmarshal(idRaw, &id); err != nil {
			return nil, fmt.Errorf("ghdb: vertex in %s has invalid id: %w", path, err)
		}
		result[id] = raw
	}
	return result, nil
}

func loadEdgeFile(fsys fs.FS, label string, logger *log.Logger) (map[string]map[string]json.RawMessage, error) {
	path := "edges/" + label + ".json"
	f, err := fsys.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			loggerOr(logger).Printf("ghdb: %s not found, using empty edge set", path)
			return map[string]map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("ghdb: open %s: %w", path, err)
	}
	defer f.Close()

	var ef edgeFile
	if err := json.NewDecoder(f).Decode(&ef); err != nil {
		return nil, fmt.Errorf("ghdb: decode %s: %w", path, err)
	}
	adj := make(map[string]map[string]json.RawMessage, len(ef.Edges))
	for _, e := range ef.Edges {
		if adj[e.From] == nil {
			adj[e.From] = map[string]json.RawMessage{}
		}
		adj[e.From][e.To] = e.Properties // nil when field absent in JSON
	}
	return adj, nil
}
