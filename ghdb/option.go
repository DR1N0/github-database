package ghdb

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"

	"github.com/DR1N0/github-database/ghdb/base"
	"github.com/DR1N0/github-database/ghdb/github"
	"github.com/DR1N0/github-database/ghdb/graph"
	"github.com/DR1N0/github-database/ghdb/table"
)

// Option is the entry point for opening a ghdb database.
type Option struct {
	baseline fs.FS
	tokenFn  func() string
	logger   *log.Logger
	client   github.Interface // non-nil only in tests
}

// NewOption creates an Option rooted at baseline (an fs.FS holding db_meta.json and data files).
func NewOption(baseline fs.FS) *Option {
	return &Option{baseline: baseline}
}

// Token sets the function that returns the GitHub token.
// If fn returns empty the DB opens in offline mode (in-memory; Checkpoint errors).
func (o *Option) Token(fn func() string) *Option {
	o.tokenFn = fn
	return o
}

// Logger sets the logger for online-mode startup messages. Defaults to log.Default().
func (o *Option) Logger(l *log.Logger) *Option {
	o.logger = l
	return o
}

// withClient injects a pre-built GitHub client, bypassing token-based construction.
// Used exclusively in tests via export_test.go.
func (o *Option) withClient(gh github.Interface) *Option {
	o.client = gh
	return o
}

// OpenTable opens a table-mode database and returns it as a TableDB.
// Returns an error if db_meta.json declares a different mode.
func (o *Option) OpenTable() (TableDB, error) {
	db, err := o.Open()
	if err != nil {
		return nil, err
	}
	tdb, ok := db.(TableDB)
	if !ok {
		return nil, fmt.Errorf("ghdb: expected table mode, got %q", db.Mode())
	}
	return tdb, nil
}

// OpenGraph opens a graph-mode database and returns it as a GraphDB.
// Returns an error if db_meta.json declares a different mode.
func (o *Option) OpenGraph() (GraphDB, error) {
	db, err := o.Open()
	if err != nil {
		return nil, err
	}
	gdb, ok := db.(GraphDB)
	if !ok {
		return nil, fmt.Errorf("ghdb: expected graph mode, got %q", db.Mode())
	}
	return gdb, nil
}

// Open opens the database and returns the base DB interface.
// Use OpenTable or OpenGraph when the mode is known at compile time.
func (o *Option) Open() (DB, error) {
	cfg, db, eng, err := parseAndLoad(o.baseline)
	if err != nil {
		return nil, err
	}

	eng.SetLogger(o.logger)

	gh := o.client
	if gh == nil {
		var token string
		if o.tokenFn != nil {
			token = o.tokenFn()
		}
		if token == "" {
			return db, nil
		}
		gh = github.NewGitHubClient(cfg.GitHubRepo, token)
	}

	if err := eng.StartOnline(gh); err != nil {
		return nil, err
	}
	return db, nil
}

// parseAndLoad reads db_meta.json and constructs the in-memory DB.
// Returns config, DB, and the internal engine (for StartOnline).
func parseAndLoad(baseline fs.FS) (base.Config, DB, base.Engine, error) {
	metaData, err := fs.ReadFile(baseline, "db_meta.json")
	if err != nil {
		return base.Config{}, nil, nil, fmt.Errorf("ghdb: read db_meta.json: %w", err)
	}
	var cfg base.Config
	if err := json.Unmarshal(metaData, &cfg); err != nil {
		return base.Config{}, nil, nil, fmt.Errorf("ghdb: parse db_meta.json: %w", err)
	}

	var db DB
	var eng base.Engine
	switch cfg.Mode {
	case "table":
		data, err := base.LoadTableState(baseline, cfg)
		if err != nil {
			return base.Config{}, nil, nil, err
		}
		db, eng = table.New(cfg, data)
	case "graph":
		verts, edges, err := base.LoadGraphState(baseline, cfg)
		if err != nil {
			return base.Config{}, nil, nil, err
		}
		db, eng = graph.New(cfg, verts, edges)
	default:
		return base.Config{}, nil, nil, fmt.Errorf("ghdb: unknown mode %q", cfg.Mode)
	}
	return cfg, db, eng, nil
}

// ── Type aliases ──────────────────────────────────────────────────────────────

type Config = base.Config
type TableSpec = base.TableSpec
type ColumnSpec = base.ColumnSpec
type VertexSpec = base.VertexSpec
type PropertySpec = base.PropertySpec
type EdgeSpec = base.EdgeSpec
type GraphSchema = graph.GraphSchema
type DB = base.DB
type MutationRecord = base.MutationRecord

type TableDB = table.TableDB
type Table = table.Table

var ErrKeyMismatch = table.ErrKeyMismatch
var ErrRequiredMissing = table.ErrRequiredMissing
var ErrOffline = base.ErrOffline

type GraphDB = graph.GraphDB
type VertexSet = graph.VertexSet
