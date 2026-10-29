package ghdb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DR1N0/github-database/ghdb"
	"github.com/DR1N0/github-database/ghdb/github"
)

// recordingClient wraps a github.Interface and records which methods were
// called on it, so tests can confirm reads land on the base client and writes
// land on the fork client.
type recordingClient struct {
	github.Interface
	calls []string
}

func (c *recordingClient) record(name string) { c.calls = append(c.calls, name) }

func (c *recordingClient) DefaultBranch(ctx context.Context) (string, string, error) {
	c.record("DefaultBranch")
	return c.Interface.DefaultBranch(ctx)
}

func (c *recordingClient) GetBranchHead(ctx context.Context, branch string) (string, error) {
	c.record("GetBranchHead")
	return c.Interface.GetBranchHead(ctx, branch)
}

func (c *recordingClient) GetCommitTree(ctx context.Context, commitSHA string) (string, error) {
	c.record("GetCommitTree")
	return c.Interface.GetCommitTree(ctx, commitSHA)
}

func (c *recordingClient) CreateBranch(ctx context.Context, name, fromSHA string) error {
	c.record("CreateBranch")
	return c.Interface.CreateBranch(ctx, name, fromSHA)
}

func (c *recordingClient) CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (string, error) {
	c.record("CreateTree")
	return c.Interface.CreateTree(ctx, baseTreeSHA, files)
}

func (c *recordingClient) CreateCommit(ctx context.Context, treeSHA, parentSHA, message, name, email string, ts time.Time, signature string) (string, error) {
	c.record("CreateCommit")
	return c.Interface.CreateCommit(ctx, treeSHA, parentSHA, message, name, email, ts, signature)
}

func (c *recordingClient) UpdateRef(ctx context.Context, branch, commitSHA string) error {
	c.record("UpdateRef")
	return c.Interface.UpdateRef(ctx, branch, commitSHA)
}

func (c *recordingClient) CreatePR(ctx context.Context, title, head, base string) (string, error) {
	c.record("CreatePR")
	return c.Interface.CreatePR(ctx, title, head, base)
}

func (c *recordingClient) has(method string) bool {
	for _, m := range c.calls {
		if m == method {
			return true
		}
	}
	return false
}

// forkBaseline builds a db_meta.json configured with a distinct base_repo and
// fork_repo, for cross-repo checkpoint tests.
func forkBaseline(t *testing.T) fstest.MapFS {
	t.Helper()
	meta, err := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		BaseRepo: "upstream/repo", ForkRepo: "forkowner/repo", DeltaBranch: "ghdb-data",
		Tables: []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		"db_meta.json":       {Data: meta},
		"tables/things.json": {Data: []byte(`{}`)},
	}
}

func TestCheckpointCrossRepoForkFlow(t *testing.T) {
	ctx := context.Background()
	baseFC := &github.FakeClient{}
	if err := baseFC.CreateBranch(ctx, "main", "base-main-sha"); err != nil {
		t.Fatal(err)
	}
	forkFC := &github.FakeClient{ForkParent: "upstream/repo"}
	baseRec := &recordingClient{Interface: baseFC}
	forkRec := &recordingClient{Interface: forkFC}

	db, err := ghdb.OpenWithClients(forkBaseline(t), baseRec, forkRec)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)

	if err := db.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}

	// Reads (tree basis) happened on the base client, never writes.
	for _, write := range []string{"CreateBranch", "CreateTree", "CreateCommit", "UpdateRef"} {
		if baseRec.has(write) {
			t.Errorf("base client should not see %s, calls=%v", write, baseRec.calls)
		}
	}
	if !baseRec.has("DefaultBranch") {
		t.Errorf("expected DefaultBranch on base client, calls=%v", baseRec.calls)
	}
	if !baseRec.has("GetCommitTree") {
		t.Errorf("expected GetCommitTree on base client, calls=%v", baseRec.calls)
	}
	if !baseRec.has("CreatePR") {
		t.Errorf("expected CreatePR on base client, calls=%v", baseRec.calls)
	}

	// Writes (checkpoint branch/commit) happened on the fork client; PR never does.
	for _, write := range []string{"CreateBranch", "CreateTree", "CreateCommit", "UpdateRef"} {
		if !forkRec.has(write) {
			t.Errorf("expected %s on fork client, calls=%v", write, forkRec.calls)
		}
	}
	if forkRec.has("CreatePR") {
		t.Errorf("fork client should not see CreatePR, calls=%v", forkRec.calls)
	}

	// PR opened against base_repo's resolved default branch, with a cross-repo head.
	prs := baseFC.PRs()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR on base client, got %d", len(prs))
	}
	pr := prs[0]
	if !strings.HasPrefix(pr.Head, "forkowner:ghdb-ckpt-testdb-v2") {
		t.Errorf("PR head = %q, want prefix %q", pr.Head, "forkowner:ghdb-ckpt-testdb-v2")
	}
	if pr.Base != "main" {
		t.Errorf("PR base = %q, want %q", pr.Base, "main")
	}
	if len(forkFC.PRs()) != 0 {
		t.Errorf("expected 0 PRs on fork client, got %d", len(forkFC.PRs()))
	}
}

func TestStartOnlineDeltaBranchBootstrapUsesForkRepo(t *testing.T) {
	ctx := context.Background()
	baseFC := &github.FakeClient{}
	if err := baseFC.CreateBranch(ctx, "main", "base-main-sha"); err != nil {
		t.Fatal(err)
	}
	forkFC := &github.FakeClient{ForkParent: "upstream/repo"}

	db, err := ghdb.OpenWithClients(forkBaseline(t), baseFC, forkFC)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)

	if ok, _ := forkFC.BranchExists(ctx, "ghdb-data"); !ok {
		t.Error("expected delta_branch to be bootstrapped on the fork client")
	}
	if ok, _ := baseFC.BranchExists(ctx, "ghdb-data"); ok {
		t.Error("delta_branch should not be created on the base client")
	}
}

func TestStartOnlineFailsWhenForkNotWritable(t *testing.T) {
	ctx := context.Background()
	baseFC := &github.FakeClient{}
	if err := baseFC.CreateBranch(ctx, "main", "base-main-sha"); err != nil {
		t.Fatal(err)
	}
	forkFC := &github.FakeClient{ForkParent: "upstream/repo", PushDenied: true}

	_, err := ghdb.OpenWithClients(forkBaseline(t), baseFC, forkFC)
	if err == nil {
		t.Fatal("expected error when fork_repo is not writable")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error = %v, want mention of 'not writable'", err)
	}
}

func TestStartOnlineFailsWhenForkRepoNotActualFork(t *testing.T) {
	ctx := context.Background()
	baseFC := &github.FakeClient{}
	if err := baseFC.CreateBranch(ctx, "main", "base-main-sha"); err != nil {
		t.Fatal(err)
	}
	forkFC := &github.FakeClient{} // ForkParent unset: not a fork of anything

	_, err := ghdb.OpenWithClients(forkBaseline(t), baseFC, forkFC)
	if err == nil {
		t.Fatal("expected error when fork_repo is not a GitHub fork of base_repo")
	}
	if !strings.Contains(err.Error(), "not a GitHub fork") {
		t.Errorf("error = %v, want mention of 'not a GitHub fork'", err)
	}
}
