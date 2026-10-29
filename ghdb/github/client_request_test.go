package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a client at an httptest server.
func newTestClient(t *testing.T, handler http.HandlerFunc) *httpClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewGitHubClient("owner/repo", "token", "github.com", 0).(*httpClient)
	client.baseURL = server.URL
	return client
}

// TestClientEscapesPathAndBranch covers repository paths and branch names that
// contain characters which are meaningful in a URL. An unescaped path or ref
// would silently address the wrong resource.
func TestClientEscapesPathAndBranch(t *testing.T) {
	var gotPath, gotRef string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotRef = r.URL.Query().Get("ref")
		_, _ = w.Write([]byte(`{"sha":"s","size":0,"content":"","encoding":"base64"}`))
	})

	if _, _, err := client.GetFile(context.Background(), "feature/da ta#1", "dir/my file.jsonl"); err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if want := "/repos/owner/repo/contents/dir/my%20file.jsonl"; gotPath != want {
		t.Errorf("escaped path = %q, want %q", gotPath, want)
	}
	if gotRef != "feature/da ta#1" {
		t.Errorf("ref = %q, want the original branch name round-tripped", gotRef)
	}
}

// TestClientSendsUserAgent ensures the client identifies itself; GitHub rejects
// requests without a User-Agent header.
func TestClientSendsUserAgent(t *testing.T) {
	var got string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"sha":"s","size":0,"content":"","encoding":"base64"}`))
	})
	if _, _, err := client.GetFile(context.Background(), "main", "a.jsonl"); err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got != userAgent {
		t.Errorf("User-Agent = %q, want %q", got, userAgent)
	}
}

// TestClientReportsRateLimit distinguishes an exhausted quota from a permanent
// failure, and reports when the quota resets.
func TestClientReportsRateLimit(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})

	_, _, err := client.GetFile(context.Background(), "main", "a.jsonl")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Fatalf("error = %v, want a *RateLimitError", err)
	}
	if got := rateLimit.ResetAt.Unix(); got != 1700000000 {
		t.Errorf("ResetAt = %d, want 1700000000", got)
	}
	if rateLimit.Operation == "" {
		t.Error("Operation is empty, want the failing call")
	}
}

// TestClientSurfacesGitHubErrorMessage keeps GitHub's explanation in the error
// instead of discarding it with the response body.
func TestClientSurfacesGitHubErrorMessage(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"sha does not match"}`))
	})

	_, _, err := client.GetFile(context.Background(), "main", "a.jsonl")
	if err == nil {
		t.Fatal("GetFile error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "sha does not match") {
		t.Errorf("error = %v, want GitHub's message", err)
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error = %v, want the status code", err)
	}
}

// TestHTTPErrorOmitsEmptyDetail keeps the "op detail:" prefix readable for
// endpoints without a natural detail argument.
func TestHTTPErrorOmitsEmptyDetail(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{},
		Body:       http.NoBody,
	}
	err := (&httpClient{}).httpError("CreatePR", "", resp)
	if !strings.Contains(err.Error(), "ghdb: CreatePR: HTTP 500") {
		t.Errorf("error = %q, want a compact prefix", err)
	}
}
