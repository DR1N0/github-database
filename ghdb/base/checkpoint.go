package base

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

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

	snapshotBranch := fmt.Sprintf("ghdb-ckpt-%s-v%d", b.cfg.Name, nextVer)
	if err := b.gh.CreateBranch(ctx, snapshotBranch, mainSHA); err != nil {
		return fmt.Errorf("ghdb: checkpoint: create snapshot branch: %w", err)
	}

	prefix := GetDataRepoPath(b.cfg)
	for relPath, content := range modeFiles {
		fullPath := prefix + "/" + relPath
		_, sha, err := b.gh.GetFile(ctx, snapshotBranch, fullPath)
		if err != nil && err != github.ErrNotFound {
			return fmt.Errorf("ghdb: checkpoint get %s: %w", relPath, err)
		}
		if err := b.gh.PutFile(ctx, snapshotBranch, fullPath, "ghdb: checkpoint "+relPath, content, sha); err != nil {
			return fmt.Errorf("ghdb: checkpoint write %s: %w", relPath, err)
		}
	}

	nextCfg := b.cfg
	nextCfg.Version = nextVer
	nextCfg.BaselineTime = checkpointedAt
	dbMetaContent, err := json.MarshalIndent(nextCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ghdb: checkpoint marshal db_meta: %w", err)
	}
	dbMetaPath := prefix + "/db_meta.json"
	_, metaSHA, _ := b.gh.GetFile(ctx, snapshotBranch, dbMetaPath)
	if err := b.gh.PutFile(ctx, snapshotBranch, dbMetaPath, "ghdb: checkpoint db_meta.json", dbMetaContent, metaSHA); err != nil {
		return fmt.Errorf("ghdb: checkpoint write db_meta.json: %w", err)
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
