# github-database

A lightweight, GitHub-backed in-memory database for Go. Data is served from memory (fast reads), mutations are logged to a GitHub branch (durable writes), and a checkpoint commits the current state back to your baseline. Supports table mode and graph mode.

## How it works

On startup the library reads your baseline `fs.FS` (typically a directory embedded with `go:embed`) into memory. If a GitHub token is provided it goes online: it replays any pending mutation logs from the delta branch, then starts background goroutines that flush new mutations and poll for remote changes. Offline mode skips all of that — data lives in memory for the lifetime of the process.

## Quick start

### 1. Credentials

```bash
cp .env.example .env
# set GITHUB_TOKEN to a token with repo read/write access
```

### 2. Create your baseline

```
mydb/
  db_meta.json
  tables/
    user_info.json      # optional seed data: {"alice": {"username":"alice","role":"admin"}}
```

**`db_meta.json`**

```json
{
  "name": "mydb",
  "version": 1,
  "mode": "table",
  "base_repo": "owner/repo",
  "delta_branch": "mydb-data",
  "data_repo_path": "relative/path/to/data",
  "flush_interval_sec": 30,
  "sync_interval_sec": 30,
  "tables": [
    {
      "name": "user_info",
      "key": "username",
      "columns": [
        { "name": "username", "required": true },
        { "name": "role", "required": true }
      ]
    }
  ]
}
```

### 3. Open the database

```go
import (
    "context"
    "embed"
    "io/fs"
    "log"
    "os"
    "time"

    "github.com/DR1N0/github-database/ghdb"
)

//go:embed mydb
var baseline embed.FS

func openDB() (ghdb.TableDB, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()

    sub, _ := fs.Sub(baseline, "mydb")
    return ghdb.NewOption(sub).
        Token(func() string { return os.Getenv("GITHUB_TOKEN") }).
        GitHubRequestTimeout(90 * time.Second).
        StartupProgress(func(event ghdb.StartupProgress) {
            log.Printf("database startup: %s", event.Kind)
        }).
        AllowOfflineFallback().
        OpenTable(ctx)
}
```

Omit `.Token(...)` (or return an empty string) to open in offline mode.

### Startup controls

`Open`, `OpenTable`, and `OpenGraph` require a `context.Context`. Use the application or request context so startup stops when its caller is cancelled or its deadline expires. A cancelled or expired context returns `context.Canceled` or `context.DeadlineExceeded` and never activates online workers.

Each GitHub HTTP request uses a 30-second timeout by default. Set `GitHubRequestTimeout` to a positive duration to override it; zero and negative values retain the default. The caller context and the per-request timeout both apply, so the first deadline to expire ends the request. `AllowOfflineFallback` handles ordinary online-startup failures only: it never converts cancellation or deadline errors into an offline database.

`StartupProgress` receives synchronous online-startup notifications for the branch lookup, each replay-directory listing, and replay segments 1, every tenth segment, and the final segment. No events are emitted for token-less offline opens. Callbacks are never invoked concurrently or while database data locks are held, but run inline with startup; keep them short and non-blocking, and do not call back into startup from them.

For GitHub Enterprise, add `.Host("your.ghe.host")`. For signed checkpoints (so CI "require signed commits" rules pass), add `.CommitSigner(fn)`:

```go
ghdb.NewOption(sub).
    Token(func() string { return os.Getenv("GITHUB_TOKEN") }).
    Host("github.example.com").             // optional: GitHub Enterprise host
    Committer("Deploy Bot", "<EMAIL_ADDRESS>"). // optional: override committer identity
    CommitSigner(func(payload []byte) (string, error) {
        // payload is the raw git commit object; return an ASCII-armored PGP signature.
        // Example: shell out to gpg, use a Go GPG library, etc.
        return signWithGPG(payload)
    }).
    AllowOfflineFallback().
    OpenTable(ctx)
```

When `CommitSigner` is set, each checkpoint commit is signed and GitHub shows a "Verified" badge (assuming your GPG key is registered on the account).

> **Important:** `CommitSigner` requires `Committer(name, email)` to be set explicitly. The email must match the one associated with your GPG key on GitHub. If `Committer` is omitted, `Open(ctx)` returns an error.
>
> GitHub retrieves a no-reply address (e.g. `12345+user@users.noreply.github.com`) for many accounts, which will not match a GPG key — commits would be "Unverified". Setting `Committer` explicitly ensures the email in the commit matches the key.

### 4. Read and write

```go
tbl, err := db.Table("user_info") // ghdb.ErrUnknownTable when not declared
if err != nil {
    return err
}

// Write
tbl.Set("alice", json.RawMessage(`{"username":"alice","role":"admin"}`))
tbl.Patch("alice", map[string]json.RawMessage{"role": json.RawMessage(`"viewer"`)})
tbl.Delete("alice")

// Read
val, ok := tbl.Get("alice")
all := tbl.All()
```

`Table` and `Vertex` return an error instead of a nil value, so a typo in a table or label name surfaces immediately rather than panicking on the first call. `Set` enforces key-field consistency and `required` columns. Both `Set` and `Patch` return typed sentinel errors (`ghdb.ErrKeyMismatch`, `ghdb.ErrRequiredMissing`) that you can check with `errors.Is`.

### 5. Checkpoint

```go
if err := db.Checkpoint(ctx); err != nil {
    // ghdb.ErrOffline if running without a token
}
```

Checkpoint commits a full snapshot of current state to the repository in a single atomic commit and opens a pull request to main. If `CommitSigner` is configured, the commit is GPG-signed. A checkpoint starts a new delta version.

A checkpoint may succeed once per instance: after it completes, further calls return an error and the instance keeps writing to the new version until it is restarted. A checkpoint that fails — network error, rate limit, cancelled context — can be retried on the same instance; a snapshot branch left behind by the failed attempt is reused. The new version becomes the baseline only after the pull request is merged and the embedded `db_meta.json` is rebuilt, so merge the PR before redeploying.

### Multiple instances

Several instances may serve the same database. A checkpoint is a single-writer operation across processes: the right to checkpoint version `v<N>` is claimed through the lease file `<data_repo_path>/_checkpoint/v<N>.lease`, written with GitHub's file-SHA compare-and-swap. GitHub decides the winner, not local timing.

- An instance that finds a live lease held by someone else gets `ghdb.ErrCheckpointBusy`.
- An instance that finds the lease marked done gets `ghdb.ErrCheckpointTaken`, and it moves its writes to the next version so they are not lost.
- A failed checkpoint keeps the lease, so the same instance can retry immediately; another instance may take over only after the lease expires (5 minutes by default, set with `Option.CheckpointLease`).

The lease is not renewed while a checkpoint runs, so a checkpoint on a large database that can take longer than the window should raise `Option.CheckpointLease` above the expected duration.

Instances are not expected to agree on a version at any instant: a rolling update is the normal way to move a cluster forward. Instances pick up a new version from the delta branch within `sync_interval_sec` and start writing to it; the checkpoint lease only guarantees that a version is snapshotted once.

### Graph mode

Set `"mode": "graph"` and declare vertices and edges. Vertices live in `vertices/<label>.json`, edges in `edges/<label>.json`.

```json
{
  "mode": "graph",
  "vertices": [{ "label": "service", "properties": [{ "name": "name" }] }],
  "edges": [{ "label": "depends_on" }]
}
```

```go
db, err := ghdb.NewOption(sub).Token(func() string { return os.Getenv("GITHUB_TOKEN") }).OpenGraph(ctx)

svc, err := db.Vertex("service") // ghdb.ErrUnknownVertex when not declared
if err != nil {
    return err
}
svc.Set("svc-a", json.RawMessage(`{"id":"svc-a","name":"payments"}`)) // id must equal the key
svc.Patch("svc-a", map[string]json.RawMessage{"name": json.RawMessage(`"billing"`)})

db.AddEdge("depends_on", "svc-a", "svc-b")                                  // property-less edge
db.SetEdge("depends_on", "svc-a", "svc-b", json.RawMessage(`{"weight":2}`)) // with properties
db.OutNeighbors("depends_on", "svc-a")
db.OutEdges("", "svc-a") // all labels
```

Vertex values must be JSON objects whose `id` field is a string equal to the vertex id; otherwise `VertexSet.Set` returns `ghdb.ErrVertexIDMismatch`. Checkpoint writes each vertex label as a flat array, so that field is the only key available when the snapshot is loaded again — the constraint is what keeps a checkpointed graph openable. Edge labels are dynamic: mutations may introduce labels that are absent from the baseline schema.

### Online delta segments

In online mode, each pod writes bounded JSONL delta segments and rolls over to a new segment when its configured byte or record cap is reached. A fixed, non-configurable 32 MiB ceiling applies to each mutation: records above normal segment limits but at or below that ceiling are stored in immutable one-record segments, while larger changes fail at the caller API with `change is too large` and leave local state unchanged. Before deploying this version, operators must restore any already-truncated delta logs from Git history or verified backups.

Malformed or semantically invalid remote JSONL records are skipped so other records in the segment can still replay. Configure `Option.Logger` to receive a warning for each skipped record, including its segment path, line number, and SHA; a fetched segment revision is acknowledged, so unchanged corruption is not fetched or warned about again.

## Run the example

```bash
# online (requires GITHUB_TOKEN in .env)
make run

# offline (no GitHub needed)
make run-offline
```

The example runs an HTTP server on `:8080` with endpoints:

| Method   | Path                           | Description          |
| -------- | ------------------------------ | -------------------- |
| `GET`    | `/api/v1/table/user_info`      | List all records     |
| `GET`    | `/api/v1/table/user_info/:key` | Get one record       |
| `PUT`    | `/api/v1/table/user_info/:key` | Create or replace    |
| `DELETE` | `/api/v1/table/user_info/:key` | Delete               |
| `POST`   | `/api/v1/checkpoint`           | Checkpoint to GitHub |

## Configuration reference

| Field                | Description                                            |
| -------------------- | ------------------------------------------------------ |
| `name`               | Database name (used as path prefix in the repo)        |
| `version`            | Schema version; bump to start a new mutation log       |
| `mode`               | `"table"` or `"graph"`                                 |
| `base_repo`          | `"owner/repo"`; hosts `source_branch` and `target_branch` |
| `fork_repo`          | Optional `"owner/repo"` with write access, used for `delta_branch` and checkpoint commits when the token can't write to `base_repo`. Must be an actual GitHub fork of `base_repo` — forks share the underlying git object store, which checkpoint relies on to build a commit in `fork_repo` on top of a tree from `base_repo`. Empty means use `base_repo` for everything. |
| `source_branch`      | Branch (on `base_repo`) checkpoint snapshots are built from. Empty means the repo's actual default branch. |
| `target_branch`      | PR base (on `base_repo`) for checkpoint commits. Empty means whichever branch `source_branch` resolved to. |
| `delta_branch`       | Branch (on `fork_repo`, or `base_repo` if unset) where mutation logs are written |
| `data_repo_path`     | Override the path prefix in the repo (default: `name`) |
| `baseline_time`      | Ignore mutations older than this timestamp             |
| `flush_interval_sec`          | How often to flush mutations to GitHub (default: 300)      |
| `sync_interval_sec`           | How often to poll for remote mutations (default: 30)       |
| `max_delta_segment_bytes`     | Online delta segment byte cap (default: 786432; max: 917504) |
| `max_delta_segment_records`   | Online delta segment record cap (default: 1000)             |

## Limitations and operations

- **Delta history is never pruned.** Each checkpoint starts a new version directory; older version directories stay on `delta_branch` indefinitely. A long-running instance also appends a new segment file every flush, so each version directory keeps growing. Plan storage and PR-history growth, or archive/delete old version directories yourself once the checkpoint's pull request is merged.
- **Last write wins on timestamps.** Replay applies records in timestamp order, and each instance skips records at or before the newest timestamp it has applied. Instances need reasonably synchronized clocks; a large skew makes one instance drop another's writes.
- **No automatic retries, but an exhausted quota pauses background work.** A failed GitHub request surfaces immediately; flushing and polling retry on their next interval. An exhausted quota is returned as `github.RateLimitError`, which still matches `errors.Is(err, github.ErrRateLimited)` and carries the reset time; while that time is within 15 minutes, the background flusher and poller skip their ticks instead of spending requests that cannot succeed. An explicit `Checkpoint` still returns the error to its caller.
- **Directory listings use git trees.** `ListDir` walks `GET /git/trees` instead of the contents API, so a version directory is not capped at the contents API's 1,000 entries. Branch names containing `/` are resolved through the branch head commit first, because a slash cannot appear inside the trees path segment. A listing GitHub truncates (`"truncated": true`) is reported as an error rather than silently dropping segments.
- **API budget.** A directory listing costs one request per path level plus the listing itself (three requests for `<data_repo_path>/v<N>`, five for a branch name containing `/`), and every flush costs a listing plus one file update. Every poll lists each active version directory. Intervals are floored at 10 s to stay clear of rate limits.
- **Checkpoint size.** Snapshots are submitted inline through the git trees API, so very large databases may exceed GitHub's payload limits.

## Development

```bash
go build ./...
go vet ./...
go test -race -coverpkg=./... ./...
go test -run '^$' -bench=. -benchmem ./ghdb/base/
```

CI runs these checks on Linux and Windows across the oldest and latest supported Go versions; the race detector runs on Linux.

## Performance

Measured on a Ryzen 7 9700X with Go 1.27 (`go test -bench=. -benchmem ./ghdb/base/`):

| Benchmark | Time per op | Notes |
| --- | --- | --- |
| `MarshalJSONL` (1,000 records) | ~0.55 ms | ~0.9 MB allocated |
| `DecodeJSONL` (1,000 records) | ~0.57 ms | ~258 MB/s |
| `SplitMutationRecords` (1,000 records) | ~0.61 ms | default segment caps |
| `JSONMerge` | ~1.2 µs | one field |
| `Flush` (500 records) | ~0.92 ms | in-memory client |

These bound the CPU side only: in a real deployment a flush waits on GitHub, not on JSON encoding.
