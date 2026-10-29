package ghdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing/fstest"

	"github.com/DR1N0/github-database/ghdb"
)

// Example shows the smallest useful table database: open offline, write, read.
// Without a token the database lives only in memory, which is enough to try the
// API and to run tests.
func Example() {
	baseline := fstest.MapFS{
		"db_meta.json": {Data: []byte(`{
			"name": "example",
			"version": 1,
			"mode": "table",
			"base_repo": "owner/repo",
			"delta_branch": "ghdb-data",
			"tables": [{
				"name": "user_info",
				"key": "username",
				"columns": [{"name": "username", "required": true}, {"name": "role", "required": true}]
			}]
		}`)},
	}

	db, err := ghdb.NewOption(baseline).OpenTable(context.Background())
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	tbl, err := db.Table("user_info")
	if err != nil {
		fmt.Println("table:", err)
		return
	}
	if err := tbl.Set("alice", json.RawMessage(`{"username":"alice","role":"admin"}`)); err != nil {
		fmt.Println("set:", err)
		return
	}

	value, ok := tbl.Get("alice")
	fmt.Println(ok, string(value))
	// Output: true {"username":"alice","role":"admin"}
}

// ExampleOption_OpenGraph shows vertex and edge writes in graph mode.
func ExampleOption_OpenGraph() {
	baseline := fstest.MapFS{
		"db_meta.json": {Data: []byte(`{
			"name": "example",
			"version": 1,
			"mode": "graph",
			"base_repo": "owner/repo",
			"delta_branch": "ghdb-data",
			"vertices": [{"label": "service"}],
			"edges": [{"label": "depends_on"}]
		}`)},
	}

	db, err := ghdb.NewOption(baseline).OpenGraph(context.Background())
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	services, err := db.Vertex("service")
	if err != nil {
		fmt.Println("vertex:", err)
		return
	}
	// The "id" field must match the vertex id; it is what reloads the vertex
	// from a checkpoint.
	if err := services.Set("svc-a", json.RawMessage(`{"id":"svc-a","name":"payments"}`)); err != nil {
		fmt.Println("set:", err)
		return
	}
	if err := db.AddEdge("depends_on", "svc-a", "svc-b"); err != nil {
		fmt.Println("edge:", err)
		return
	}

	fmt.Println(db.OutNeighbors("depends_on", "svc-a"))
	// Output: [svc-b]
}

// ExampleTableDB_Table_missing shows the error a misspelled table name produces.
func ExampleTableDB_Table_missing() {
	baseline := fstest.MapFS{
		"db_meta.json": {Data: []byte(`{
			"name": "example", "version": 1, "mode": "table",
			"base_repo": "owner/repo", "delta_branch": "ghdb-data",
			"tables": [{"name": "user_info", "key": "username"}]
		}`)},
	}

	db, err := ghdb.NewOption(baseline).OpenTable(context.Background())
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	_, err = db.Table("user_inf")
	fmt.Println(errors.Is(err, ghdb.ErrUnknownTable))
	// Output: true
}
