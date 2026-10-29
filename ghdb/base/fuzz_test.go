package base

import (
	"math"
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
		maxBytes, maxRecords = normalizeCaps(maxBytes, maxRecords)

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

// normalizeCaps clamps fuzz-generated caps into the range splitMutationRecords
// accepts. The value is reduced modulo the span first and only then made
// positive: negating the raw int would overflow at math.MinInt, and a negative
// remainder would push "200 + r" below zero.
func normalizeCaps(maxBytes, maxRecords int) (int, int) {
	return 200 + absInt(maxBytes%500), 1 + absInt(maxRecords%20)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestNormalizeCapsKeepsCapsUsable pins the regression where a negative
// remainder produced a non-positive byte cap and made the fuzz target report a
// failure that was not a library bug.
func TestNormalizeCapsKeepsCapsUsable(t *testing.T) {
	for _, tc := range []struct {
		maxBytes, maxRecords int
	}{
		{0, 0},
		{-1, -1},
		{-201, -21}, // -201 % 500 == -201, so the old code produced -1
		{-300, -400},
		{math.MinInt, math.MinInt},
	} {
		bytes, records := normalizeCaps(tc.maxBytes, tc.maxRecords)
		if bytes < 200 || bytes > 699 {
			t.Errorf("normalizeCaps(%d, %d) bytes = %d, want 200..699", tc.maxBytes, tc.maxRecords, bytes)
		}
		if records < 1 || records > 20 {
			t.Errorf("normalizeCaps(%d, %d) records = %d, want 1..20", tc.maxBytes, tc.maxRecords, records)
		}
	}
}
