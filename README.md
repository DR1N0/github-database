# github-database

A lightweight, GitHub-backed in-memory database for Go. Data is served from memory (fast reads), mutations are logged to a GitHub branch (durable writes), and a checkpoint commits the current state back to your baseline. Supports table mode and graph mode.

## How it works

On startup the library reads your baseline `fs.FS` (typically a directory embedded with `go:embed`) into memory. If a GitHub token is provided it goes online: it replays any pending mutation logs from the delta branch, then starts background goroutines that flush new mutations and poll for remote changes. Offline mode skips all of that — data lives in memory for the lifetime of the process.

## Quick start

### 1. Credentials

```bash
cp .env.example .env
# set GITHUB_TOKEN to a token with repo read/write access
# set GITHUB_HOST for specific github host (default: github.com)
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
  "github_repo": "owner/repo",
  "delta_branch": "mydb-data",
  "flush_interval_sec": 30,
  "sync_interval_sec":  30,
  "tables": [
    {
      "name": "user_info",
      "key": "username",
      "columns": [
        {"name": "username", "required": true},
        {"name": "role",     "required": true}
      ]
    }
  ]
}
```

### 3. Open the database

```go
import (
    "embed"
    "io/fs"
    "os"

    "github.com/DR1N0/github-database/ghdb"
)

//go:embed mydb
var baseline embed.FS

func openDB() (ghdb.TableDB, error) {
    sub, _ := fs.Sub(baseline, "mydb")
    return ghdb.NewOption(sub).
        Token(func() string { return os.Getenv("GITHUB_TOKEN") }).
        OpenTable()
}
```

Omit `.Token(...)` (or return an empty string) to open in offline mode.

### 4. Read and write

```go
tbl := db.Table("user_info")

// Write
tbl.Set("alice", json.RawMessage(`{"username":"alice","role":"admin"}`))
tbl.Patch("alice", map[string]json.RawMessage{"role": json.RawMessage(`"viewer"`)})
tbl.Delete("alice")

// Read
val, ok := tbl.Get("alice")
all := tbl.All()
```

`Set` enforces key-field consistency and `required` columns. Both `Set` and `Patch` return typed sentinel errors (`ghdb.ErrKeyMismatch`, `ghdb.ErrRequiredMissing`) that you can check with `errors.Is`.

### 5. Checkpoint

```go
if err := db.Checkpoint(ctx); err != nil {
    // ghdb.ErrOffline if running without a token
}
```

Checkpoint commits a full snapshot of current state to the repository and opens a pull request to main.

## Run the example

```bash
# online (requires GITHUB_TOKEN in .env)
make run

# offline (no GitHub needed)
make run-offline
```

The example runs an HTTP server on `:8080` with endpoints:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/table/user_info` | List all records |
| `GET` | `/api/v1/table/user_info/:key` | Get one record |
| `PUT` | `/api/v1/table/user_info/:key` | Create or replace |
| `DELETE` | `/api/v1/table/user_info/:key` | Delete |
| `POST` | `/api/v1/checkpoint` | Checkpoint to GitHub |

## Configuration reference

| Field | Description |
|-------|-------------|
| `name` | Database name (used as path prefix in the repo) |
| `version` | Schema version; bump to start a new mutation log |
| `mode` | `"table"` or `"graph"` |
| `github_repo` | `"owner/repo"` |
| `delta_branch` | Branch where mutation logs are written |
| `data_repo_path` | Override the path prefix in the repo (default: `name`) |
| `baseline_time` | Ignore mutations older than this timestamp |
| `flush_interval_sec` | How often to flush mutations to GitHub (default: 30) |
| `sync_interval_sec` | How often to poll for remote mutations (default: 30) |
