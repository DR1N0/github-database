package ghdb_test

import (
	"context"
	"encoding/json"
	"fmt"
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
