package ghdb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func testBaseline(t *testing.T) fstest.MapFS {
	t.Helper()
	meta, _ := json.Marshal(Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data",
		Tables: []TableSpec{{Name: "things", Key: "id"}},
	})
	return fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{"a":{"id":"a"}}`)},
	}
}

func TestOpenOfflineTable(t *testing.T) {
	db, err := NewOption(testBaseline(t)).Open()
	if err != nil {
		t.Fatal(err)
	}
	if db.Mode() != "table" {
		t.Errorf("mode: got %q want %q", db.Mode(), "table")
	}
	if db.IsOnline() {
		t.Error("expected offline")
	}
	v, ok := db.(TableDB).Table("things").Get("a")
	if !ok || string(v) != `{"id":"a"}` {
		t.Errorf("Get: got %q ok=%v", v, ok)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestOpenBadMeta(t *testing.T) {
	fsys := fstest.MapFS{"db_meta.json": {Data: []byte(`not json`)}}
	_, err := NewOption(fsys).Open()
	if err == nil {
		t.Fatal("expected error for bad db_meta.json")
	}
}

func TestOpenMissingMeta(t *testing.T) {
	_, err := NewOption(fstest.MapFS{}).Open()
	if err == nil {
		t.Fatal("expected error for missing db_meta.json")
	}
}

func TestOpenOfflineCheckpointReturnsError(t *testing.T) {
	db, err := NewOption(testBaseline(t)).Open()
	if err != nil {
		t.Fatal(err)
	}
	err = db.Checkpoint(context.Background())
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("expected ErrOffline, got: %v", err)
	}
}

func TestOpenValidDeltaSegmentConfig(t *testing.T) {
	baseline := testBaseline(t)
	meta, err := json.Marshal(Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data",
		MaxDeltaSegmentBytes: 512_000, MaxDeltaSegmentRecords: 500,
		Tables: []TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline["db_meta.json"] = &fstest.MapFile{Data: meta}

	if _, err := NewOption(baseline).Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestOpenDefaultDeltaSegmentConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta []byte
	}{
		{
			name: "missing fields",
			meta: []byte(`{"name":"testdb","version":1,"mode":"table","github_repo":"owner/repo","delta_branch":"ghdb-data","tables":[{"name":"things","key":"id"}]}`),
		},
		{
			name: "zero fields",
			meta: []byte(`{"name":"testdb","version":1,"mode":"table","github_repo":"owner/repo","delta_branch":"ghdb-data","max_delta_segment_bytes":0,"max_delta_segment_records":0,"tables":[{"name":"things","key":"id"}]}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline := testBaseline(t)
			baseline["db_meta.json"] = &fstest.MapFile{Data: tc.meta}
			cfg, _, _, err := parseAndLoad(baseline)
			if err != nil {
				t.Fatalf("parseAndLoad: %v", err)
			}
			if cfg.MaxDeltaSegmentBytes != 786_432 {
				t.Errorf("MaxDeltaSegmentBytes = %d, want 786432", cfg.MaxDeltaSegmentBytes)
			}
			if cfg.MaxDeltaSegmentRecords != 1_000 {
				t.Errorf("MaxDeltaSegmentRecords = %d, want 1000", cfg.MaxDeltaSegmentRecords)
			}
		})
	}
}

func TestOpenRejectsOversizedDeltaSegmentBytes(t *testing.T) {
	baseline := testBaseline(t)
	meta, err := json.Marshal(Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data",
		MaxDeltaSegmentBytes: 917_505,
		Tables:               []TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline["db_meta.json"] = &fstest.MapFile{Data: meta}

	_, err = NewOption(baseline).Open()
	if err == nil || !strings.Contains(err.Error(), "max_delta_segment_bytes") {
		t.Fatalf("Open error = %v, want max_delta_segment_bytes validation error", err)
	}
}
