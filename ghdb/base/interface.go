package base

import (
	"context"
	"errors"
	"log"

	"github.com/DR1N0/github-database/ghdb/github"
)

// ErrOffline is returned by Checkpoint when the DB was opened without a token.
var ErrOffline = errors.New("ghdb: offline mode")

// DB is the top-level interface used by all callers.
type DB interface {
	Mode() string
	IsOnline() bool
	Checkpoint(ctx context.Context) error
	Close(ctx context.Context) error
}

// Engine is the engine interface implemented by the unexported *baseDB.
// Only ghdb/table and ghdb/graph call it — callers never see it.
type Engine interface {
	// Locking — held during data mutations by table/graph
	LockCkptRead()
	UnlockCkptRead()
	LockData()
	UnlockData()
	RLockData()
	RUnlockData()

	// Write buffer
	AppendMutation(r MutationRecord)
	ValidateMutation(r MutationRecord) error

	// Guards and config
	IsOnline() bool
	GetConfig() Config

	// Logger — set by Option.Open so table/graph layers can log mutations.
	SetLogger(l *log.Logger)
	Logger() *log.Logger

	// Lifecycle — StartOnline is called once by Option.Open when credentials are present.
	StartOnline(gh github.Interface) error
	Checkpoint(ctx context.Context) error
	Close(ctx context.Context) error

	// Wired by table/graph constructors before StartOnline is called.
	// SetApplyFn is retained for callbacks that cannot return an error.
	SetApplyFn(fn func(MutationRecord))
	SetApplyFnWithError(fn func(MutationRecord) error)
	SetSnapshotFn(fn func() (map[string][]byte, error))

	// SetCommitterIdentity overrides the name/email used for checkpoint commits.
	// Must be called before StartOnline; if set, StartOnline skips GetAuthenticatedUser.
	SetCommitterIdentity(name, email string)
	// SetCommitSigner sets the optional signing callback for checkpoint commits.
	SetCommitSigner(fn func([]byte) (string, error))
}
