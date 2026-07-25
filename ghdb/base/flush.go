package base

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

// startFlush starts the periodic write-buffer flush goroutine.
// It must be called after b.online, b.gh, b.cfg, b.instanceID, and b.writeVer are set.
func (b *baseDB) startFlush() {
	interval := time.Duration(b.cfg.FlushIntervalSec) * time.Second
	// By default 5 minutes, and at least 10 seconds to avoid hitting GitHub API rate limits.
	if interval <= 0 {
		interval = 300 * time.Second
	} else if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	go func() {
		defer close(b.flushDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-b.stopCh:
				// Final flush before exit.
				ctx25, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				if err := b.flush(ctx25); err != nil {
					log.Printf("ghdb: final flush error: %v", err)
				}
				return
			case <-ticker.C:
				if err := b.flush(context.Background()); err != nil {
					log.Printf("ghdb: flush error: %v", err)
				}
			}
		}
	}()
}

// flush drains the write buffer and appends to the instance JSONL file on delta_branch.
// If nextVerExists is true, migrates writeVer to cfg.Version+1 before writing.
func (b *baseDB) flush(ctx context.Context) error {
	b.wbufMu.Lock()
	if len(b.wbuf) == 0 {
		b.wbufMu.Unlock()
		return nil
	}
	recs := b.wbuf
	b.wbuf = nil
	b.wbufMu.Unlock()

	// Check for version migration before writing.
	b.mu.RLock()
	ver := b.writeVer
	nextExists := b.nextVerExists
	b.mu.RUnlock()

	if nextExists {
		b.mu.Lock()
		b.writeVer = b.cfg.Version + 1
		ver = b.writeVer
		b.mu.Unlock()
	}

	path := fmt.Sprintf("%s/v%d/%s.jsonl", GetDataRepoPath(b.cfg), ver, b.instanceID)
	newLines, err := MarshalJSONL(recs)
	if err != nil {
		b.wbufMu.Lock()
		b.wbuf = append(recs, b.wbuf...)
		b.wbufMu.Unlock()
		return err
	}

	// GET existing content + SHA.
	existing, sha, err := b.gh.GetFile(ctx, b.cfg.DeltaBranch, path)
	if err != nil && err != github.ErrNotFound {
		b.wbufMu.Lock()
		b.wbuf = append(recs, b.wbuf...)
		b.wbufMu.Unlock()
		return err
	}

	var content []byte
	if len(existing) > 0 {
		content = append(existing, '\n')
	}
	content = append(content, newLines...)

	msg := fmt.Sprintf("ghdb: flush %s ver %d (%d records)", b.instanceID, ver, len(recs))
	if err := b.gh.PutFile(ctx, b.cfg.DeltaBranch, path, msg, content, sha); err != nil {
		b.wbufMu.Lock()
		b.wbuf = append(recs, b.wbuf...)
		b.wbufMu.Unlock()
		return err
	}
	return nil
}
