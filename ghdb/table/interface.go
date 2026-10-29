package table

import (
	"encoding/json"

	"github.com/DR1N0/github-database/ghdb/base"
)

type TableDB interface {
	base.DB // Mode, IsOnline, Checkpoint, Close
	Schema() []base.TableSpec
	// Table returns the named table. It returns ErrUnknownTable when the name is
	// not declared in db_meta.json, so callers never hold a nil Table.
	Table(name string) (Table, error)
}

type Table interface {
	Get(key string) (json.RawMessage, bool)
	Set(key string, value json.RawMessage) error
	Patch(key string, fields map[string]json.RawMessage) error
	Delete(key string) error
	All() map[string]json.RawMessage
}
