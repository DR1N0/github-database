package ghdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb"
	"github.com/DR1N0/github-database/ghdb/internal/githubtest"
)

// checkpointLeasePath mirrors the internal lease location for the test baseline.
const checkpointLeasePath = "testdb/_checkpoint/v2.lease"

// openTwoInstances opens two databases over the same FakeClient, with distinct
// instance IDs, so their delta segments and leases are distinguishable.
func openTwoInstances(t *testing.T, store *githubtest.FakeClient, a, b string) (ghdb.TableDB, ghdb.TableDB) {
	t.Helper()
	dbA, err := ghdb.OpenWithClient(onlineBaseline(t), store)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	ghdb.SetInstanceID(dbA, a)
	dbB, err := ghdb.OpenWithClient(onlineBaseline(t), store)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	ghdb.SetInstanceID(dbB, b)
	return dbA.(ghdb.TableDB), dbB.(ghdb.TableDB)
}

// TestCheckpointLeaseTakenByAnotherInstance covers the case where one instance
// already produced the snapshot: the other must not overwrite it.
func TestCheckpointLeaseTakenByAnotherInstance(t *testing.T) {
	store := &githubtest.FakeClient{}
	dbA, dbB := openTwoInstances(t, store, "pod-a", "pod-b")

	if err := dbA.Checkpoint(context.Background()); err != nil {
		t.Fatalf("A checkpoint: %v", err)
	}

	err := dbB.Checkpoint(context.Background())
	if !errors.Is(err, ghdb.ErrCheckpointTaken) {
		t.Fatalf("B checkpoint error = %v, want ErrCheckpointTaken", err)
	}
}

// TestCheckpointLeaseBusyWhileHeld covers an unfinished checkpoint: the second
// instance must back off instead of racing on the snapshot branch.
func TestCheckpointLeaseBusyWhileHeld(t *testing.T) {
	store := &githubtest.FakeClient{}
	flaky := &failingTreeClient{FakeClient: store, failures: 1}
	dbA, err := ghdb.OpenWithClient(onlineBaseline(t), flaky)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	ghdb.SetInstanceID(dbA, "pod-a")
	dbB, err := ghdb.OpenWithClient(onlineBaseline(t), store)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	ghdb.SetInstanceID(dbB, "pod-b")

	if err := dbA.(ghdb.TableDB).Checkpoint(context.Background()); err == nil {
		t.Fatal("A checkpoint error = nil, want the injected failure")
	}

	if err := dbB.(ghdb.TableDB).Checkpoint(context.Background()); !errors.Is(err, ghdb.ErrCheckpointBusy) {
		t.Fatalf("B checkpoint error = %v, want ErrCheckpointBusy", err)
	}
}

// TestCheckpointLeaseExpiresAndCanBeTakenOver covers a crashed instance: once
// the lease is stale, another instance may take it over and finish.
func TestCheckpointLeaseExpiresAndCanBeTakenOver(t *testing.T) {
	store := &githubtest.FakeClient{}
	flaky := &failingTreeClient{FakeClient: store, failures: 1}
	dbA, err := ghdb.OpenWithClient(onlineBaseline(t), flaky)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	ghdb.SetInstanceID(dbA, "pod-a")
	dbB, err := ghdb.OpenWithClient(onlineBaseline(t), store)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	ghdb.SetInstanceID(dbB, "pod-b")

	if err := dbA.(ghdb.TableDB).Checkpoint(context.Background()); err == nil {
		t.Fatal("A checkpoint error = nil, want the injected failure")
	}

	// Age the lease the way a crashed instance would leave it.
	raw, sha, err := store.GetFile(context.Background(), "ghdb-data", checkpointLeasePath)
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	var lease map[string]any
	if err := json.Unmarshal(raw, &lease); err != nil {
		t.Fatalf("parse lease: %v", err)
	}
	lease["acquired_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	aged, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutFile(context.Background(), "ghdb-data", checkpointLeasePath, "age lease", aged, sha); err != nil {
		t.Fatalf("age lease: %v", err)
	}

	if err := dbB.(ghdb.TableDB).Checkpoint(context.Background()); err != nil {
		t.Fatalf("B takeover: %v", err)
	}
}

// TestCheckpointLeaseRenewedByOwner keeps a failed checkpoint retryable on the
// instance that owns the lease.
func TestCheckpointLeaseRenewedByOwner(t *testing.T) {
	store := &githubtest.FakeClient{}
	flaky := &failingTreeClient{FakeClient: store, failures: 1}
	db, err := ghdb.OpenWithClient(onlineBaseline(t), flaky)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ghdb.SetInstanceID(db, "pod-a")
	tdb := db.(ghdb.TableDB)

	if err := tdb.Checkpoint(context.Background()); err == nil {
		t.Fatal("first checkpoint error = nil, want the injected failure")
	}
	if err := tdb.Checkpoint(context.Background()); err != nil {
		t.Fatalf("retry by the lease owner: %v", err)
	}
}

// leaseStealingClient lets another instance take the lease while CreateTree
// runs, emulating a checkpoint that outlived its lease window.
type leaseStealingClient struct {
	*githubtest.FakeClient
	steal bool
}

func (c *leaseStealingClient) CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (string, error) {
	if c.steal {
		c.steal = false
		raw, sha, err := c.FakeClient.GetFile(ctx, "ghdb-data", checkpointLeasePath)
		if err != nil {
			return "", err
		}
		var lease map[string]any
		if err := json.Unmarshal(raw, &lease); err != nil {
			return "", err
		}
		lease["owner"] = "pod-x"
		updated, err := json.Marshal(lease)
		if err != nil {
			return "", err
		}
		if err := c.FakeClient.PutFile(ctx, "ghdb-data", checkpointLeasePath, "steal lease", updated, sha); err != nil {
			return "", err
		}
	}
	return c.FakeClient.CreateTree(ctx, baseTreeSHA, files)
}

// TestCheckpointLeaseRenewedMidRun covers a checkpoint that outlives its lease:
// the next renewal must notice the takeover and stop instead of committing a
// second snapshot.
func TestCheckpointLeaseRenewedMidRun(t *testing.T) {
	store := &githubtest.FakeClient{}
	stealing := &leaseStealingClient{FakeClient: store, steal: true}
	db, err := ghdb.OpenWithClient(onlineBaseline(t), stealing)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ghdb.SetInstanceID(db, "pod-a")

	err = db.(ghdb.TableDB).Checkpoint(context.Background())
	if !errors.Is(err, ghdb.ErrCheckpointBusy) {
		t.Fatalf("checkpoint error = %v, want ErrCheckpointBusy once the lease was taken", err)
	}
}

// TestConcurrentCheckpointsAllowOneOwner runs both instances at once: exactly
// one checkpoint may win, the other must report busy or taken.
func TestConcurrentCheckpointsAllowOneOwner(t *testing.T) {
	store := &githubtest.FakeClient{}
	dbA, dbB := openTwoInstances(t, store, "pod-a", "pod-b")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, db := range []ghdb.TableDB{dbA, dbB} {
		wg.Add(1)
		go func(i int, db ghdb.TableDB) {
			defer wg.Done()
			errs[i] = db.Checkpoint(context.Background())
		}(i, db)
	}
	wg.Wait()

	success := 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ghdb.ErrCheckpointBusy), errors.Is(err, ghdb.ErrCheckpointTaken):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successes = %d (errors: %v), want exactly one", success, errs)
	}
}
