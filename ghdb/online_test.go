package ghdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DR1N0/github-database/ghdb"
	"github.com/DR1N0/github-database/ghdb/github"
)

// onlineBaseline builds a minimal table baseline for online tests.
func onlineBaseline(t *testing.T) fstest.MapFS {
	t.Helper()
	meta, _ := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data",
		Tables: []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	return fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{"a":{"id":"a"}}`)},
	}
}

// newOnlineFake creates a FakeClient with delta_branch pre-created.
func newOnlineFake(t *testing.T) (*github.FakeClient, fstest.MapFS) {
	t.Helper()
	fc := &github.FakeClient{}
	if err := fc.CreateBranch(context.Background(), "ghdb-data", "sha0"); err != nil {
		t.Fatal(err)
	}
	return fc, onlineBaseline(t)
}

func TestOpenOnlineCreatesBranch(t *testing.T) {
	fc := &github.FakeClient{}
	db, err := ghdb.OpenWithClient(onlineBaseline(t), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !db.IsOnline() {
		t.Error("expected online")
	}
	ok, _ := fc.BranchExists(context.Background(), "ghdb-data")
	if !ok {
		t.Error("expected delta_branch to be created")
	}
	_, _, err = fc.GetFile(context.Background(), "ghdb-data", "testdb/v1/_init.json")
	if err == nil {
		t.Error("_init.json must not exist in V2")
	}
	db.Close(context.Background())
}

func TestOpenOnlineReplay(t *testing.T) {
	fc, bl := newOnlineFake(t)
	rec := ghdb.MutationRecord{
		TS:    time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Op:    "set",
		Table: "things",
		Key:   "b",
		Value: json.RawMessage(`{"id":"b"}`),
	}
	data, _ := ghdb.MarshalJSONL([]ghdb.MutationRecord{rec})
	fc.PutFile(context.Background(), "ghdb-data", "testdb/v1/other.jsonl", "prior", data, "")
	db, err := ghdb.OpenWithClient(bl, fc)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := db.(ghdb.TableDB).Table("things").Get("b")
	if !ok || string(v) != `{"id":"b"}` {
		t.Errorf("replay: got %q ok=%v", v, ok)
	}
	db.Close(context.Background())
}

func TestOpenOnlineSegmentReplayAndPoll(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	record := func(ts time.Time, key string) ghdb.MutationRecord {
		return ghdb.MutationRecord{
			TS: ts, Op: "set", Table: "things", Key: key,
			Value: json.RawMessage(`{"id":"` + key + `"}`),
		}
	}
	paths := []struct {
		path string
		rec  ghdb.MutationRecord
	}{
		{"testdb/v1/pod-a.jsonl", record(time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC), "legacy")},
		{"testdb/v1/pod-a-20260902T010001Z.jsonl", record(time.Date(2026, 9, 2, 1, 0, 1, 0, time.UTC), "rollover-one")},
		{"testdb/v1/pod-a-20260902T010002Z.jsonl", record(time.Date(2026, 9, 2, 1, 0, 2, 0, time.UTC), "rollover-two")},
	}
	for _, entry := range paths {
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{entry.rec})
		if err != nil {
			t.Fatal(err)
		}
		if err := fc.PutFile(context.Background(), "ghdb-data", entry.path, "seed", data, ""); err != nil {
			t.Fatal(err)
		}
	}

	db, err := ghdb.OpenWithClient(baseline, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	table := db.(ghdb.TableDB).Table("things")
	for _, key := range []string{"legacy", "rollover-one", "rollover-two"} {
		if value, ok := table.Get(key); !ok || string(value) != `{"id":"`+key+`"}` {
			t.Fatalf("replay %q = %q, %v; want matching record", key, value, ok)
		}
	}

	rolloverPath := "testdb/v1/pod-a-20260902T010002Z.jsonl"
	existing, sha, err := fc.GetFile(context.Background(), "ghdb-data", rolloverPath)
	if err != nil {
		t.Fatal(err)
	}
	newRecord := record(time.Date(2026, 9, 2, 1, 0, 3, 0, time.UTC), "polled")
	newLine, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{newRecord})
	if err != nil {
		t.Fatal(err)
	}
	if err := fc.PutFile(context.Background(), "ghdb-data", rolloverPath, "append", append(existing, newLine...), sha); err != nil {
		t.Fatal(err)
	}

	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, ok := table.Get("polled"); !ok || string(value) != `{"id":"polled"}` {
		t.Fatalf("polled record = %q, %v; want matching record", value, ok)
	}
	callsAfterFirstPoll := fc.GetFileCallCount()
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fc.GetFileCallCount(); got != callsAfterFirstPoll {
		t.Fatalf("GetFile calls after unchanged poll = %d, want %d", got, callsAfterFirstPoll)
	}
	if got := len(table.All()); got != 5 {
		t.Fatalf("record count after repeated poll = %d, want 5", got)
	}
}

func TestPollSkipsUnchangedStartupSegments(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	for _, entry := range []struct {
		path string
		rec  ghdb.MutationRecord
	}{
		{
			path: "testdb/v1/pod-a.jsonl",
			rec:  ghdb.MutationRecord{TS: time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC), Op: "set", Table: "things", Key: "legacy", Value: json.RawMessage(`{"id":"legacy"}`)},
		},
		{
			path: "testdb/v1/pod-a-20260902T010001Z.jsonl",
			rec:  ghdb.MutationRecord{TS: time.Date(2026, 9, 2, 1, 0, 1, 0, time.UTC), Op: "set", Table: "things", Key: "rollover", Value: json.RawMessage(`{"id":"rollover"}`)},
		},
	} {
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{entry.rec})
		if err != nil {
			t.Fatal(err)
		}
		if err := fc.PutFile(context.Background(), "ghdb-data", entry.path, "seed", data, ""); err != nil {
			t.Fatal(err)
		}
	}

	db, err := ghdb.OpenWithClient(baseline, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var reapplied int
	ghdb.SetApplyFn(db, func(ghdb.MutationRecord) { reapplied++ })
	callsBeforePoll := fc.GetFileCallCount()

	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fc.GetFileCallCount(); got != callsBeforePoll {
		t.Fatalf("GetFile calls after unchanged startup poll = %d, want %d", got, callsBeforePoll)
	}
	if reapplied != 0 {
		t.Fatalf("reapplied records after unchanged startup poll = %d, want 0", reapplied)
	}
}

func TestPollUsesBaselineWatermarkForStartupSegments(t *testing.T) {
	cutoff := time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)
	fc := &github.FakeClient{}
	if err := fc.CreateBranch(context.Background(), "ghdb-data", "sha0"); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data", BaselineTime: cutoff,
		Tables: []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline := fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{"a":{"id":"a"}}`)},
	}
	preBaseline := ghdb.MutationRecord{
		TS: cutoff.Add(-time.Second), Op: "set", Table: "things", Key: "before",
		Value: json.RawMessage(`{"id":"before"}`),
	}
	for _, path := range []string{
		"testdb/v1/pod-a.jsonl",
		"testdb/v1/pod-a-20260902T010000Z.jsonl",
	} {
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{preBaseline})
		if err != nil {
			t.Fatal(err)
		}
		if err := fc.PutFile(context.Background(), "ghdb-data", path, "seed", data, ""); err != nil {
			t.Fatal(err)
		}
	}

	db, err := ghdb.OpenWithClient(baseline, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	rolloverPath := "testdb/v1/pod-a-20260902T010000Z.jsonl"
	existing, sha, err := fc.GetFile(context.Background(), "ghdb-data", rolloverPath)
	if err != nil {
		t.Fatal(err)
	}
	postBaseline := ghdb.MutationRecord{
		TS: cutoff.Add(time.Second), Op: "set", Table: "things", Key: "after",
		Value: json.RawMessage(`{"id":"after"}`),
	}
	newLine, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{postBaseline})
	if err != nil {
		t.Fatal(err)
	}
	if err := fc.PutFile(context.Background(), "ghdb-data", rolloverPath, "append", append(existing, newLine...), sha); err != nil {
		t.Fatal(err)
	}

	var applied []string
	ghdb.SetApplyFn(db, func(rec ghdb.MutationRecord) { applied = append(applied, rec.Key) })
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "after" {
		t.Fatalf("polled records = %q, want only post-baseline record", applied)
	}
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("polled record count after unchanged poll = %d, want 1", len(applied))
	}
}

func TestCommitterFromAPI(t *testing.T) {
	fc, bl := newOnlineFake(t)
	fc.AuthenticatedName = "API User"
	fc.AuthenticatedEmail = "<EMAIL_ADDRESS>"
	db, err := ghdb.OpenWithClient(bl, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	commit := fc.LastCommit()
	if commit.Name != "API User" {
		t.Errorf("committer name: got %q want %q", commit.Name, "API User")
	}
	if commit.Email != "<EMAIL_ADDRESS>" {
		t.Errorf("committer email: got %q want %q", commit.Email, "<EMAIL_ADDRESS>")
	}
}

func TestCommitterFromOption(t *testing.T) {
	fc, bl := newOnlineFake(t)
	fc.AuthenticatedName = "API User" // would be used if override not set
	db, err := ghdb.OpenWithClientAndCommitter(bl, fc, "Bot", "<EMAIL_ADDRESS>")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	commit := fc.LastCommit()
	if commit.Name != "Bot" {
		t.Errorf("committer name: got %q want %q", commit.Name, "Bot")
	}
	if commit.Email != "<EMAIL_ADDRESS>" {
		t.Errorf("committer email: got %q want %q", commit.Email, "<EMAIL_ADDRESS>")
	}
}

func TestCheckpointCreatesOneCommit(t *testing.T) {
	fc, bl := newOnlineFake(t)
	db, err := ghdb.OpenWithClient(bl, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Exactly one PR should have been created.
	prs := fc.PRs()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	// CreateCommit must have been called (LastCommit non-zero means it was called).
	if fc.LastCommit().TreeSHA == "" {
		t.Error("expected CreateCommit to be called; LastCommit.TreeSHA is empty")
	}
}

func TestCheckpointUnsignedCommit(t *testing.T) {
	fc, bl := newOnlineFake(t)
	db, err := ghdb.OpenWithClient(bl, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}

	if sig := fc.LastCommit().Signature; sig != "" {
		t.Errorf("unsigned checkpoint: expected empty signature, got %q", sig)
	}
}

func TestCheckpointSignedCommit(t *testing.T) {
	fc, bl := newOnlineFake(t)
	const sentinel = "-----BEGIN PGP SIGNATURE-----\nsentinel\n-----END PGP SIGNATURE-----"
	db, err := ghdb.OpenWithClientAndSigner(bl, fc, func(_ []byte) (string, error) {
		return sentinel, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}

	if sig := fc.LastCommit().Signature; sig != sentinel {
		t.Errorf("signed checkpoint: got signature %q, want %q", sig, sentinel)
	}
}

func TestCheckpointSignerError(t *testing.T) {
	fc, bl := newOnlineFake(t)
	db, err := ghdb.OpenWithClientAndSigner(bl, fc, func(_ []byte) (string, error) {
		return "", fmt.Errorf("gpg agent not available")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	err = db.Checkpoint(context.Background())
	if err == nil {
		t.Fatal("expected error when signer fails")
	}
	if !strings.Contains(err.Error(), "sign commit") {
		t.Errorf("error should mention sign commit, got: %v", err)
	}
}

func TestCommitSignerRequiresCommitter(t *testing.T) {
	fc, bl := newOnlineFake(t)
	_, err := ghdb.OpenWithClientAndSigner(bl, fc, func(_ []byte) (string, error) {
		return "sig", nil
	})
	// OpenWithClientAndSigner sets Committer internally, so this should succeed.
	if err != nil {
		t.Errorf("OpenWithClientAndSigner should succeed when committer is set: %v", err)
	}

	// Direct call without Committer must fail.
	_, err = ghdb.OpenWithClient(bl, &github.FakeClient{})
	if err != nil {
		t.Fatalf("open without signer should succeed: %v", err)
	}
}

func TestCommitSignerWithoutCommitterFails(t *testing.T) {
	_, bl := newOnlineFake(t)
	_, err := ghdb.OpenSignerOnly(bl, func(_ []byte) (string, error) { return "sig", nil })
	if err == nil {
		t.Fatal("expected error when CommitSigner is set without Committer")
	}
	if !strings.Contains(err.Error(), "Committer") {
		t.Errorf("error should mention Committer, got: %v", err)
	}
}

func TestOpenOnlineBaselineTimeCutoff(t *testing.T) {
	fc := &github.FakeClient{}
	if err := fc.CreateBranch(context.Background(), "ghdb-data", "sha0"); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo:   "owner/repo",
		DeltaBranch:  "ghdb-data",
		BaselineTime: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		Tables:       []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	bl := fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{"a":{"id":"a"}}`)},
	}
	rec := ghdb.MutationRecord{
		TS:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Op:    "set",
		Table: "things",
		Key:   "shouldNotAppear",
		Value: json.RawMessage(`{}`),
	}
	data, _ := ghdb.MarshalJSONL([]ghdb.MutationRecord{rec})
	fc.PutFile(context.Background(), "ghdb-data", "testdb/v1/x.jsonl", "x", data, "")
	db, err := ghdb.OpenWithClient(bl, fc)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := db.(ghdb.TableDB).Table("things").Get("shouldNotAppear")
	if ok {
		t.Error("record before cutoff should have been skipped")
	}
	db.Close(context.Background())
}

func onlineBaselineWithSegmentCaps(t *testing.T, maxBytes, maxRecords int) fstest.MapFS {
	t.Helper()
	meta, err := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		GitHubRepo: "owner/repo", DeltaBranch: "ghdb-data",
		MaxDeltaSegmentBytes: maxBytes, MaxDeltaSegmentRecords: maxRecords,
		Tables: []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{"a":{"id":"a"}}`)},
	}
}

func openOnlineWithSegmentCaps(t *testing.T, maxBytes, maxRecords int) (*github.FakeClient, ghdb.DB) {
	t.Helper()
	fc := &github.FakeClient{}
	if err := fc.CreateBranch(context.Background(), "ghdb-data", "sha0"); err != nil {
		t.Fatal(err)
	}
	db, err := ghdb.OpenWithClient(onlineBaselineWithSegmentCaps(t, maxBytes, maxRecords), fc)
	if err != nil {
		t.Fatal(err)
	}
	return fc, db
}

func setSegmentRecord(t *testing.T, db ghdb.DB, key string, padding int) {
	t.Helper()
	value, err := json.Marshal(map[string]string{"id": key, "padding": strings.Repeat("x", padding)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.(ghdb.TableDB).Table("things").Set(key, value); err != nil {
		t.Fatal(err)
	}
}

func segmentContents(t *testing.T, fc *github.FakeClient, instanceID string) map[string][]byte {
	t.Helper()
	entries, err := fc.ListDir(context.Background(), "ghdb-data", "testdb/v1")
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string][]byte)
	for _, entry := range entries {
		if entry.Name == instanceID+".jsonl" || strings.HasPrefix(entry.Name, instanceID+"-") {
			content, _, err := fc.GetFile(context.Background(), "ghdb-data", "testdb/v1/"+entry.Name)
			if err != nil {
				t.Fatal(err)
			}
			contents[entry.Name] = content
		}
	}
	return contents
}

func segmentNameOrder(name, instanceID string) (string, int) {
	base := strings.TrimSuffix(strings.TrimPrefix(name, instanceID+"-"), ".jsonl")
	if dash := strings.LastIndexByte(base, '-'); dash >= 0 {
		if sequence, err := strconv.Atoi(base[dash+1:]); err == nil && sequence >= 2 {
			return base[:dash], sequence
		}
	}
	return base, 1
}

func orderedSegmentRecords(t *testing.T, contents map[string][]byte, instanceID string) []ghdb.MutationRecord {
	t.Helper()
	names := make([]string, 0, len(contents))
	for name := range contents {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == instanceID+".jsonl" {
			return true
		}
		if names[j] == instanceID+".jsonl" {
			return false
		}
		leftTimestamp, leftSequence := segmentNameOrder(names[i], instanceID)
		rightTimestamp, rightSequence := segmentNameOrder(names[j], instanceID)
		if leftTimestamp == rightTimestamp {
			return leftSequence < rightSequence
		}
		return leftTimestamp < rightTimestamp
	})
	var records []ghdb.MutationRecord
	for _, name := range names {
		recs, err := ghdb.UnmarshalJSONL(contents[name])
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, recs...)
	}
	return records
}

func TestFlushSegmentRollover(t *testing.T) {
	const maxBytes = 400
	fc, db := openOnlineWithSegmentCaps(t, maxBytes, 100)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-a")

	setSegmentRecord(t, db, "one", 80)
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	setSegmentRecord(t, db, "two", 80)
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyPath := "testdb/v1/pod-a.jsonl"
	legacyBefore, _, err := fc.GetFile(context.Background(), "ghdb-data", legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	setSegmentRecord(t, db, "three", 80)
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyAfter, _, err := fc.GetFile(context.Background(), "ghdb-data", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyAfter) != string(legacyBefore) {
		t.Fatal("full legacy segment changed instead of rolling over")
	}

	contents := segmentContents(t, fc, "pod-a")
	if len(contents) != 2 {
		t.Fatalf("segment count = %d, want 2", len(contents))
	}
	for name, content := range contents {
		if len(content) > maxBytes {
			t.Errorf("segment %s has %d bytes, cap is %d", name, len(content), maxBytes)
		}
	}
	records := orderedSegmentRecords(t, contents, "pod-a")
	if len(records) != 3 || records[0].Key != "one" || records[1].Key != "two" || records[2].Key != "three" {
		t.Fatalf("flushed records = %#v, want one, two, three in order", records)
	}
}

func TestFlushSegmentBatchRollover(t *testing.T) {
	const maxBytes = 400
	fc, db := openOnlineWithSegmentCaps(t, maxBytes, 100)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-batch")
	for _, key := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		setSegmentRecord(t, db, key, 80)
	}
	started := time.Now()
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Flush took %v creating rollover segments, want no clock wait", elapsed)
	}

	contents := segmentContents(t, fc, "pod-batch")
	if len(contents) != 4 {
		t.Fatalf("segment count = %d, want 4", len(contents))
	}
	for name, content := range contents {
		if len(content) > maxBytes {
			t.Errorf("segment %s has %d bytes, cap is %d", name, len(content), maxBytes)
		}
	}
	records := orderedSegmentRecords(t, contents, "pod-batch")
	want := []string{"one", "two", "three", "four", "five", "six", "seven"}
	if len(records) != len(want) {
		t.Fatalf("record count = %d, want %d", len(records), len(want))
	}
	for i, key := range want {
		if records[i].Key != key {
			t.Fatalf("record %d key = %q, want %q", i, records[i].Key, key)
		}
	}
}

func TestFlushSegmentRecordCap(t *testing.T) {
	fc, db := openOnlineWithSegmentCaps(t, 1_000, 1)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-record-cap")
	for _, key := range []string{"one", "two", "three"} {
		setSegmentRecord(t, db, key, 20)
	}
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}

	contents := segmentContents(t, fc, "pod-record-cap")
	if len(contents) != 3 {
		t.Fatalf("segment count = %d, want 3", len(contents))
	}
	for name, content := range contents {
		records, err := ghdb.UnmarshalJSONL(content)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 {
			t.Errorf("segment %s has %d records, cap is 1", name, len(records))
		}
	}
}

func TestFlushOversizedMutationUsesImmutableSegment(t *testing.T) {
	const maxBytes = 300
	fc, db := openOnlineWithSegmentCaps(t, maxBytes, 100)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-oversized")
	setSegmentRecord(t, db, "large", 500)

	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	entries, err := fc.ListDir(context.Background(), "ghdb-data", "testdb/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("segment entries = %#v, want one immutable segment", entries)
	}
	content, _, err := fc.GetFile(context.Background(), "ghdb-data", "testdb/v1/"+entries[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) <= maxBytes {
		t.Fatalf("immutable segment has %d bytes, want over normal cap", len(content))
	}
	setSegmentRecord(t, db, "normal", 1)
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err = fc.ListDir(context.Background(), "ghdb-data", "testdb/v1")
	if err != nil || len(entries) != 2 {
		t.Fatalf("segments after normal mutation = %#v, %v; want two", entries, err)
	}
	foundNormal := false
	for _, entry := range entries {
		data, _, err := fc.GetFile(context.Background(), "ghdb-data", "testdb/v1/"+entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		records, err := ghdb.UnmarshalJSONL(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].Key == "normal" {
			foundNormal = true
			if len(data) > maxBytes {
				t.Fatalf("normal segment %s has %d bytes, cap %d", entry.Name, len(data), maxBytes)
			}
		}
	}
	if !foundNormal {
		t.Fatal("normal mutation was not written to its own segment")
	}
	replayed, err := ghdb.OpenWithClient(onlineBaselineWithSegmentCaps(t, maxBytes, 100), fc)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close(context.Background())
	for _, key := range []string{"large", "normal"} {
		if _, ok := replayed.(ghdb.TableDB).Table("things").Get(key); !ok {
			t.Fatalf("replay missing %q", key)
		}
	}
}

func TestFlushSelectsLatestInstanceSegment(t *testing.T) {
	fc, db := openOnlineWithSegmentCaps(t, 1_000, 100)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-a")

	seed := func(key string) []byte {
		t.Helper()
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{{TS: time.Now().UTC(), Op: "set", Table: "things", Key: key, Value: json.RawMessage(`{"id":"` + key + `"}`)}})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	legacy := seed("legacy")
	rollover := seed("rollover")
	other := seed("other")
	paths := map[string][]byte{
		"testdb/v1/pod-a.jsonl":                  legacy,
		"testdb/v1/pod-a-20260902T010203Z.jsonl": rollover,
		"testdb/v1/pod-b-20260902T010203Z.jsonl": other,
	}
	for path, content := range paths {
		if err := fc.PutFile(context.Background(), "ghdb-data", path, "seed", content, ""); err != nil {
			t.Fatal(err)
		}
	}

	setSegmentRecord(t, db, "new", 20)
	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		"testdb/v1/pod-a.jsonl":                  legacy,
		"testdb/v1/pod-b-20260902T010203Z.jsonl": other,
	} {
		got, _, err := fc.GetFile(context.Background(), "ghdb-data", path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s changed unexpectedly", path)
		}
	}
	updated, _, err := fc.GetFile(context.Background(), "ghdb-data", "testdb/v1/pod-a-20260902T010203Z.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	records, err := ghdb.UnmarshalJSONL(updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Key != "new" {
		t.Fatalf("latest segment records = %#v, want rollover then new", records)
	}
}

func TestFlushReadFailureRetainsBuffer(t *testing.T) {
	fc, db := openOnlineWithSegmentCaps(t, 1_000, 100)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-read")
	seed, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{{TS: time.Now().UTC(), Op: "set", Table: "things", Key: "old", Value: json.RawMessage(`{"id":"old"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fc.PutFile(context.Background(), "ghdb-data", "testdb/v1/pod-read.jsonl", "seed", seed, ""); err != nil {
		t.Fatal(err)
	}
	setSegmentRecord(t, db, "new", 20)
	putCalls := fc.PutFileCallCount()
	fc.SetGetFileError(errors.New("injected read failure"))

	if err := ghdb.Flush(db, context.Background()); err == nil {
		t.Fatal("Flush error = nil, want injected read failure")
	}
	if got := fc.PutFileCallCount(); got != putCalls {
		t.Fatalf("PutFile calls = %d, want %d", got, putCalls)
	}
	if got := ghdb.EngineWbufLen(db); got != 1 {
		t.Fatalf("write buffer length = %d, want 1", got)
	}
}

func TestFlushPartialWriteFailureRestoresOnlyUnpersisted(t *testing.T) {
	fc, db := openOnlineWithSegmentCaps(t, 1_000, 1)
	defer db.Close(context.Background())
	ghdb.SetInstanceID(db, "pod-partial")
	setSegmentRecord(t, db, "one", 20)
	setSegmentRecord(t, db, "two", 20)
	fc.SetPutFileErrorAfter(1, errors.New("injected write failure"))

	if err := ghdb.Flush(db, context.Background()); err == nil {
		t.Fatal("Flush error = nil, want injected write failure")
	}
	if got := ghdb.EngineWbufLen(db); got != 1 {
		t.Fatalf("write buffer length after partial failure = %d, want 1", got)
	}
	contents := segmentContents(t, fc, "pod-partial")
	records := orderedSegmentRecords(t, contents, "pod-partial")
	if len(records) != 1 || records[0].Key != "one" {
		t.Fatalf("persisted records after partial failure = %#v, want only one", records)
	}

	if err := ghdb.Flush(db, context.Background()); err != nil {
		t.Fatalf("Flush retry: %v", err)
	}
	contents = segmentContents(t, fc, "pod-partial")
	records = orderedSegmentRecords(t, contents, "pod-partial")
	if len(records) != 2 || records[0].Key != "one" || records[1].Key != "two" {
		t.Fatalf("records after retry = %#v, want one, two exactly once", records)
	}
}
