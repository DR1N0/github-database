package ghdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"time"

	"github.com/DR1N0/github-database/ghdb/base"
	"github.com/DR1N0/github-database/ghdb/github"
	"github.com/DR1N0/github-database/ghdb/graph"
	"github.com/DR1N0/github-database/ghdb/table"
)

// Option is the entry point for opening a ghdb database.
type Option struct {
	baseline             fs.FS
	tokenFn              func() string
	host                 string // GitHub host; defaults to "github.com"
	offlineFallback      bool
	logger               *log.Logger
	client               github.Interface // non-nil only in tests
	committerName        string
	committerEmail       string
	commitSigner         func([]byte) (string, error)
	startupProgress      base.StartupProgressFunc
	githubRequestTimeout time.Duration
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

// AllowOfflineFallback makes Open(ctx) return an offline DB instead of an error
// when online startup fails (e.g. bad credentials, GitHub unreachable). It never
// absorbs context cancellation or deadline errors. The returned DB will have
// IsOnline() == false and Checkpoint() will return ErrOffline.
func (o *Option) AllowOfflineFallback() *Option {
	o.offlineFallback = true
	return o
}

// Committer overrides the name and email used as the git committer for checkpoint commits.
// When not set, the authenticated GitHub user's identity is used.
func (o *Option) Committer(name, email string) *Option {
	o.committerName = name
	o.committerEmail = email
	return o
}

// CommitSigner sets the callback that signs checkpoint commits.
// payload is the raw git commit object. Return an ASCII-armored PGP detached signature.
// When nil (default), commits are created unsigned.
func (o *Option) CommitSigner(fn func(payload []byte) (armored string, err error)) *Option {
	o.commitSigner = fn
	return o
}

// Host sets the GitHub host. Defaults to "github.com".
// Use the hostname of your GitHub Enterprise Server (e.g. "github.mycompany.com").
func (o *Option) Host(host string) *Option {
	o.host = host
	return o
}

// Logger sets the logger for online-mode startup, replay, and polling messages. Defaults to log.Default().
func (o *Option) Logger(l *log.Logger) *Option {
	o.logger = l
	return o
}

// StartupProgress sets the callback for synchronous online startup progress notifications.
// Callbacks are serialized and run outside data locks; keep them short and do not
// re-enter startup from a callback.
func (o *Option) StartupProgress(fn StartupProgressFunc) *Option {
	o.startupProgress = fn
	return o
}

// GitHubRequestTimeout sets the timeout for each GitHub HTTP request during online operation.
// Non-positive values use the default timeout.
func (o *Option) GitHubRequestTimeout(timeout time.Duration) *Option {
	o.githubRequestTimeout = timeout
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
func (o *Option) OpenTable(ctx context.Context) (TableDB, error) {
	db, err := o.Open(ctx)
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
func (o *Option) OpenGraph(ctx context.Context) (GraphDB, error) {
	db, err := o.Open(ctx)
	if err != nil {
		return nil, err
	}
	gdb, ok := db.(GraphDB)
	if !ok {
		return nil, fmt.Errorf("ghdb: expected graph mode, got %q", db.Mode())
	}
	return gdb, nil
}

// Open opens the database with ctx and returns the base DB interface.
// Use OpenTable or OpenGraph when the mode is known at compile time. ctx controls
// online startup only; cancellation and deadline errors are returned directly.
func (o *Option) Open(ctx context.Context) (DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, db, eng, err := parseAndLoad(o.baseline)
	if err != nil {
		return nil, err
	}

	eng.SetLogger(o.logger)

	if o.commitSigner != nil && o.committerEmail == "" {
		return nil, fmt.Errorf("ghdb: CommitSigner requires Committer(name, email) — the email must match your GPG key registered on GitHub")
	}

	if o.committerName != "" {
		eng.SetCommitterIdentity(o.committerName, o.committerEmail)
	}
	eng.SetCommitSigner(o.commitSigner)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gh := o.client
	if gh == nil {
		var token string
		if o.tokenFn != nil {
			token = o.tokenFn()
		}
		if token == "" {
			return db, nil
		}
		gh = github.NewGitHubClient(cfg.GitHubRepo, token, o.host, o.githubRequestTimeout)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := eng.StartOnline(ctx, gh, o.startupProgress); err != nil {
		// Only the caller's context determines whether fallback is forbidden. A
		// per-request http.Client timeout can also wrap context.DeadlineExceeded,
		// but it is an ordinary online-startup failure while ctx remains active.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if o.offlineFallback {
			eng.Logger().Printf("ghdb: StartOnline failed (%v); falling back to offline mode", err)
			return db, nil
		}
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
	if cfg.MaxDeltaSegmentBytes < 0 || cfg.MaxDeltaSegmentBytes > base.MaxDeltaSegmentBytes {
		return base.Config{}, nil, nil, fmt.Errorf("ghdb: max_delta_segment_bytes must be between 0 and %d", base.MaxDeltaSegmentBytes)
	}
	if cfg.MaxDeltaSegmentRecords < 0 {
		return base.Config{}, nil, nil, fmt.Errorf("ghdb: max_delta_segment_records must be non-negative")
	}
	if cfg.MaxDeltaSegmentBytes == 0 {
		cfg.MaxDeltaSegmentBytes = base.DefaultMaxDeltaSegmentBytes
	}
	if cfg.MaxDeltaSegmentRecords == 0 {
		cfg.MaxDeltaSegmentRecords = base.DefaultMaxDeltaSegmentRecords
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
type StartupProgressKind = base.StartupProgressKind
type StartupProgress = base.StartupProgress
type StartupProgressFunc = base.StartupProgressFunc

const (
	StartupBranchLookup  = base.StartupBranchLookup
	StartupReplayListing = base.StartupReplayListing
	StartupReplaySegment = base.StartupReplaySegment
)

type TableDB = table.TableDB
type Table = table.Table

var ErrKeyMismatch = table.ErrKeyMismatch
var ErrRequiredMissing = table.ErrRequiredMissing
var ErrOffline = base.ErrOffline

type ErrMutationTooLarge = base.ErrMutationTooLarge

const MaxSingleMutationBytes = base.MaxSingleMutationBytes

type GraphDB = graph.GraphDB
type VertexSet = graph.VertexSet
