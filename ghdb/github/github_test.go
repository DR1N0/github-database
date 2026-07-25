package github

import (
	"context"
	"testing"
)

func TestFakeRoundTrip(t *testing.T) {
	fc := &FakeClient{}
	ctx := context.Background()

	// BranchExists returns false before creation
	ok, err := fc.BranchExists(ctx, "main")
	if err != nil || ok {
		t.Fatalf("expected branch not to exist: ok=%v err=%v", ok, err)
	}

	// CreateBranch + BranchExists
	if err := fc.CreateBranch(ctx, "main", "abc123"); err != nil {
		t.Fatal(err)
	}
	ok, err = fc.BranchExists(ctx, "main")
	if err != nil || !ok {
		t.Fatalf("expected branch to exist: ok=%v err=%v", ok, err)
	}

	// PutFile (create) + GetFile
	data := []byte(`{"hello":"world"}`)
	if err := fc.PutFile(ctx, "main", "foo/bar.json", "msg", data, ""); err != nil {
		t.Fatal(err)
	}
	got, sha, err := fc.GetFile(ctx, "main", "foo/bar.json")
	if err != nil || string(got) != string(data) || sha == "" {
		t.Fatalf("GetFile: got=%q sha=%q err=%v", got, sha, err)
	}

	// PutFile (update) + SHA change
	data2 := []byte(`{"hello":"updated"}`)
	if err := fc.PutFile(ctx, "main", "foo/bar.json", "update", data2, sha); err != nil {
		t.Fatal(err)
	}
	got2, sha2, err := fc.GetFile(ctx, "main", "foo/bar.json")
	if err != nil || string(got2) != string(data2) || sha2 == sha {
		t.Fatalf("update: got=%q sha2=%q sha=%q err=%v", got2, sha2, sha, err)
	}

	// ListDir
	entries, err := fc.ListDir(ctx, "main", "foo")
	if err != nil || len(entries) != 1 || entries[0].Name != "bar.json" {
		t.Fatalf("ListDir: %+v err=%v", entries, err)
	}

	// GetFile missing → ErrNotFound equivalent (non-nil error)
	_, _, err = fc.GetFile(ctx, "main", "does/not/exist.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}

	// DefaultBranch
	name, sha3, err := fc.DefaultBranch(ctx)
	if err != nil || name == "" || sha3 == "" {
		t.Fatalf("DefaultBranch: name=%q sha=%q err=%v", name, sha3, err)
	}

	// CreatePR
	url, err := fc.CreatePR(ctx, "title", "head", "main")
	if err != nil || url == "" {
		t.Fatalf("CreatePR: url=%q err=%v", url, err)
	}
	if len(fc.PRs()) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(fc.PRs()))
	}
}
