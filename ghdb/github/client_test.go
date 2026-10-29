package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewGitHubClientZeroTimeoutUsesDefault(t *testing.T) {
	client := NewGitHubClient("owner/repo", "token", "github.com", 0)
	hc := client.(*httpClient)
	if hc.hc.Timeout != DefaultRequestTimeout {
		t.Fatalf("timeout = %v, want %v", hc.hc.Timeout, DefaultRequestTimeout)
	}
}

func TestNewGitHubClientNegativeTimeoutUsesDefault(t *testing.T) {
	client := NewGitHubClient("owner/repo", "token", "github.com", -time.Second)
	hc := client.(*httpClient)
	if hc.hc.Timeout != DefaultRequestTimeout {
		t.Fatalf("timeout = %v, want %v", hc.hc.Timeout, DefaultRequestTimeout)
	}
}

func TestNewGitHubClientCustomTimeout(t *testing.T) {
	client := NewGitHubClient("owner/repo", "token", "github.com", 90*time.Second)
	hc := client.(*httpClient)
	if hc.hc.Timeout != 90*time.Second {
		t.Fatalf("timeout = %v, want %v", hc.hc.Timeout, 90*time.Second)
	}
}

func TestConfiguredRequestTimeoutPrecedesCallerDeadline(t *testing.T) {
	const requestTimeout = 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte(`{"sha":"late","size":0,"content":"","encoding":"base64"}`))
	}))
	defer server.Close()

	client := NewGitHubClient("owner/repo", "token", "github.com", requestTimeout).(*httpClient)
	client.baseURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	started := time.Now()
	_, _, err := client.GetFile(ctx, "main", "late.jsonl")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetFile error = %v, want request deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("GetFile returned after %v, want configured request timeout before caller deadline", elapsed)
	}
}

func TestGetFileLargeContentFallsBackToBlob(t *testing.T) {
	want := []byte(strings.Repeat("x", 1_048_577))
	var blobRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/contents/large.jsonl":
			if r.URL.Query().Get("ref") != "main" {
				t.Fatalf("unexpected ref: %q", r.URL.Query().Get("ref"))
			}
			_, _ = w.Write([]byte(`{"sha":"blob-sha","size":1048577,"content":"","encoding":"none"}`))
		case "/repos/owner/repo/git/blobs/blob-sha":
			blobRequests.Add(1)
			_, _ = w.Write([]byte(`{"content":"` + base64.StdEncoding.EncodeToString(want) + `","encoding":"base64"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	got, sha, err := client.GetFile(context.Background(), "main", "large.jsonl")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !bytes.Equal(got, want) || sha != "blob-sha" {
		t.Fatalf("GetFile content/SHA mismatch: got len=%d sha=%q, want len=%d sha=%q", len(got), sha, len(want), "blob-sha")
	}
	if got := blobRequests.Load(); got != 1 {
		t.Fatalf("Blob request count = %d, want 1", got)
	}
}

func TestGetFileBlobFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/contents/large.jsonl":
			_, _ = w.Write([]byte(`{"sha":"blob-sha","size":1048577,"content":"","encoding":"none"}`))
		case "/repos/owner/repo/git/blobs/blob-sha":
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	content, _, err := client.GetFile(context.Background(), "main", "large.jsonl")
	if err == nil {
		t.Fatal("GetFile error = nil, want Blob endpoint error")
	}
	if content != nil {
		t.Fatalf("GetFile content = %q, want nil", content)
	}
}

func TestGetFileEmptyFile(t *testing.T) {
	var blobRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/contents/empty.jsonl":
			_, _ = w.Write([]byte(`{"sha":"empty-sha","size":0,"content":"","encoding":"base64"}`))
		case "/repos/owner/repo/git/blobs/empty-sha":
			blobRequests.Add(1)
			http.Error(w, "unexpected blob request", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	got, sha, err := client.GetFile(context.Background(), "main", "empty.jsonl")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got == nil || len(got) != 0 || sha != "empty-sha" {
		t.Fatalf("GetFile = (%q, %q), want (empty bytes, %q)", got, sha, "empty-sha")
	}
	if got := blobRequests.Load(); got != 0 {
		t.Fatalf("Blob request count = %d, want 0", got)
	}
}

func TestGetFileMissingSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/contents/malformed.jsonl" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"sha":"malformed-sha","content":"","encoding":"none"}`))
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	content, _, err := client.GetFile(context.Background(), "main", "malformed.jsonl")
	if err == nil {
		t.Fatal("GetFile error = nil, want missing size error")
	}
	if content != nil {
		t.Fatalf("GetFile content = %q, want nil", content)
	}
}

func TestGetBranchHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/branches/develop" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"commit":{"sha":"develop-sha"}}`))
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	sha, err := client.GetBranchHead(context.Background(), "develop")
	if err != nil || sha != "develop-sha" {
		t.Fatalf("GetBranchHead: sha=%q err=%v, want %q", sha, err, "develop-sha")
	}
}

func TestGetBranchHeadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	_, err := client.GetBranchHead(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBranchHead(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDefaultBranchResolvesHeadSHA(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			_, _ = w.Write([]byte(`{"default_branch":"master"}`))
		case "/repos/owner/repo/branches/master":
			_, _ = w.Write([]byte(`{"commit":{"sha":"master-sha"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	name, sha, err := client.DefaultBranch(context.Background())
	if err != nil || name != "master" || sha != "master-sha" {
		t.Fatalf("DefaultBranch: name=%q sha=%q err=%v, want (%q, %q)", name, sha, err, "master", "master-sha")
	}
}

func TestGetRepoInfoNonFork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"fork":false,"permissions":{"push":true}}`))
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	info, err := client.GetRepoInfo(context.Background())
	if err != nil {
		t.Fatalf("GetRepoInfo: %v", err)
	}
	if !info.CanPush || info.ForkParent != "" {
		t.Fatalf("GetRepoInfo = %+v, want CanPush=true ForkParent=\"\"", info)
	}
}

func TestGetRepoInfoFork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/forkowner/repo" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"fork":true,"permissions":{"push":false},"parent":{"full_name":"upstream/repo"}}`))
	}))
	defer server.Close()

	client := &httpClient{repo: "forkowner/repo", baseURL: server.URL, hc: server.Client()}
	info, err := client.GetRepoInfo(context.Background())
	if err != nil {
		t.Fatalf("GetRepoInfo: %v", err)
	}
	if info.CanPush || info.ForkParent != "upstream/repo" {
		t.Fatalf("GetRepoInfo = %+v, want CanPush=false ForkParent=%q", info, "upstream/repo")
	}
}

func TestGetFileBlobSizeMismatch(t *testing.T) {
	want := []byte("blob content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/contents/large.jsonl":
			_, _ = w.Write([]byte(`{"sha":"blob-sha","size":` + strconv.Itoa(len(want)+1) + `,"content":"","encoding":"none"}`))
		case "/repos/owner/repo/git/blobs/blob-sha":
			_, _ = w.Write([]byte(`{"content":"` + base64.StdEncoding.EncodeToString(want) + `","encoding":"base64"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpClient{repo: "owner/repo", baseURL: server.URL, hc: server.Client()}
	content, _, err := client.GetFile(context.Background(), "main", "large.jsonl")
	if err == nil {
		t.Fatal("GetFile error = nil, want decoded size mismatch error")
	}
	if content != nil {
		t.Fatalf("GetFile content = %q, want nil", content)
	}
}
