package base

import (
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

func TestInstanceSegmentOwnershipAndLatestSelection(t *testing.T) {
	const instanceID = "pod-a"
	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: "pod-a.jsonl", want: true},
		{name: "pod-a-20260902T010203Z.jsonl", want: true},
		{name: "pod-a-legacy-segment", want: true},
		{name: "pod-b-20260902T010203Z.jsonl", want: false},
	} {
		if got := isInstanceSegment(tc.name, instanceID); got != tc.want {
			t.Errorf("isInstanceSegment(%q, %q) = %v, want %v", tc.name, instanceID, got, tc.want)
		}
	}

	entries := []github.DirEntry{
		{Name: "pod-a.jsonl"},
		{Name: "pod-a-20260902T010203Z.jsonl"},
		{Name: "pod-a-20260902T010203Z-2.jsonl"},
		{Name: "pod-b-20260902T010205Z.jsonl"},
	}
	if got, want := latestInstanceSegment(entries, instanceID), "pod-a-20260902T010203Z-2.jsonl"; got != want {
		t.Errorf("latestInstanceSegment() = %q, want %q", got, want)
	}
}

func TestRolloverSegmentNameUsesAvailableNumericSuffixWithoutWaiting(t *testing.T) {
	now := time.Date(2026, 9, 2, 1, 2, 3, 999_999_999, time.UTC)
	used := map[string]struct{}{
		"pod-a-20260902T010203Z.jsonl":   {},
		"pod-a-20260902T010203Z-2.jsonl": {},
	}
	if got, want := rolloverSegmentName("pod-a", now, used), "pod-a-20260902T010203Z-3.jsonl"; got != want {
		t.Errorf("rolloverSegmentName() = %q, want %q", got, want)
	}
}

func TestSplitMutationRecordsKeepsBatchesBoundedAndOrdered(t *testing.T) {
	recs := []MutationRecord{
		{TS: time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC), Op: "set", Key: "one"},
		{TS: time.Date(2026, 9, 2, 1, 0, 1, 0, time.UTC), Op: "set", Key: "two"},
		{TS: time.Date(2026, 9, 2, 1, 0, 2, 0, time.UTC), Op: "set", Key: "three"},
	}
	first, err := MarshalJSONL(recs[:1])
	if err != nil {
		t.Fatal(err)
	}
	batches, err := splitMutationRecords(recs, len(first)*2, 2)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, batch := range batches {
		data, err := MarshalJSONL(batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > len(first)*2 || len(batch) > 2 {
			t.Fatalf("batch is not bounded: %d bytes, %d records", len(data), len(batch))
		}
		for _, rec := range batch {
			keys = append(keys, rec.Key)
		}
	}
	if got, want := len(keys), len(recs); got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}
	for i, rec := range recs {
		if keys[i] != rec.Key {
			t.Fatalf("record %d key = %q, want %q", i, keys[i], rec.Key)
		}
	}
}
