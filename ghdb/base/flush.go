package base

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
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

// isInstanceSegment reports whether name is a delta segment owned by instanceID.
func isInstanceSegment(name, instanceID string) bool {
	return name == instanceID+".jsonl" || strings.HasPrefix(name, instanceID+"-")
}

func rolloverSegmentOrder(name, instanceID string) (string, int) {
	base := strings.TrimSuffix(strings.TrimPrefix(name, instanceID+"-"), ".jsonl")
	if dash := strings.LastIndexByte(base, '-'); dash >= 0 {
		if sequence, err := strconv.Atoi(base[dash+1:]); err == nil && sequence >= 2 {
			return base[:dash], sequence
		}
	}
	return base, 1
}

// latestInstanceSegment returns the newest rollover segment for instanceID, or its legacy segment.
func latestInstanceSegment(entries []github.DirEntry, instanceID string) string {
	legacy := instanceID + ".jsonl"
	rollovers := make([]string, 0)
	for _, entry := range entries {
		if !isInstanceSegment(entry.Name, instanceID) {
			continue
		}
		if entry.Name != legacy {
			rollovers = append(rollovers, entry.Name)
		}
	}
	if len(rollovers) > 0 {
		sort.Slice(rollovers, func(i, j int) bool {
			leftTimestamp, leftSequence := rolloverSegmentOrder(rollovers[i], instanceID)
			rightTimestamp, rightSequence := rolloverSegmentOrder(rollovers[j], instanceID)
			if leftTimestamp == rightTimestamp {
				return leftSequence < rightSequence
			}
			return leftTimestamp < rightTimestamp
		})
		return rollovers[len(rollovers)-1]
	}
	for _, entry := range entries {
		if entry.Name == legacy {
			return legacy
		}
	}
	return ""
}

// splitMutationRecords serializes records into byte- and record-bounded JSONL batches.
func splitMutationRecords(recs []MutationRecord, maxBytes, maxRecords int) ([][]MutationRecord, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("ghdb: max delta segment bytes must be positive")
	}
	if maxRecords <= 0 {
		return nil, fmt.Errorf("ghdb: max delta segment records must be positive")
	}

	batches := make([][]MutationRecord, 0)
	var current []MutationRecord
	currentBytes := 0
	for _, rec := range recs {
		line, err := MarshalJSONL([]MutationRecord{rec})
		if err != nil {
			return nil, err
		}
		if len(line) > MaxSingleMutationBytes {
			return nil, &ErrMutationTooLarge{Size: len(line), Limit: MaxSingleMutationBytes}
		}
		if len(line) > maxBytes {
			if len(current) > 0 {
				batches = append(batches, current)
				current = nil
				currentBytes = 0
			}
			batches = append(batches, []MutationRecord{rec})
			continue
		}
		if len(current) > 0 && (currentBytes+len(line) > maxBytes || len(current) >= maxRecords) {
			batches = append(batches, current)
			current = nil
			currentBytes = 0
		}
		current = append(current, rec)
		currentBytes += len(line)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches, nil
}

func appendMutationRecords(existing []byte, recs []MutationRecord) ([]byte, error) {
	content := append([]byte(nil), existing...)
	if len(recs) == 0 {
		return content, nil
	}
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	newLines, err := MarshalJSONL(recs)
	if err != nil {
		return nil, err
	}
	return append(content, newLines...), nil
}

func recordsThatFit(recs []MutationRecord, existing []byte, existingCount, maxBytes, maxRecords int) ([]MutationRecord, error) {
	if len(existing) >= maxBytes || existingCount >= maxRecords {
		return nil, nil
	}

	usedBytes := len(existing)
	if usedBytes > 0 && existing[usedBytes-1] != '\n' {
		usedBytes++
	}
	usedRecords := existingCount
	var batch []MutationRecord
	for _, rec := range recs {
		line, err := MarshalJSONL([]MutationRecord{rec})
		if err != nil {
			return nil, err
		}
		if usedBytes+len(line) > maxBytes || usedRecords+1 > maxRecords {
			break
		}
		batch = append(batch, rec)
		usedBytes += len(line)
		usedRecords++
	}
	return batch, nil
}

func rolloverSegmentName(instanceID string, now time.Time, used map[string]struct{}) string {
	base := fmt.Sprintf("%s-%s", instanceID, now.UTC().Format("20060102T150405Z"))
	name := base + ".jsonl"
	if _, exists := used[name]; !exists {
		return name
	}
	for sequence := 2; ; sequence++ {
		name = fmt.Sprintf("%s-%d.jsonl", base, sequence)
		if _, exists := used[name]; !exists {
			return name
		}
	}
}

// flush drains the write buffer and writes bounded instance JSONL segments on delta_branch.
// If nextVerExists is true, migrates writeVer to cfg.Version+1 before writing.
func (b *baseDB) flush(ctx context.Context) error {
	b.wbufMu.Lock()
	if len(b.wbuf) == 0 {
		b.wbufMu.Unlock()
		return nil
	}
	pending := b.wbuf
	b.wbuf = nil
	b.wbufMu.Unlock()

	restorePending := func() {
		b.wbufMu.Lock()
		b.wbuf = append(pending, b.wbuf...)
		b.wbufMu.Unlock()
	}

	b.mu.RLock()
	cfg := b.cfg
	ver := b.writeVer
	nextExists := b.nextVerExists
	instanceID := b.instanceID
	b.mu.RUnlock()

	if nextExists {
		b.mu.Lock()
		b.writeVer = cfg.Version + 1
		ver = b.writeVer
		b.mu.Unlock()
	}

	maxBytes := deltaSegmentBytes(cfg)
	maxRecords := deltaSegmentRecords(cfg)
	if _, err := splitMutationRecords(pending, maxBytes, maxRecords); err != nil {
		restorePending()
		return err
	}

	versionPath := fmt.Sprintf("%s/v%d", GetDataRepoPath(cfg), ver)
	entries, err := b.gh.ListDir(ctx, cfg.DeltaBranch, versionPath)
	if err != nil {
		restorePending()
		return err
	}

	usedSegmentNames := make(map[string]struct{})
	for _, entry := range entries {
		if isInstanceSegment(entry.Name, instanceID) {
			usedSegmentNames[entry.Name] = struct{}{}
		}
	}

	activeName := latestInstanceSegment(entries, instanceID)
	activePath := ""
	var existing []byte
	var sha string
	existingCount := 0
	if activeName != "" {
		activePath = versionPath + "/" + activeName
		existing, sha, err = b.gh.GetFile(ctx, cfg.DeltaBranch, activePath)
		if err != nil {
			restorePending()
			return err
		}
		if len(existing) < maxBytes {
			existingRecords, err := UnmarshalJSONL(existing)
			if err != nil {
				restorePending()
				return err
			}
			existingCount = len(existingRecords)
		}
	} else {
		activeName = instanceID + ".jsonl"
		activePath = versionPath + "/" + activeName
	}

	writeBatch := func(path string, prior []byte, currentSHA string, batch []MutationRecord) error {
		content, err := appendMutationRecords(prior, batch)
		if err != nil {
			return err
		}
		if len(content) > maxBytes && !(len(batch) == 1 && len(prior) == 0 && currentSHA == "" && len(content) <= MaxSingleMutationBytes) {
			return fmt.Errorf("ghdb: delta segment %s exceeds max bytes", path)
		}
		msg := fmt.Sprintf("ghdb: flush %s ver %d (%d records)", instanceID, ver, len(batch))
		return b.gh.PutFile(ctx, cfg.DeltaBranch, path, msg, content, currentSHA)
	}

	batch, err := recordsThatFit(pending, existing, existingCount, maxBytes, maxRecords)
	if err != nil {
		restorePending()
		return err
	}
	if len(batch) > 0 {
		if err := writeBatch(activePath, existing, sha, batch); err != nil {
			restorePending()
			return err
		}
		pending = pending[len(batch):]
	}

	for len(pending) > 0 {
		batches, err := splitMutationRecords(pending, maxBytes, maxRecords)
		if err != nil {
			restorePending()
			return err
		}
		batch = batches[0]
		name := rolloverSegmentName(instanceID, time.Now(), usedSegmentNames)
		if err := writeBatch(versionPath+"/"+name, nil, "", batch); err != nil {
			restorePending()
			return err
		}
		usedSegmentNames[name] = struct{}{}
		pending = pending[len(batch):]
	}
	return nil
}
