package base

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

// TestConcurrentFlushKeepsEveryRecordOnce covers the flush mutex: the background
// flusher and Checkpoint can both drain the write buffer, and overlapping runs
// used to read the same active segment SHA and race on PutFile.
func TestConcurrentFlushKeepsEveryRecordOnce(t *testing.T) {
	client := &github.FakeClient{}
	cfg := Config{
		Name:                   "d",
		Version:                1,
		BaseRepo:               "o/r",
		DeltaBranch:            "b",
		MaxDeltaSegmentBytes:   1_000_000,
		MaxDeltaSegmentRecords: 1_000,
	}
	b := NewBaseDB(cfg).(*baseDB)
	b.baseGH, b.forkGH = client, client
	b.instanceID = "pod"
	b.writeVer = 1
	b.online = true

	const records = 40
	for i := 0; i < records; i++ {
		b.AppendMutation(MutationRecord{
			TS:    time.Now().UTC(),
			Op:    "set",
			Table: "t",
			Key:   fmt.Sprintf("k%d", i),
			Value: json.RawMessage(`{"id":"x"}`),
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.flush(context.Background()); err != nil {
				t.Errorf("flush: %v", err)
			}
		}()
	}
	wg.Wait()

	b.wbufMu.Lock()
	remaining := len(b.wbuf)
	b.wbufMu.Unlock()
	if remaining != 0 {
		t.Fatalf("write buffer length = %d, want 0", remaining)
	}

	entries, err := client.ListDir(context.Background(), "b", "d/v1")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var contents []byte
	for _, entry := range entries {
		data, _, err := client.GetFile(context.Background(), "b", "d/v1/"+entry.Name)
		if err != nil {
			t.Fatalf("GetFile %s: %v", entry.Name, err)
		}
		contents = append(contents, data...)
	}
	got, err := UnmarshalJSONL(contents)
	if err != nil {
		t.Fatalf("UnmarshalJSONL: %v", err)
	}
	seen := make(map[string]bool, len(got))
	for _, rec := range got {
		if seen[rec.Key] {
			t.Fatalf("record %q was written twice", rec.Key)
		}
		seen[rec.Key] = true
	}
	if len(seen) != records {
		t.Fatalf("persisted records = %d, want %d", len(seen), records)
	}
}
