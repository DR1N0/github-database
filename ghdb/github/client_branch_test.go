package github

import (
	"context"
	"net/http"
	"testing"
)

// TestListDirResolvesSlashBranchThroughCommit covers branch names that contain
// "/": they cannot be passed to the trees endpoint as a ref, because the slash
// inside the URL path would be read as another path segment.
func TestListDirResolvesSlashBranchThroughCommit(t *testing.T) {
	var requested []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		requested = append(requested, path)
		switch path {
		case "/repos/owner/repo/branches/feature%2Fx":
			_, _ = w.Write([]byte(`{"commit":{"sha":"commit-1"}}`))
		case "/repos/owner/repo/git/commits/commit-1":
			_, _ = w.Write([]byte(`{"tree":{"sha":"tree-root"}}`))
		case "/repos/owner/repo/git/trees/tree-root":
			_, _ = w.Write([]byte(`{"tree":[{"path":"db","type":"tree","sha":"tree-db"}]}`))
		case "/repos/owner/repo/git/trees/tree-db":
			_, _ = w.Write([]byte(`{"tree":[{"path":"pod.jsonl","type":"blob","sha":"blob-1"}]}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	})

	entries, err := client.ListDir(context.Background(), "feature/x", "db")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 1 || entries[0] != (DirEntry{Name: "pod.jsonl", SHA: "blob-1"}) {
		t.Fatalf("entries = %#v, want the single segment", entries)
	}

	want := []string{
		"/repos/owner/repo/branches/feature%2Fx",
		"/repos/owner/repo/git/commits/commit-1",
		"/repos/owner/repo/git/trees/tree-root",
		"/repos/owner/repo/git/trees/tree-db",
	}
	if len(requested) != len(want) {
		t.Fatalf("requests = %v, want %v", requested, want)
	}
	for i := range want {
		if requested[i] != want[i] {
			t.Fatalf("request %d = %q, want %q", i, requested[i], want[i])
		}
	}
}

// TestListDirPlainBranchSkipsCommitLookup keeps the common case to two requests.
func TestListDirPlainBranchSkipsCommitLookup(t *testing.T) {
	var requested []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.EscapedPath())
		_, _ = w.Write([]byte(`{"tree":[{"path":"a.jsonl","type":"blob","sha":"a"}]}`))
	})

	if _, err := client.ListDir(context.Background(), "main", ""); err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(requested) != 1 || requested[0] != "/repos/owner/repo/git/trees/main" {
		t.Fatalf("requests = %v, want a single tree request", requested)
	}
}

// TestBranchEndpointsEscapeName ensures branch names with a slash address the
// branch resource rather than a nested path.
func TestBranchEndpointsEscapeName(t *testing.T) {
	var requested []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.EscapedPath())
		_, _ = w.Write([]byte(`{"commit":{"sha":"c1"}}`))
	})

	if _, err := client.BranchExists(context.Background(), "release/1.0"); err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if _, err := client.GetBranchHead(context.Background(), "release/1.0"); err != nil {
		t.Fatalf("GetBranchHead: %v", err)
	}

	want := "/repos/owner/repo/branches/release%2F1.0"
	for i, got := range requested {
		if got != want {
			t.Fatalf("request %d = %q, want %q", i, got, want)
		}
	}
}
