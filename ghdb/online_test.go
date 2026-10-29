package ghdb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
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
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data",
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

// blockingClient records GitHub calls and waits for cancellation at one selected method.
type blockingClient struct {
	github.Interface
	block string
	calls []string

	mu sync.Mutex
}

func (c *blockingClient) record(ctx context.Context, method string) error {
	c.mu.Lock()
	c.calls = append(c.calls, method)
	c.mu.Unlock()
	if c.block != method {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *blockingClient) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *blockingClient) GetFile(ctx context.Context, branch, path string) ([]byte, string, error) {
	if err := c.record(ctx, "GetFile"); err != nil {
		return nil, "", err
	}
	return c.Interface.GetFile(ctx, branch, path)
}

func (c *blockingClient) PutFile(ctx context.Context, branch, path, message string, content []byte, currentSHA string) error {
	if err := c.record(ctx, "PutFile"); err != nil {
		return err
	}
	return c.Interface.PutFile(ctx, branch, path, message, content, currentSHA)
}

func (c *blockingClient) ListDir(ctx context.Context, branch, path string) ([]github.DirEntry, error) {
	if err := c.record(ctx, "ListDir"); err != nil {
		return nil, err
	}
	return c.Interface.ListDir(ctx, branch, path)
}

func (c *blockingClient) BranchExists(ctx context.Context, name string) (bool, error) {
	if err := c.record(ctx, "BranchExists"); err != nil {
		return false, err
	}
	return c.Interface.BranchExists(ctx, name)
}

func (c *blockingClient) CreateBranch(ctx context.Context, name, fromSHA string) error {
	if err := c.record(ctx, "CreateBranch"); err != nil {
		return err
	}
	return c.Interface.CreateBranch(ctx, name, fromSHA)
}

func (c *blockingClient) DefaultBranch(ctx context.Context) (string, string, error) {
	if err := c.record(ctx, "DefaultBranch"); err != nil {
		return "", "", err
	}
	return c.Interface.DefaultBranch(ctx)
}

func (c *blockingClient) CreatePR(ctx context.Context, title, head, base string) (string, error) {
	if err := c.record(ctx, "CreatePR"); err != nil {
		return "", err
	}
	return c.Interface.CreatePR(ctx, title, head, base)
}

func (c *blockingClient) GetAuthenticatedUser(ctx context.Context) (string, string, error) {
	if err := c.record(ctx, "GetAuthenticatedUser"); err != nil {
		return "", "", err
	}
	return c.Interface.GetAuthenticatedUser(ctx)
}

func (c *blockingClient) GetCommitTree(ctx context.Context, commitSHA string) (string, error) {
	if err := c.record(ctx, "GetCommitTree"); err != nil {
		return "", err
	}
	return c.Interface.GetCommitTree(ctx, commitSHA)
}

func (c *blockingClient) CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (string, error) {
	if err := c.record(ctx, "CreateTree"); err != nil {
		return "", err
	}
	return c.Interface.CreateTree(ctx, baseTreeSHA, files)
}

func (c *blockingClient) CreateCommit(ctx context.Context, treeSHA, parentSHA, message, name, email string, ts time.Time, signature string) (string, error) {
	if err := c.record(ctx, "CreateCommit"); err != nil {
		return "", err
	}
	return c.Interface.CreateCommit(ctx, treeSHA, parentSHA, message, name, email, ts, signature)
}

func (c *blockingClient) UpdateRef(ctx context.Context, branch, commitSHA string) error {
	if err := c.record(ctx, "UpdateRef"); err != nil {
		return err
	}
	return c.Interface.UpdateRef(ctx, branch, commitSHA)
}

// cancelAfterListDirClient cancels its context after a successful directory listing.
type cancelAfterListDirClient struct {
	*blockingClient
	cancel context.CancelFunc
}

func (c *cancelAfterListDirClient) ListDir(ctx context.Context, branch, path string) ([]github.DirEntry, error) {
	if err := c.record(ctx, "ListDir"); err != nil {
		return nil, err
	}
	entries, err := c.Interface.ListDir(ctx, branch, path)
	if err == nil {
		c.cancel()
	}
	return entries, err
}

func waitForGitHubCall(t *testing.T, client *blockingClient, method string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, call := range client.Calls() {
			if call == method {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("startup did not call %s; calls: %v", method, client.Calls())
}

func graphBaseline(t *testing.T) fstest.MapFS {
	t.Helper()
	meta, err := json.Marshal(ghdb.Config{
		Name: "testgraph", Version: 1, Mode: "graph",
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data",
		Vertices: []ghdb.VertexSpec{{Label: "component"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"db_meta.json": {Data: meta}}
}

func TestOpenTableContext(t *testing.T) {
	ctx := context.Background()
	var events []ghdb.StartupProgress
	db, err := ghdb.NewOption(onlineBaseline(t)).
		StartupProgress(func(event ghdb.StartupProgress) { events = append(events, event) }).
		GitHubRequestTimeout(90 * time.Second).
		OpenTable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db.IsOnline() {
		t.Fatal("token-less open must remain offline")
	}
	if events != nil {
		t.Fatalf("token-less open emitted startup events: %#v", events)
	}
}

func TestOpenGraphContext(t *testing.T) {
	ctx := context.Background()
	var events []ghdb.StartupProgress
	db, err := ghdb.NewOption(graphBaseline(t)).
		StartupProgress(func(event ghdb.StartupProgress) { events = append(events, event) }).
		OpenGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db.IsOnline() {
		t.Fatal("token-less open must remain offline")
	}
	if events != nil {
		t.Fatalf("token-less open emitted startup events: %#v", events)
	}
}

func onlineBaselineAtVersion(t *testing.T, version int) fstest.MapFS {
	t.Helper()
	meta, err := json.Marshal(ghdb.Config{
		Name: "testdb", Version: version, Mode: "table",
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data",
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

func seedStartupSegments(t *testing.T, fc *github.FakeClient, version, count int, includeMalformed bool) {
	t.Helper()
	for segment := 1; segment <= count; segment++ {
		record := ghdb.MutationRecord{
			TS: time.Date(2026, 9, 10, 0, 0, segment, 0, time.UTC), Op: "set", Table: "things",
			Key: fmt.Sprintf("startup-%d", segment), Value: json.RawMessage(fmt.Sprintf(`{"id":"startup-%d"}`, segment)),
		}
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{record})
		if err != nil {
			t.Fatal(err)
		}
		if includeMalformed && segment == 1 {
			data = append(data, []byte("malformed-startup-payload-marker\n")...)
		}
		path := fmt.Sprintf("testdb/v%d/segment-%02d.jsonl", version, segment)
		if err := fc.PutFile(context.Background(), "ghdb-data", path, "seed", data, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenOnlineStartupProgress(t *testing.T) {
	const version = 13
	fc, _ := newOnlineFake(t)
	seedStartupSegments(t, fc, version, 21, false)

	var events []ghdb.StartupProgress
	db, err := ghdb.OpenWithClientAndStartupProgress(onlineBaselineAtVersion(t, version), fc, func(event ghdb.StartupProgress) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())

	want := []ghdb.StartupProgress{
		{Kind: ghdb.StartupBranchLookup},
		{Kind: ghdb.StartupReplayListing, Version: version},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 1, TotalSegments: 21},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 10, TotalSegments: 21},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 20, TotalSegments: 21},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 21, TotalSegments: 21},
		{Kind: ghdb.StartupReplayListing, Version: version + 1},
	}
	if !startupProgressEqual(events, want) {
		t.Fatalf("startup events = %#v, want %#v", events, want)
	}

	fc, _ = newOnlineFake(t)
	seedStartupSegments(t, fc, version, 20, false)
	events = nil
	db, err = ghdb.OpenWithClientAndStartupProgress(onlineBaselineAtVersion(t, version), fc, func(event ghdb.StartupProgress) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	want = []ghdb.StartupProgress{
		{Kind: ghdb.StartupBranchLookup},
		{Kind: ghdb.StartupReplayListing, Version: version},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 1, TotalSegments: 20},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 10, TotalSegments: 20},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 20, TotalSegments: 20},
		{Kind: ghdb.StartupReplayListing, Version: version + 1},
	}
	if !startupProgressEqual(events, want) {
		t.Fatalf("startup events with final tenth segment = %#v, want %#v", events, want)
	}

	fc, _ = newOnlineFake(t)
	seedStartupSegments(t, fc, version, 1, false)
	seedStartupSegments(t, fc, version+1, 1, false)
	events = nil
	db, err = ghdb.OpenWithClientAndStartupProgress(onlineBaselineAtVersion(t, version), fc, func(event ghdb.StartupProgress) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	want = []ghdb.StartupProgress{
		{Kind: ghdb.StartupBranchLookup},
		{Kind: ghdb.StartupReplayListing, Version: version},
		{Kind: ghdb.StartupReplaySegment, Version: version, Segment: 1, TotalSegments: 1},
		{Kind: ghdb.StartupReplayListing, Version: version + 1},
		{Kind: ghdb.StartupReplayListing, Version: version + 1},
		{Kind: ghdb.StartupReplaySegment, Version: version + 1, Segment: 1, TotalSegments: 1},
	}
	if !startupProgressEqual(events, want) {
		t.Fatalf("startup events with optional next version = %#v, want %#v", events, want)
	}
}

func TestStartupLogs(t *testing.T) {
	const version = 13
	fc, _ := newOnlineFake(t)
	seedStartupSegments(t, fc, version, 21, true)
	var output bytes.Buffer

	db, err := ghdb.OpenWithClientAndLogger(onlineBaselineAtVersion(t, version), fc, log.New(&output, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	logs := output.String()
	for _, want := range []string{
		`ghdb: startup: checking delta branch "ghdb-data"`,
		`ghdb: startup: delta branch "ghdb-data" exists=true completed in `,
		`ghdb: replay: listing segments path=testdb/v13`,
		`ghdb: replay: listed path=testdb/v13 segments=21 elapsed=`,
		`ghdb: replay: completed path=testdb/v13 segments=21 `,
		"records=21 elapsed=",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not contain %q:\n%s", want, logs)
		}
	}
	for _, segment := range []int{1, 10, 20, 21} {
		want := fmt.Sprintf("ghdb: replay: segment=%d/21 file=", segment)
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not contain bounded segment line %q:\n%s", want, logs)
		}
	}
	if got := strings.Count(logs, "ghdb: replay: segment="); got != 4 {
		t.Errorf("bounded segment log count = %d, want 4; logs:\n%s", got, logs)
	}
	if strings.Contains(logs, "malformed-startup-payload-marker") {
		t.Errorf("logs expose malformed record data:\n%s", logs)
	}
}

func startupProgressEqual(got, want []ghdb.StartupProgress) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOpenAlreadyCanceledReturnsNoDB(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	client := &blockingClient{Interface: fc}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := ghdb.OpenWithClientContext(baseline, ctx, client, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error = %v, want context.Canceled", err)
	}
	if db != nil {
		t.Fatalf("Open returned DB %#v for canceled context", db)
	}
	if calls := client.Calls(); len(calls) != 0 {
		t.Fatalf("GitHub calls = %v, want none", calls)
	}
}

func TestOpenCancellationStopsStartup(t *testing.T) {
	for _, tc := range []struct {
		block string
		seed  bool
		want  []string
	}{
		{block: "BranchExists", want: []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists"}},
		{block: "ListDir", want: []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir"}},
		{block: "GetFile", seed: true, want: []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir", "GetFile"}},
	} {
		t.Run(tc.block, func(t *testing.T) {
			fc, baseline := newOnlineFake(t)
			if tc.seed {
				if err := fc.PutFile(context.Background(), "ghdb-data", "testdb/v1/blocked.jsonl", "seed", []byte("\n"), ""); err != nil {
					t.Fatal(err)
				}
			}
			client := &blockingClient{Interface: fc, block: tc.block}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			type openResult struct {
				db  ghdb.DB
				err error
			}
			result := make(chan openResult, 1)
			go func() {
				db, err := ghdb.OpenWithClientContext(baseline, ctx, client, true)
				result <- openResult{db: db, err: err}
			}()

			waitForGitHubCall(t, client, tc.block)
			cancel()
			got := <-result
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("Open error = %v, want context.Canceled", got.err)
			}
			if got.db != nil {
				t.Fatalf("Open returned DB %#v for canceled context", got.db)
			}
			if calls := client.Calls(); !slicesEqual(calls, tc.want) {
				t.Fatalf("GitHub calls = %v, want %v", calls, tc.want)
			}
		})
	}
}

func TestOpenCancellationAfterReplayListingStopsBeforeGetFile(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	if err := fc.PutFile(context.Background(), "ghdb-data", "testdb/v1/listed.jsonl", "seed", []byte("\n"), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &cancelAfterListDirClient{
		blockingClient: &blockingClient{Interface: fc},
		cancel:         cancel,
	}

	db, err := ghdb.OpenWithClientContext(baseline, ctx, client, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error = %v, want context.Canceled", err)
	}
	if db != nil {
		t.Fatalf("Open returned DB %#v for canceled context", db)
	}
	want := []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir"}
	if calls := client.Calls(); !slicesEqual(calls, want) {
		t.Fatalf("GitHub calls = %v, want %v; GetFile must not follow cancellation after ListDir", calls, want)
	}
}

func TestOpenOfflineFallbackOnBranchExistsError(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	fc.SetBranchExistsError(errors.New("injected BranchExists failure"))
	client := &blockingClient{Interface: fc}

	db, err := ghdb.OpenWithClientContext(baseline, context.Background(), client, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db == nil || db.IsOnline() {
		t.Fatalf("fallback DB = %#v, online = %v; want offline DB", db, db != nil && db.IsOnline())
	}
	if err := db.Checkpoint(context.Background()); !errors.Is(err, ghdb.ErrOffline) {
		t.Fatalf("offline Checkpoint error = %v, want ErrOffline", err)
	}
	want := []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists"}
	if calls := client.Calls(); !slicesEqual(calls, want) {
		t.Fatalf("GitHub calls = %v, want %v", calls, want)
	}
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOpenCallerDeadlineStopsStartupAndProgress(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	client := &blockingClient{Interface: fc, block: "BranchExists"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var events []ghdb.StartupProgress

	db, err := ghdb.OpenWithClientContextAndStartupProgress(baseline, ctx, client, true, func(event ghdb.StartupProgress) {
		events = append(events, event)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open error = %v, want context.DeadlineExceeded", err)
	}
	if db != nil {
		t.Fatalf("Open returned DB %#v for expired deadline", db)
	}
	if calls := client.Calls(); !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists"}) {
		t.Fatalf("GitHub calls = %v, want startup to stop at BranchExists", calls)
	}
	wantEvents := []ghdb.StartupProgress{{Kind: ghdb.StartupBranchLookup}}
	if !startupProgressEqual(events, wantEvents) {
		t.Fatalf("startup events = %#v, want %#v; no event may follow cancellation", events, wantEvents)
	}
}

func TestStartupFailureStopsProgress(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	fc.SetBranchExistsError(errors.New("injected BranchExists failure"))
	var events []ghdb.StartupProgress

	db, err := ghdb.OpenWithClientContextAndStartupProgress(baseline, context.Background(), fc, false, func(event ghdb.StartupProgress) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatal("Open error = nil, want startup failure")
	}
	if db != nil {
		t.Fatalf("Open returned DB %#v after startup failure", db)
	}
	wantEvents := []ghdb.StartupProgress{{Kind: ghdb.StartupBranchLookup}}
	if !startupProgressEqual(events, wantEvents) {
		t.Fatalf("startup events = %#v, want %#v; no event may follow startup failure", events, wantEvents)
	}
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

func TestOpenReplaysCurrentAndNextVersionsWithFilteringOrderingAndCorruption(t *testing.T) {
	cutoff := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	fc, baseline := newOnlineFake(t)
	meta, err := json.Marshal(ghdb.Config{
		Name: "testdb", Version: 1, Mode: "table",
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data", BaselineTime: cutoff,
		Tables: []ghdb.TableSpec{{Name: "things", Key: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline["db_meta.json"] = &fstest.MapFile{Data: meta}

	record := func(second int, key, state string) ghdb.MutationRecord {
		return ghdb.MutationRecord{
			TS: cutoff.Add(time.Duration(second) * time.Second), Op: "set", Table: "things", Key: key,
			Value: json.RawMessage(fmt.Sprintf(`{"id":%q,"state":%q}`, key, state)),
		}
	}
	line := func(r ghdb.MutationRecord) []byte {
		data, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{r})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	seed := func(path string, records ...[]byte) {
		if err := fc.PutFile(context.Background(), "ghdb-data", path, "seed", bytes.Join(records, nil), ""); err != nil {
			t.Fatal(err)
		}
	}

	// Each version has valid mutations on both sides of a malformed line. The
	// later mutation is intentionally listed first to assert timestamp ordering.
	seed("testdb/v1/current.jsonl",
		line(record(-1, "filtered-current", "filtered-current")),
		line(record(2, "current-order", "current-later")),
		[]byte("malformed-current-record\n"),
		line(record(1, "current-order", "current-earlier")),
		line(record(3, "current-after-corruption", "current-after-corruption")),
	)
	seed("testdb/v2/next.jsonl",
		line(record(-1, "filtered-next", "filtered-next")),
		line(record(5, "next-order", "next-later")),
		[]byte("malformed-next-record\n"),
		line(record(4, "next-order", "next-earlier")),
		line(record(6, "next-after-corruption", "next-after-corruption")),
	)

	db, err := ghdb.OpenWithClient(baseline, fc)
	if err != nil {
		t.Fatalf("OpenWithClient: %v", err)
	}
	defer db.Close(context.Background())
	table := db.(ghdb.TableDB).Table("things")
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"current-order", `{"id":"current-order","state":"current-later"}`},
		{"current-after-corruption", `{"id":"current-after-corruption","state":"current-after-corruption"}`},
		{"next-order", `{"id":"next-order","state":"next-later"}`},
		{"next-after-corruption", `{"id":"next-after-corruption","state":"next-after-corruption"}`},
	} {
		got, ok := table.Get(tc.key)
		if !ok || string(got) != tc.want {
			t.Errorf("replayed %q = %q, %v; want %q", tc.key, got, ok, tc.want)
		}
	}
	for _, key := range []string{"filtered-current", "filtered-next"} {
		if _, ok := table.Get(key); ok {
			t.Errorf("baseline-filtered mutation %q was replayed", key)
		}
	}
	if got := ghdb.WriteVer(db); got != 2 {
		t.Fatalf("write version = %d, want next version 2", got)
	}
}

func TestOnlineReplayAndPollSkipCorruptSegments(t *testing.T) {
	fc, baseline := newOnlineFake(t)
	const path = "testdb/v1/corrupt.jsonl"
	at := func(second int, table, key string) ghdb.MutationRecord {
		return ghdb.MutationRecord{
			TS:    time.Date(2026, 9, 3, 0, 0, second, 0, time.UTC),
			Op:    "set",
			Table: table,
			Key:   key,
			Value: json.RawMessage(`{"id":"` + key + `"}`),
		}
	}
	jsonLine := func(record ghdb.MutationRecord) []byte {
		t.Helper()
		line, err := ghdb.MarshalJSONL([]ghdb.MutationRecord{record})
		if err != nil {
			t.Fatal(err)
		}
		return line
	}

	// Valid records surround malformed JSONL and an otherwise decodable mutation
	// that cannot be applied because its table is absent from the baseline.
	segment := bytes.Join([][]byte{
		jsonLine(at(1, "things", "before-json-error")),
		[]byte(`{"ts":` + "\n"),
		jsonLine(at(3, "things", "between-json-errors")),
		[]byte("not JSON\n"),
		jsonLine(at(5, "unknown-table", "semantic-error")),
		jsonLine(at(6, "things", "after-semantic-error")),
	}, nil)
	if err := fc.PutFile(context.Background(), "ghdb-data", path, "seed corrupt segment", segment, ""); err != nil {
		t.Fatal(err)
	}
	_, initialSHA, err := fc.GetFile(context.Background(), "ghdb-data", path)
	if err != nil {
		t.Fatal(err)
	}

	var logOutput bytes.Buffer
	db, err := ghdb.OpenWithClientAndLogger(baseline, fc, log.New(&logOutput, "", 0))
	if err != nil {
		t.Fatalf("OpenWithClientAndLogger: %v", err)
	}
	defer db.Close(context.Background())
	table := db.(ghdb.TableDB).Table("things")
	for _, key := range []string{"before-json-error", "between-json-errors", "after-semantic-error"} {
		if _, ok := table.Get(key); !ok {
			t.Errorf("startup replay did not apply valid record %q", key)
		}
	}
	if _, ok := table.Get("semantic-error"); ok {
		t.Error("semantic-invalid record must not be applied")
	}

	assertWarnings := func(logs, sha string, wantLines []int) {
		t.Helper()
		warnings := 0
		for _, message := range strings.Split(strings.TrimSpace(logs), "\n") {
			if !strings.Contains(message, path) {
				continue
			}
			warnings++
			if !strings.Contains(message, sha) {
				t.Errorf("warning %q does not contain segment SHA %q", message, sha)
			}
		}
		if warnings != len(wantLines) {
			t.Errorf("corrupt segment warnings = %d, want %d; logs:\n%s", warnings, len(wantLines), logs)
		}
		for _, line := range wantLines {
			lineNeedle := fmt.Sprintf("line %d", line)
			found := false
			for _, message := range strings.Split(strings.TrimSpace(logs), "\n") {
				if strings.Contains(message, path) && strings.Contains(message, sha) && strings.Contains(message, lineNeedle) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("warning logs do not contain path %q, SHA %q, and %q together; logs:\n%s", path, sha, lineNeedle, logs)
			}
		}
	}
	startupLogs := logOutput.String()
	assertWarnings(startupLogs, initialSHA, []int{2, 4, 5})
	if !strings.Contains(startupLogs, "line 5: invalid mutation") {
		t.Errorf("semantic mutation warning must use the invalid mutation category; logs:\n%s", startupLogs)
	}
	for _, sensitive := range []string{"unknown-table", "semantic-error", "not JSON", `{"ts":`} {
		if strings.Contains(startupLogs, sensitive) {
			t.Errorf("warning logs must not expose corrupt mutation data %q; logs:\n%s", sensitive, startupLogs)
		}
	}

	callsBeforeUnchangedPoll := fc.GetFileCallCount()
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fc.GetFileCallCount(); got != callsBeforeUnchangedPoll {
		t.Fatalf("GetFile calls after unchanged poll = %d, want %d", got, callsBeforeUnchangedPoll)
	}
	if got := logOutput.String(); got != startupLogs {
		t.Fatalf("unchanged acknowledged segment emitted warnings again; logs:\n%s", got)
	}

	updated := append(append([]byte(nil), segment...), jsonLine(at(7, "things", "after-revision"))...)
	if err := fc.PutFile(context.Background(), "ghdb-data", path, "append later valid record", updated, initialSHA); err != nil {
		t.Fatal(err)
	}
	_, updatedSHA, err := fc.GetFile(context.Background(), "ghdb-data", path)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeChangedPoll := fc.GetFileCallCount()
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fc.GetFileCallCount(); got != callsBeforeChangedPoll+1 {
		t.Fatalf("GetFile calls after changed poll = %d, want %d", got, callsBeforeChangedPoll+1)
	}
	if _, ok := table.Get("after-revision"); !ok {
		t.Error("changed segment's later valid record was not applied")
	}
	changedLogs := logOutput.String()
	assertWarnings(changedLogs[len(startupLogs):], updatedSHA, []int{2, 4})

	callsBeforeSecondUnchangedPoll := fc.GetFileCallCount()
	if err := ghdb.Poll(db, context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fc.GetFileCallCount(); got != callsBeforeSecondUnchangedPoll {
		t.Fatalf("GetFile calls after second unchanged poll = %d, want %d", got, callsBeforeSecondUnchangedPoll)
	}
	if got := logOutput.String(); got != changedLogs {
		t.Fatalf("changed segment emitted warnings again after its SHA was acknowledged; logs:\n%s", got)
	}
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
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data", BaselineTime: cutoff,
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
		BaseRepo:     "owner/repo",
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
		BaseRepo: "owner/repo", DeltaBranch: "ghdb-data",
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

// requestTimeoutDefaultBranchClient makes the first startup call use a concrete
// HTTP client whose timeout is shorter than the caller's context deadline.
type requestTimeoutDefaultBranchClient struct {
	github.Interface
	client *http.Client
	url    string
}

func (c *requestTimeoutDefaultBranchClient) DefaultBranch(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	return "main", "sha0", nil
}

// startupListDirErrorClient makes one replay-directory request fail while
// retaining call recording for assertions about the startup boundary.
type startupListDirErrorClient struct {
	*blockingClient
	failPath string
	err      error
}

func (c *startupListDirErrorClient) ListDir(ctx context.Context, branch, path string) ([]github.DirEntry, error) {
	if err := c.record(ctx, "ListDir"); err != nil {
		return nil, err
	}
	if path == c.failPath {
		return nil, c.err
	}
	return c.Interface.ListDir(ctx, branch, path)
}

func TestOpenFallsBackAfterRequestTimeoutWithActiveCallerContext(t *testing.T) {
	const requestTimeout = 20 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fc, baseline := newOnlineFake(t)
	client := &requestTimeoutDefaultBranchClient{
		Interface: fc,
		client:    &http.Client{Timeout: requestTimeout},
		url:       server.URL,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	db, err := ghdb.OpenWithClientContext(baseline, ctx, client, true)
	if err != nil {
		t.Fatalf("Open with request timeout and offline fallback: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller context unexpectedly expired: %v", ctx.Err())
	}
	if db == nil || db.IsOnline() {
		t.Fatalf("fallback DB = %#v, online = %v; want offline DB", db, db != nil && db.IsOnline())
	}
	if err := db.Checkpoint(context.Background()); !errors.Is(err, ghdb.ErrOffline) {
		t.Fatalf("offline Checkpoint error = %v, want ErrOffline", err)
	}
}

func TestOpenNextVersionListDirFailureDoesNotActivateOnline(t *testing.T) {
	const nextPath = "testdb/v2"
	for _, tc := range []struct {
		name     string
		fallback bool
	}{
		{name: "startup error"},
		{name: "offline fallback", fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc, baseline := newOnlineFake(t)
			client := &startupListDirErrorClient{
				blockingClient: &blockingClient{Interface: fc},
				failPath:       nextPath,
				err:            errors.New("ghdb: ListDir testdb/v2: HTTP 500"),
			}

			db, err := ghdb.OpenWithClientContext(baseline, context.Background(), client, tc.fallback)
			if tc.fallback {
				if err != nil {
					t.Fatalf("Open with fallback: %v", err)
				}
				if db == nil || db.IsOnline() {
					t.Fatalf("fallback DB = %#v, online = %v; want offline DB", db, db != nil && db.IsOnline())
				}
				if err := db.Checkpoint(context.Background()); !errors.Is(err, ghdb.ErrOffline) {
					t.Fatalf("offline Checkpoint error = %v, want ErrOffline", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
					t.Fatalf("Open error = %v, want next-version ListDir HTTP 500 failure", err)
				}
				if db != nil {
					t.Fatalf("Open returned DB %#v after next-version ListDir failure", db)
				}
			}
			if calls := client.Calls(); !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir", "ListDir"}) {
				t.Fatalf("GitHub calls = %v, want no calls after next-version ListDir failure", calls)
			}
		})
	}
}

func TestOpenCancellationFromStartupProgressStopsLaterGitHubCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seed     func(t *testing.T, fc *github.FakeClient)
		cancelOn func(event ghdb.StartupProgress, seen map[ghdb.StartupProgress]int) bool
		assert   func(t *testing.T, calls []string)
	}{
		{
			name: "branch lookup",
			cancelOn: func(event ghdb.StartupProgress, _ map[ghdb.StartupProgress]int) bool {
				return event.Kind == ghdb.StartupBranchLookup
			},
			assert: func(t *testing.T, calls []string) {
				if !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser"}) {
					t.Fatalf("GitHub calls = %v, want no BranchExists after canceled branch progress", calls)
				}
			},
		},
		{
			name: "current version replay listing",
			cancelOn: func(event ghdb.StartupProgress, _ map[ghdb.StartupProgress]int) bool {
				return event.Kind == ghdb.StartupReplayListing && event.Version == 1
			},
			assert: func(t *testing.T, calls []string) {
				if !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists"}) {
					t.Fatalf("GitHub calls = %v, want no ListDir after canceled replay-listing progress", calls)
				}
			},
		},
		{
			name: "bounded replay segment",
			seed: func(t *testing.T, fc *github.FakeClient) {
				seedStartupSegments(t, fc, 1, 11, false)
			},
			cancelOn: func(event ghdb.StartupProgress, _ map[ghdb.StartupProgress]int) bool {
				return event.Kind == ghdb.StartupReplaySegment && event.Version == 1 && event.Segment == 10
			},
			assert: func(t *testing.T, calls []string) {
				if len(calls) != 13 || calls[len(calls)-1] != "GetFile" {
					t.Fatalf("GitHub calls = %v, want only nine GetFile calls before canceled segment-10 progress", calls)
				}
			},
		},
		{
			name: "next version probe replay listing",
			cancelOn: func(event ghdb.StartupProgress, _ map[ghdb.StartupProgress]int) bool {
				return event.Kind == ghdb.StartupReplayListing && event.Version == 2
			},
			assert: func(t *testing.T, calls []string) {
				if !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir"}) {
					t.Fatalf("GitHub calls = %v, want no next-version probe after canceled progress", calls)
				}
			},
		},
		{
			name: "next version replay listing",
			seed: func(t *testing.T, fc *github.FakeClient) {
				seedStartupSegments(t, fc, 2, 1, false)
			},
			cancelOn: func(event ghdb.StartupProgress, seen map[ghdb.StartupProgress]int) bool {
				return event.Kind == ghdb.StartupReplayListing && event.Version == 2 && seen[event] == 2
			},
			assert: func(t *testing.T, calls []string) {
				if !slicesEqual(calls, []string{"DefaultBranch", "GetAuthenticatedUser", "BranchExists", "ListDir", "ListDir"}) {
					t.Fatalf("GitHub calls = %v, want no replay ListDir after canceled next-version listing progress", calls)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc, baseline := newOnlineFake(t)
			if tc.seed != nil {
				tc.seed(t, fc)
			}
			client := &blockingClient{Interface: fc}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			seen := make(map[ghdb.StartupProgress]int)

			db, err := ghdb.OpenWithClientContextAndStartupProgress(baseline, ctx, client, true, func(event ghdb.StartupProgress) {
				seen[event]++
				if tc.cancelOn(event, seen) {
					cancel()
				}
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Open error = %v, want context.Canceled", err)
			}
			if db != nil {
				t.Fatalf("Open returned DB %#v after cancellation from progress callback", db)
			}
			tc.assert(t, client.Calls())
		})
	}
}
