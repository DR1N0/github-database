package table

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DR1N0/github-database/ghdb/base"
)

var ErrKeyMismatch = errors.New("ghdb: key mismatch")
var ErrRequiredMissing = errors.New("ghdb: required column missing")

// tableDB is the concrete implementation of TableStore (unexported).
type tableDB struct {
	eng    base.Engine
	tables map[string]*table
}

// table holds in-memory rows for one named table (unexported).
type table struct {
	eng  base.Engine
	spec base.TableSpec
	data map[string]json.RawMessage
}

// New creates a TableDB and its engine.
func New(cfg base.Config, data map[string]map[string]json.RawMessage) (TableDB, base.Engine) {
	eng := base.NewBaseDB(cfg)
	tdb := &tableDB{
		eng:    eng,
		tables: make(map[string]*table, len(cfg.Tables)),
	}
	for _, spec := range cfg.Tables {
		d := data[spec.Name]
		if d == nil {
			d = map[string]json.RawMessage{}
		}
		tdb.tables[spec.Name] = &table{eng: eng, spec: spec, data: d}
	}
	eng.SetApplyFn(func(r base.MutationRecord) { applyToTableDB(tdb.tables, r) })
	eng.SetSnapshotFn(func() (map[string][]byte, error) {
		files := make(map[string][]byte, len(tdb.tables))
		for name, tbl := range tdb.tables {
			// Normalize each row through a map so json.Marshal sorts field keys,
			// keeping checkpoints diff-stable regardless of insertion order.
			normalized := make(map[string]map[string]json.RawMessage, len(tbl.data))
			for k, v := range tbl.data {
				var row map[string]json.RawMessage
				if err := json.Unmarshal(v, &row); err != nil {
					return nil, err
				}
				normalized[k] = row
			}
			d, err := json.MarshalIndent(normalized, "", "  ")
			if err != nil {
				return nil, err
			}
			files["tables/"+name+".json"] = d
		}
		return files, nil
	})
	return tdb, eng
}

// GetInternal returns the engine for the given TableDB.
func GetInternal(s TableDB) base.Engine {
	return s.(*tableDB).eng
}

func (db *tableDB) Mode() string             { return "table" }
func (db *tableDB) IsOnline() bool           { return db.eng.IsOnline() }
func (db *tableDB) Schema() []base.TableSpec { return db.eng.GetConfig().Tables }
func (db *tableDB) Table(name string) Table  { return db.tables[name] }

func (db *tableDB) Checkpoint(ctx context.Context) error {
	if !db.eng.IsOnline() {
		return base.ErrOffline
	}
	return db.eng.Checkpoint(ctx)
}

func (db *tableDB) Close(ctx context.Context) error {
	return db.eng.Close(ctx)
}

// validateKeyField checks that the key field in row matches key.
// When requirePresent is true (Set), the key field must exist; for Patch it is optional.
func (t *table) validateKeyField(key string, row map[string]json.RawMessage, requirePresent bool) error {
	if t.spec.Key == "" {
		return nil
	}
	fieldRaw, ok := row[t.spec.Key]
	if !ok {
		if !requirePresent {
			return nil
		}
		return fmt.Errorf("ghdb: missing key field %q in value", t.spec.Key)
	}
	var fieldVal string
	if err := json.Unmarshal(fieldRaw, &fieldVal); err != nil {
		return fmt.Errorf("ghdb: key field %q must be a string: %w", t.spec.Key, err)
	}
	if fieldVal != key {
		return fmt.Errorf("%w: Set key %q but %s field is %q", ErrKeyMismatch, key, t.spec.Key, fieldVal)
	}
	return nil
}

// validateRequired checks that all columns marked Required are present in row (used by Set).
func (t *table) validateRequired(row map[string]json.RawMessage) error {
	for _, col := range t.spec.Columns {
		if col.Required {
			if _, ok := row[col.Name]; !ok {
				return fmt.Errorf("%w: %q", ErrRequiredMissing, col.Name)
			}
		}
	}
	return nil
}

// Get returns the raw JSON value for key, and whether it exists.
func (t *table) Get(key string) (json.RawMessage, bool) {
	t.eng.RLockData()
	defer t.eng.RUnlockData()
	v, ok := t.data[key]
	return v, ok
}

// Set writes value under key and buffers a mutation record.
// Returns ErrKeyMismatch if the value's key field does not match key.
func (t *table) Set(key string, value json.RawMessage) error {
	var row map[string]json.RawMessage
	if err := json.Unmarshal(value, &row); err != nil {
		return fmt.Errorf("ghdb: invalid JSON: %w", err)
	}
	if err := t.validateKeyField(key, row, true); err != nil {
		return err
	}
	if err := t.validateRequired(row); err != nil {
		return err
	}
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "set", Table: t.spec.Name, Key: key, Value: value}
	if err := t.eng.ValidateMutation(record); err != nil {
		return err
	}
	t.eng.LockCkptRead()
	defer t.eng.UnlockCkptRead()
	t.eng.LockData()
	t.data[key] = value
	t.eng.UnlockData()
	t.eng.AppendMutation(record)
	t.eng.Logger().Printf("ghdb: set %s/%s", t.spec.Name, key)
	return nil
}

// Patch merges fields into the existing record (upsert if missing) and buffers a mutation.
// Returns ErrKeyMismatch if fields contains the key field with a value that does not match key.
func (t *table) Patch(key string, fields map[string]json.RawMessage) error {
	if err := t.validateKeyField(key, fields, false); err != nil {
		return err
	}
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "patch", Table: t.spec.Name, Key: key, Fields: fields}
	if err := t.eng.ValidateMutation(record); err != nil {
		return err
	}
	t.eng.LockCkptRead()
	defer t.eng.UnlockCkptRead()
	t.eng.LockData()
	merged, err := base.JSONMerge(t.data[key], fields)
	if err != nil {
		t.eng.UnlockData()
		return err
	}
	t.data[key] = merged
	t.eng.UnlockData()
	t.eng.AppendMutation(record)
	t.eng.Logger().Printf("ghdb: patch %s/%s", t.spec.Name, key)
	return nil
}

// Delete removes key from the table and buffers a mutation record.
func (t *table) Delete(key string) error {
	record := base.MutationRecord{TS: time.Now().UTC(), Op: "delete", Table: t.spec.Name, Key: key}
	if err := t.eng.ValidateMutation(record); err != nil {
		return err
	}
	t.eng.LockCkptRead()
	defer t.eng.UnlockCkptRead()
	t.eng.LockData()
	delete(t.data, key)
	t.eng.UnlockData()
	t.eng.AppendMutation(record)
	t.eng.Logger().Printf("ghdb: delete %s/%s", t.spec.Name, key)
	return nil
}

// All returns a snapshot copy of all rows in the table.
func (t *table) All() map[string]json.RawMessage {
	t.eng.RLockData()
	defer t.eng.RUnlockData()
	out := make(map[string]json.RawMessage, len(t.data))
	for k, v := range t.data {
		out[k] = v
	}
	return out
}

// applyToTableDB replays a MutationRecord onto the tables map (called under mu.Lock by the engine).
func applyToTableDB(tables map[string]*table, r base.MutationRecord) {
	tbl, ok := tables[r.Table]
	if !ok {
		return
	}
	switch r.Op {
	case "set":
		tbl.data[r.Key] = r.Value
	case "patch":
		merged, err := base.JSONMerge(tbl.data[r.Key], r.Fields)
		if err == nil {
			tbl.data[r.Key] = merged
		}
	case "delete":
		delete(tbl.data, r.Key)
	}
}

// newTableDB is a test helper that returns the concrete *tableDB.
func newTableDB(cfg base.Config, data map[string]map[string]json.RawMessage) *tableDB {
	store, _ := New(cfg, data)
	return store.(*tableDB)
}
