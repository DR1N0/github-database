package ghdb

import (
	"context"
	"encoding/json"
	"errors"
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
