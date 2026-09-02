package base

import (
	"encoding/json"
	"testing"
)

func TestDeltaSegmentCapsUseDefaults(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"max_delta_segment_bytes":0,"max_delta_segment_records":0}`,
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if got := deltaSegmentBytes(cfg); got != DefaultMaxDeltaSegmentBytes {
			t.Errorf("deltaSegmentBytes() = %d, want %d", got, DefaultMaxDeltaSegmentBytes)
		}
		if got := deltaSegmentRecords(cfg); got != DefaultMaxDeltaSegmentRecords {
			t.Errorf("deltaSegmentRecords() = %d, want %d", got, DefaultMaxDeltaSegmentRecords)
		}
	}

	cfg := Config{MaxDeltaSegmentBytes: 512_000, MaxDeltaSegmentRecords: 500}
	if got := deltaSegmentBytes(cfg); got != 512_000 {
		t.Errorf("deltaSegmentBytes() = %d, want 512000", got)
	}
	if got := deltaSegmentRecords(cfg); got != 500 {
		t.Errorf("deltaSegmentRecords() = %d, want 500", got)
	}
}
