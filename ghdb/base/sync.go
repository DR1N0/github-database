package base

import (
	"context"
	"fmt"
	"sort"
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
					b.Logger().Printf("ghdb: sync poll failed")
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
			b.Logger().Printf("ghdb: poll ListDir %s failed", vpath)
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
				b.Logger().Printf("ghdb: poll GetFile %s (sha %s) failed", key, e.SHA)
				continue
			}
			recs, parseErrs := decodeJSONL(data)
			for _, parseErr := range parseErrs {
				b.Logger().Printf("ghdb: poll: skip %s (sha %s) line %d: %s", key, e.SHA, parseErr.Line, jsonlWarningCategory(parseErr))
			}

			b.mu.RLock()
			lastApplied := b.syncTimes[key]
			b.mu.RUnlock()

			var fresh []decodedMutationRecord
			for _, decoded := range recs {
				if decoded.record.TS.After(lastApplied) {
					fresh = append(fresh, decoded)
				}
			}
			sort.Slice(fresh, func(i, j int) bool {
				return fresh[i].record.TS.Before(fresh[j].record.TS)
			})

			b.mu.Lock()
			var latestSuccessful time.Time
			hasSuccessful := false
			for _, decoded := range fresh {
				if b.applyFn != nil {
					if err := b.applyFn(decoded.record); err != nil {
						b.Logger().Printf("ghdb: poll: skip %s (sha %s) line %d: invalid mutation", key, e.SHA, decoded.line)
						continue
					}
				}
				if !hasSuccessful || decoded.record.TS.After(latestSuccessful) {
					latestSuccessful = decoded.record.TS
					hasSuccessful = true
				}
			}
			if hasSuccessful {
				b.syncTimes[key] = latestSuccessful
			}
			// The successfully fetched content is acknowledged even when individual
			// lines were bad, preventing unchanged segments from warning forever.
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
