package ghdb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DR1N0/github-database/ghdb"
	"github.com/DR1N0/github-database/ghdb/internal/githubtest"
)

// failingTreeClient fails CreateTree a fixed number of times, emulating a
// transient GitHub error in the middle of a checkpoint.
type failingTreeClient struct {
	*githubtest.FakeClient
	failures int
}

func (c *failingTreeClient) CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (string, error) {
	if c.failures > 0 {
		c.failures--
		return "", errors.New("injected create-tree failure")
	}
	return c.FakeClient.CreateTree(ctx, baseTreeSHA, files)
}

// TestCheckpointCanBeRetriedAfterFailure guards the fix for a checkpoint that
// failed once (network error, rate limit, expired token) permanently disabling
// further checkpoints until the process restarted. The retry also exercises
// reuse of the snapshot branch that the failed attempt already created.
func TestCheckpointCanBeRetriedAfterFailure(t *testing.T) {
	client := &failingTreeClient{FakeClient: &githubtest.FakeClient{}, failures: 1}
	db, err := ghdb.OpenWithClient(onlineBaseline(t), client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tdb := db.(ghdb.TableDB)
	ctx := context.Background()

	if err := tdb.Checkpoint(ctx); err == nil {
		t.Fatal("first checkpoint error = nil, want the injected failure")
	}

	if err := tdb.Checkpoint(ctx); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}

	// A checkpoint that succeeded still admits only one per instance.
	if err := tdb.Checkpoint(ctx); err == nil {
		t.Fatal("post-success checkpoint error = nil, want already-performed rejection")
	}
}

// TestCheckpointRetryDoesNotDuplicateState verifies the retry produces exactly
// one checkpoint commit and that the data is intact afterwards.
func TestCheckpointRetryDoesNotDuplicateState(t *testing.T) {
	client := &failingTreeClient{FakeClient: &githubtest.FakeClient{}, failures: 1}
	db, err := ghdb.OpenWithClient(onlineBaseline(t), client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tdb := db.(ghdb.TableDB)
	ctx := context.Background()

	if err := tdb.Checkpoint(ctx); err == nil {
		t.Fatal("first checkpoint error = nil, want the injected failure")
	}
	if err := tdb.Checkpoint(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := client.PRs(); len(got) != 1 {
		t.Fatalf("PRs created = %d, want 1", len(got))
	}
	if got := client.LastCommit(); got.TreeSHA == "" || got.ParentSHA == "" {
		t.Fatalf("checkpoint commit = %#v, want a tree and parent", got)
	}
}
