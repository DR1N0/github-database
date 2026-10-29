package base

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb/internal/githubtest"
)

// benchRecords builds n realistic mutation records: a table set with a small
// JSON object payload.
func benchRecords(n int) []MutationRecord {
	recs := make([]MutationRecord, n)
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range recs {
		recs[i] = MutationRecord{
			TS:    ts.Add(time.Duration(i) * time.Millisecond),
			Op:    "set",
			Table: "services",
			Key:   fmt.Sprintf("svc-%04d", i),
			Value: json.RawMessage(`{"name":"payments","replicas":3,"labels":{"team":"core"}}`),
		}
	}
	return recs
}

func BenchmarkMarshalJSONL(b *testing.B) {
	recs := benchRecords(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MarshalJSONL(recs); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeJSONL(b *testing.B) {
	data, err := MarshalJSONL(benchRecords(1000))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, errs := decodeJSONL(data); len(errs) > 0 {
			b.Fatal(errs[0])
		}
	}
}

func BenchmarkSplitMutationRecords(b *testing.B) {
	recs := benchRecords(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := splitMutationRecords(recs, DefaultMaxDeltaSegmentBytes, DefaultMaxDeltaSegmentRecords); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONMerge(b *testing.B) {
	existing := json.RawMessage(`{"name":"payments","replicas":3,"labels":{"team":"core"}}`)
	fields := map[string]json.RawMessage{"replicas": json.RawMessage(`4`)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := JSONMerge(existing, fields); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFlush measures one drain of 500 buffered records into a segment.
func BenchmarkFlush(b *testing.B) {
	recs := benchRecords(500)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		client := &githubtest.FakeClient{}
		db := NewBaseDB(Config{
			Name: "bench", Version: 1, BaseRepo: "owner/repo", DeltaBranch: "ghdb-data",
		}).(*baseDB)
		db.baseGH, db.forkGH = client, client
		db.instanceID = "pod"
		db.writeVer = 1
		for _, r := range recs {
			db.AppendMutation(r)
		}
		b.StartTimer()
		if err := db.flush(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
