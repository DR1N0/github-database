package base

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

// buildCommitObject constructs the raw git commit object bytes that a signing
// callback must sign. The format matches git's internal representation exactly,
// with Unix-epoch integer timestamps and +0000 timezone offset.
func buildCommitObject(treeSHA, parentSHA, name, email string, ts time.Time, message string) []byte {
	unix := ts.UTC().Unix()
	return []byte(fmt.Sprintf(
		"tree %s\nparent %s\nauthor %s <%s> %d +0000\ncommitter %s <%s> %d +0000\n\n%s",
		treeSHA, parentSHA,
		name, email, unix,
		name, email, unix,
		message,
	))
}

// ErrCheckpointBusy is returned when another instance holds an unexpired
// checkpoint lease for the current version. The call can be retried once that
// lease expires (see Option.CheckpointLease).
var ErrCheckpointBusy = errors.New("ghdb: another instance is checkpointing this version")

// ErrCheckpointTaken is returned when another instance already checkpointed the
// current version. The instance switches its writes to the next version and
// keeps serving; merging the snapshot's pull request is what makes it the new
// baseline.
var ErrCheckpointTaken = errors.New("ghdb: this version was already checkpointed")

// defaultCheckpointLease bounds how long a checkpoint may stay unfinished
// before another instance is allowed to take it over.
const defaultCheckpointLease = 5 * time.Minute

// checkpointLease is the compare-and-swap record that makes a checkpoint a
// single-writer operation across instances. It lives beside the version
// directories, so replay and poll never list it as a segment.
type checkpointLease struct {
	Owner      string    `json:"owner"`
	AcquiredAt time.Time `json:"acquired_at"`
	Done       bool      `json:"done,omitempty"`
	Snapshot   string    `json:"snapshot,omitempty"`
}

func checkpointLeasePath(cfg Config, version int) string {
	return fmt.Sprintf("%s/_checkpoint/v%d.lease", GetDataRepoPath(cfg), version)
}

func (b *baseDB) checkpointLeaseDuration() time.Duration {
	if b.checkpointLease > 0 {
		return b.checkpointLease
	}
	return defaultCheckpointLease
}

// acquireCheckpointLease claims the right to checkpoint nextVer. GitHub decides
// the winner, not local timing: a create without a SHA loses to an existing
// file, and a write carrying a stale SHA loses to a concurrent writer. The same
// instance may renew its own lease, which makes a failed checkpoint retryable.
func (b *baseDB) acquireCheckpointLease(ctx context.Context, nextVer int) (string, error) {
	path := checkpointLeasePath(b.cfg, nextVer)
	lease := checkpointLease{Owner: b.instanceID, AcquiredAt: time.Now().UTC()}

	for attempt := 0; attempt < 3; attempt++ {
		raw, sha, err := b.forkGH.GetFile(ctx, b.cfg.DeltaBranch, path)
		if err != nil {
			if !errors.Is(err, github.ErrNotFound) {
				return "", err
			}
			if err := b.putCheckpointLease(ctx, path, lease, ""); err != nil {
				if isLeaseConflict(err) {
					continue
				}
				return "", err
			}
			return path, nil
		}

		var current checkpointLease
		if err := json.Unmarshal(raw, &current); err != nil {
			return "", fmt.Errorf("ghdb: checkpoint: parse lease %s: %w", path, err)
		}
		switch {
		case current.Done:
			return "", fmt.Errorf("%w: %s completed v%d", ErrCheckpointTaken, current.Owner, nextVer)
		case current.Owner != b.instanceID && time.Since(current.AcquiredAt) < b.checkpointLeaseDuration():
			return "", fmt.Errorf("%w: %s holds the lease for v%d", ErrCheckpointBusy, current.Owner, nextVer)
		}
		// Our own unfinished lease (a retry) or an expired one: take it over.
		if err := b.putCheckpointLease(ctx, path, lease, sha); err != nil {
			if isLeaseConflict(err) {
				continue
			}
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("%w: could not acquire the checkpoint lease for v%d", ErrCheckpointBusy, nextVer)
}

func (b *baseDB) putCheckpointLease(ctx context.Context, path string, lease checkpointLease, sha string) error {
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("ghdb: checkpoint lease %s", b.instanceID)
	return b.forkGH.PutFile(ctx, b.cfg.DeltaBranch, path, message, data, sha)
}

// markCheckpointLeaseDone records that nextVer has a completed snapshot. A
// failure here is not fatal: the lease expires on its own, and the snapshot
// pull request is what actually carries the new baseline.
func (b *baseDB) markCheckpointLeaseDone(ctx context.Context, path, snapshot string) {
	raw, sha, err := b.forkGH.GetFile(ctx, b.cfg.DeltaBranch, path)
	if err != nil {
		b.Logger().Printf("ghdb: checkpoint: could not read lease %s: %v", path, err)
		return
	}
	var lease checkpointLease
	if err := json.Unmarshal(raw, &lease); err != nil {
		b.Logger().Printf("ghdb: checkpoint: could not parse lease %s: %v", path, err)
		return
	}
	lease.Done = true
	lease.Snapshot = snapshot
	if err := b.putCheckpointLease(ctx, path, lease, sha); err != nil {
		b.Logger().Printf("ghdb: checkpoint: could not mark lease %s done: %v", path, err)
	}
}

// isLeaseConflict reports whether err means another writer changed the lease
// between our read and our write. GitHub answers 409 for a stale SHA and 422
// when a create omits the SHA of an existing file.
func isLeaseConflict(err error) bool {
	if errors.Is(err, github.ErrConflict) {
		return true
	}
	return strings.Contains(err.Error(), "HTTP 422")
}

func (b *baseDB) Checkpoint(ctx context.Context) error {
	b.ckptMu.Lock()
	defer b.ckptMu.Unlock()

	if b.hasCheckpointed {
		return fmt.Errorf("ghdb: Checkpoint already performed; restart instance for next version")
	}

	// hasCheckpointed is only set once every step has succeeded, so a failed
	// checkpoint (network error, bad credentials, cancelled context) can be
	// retried on the same instance instead of requiring a restart.

	nextVer := b.cfg.Version + 1
	leasePath, err := b.acquireCheckpointLease(ctx, nextVer)
	if err != nil {
		if errors.Is(err, ErrCheckpointTaken) {
			// Another instance produced the snapshot. Stop writing to this
			// version so this instance's writes land on the next one.
			b.mu.Lock()
			b.nextVerExists = true
			b.writeVer = nextVer
			b.mu.Unlock()
		}
		return err
	}

	if err := b.flush(ctx); err != nil {
		return fmt.Errorf("ghdb: checkpoint flush: %w", err)
	}

	b.mu.RLock()
	modeFiles, err := b.snapshotFn()
	b.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint snapshot: %w", err)
	}

	checkpointedAt := time.Now().UTC()

	sourceBranch, mainSHA, err := resolveSourceBranch(ctx, b.baseGH, b.cfg)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: get main SHA: %w", err)
	}

	targetBranch := b.cfg.TargetBranch
	if targetBranch == "" {
		targetBranch = sourceBranch
	}

	mainTreeSHA, err := b.baseGH.GetCommitTree(ctx, mainSHA)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: get main tree: %w", err)
	}

	snapshotBranch := fmt.Sprintf("ghdb-ckpt-%s-v%d", b.cfg.Name, nextVer)
	// Our own failed attempt may have left the snapshot branch behind; the lease
	// makes reuse safe. UpdateRef below force-moves it to the new commit.
	if err := b.forkGH.CreateBranch(ctx, snapshotBranch, mainSHA); err != nil {
		exists, existsErr := b.forkGH.BranchExists(ctx, snapshotBranch)
		if existsErr != nil || !exists {
			return fmt.Errorf("ghdb: checkpoint: create snapshot branch: %w", err)
		}
		b.Logger().Printf("ghdb: checkpoint: reusing existing snapshot branch %q", snapshotBranch)
	}

	// Serialize the next db_meta.json.
	nextCfg := b.cfg
	nextCfg.Version = nextVer
	nextCfg.BaselineTime = checkpointedAt
	dbMetaContent, err := json.MarshalIndent(nextCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint marshal db_meta: %w", err)
	}

	// Collect all files into one tree (mode files + db_meta).
	prefix := GetDataRepoPath(b.cfg)
	allFiles := make(map[string][]byte, len(modeFiles)+1)
	for relPath, content := range modeFiles {
		allFiles[prefix+"/"+relPath] = content
	}
	allFiles[prefix+"/db_meta.json"] = dbMetaContent

	treeSHA, err := b.forkGH.CreateTree(ctx, mainTreeSHA, allFiles)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: create tree: %w", err)
	}

	// Fix timestamp at second precision so the signing payload and API call agree.
	ts := time.Now().UTC().Truncate(time.Second)
	message := fmt.Sprintf("ghdb: checkpoint %s to v%d", b.cfg.Name, nextVer)

	var signature string
	if b.commitSigner != nil {
		payload := buildCommitObject(treeSHA, mainSHA, b.committerName, b.committerEmail, ts, message)
		signature, err = b.commitSigner(payload)
		if err != nil {
			return fmt.Errorf("ghdb: checkpoint: sign commit: %w", err)
		}
	}

	commitSHA, err := b.forkGH.CreateCommit(ctx, treeSHA, mainSHA, message, b.committerName, b.committerEmail, ts, signature)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: create commit: %w", err)
	}

	if err := b.forkGH.UpdateRef(ctx, snapshotBranch, commitSHA); err != nil {
		return fmt.Errorf("ghdb: checkpoint: update ref: %w", err)
	}

	head := snapshotBranch
	if b.cfg.ForkRepo != "" && b.cfg.ForkRepo != b.cfg.BaseRepo {
		if owner, _, ok := strings.Cut(b.cfg.ForkRepo, "/"); ok {
			head = owner + ":" + snapshotBranch
		}
	}

	title := fmt.Sprintf("ghdb: checkpoint %s to v%d", b.cfg.Name, nextVer)
	if _, err := b.baseGH.CreatePR(ctx, title, head, targetBranch); err != nil {
		b.Logger().Printf("ghdb: checkpoint PR: %v", err)
	}

	b.markCheckpointLeaseDone(ctx, leasePath, commitSHA)

	b.mu.Lock()
	b.nextVerExists = true
	b.writeVer = nextVer
	b.mu.Unlock()
	b.hasCheckpointed = true

	return nil
}
