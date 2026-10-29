package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// treeServer answers git/trees requests with canned JSON per path.
func treeServer(t *testing.T, responses map[string]string, requested *[]string) *httpClient {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requested != nil {
			*requested = append(*requested, r.URL.Path)
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
}

// TestListDirWalksGitTrees replaces the contents-API listing, which stops at
// 1,000 entries per directory.
func TestListDirWalksGitTrees(t *testing.T) {
	var requested []string
	client := treeServer(t, map[string]string{
		"/repos/owner/repo/git/trees/main": `{"tree":[
			{"path":"README.md","type":"blob","sha":"blob-readme"},
			{"path":"db","type":"tree","sha":"tree-db"}
		]}`,
		"/repos/owner/repo/git/trees/tree-db": `{"tree":[{"path":"v1","type":"tree","sha":"tree-v1"}]}`,
		"/repos/owner/repo/git/trees/tree-v1": `{"tree":[
			{"path":"pod-a.jsonl","type":"blob","sha":"blob-a"},
			{"path":"pod-b.jsonl","type":"blob","sha":"blob-b"},
			{"path":"nested","type":"tree","sha":"tree-nested"},
			{"path":"submodule","type":"commit","sha":"commit-x"}
		]}`,
	}, &requested)

	entries, err := client.ListDir(context.Background(), "main", "db/v1")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v, want the two blobs only", entries)
	}
	if entries[0] != (DirEntry{Name: "pod-a.jsonl", SHA: "blob-a"}) ||
		entries[1] != (DirEntry{Name: "pod-b.jsonl", SHA: "blob-b"}) {
		t.Fatalf("entries = %#v, want pod-a then pod-b", entries)
	}
	if len(requested) != 3 {
		t.Fatalf("tree requests = %v, want one per path level plus the listing", requested)
	}
}

// TestListDirMissingBranchOrPathIsEmpty keeps the previous contract: a path that
// does not exist yet yields no entries and no error.
func TestListDirMissingBranchOrPathIsEmpty(t *testing.T) {
	client := treeServer(t, map[string]string{
		"/repos/owner/repo/git/trees/main":    `{"tree":[{"path":"db","type":"tree","sha":"tree-db"}]}`,
		"/repos/owner/repo/git/trees/tree-db": `{"tree":[]}`,
	}, nil)

	entries, err := client.ListDir(context.Background(), "main", "db/v9")
	if err != nil {
		t.Fatalf("ListDir missing path: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want none", entries)
	}

	entries, err = client.ListDir(context.Background(), "absent-branch", "db/v1")
	if err != nil {
		t.Fatalf("ListDir missing branch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want none for a missing branch", entries)
	}
}

// TestListDirRejectsTruncatedTree fails loudly instead of returning a partial
// listing that would silently skip delta segments.
func TestListDirRejectsTruncatedTree(t *testing.T) {
	client := treeServer(t, map[string]string{
		"/repos/owner/repo/git/trees/main": `{"tree":[
			{"path":"db","type":"tree","sha":"tree-db"}
		]}`,
		"/repos/owner/repo/git/trees/tree-db": `{"tree":[{"path":"v1","type":"tree","sha":"tree-v1"}]}`,
		"/repos/owner/repo/git/trees/tree-v1": `{"truncated":true,"tree":[{"path":"a.jsonl","type":"blob","sha":"a"}]}`,
	}, nil)

	_, err := client.ListDir(context.Background(), "main", "db/v1")
	if err == nil {
		t.Fatal("ListDir error = nil, want a truncation error")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error = %v, want it to mention truncation", err)
	}
}

// TestListDirListsRoot covers an empty path, which lists the branch root's blobs.
func TestListDirListsRoot(t *testing.T) {
	client := treeServer(t, map[string]string{
		"/repos/owner/repo/git/trees/main": `{"tree":[
			{"path":"a.jsonl","type":"blob","sha":"a"},
			{"path":"dir","type":"tree","sha":"tree-dir"}
		]}`,
	}, nil)

	entries, err := client.ListDir(context.Background(), "main", "")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.jsonl" {
		t.Fatalf("entries = %#v, want only a.jsonl", entries)
	}
}
