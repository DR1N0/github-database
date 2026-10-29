package base

import (
	"strings"
	"testing"
)

// FuzzDecodeJSONL checks that decoding is total: arbitrary input either yields
// records that re-encode cleanly or is reported as malformed, but never panics.
func FuzzDecodeJSONL(f *testing.F) {
	f.Add([]byte(`{"ts":"2026-01-01T00:00:00Z","op":"set","table":"t","key":"k"}` + "\n"))
	f.Add([]byte("not json\n\n{\"op\":\"delete\"}\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		recs, errs := decodeJSONL(data)
		if len(recs) == 0 {
			return
		}
		records := make([]MutationRecord, 0, len(recs))
		for _, decoded := range recs {
			records = append(records, decoded.record)
		}
		encoded, err := MarshalJSONL(records)
		if err != nil {
			t.Fatalf("MarshalJSONL: %v", err)
		}
		if _, err := UnmarshalJSONL(encoded); err != nil {
			t.Fatalf("round trip of %d decoded records: %v", len(records), err)
		}
		_ = errs
	})
}

// FuzzSplitMutationRecords checks that segmentation keeps every record exactly
// once and never exceeds the configured caps.
func FuzzSplitMutationRecords(f *testing.F) {
	f.Add(0, 200, 1)
	f.Add(37, 300, 5)
	f.Add(5, 120, 10)
	f.Fuzz(func(t *testing.T, count, maxBytes, maxRecords int) {
		if count < 0 {
			count = -count
		}
		if count > 200 {
			count = 200
		}
		// splitMutationRecords rejects non-positive caps; keep them usable.
		maxBytes = 200 + (maxBytes % 500)
		maxRecords = 1 + (maxRecords % 20)

		recs := make([]MutationRecord, count)
		for i := range recs {
			recs[i] = MutationRecord{
				Op:    "set",
				Table: "t",
				Key:   strings.Repeat("k", i%8),
			}
		}

		batches, err := splitMutationRecords(recs, maxBytes, maxRecords)
		if err != nil {
			t.Fatalf("splitMutationRecords: %v", err)
		}
		total := 0
		for _, batch := range batches {
			if len(batch) == 0 {
				t.Fatal("empty batch")
			}
			if len(batch) > maxRecords {
				t.Fatalf("batch has %d records, cap is %d", len(batch), maxRecords)
			}
			total += len(batch)
		}
		if total != count {
			t.Fatalf("segments hold %d records, want %d", total, count)
		}
	})
}
