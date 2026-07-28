package base

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

// NewBaseDB creates an engine for cfg. The engine is offline until StartOnline is called.
func NewBaseDB(cfg Config) Engine {
	return &baseDB{
		cfg:       cfg,
		syncSHAs:  map[string]string{},
		syncTimes: map[string]time.Time{},
		stopCh:    make(chan struct{}),
		flushDone: make(chan struct{}),
		pollDone:  make(chan struct{}),
	}
}

// StartOnline wires the engine into online mode: validates credentials, ensures the
// delta branch exists, replays startup mutations, and starts flush/sync goroutines.
// Call SetLogger before StartOnline so startup messages use the configured logger.
func (b *baseDB) StartOnline(gh github.Interface) error {
	b.gh = gh
	cfg := b.cfg

	ctx := context.Background()
	_, mainSHA, err := gh.DefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("ghdb: credential validation failed: %w", err)
	}
	b.Logger().Printf("ghdb: online mode (credential validated)")

	if b.committerName == "" {
		name, email, err := gh.GetAuthenticatedUser(ctx)
		if err != nil {
			return fmt.Errorf("ghdb: get authenticated user: %w", err)
		}
		b.committerName = name
		b.committerEmail = email
	}

	b.instanceID = deriveInstanceID()
	b.online = true

	exists, err := gh.BranchExists(ctx, cfg.DeltaBranch)
	if err != nil {
		return fmt.Errorf("ghdb: check delta_branch: %w", err)
	}
	if !exists {
		if err := gh.CreateBranch(ctx, cfg.DeltaBranch, mainSHA); err != nil {
			return fmt.Errorf("ghdb: create delta_branch: %w", err)
		}
	}

	tCut := cfg.BaselineTime
	versionPath := fmt.Sprintf("%s/v%d", GetDataRepoPath(cfg), cfg.Version)
	if err := b.replayVersion(ctx, cfg, versionPath, tCut); err != nil {
		return fmt.Errorf("ghdb: startup replay: %w", err)
	}

	nextPath := fmt.Sprintf("%s/v%d", GetDataRepoPath(cfg), cfg.Version+1)
	nextEntries, _ := gh.ListDir(ctx, cfg.DeltaBranch, nextPath)
	if len(nextEntries) > 0 {
		b.nextVerExists = true
		b.writeVer = cfg.Version + 1
		if err := b.replayVersion(ctx, cfg, nextPath, tCut); err != nil {
			b.Logger().Printf("ghdb: startup replay v%d: %v", cfg.Version+1, err)
		}
	} else {
		b.writeVer = cfg.Version
	}

	b.startFlush()
	b.startSync()
	return nil
}

type baseDB struct {
	cfg        Config
	gh         github.Interface
	logger     *log.Logger
	online     bool
	instanceID string
	writeVer   int

	mu     sync.RWMutex
	wbuf   []MutationRecord
	wbufMu sync.Mutex

	// ckptMu: writes hold RLock; Checkpoint holds Lock (steps 0-3)
	ckptMu sync.RWMutex

	syncSHAs      map[string]string
	syncTimes     map[string]time.Time
	nextVerExists bool

	// Set by table/graph constructors before StartOnline is called.
	applyFn    func(MutationRecord)
	snapshotFn func() (map[string][]byte, error)

	// Committer identity for checkpoint commits. Set during StartOnline.
	committerName  string
	committerEmail string
	// Optional signing callback. Set via SetCommitSigner before StartOnline.
	commitSigner func([]byte) (string, error)

	stopCh    chan struct{}
	flushDone chan struct{}
	pollDone  chan struct{}

	closeOnce       sync.Once
	hasCheckpointed bool
}

func (b *baseDB) LockCkptRead()   { b.ckptMu.RLock() }
func (b *baseDB) UnlockCkptRead() { b.ckptMu.RUnlock() }
func (b *baseDB) LockData()       { b.mu.Lock() }
func (b *baseDB) UnlockData()     { b.mu.Unlock() }
func (b *baseDB) RLockData()      { b.mu.RLock() }
func (b *baseDB) RUnlockData()    { b.mu.RUnlock() }

func (b *baseDB) AppendMutation(r MutationRecord) {
	b.wbufMu.Lock()
	b.wbuf = append(b.wbuf, r)
	b.wbufMu.Unlock()
}

func (b *baseDB) IsOnline() bool    { return b.online }
func (b *baseDB) GetConfig() Config { return b.cfg }

func (b *baseDB) SetLogger(l *log.Logger) { b.logger = l }
func (b *baseDB) Logger() *log.Logger {
	if b.logger != nil {
		return b.logger
	}
	return log.Default()
}

func (b *baseDB) SetApplyFn(fn func(MutationRecord))                 { b.applyFn = fn }
func (b *baseDB) SetSnapshotFn(fn func() (map[string][]byte, error)) { b.snapshotFn = fn }

func (b *baseDB) SetCommitterIdentity(name, email string) {
	b.committerName = name
	b.committerEmail = email
}

func (b *baseDB) SetCommitSigner(fn func([]byte) (string, error)) {
	b.commitSigner = fn
}

func (b *baseDB) Close(ctx context.Context) error {
	if !b.online {
		return nil
	}
	b.closeOnce.Do(func() { close(b.stopCh) })
	done := make(chan struct{})
	go func() {
		<-b.flushDone
		<-b.pollDone
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ghdb: Close timed out: %w", ctx.Err())
	}
}

// replayVersion fetches all JSONL files in versionPath, filters records after tCut,
// sorts by timestamp, and applies them via applyFn.
func (b *baseDB) replayVersion(ctx context.Context, cfg Config, versionPath string, tCut time.Time) error {
	entries, err := b.gh.ListDir(ctx, cfg.DeltaBranch, versionPath)
	if err != nil {
		return err
	}
	var allRecs []MutationRecord
	for _, e := range entries {
		data, _, err := b.gh.GetFile(ctx, cfg.DeltaBranch, versionPath+"/"+e.Name)
		if err != nil {
			b.Logger().Printf("ghdb: replay: skip %s: %v", e.Name, err)
			continue
		}
		recs, err := UnmarshalJSONL(data)
		if err != nil {
			b.Logger().Printf("ghdb: replay: parse %s: %v", e.Name, err)
			continue
		}
		for _, r := range recs {
			if r.TS.After(tCut) {
				allRecs = append(allRecs, r)
			}
		}
	}
	SortByTS(allRecs)
	b.mu.Lock()
	for _, r := range allRecs {
		if b.applyFn != nil {
			b.applyFn(r)
		}
	}
	b.mu.Unlock()
	return nil
}

func deriveInstanceID() string {
	if v := os.Getenv("POD_NAME"); v != "" {
		log.Printf("ghdb: instance ID from POD_NAME: %s", v)
		return v
	}
	if h, err := os.Hostname(); err == nil {
		log.Printf("ghdb: instance ID from hostname: %s", h)
		return h
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ghdb: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	log.Printf("ghdb: instance ID from random UUID: %s", id)
	return id
}
