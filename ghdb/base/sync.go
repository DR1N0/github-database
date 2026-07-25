package base

import (
	"context"
	"fmt"
	"log"
	"time"
)

// startSync starts the periodic sync poll goroutine.
func (b *baseDB) startSync() {
	interval := time.Duration(b.cfg.SyncIntervalSec) * time.Second
	// By default 30 seconds, and at least 10 seconds to avoid hitting GitHub API rate limits.
	if interval <= 0 {
		interval = 30 * time.Second
	} else if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	go func() {
		defer close(b.pollDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				if err := b.poll(context.Background()); err != nil {
					log.Printf("ghdb: sync poll error: %v", err)
				}
			}
		}
	}()
}

// poll lists the current version directory, fetches files whose SHA has changed,
// and applies new mutations via applyFn. Also detects when v{N+1} first appears.
func (b *baseDB) poll(ctx context.Context) error {
	b.mu.RLock()
	ver := b.cfg.Version
	nextExists := b.nextVerExists
	b.mu.RUnlock()

	paths := []string{fmt.Sprintf("%s/v%d", GetDataRepoPath(b.cfg), ver)}
	if nextExists {
		paths = append(paths, fmt.Sprintf("%s/v%d", GetDataRepoPath(b.cfg), ver+1))
	}

	for _, vpath := range paths {
		entries, err := b.gh.ListDir(ctx, b.cfg.DeltaBranch, vpath)
		if err != nil {
			log.Printf("ghdb: poll ListDir %s: %v", vpath, err)
			continue
		}
		for _, e := range entries {
			key := vpath + "/" + e.Name
			b.mu.RLock()
			knownSHA := b.syncSHAs[key]
			b.mu.RUnlock()
			if knownSHA == e.SHA {
				continue
			}

			data, _, err := b.gh.GetFile(ctx, b.cfg.DeltaBranch, key)
			if err != nil {
				log.Printf("ghdb: poll GetFile %s: %v", key, err)
				continue
			}
			recs, err := UnmarshalJSONL(data)
			if err != nil {
				log.Printf("ghdb: poll parse %s: %v", key, err)
				continue
			}

			b.mu.RLock()
			lastApplied := b.syncTimes[key]
			b.mu.RUnlock()

			var fresh []MutationRecord
			for _, r := range recs {
				if r.TS.After(lastApplied) {
					fresh = append(fresh, r)
				}
			}
			SortByTS(fresh)

			b.mu.Lock()
			for _, r := range fresh {
				if b.applyFn != nil {
					b.applyFn(r)
				}
			}
			if len(fresh) > 0 {
				b.syncTimes[key] = fresh[len(fresh)-1].TS
			}
			b.syncSHAs[key] = e.SHA
			b.mu.Unlock()
		}
	}

	// Detect first appearance of v{N+1}.
	if !nextExists {
		nextPath := fmt.Sprintf("%s/v%d", GetDataRepoPath(b.cfg), ver+1)
		nextEntries, _ := b.gh.ListDir(ctx, b.cfg.DeltaBranch, nextPath)
		if len(nextEntries) > 0 {
			b.mu.Lock()
			b.nextVerExists = true
			b.mu.Unlock()
		}
	}

	return nil
}
