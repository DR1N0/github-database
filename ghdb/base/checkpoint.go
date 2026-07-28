package base

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
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

func (b *baseDB) Checkpoint(ctx context.Context) error {
	b.ckptMu.Lock()
	defer b.ckptMu.Unlock()

	if b.hasCheckpointed {
		return fmt.Errorf("ghdb: Checkpoint already performed; restart instance for next version")
	}
	b.hasCheckpointed = true

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
	nextVer := b.cfg.Version + 1

	mainBranch := b.cfg.MainBranch
	if mainBranch == "" {
		mainBranch = "main"
	}
	_, mainSHA, err := b.gh.DefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: get main SHA: %w", err)
	}

	mainTreeSHA, err := b.gh.GetCommitTree(ctx, mainSHA)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: get main tree: %w", err)
	}

	snapshotBranch := fmt.Sprintf("ghdb-ckpt-%s-v%d", b.cfg.Name, nextVer)
	if err := b.gh.CreateBranch(ctx, snapshotBranch, mainSHA); err != nil {
		return fmt.Errorf("ghdb: checkpoint: create snapshot branch: %w", err)
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

	treeSHA, err := b.gh.CreateTree(ctx, mainTreeSHA, allFiles)
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

	commitSHA, err := b.gh.CreateCommit(ctx, treeSHA, mainSHA, message, b.committerName, b.committerEmail, ts, signature)
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint: create commit: %w", err)
	}

	if err := b.gh.UpdateRef(ctx, snapshotBranch, commitSHA); err != nil {
		return fmt.Errorf("ghdb: checkpoint: update ref: %w", err)
	}

	title := fmt.Sprintf("ghdb: checkpoint %s to v%d", b.cfg.Name, nextVer)
	if _, err := b.gh.CreatePR(ctx, title, snapshotBranch, mainBranch); err != nil {
		log.Printf("ghdb: checkpoint PR: %v", err)
	}

	b.mu.Lock()
	b.nextVerExists = true
	b.writeVer = nextVer
	b.mu.Unlock()

	return nil
}
