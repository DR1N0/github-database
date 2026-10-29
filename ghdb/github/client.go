package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("ghdb: not found")

// ErrRateLimited is returned when GitHub rejects a request because the token's
// rate limit is exhausted. Callers may treat it as retryable later, not as a
// permanent failure.
var ErrRateLimited = errors.New("ghdb: GitHub API rate limit exceeded")

// userAgent identifies this client. GitHub rejects requests without a
// User-Agent, and a stable value makes the library's traffic attributable.
const userAgent = "github-database-ghdb/1 (+https://github.com/DR1N0/github-database)"

// DefaultRequestTimeout is the timeout used for each GitHub HTTP request.
const DefaultRequestTimeout = 30 * time.Second

type DirEntry struct {
	Name string
	SHA  string
}

type httpClient struct {
	repo    string // "owner/repo"
	token   string
	baseURL string // https://api.github.com or https://{host}/api/v3
	hc      *http.Client
}

// NewGitHubClient creates a GitHub API client.
// host defaults to "github.com" when empty.
// github.com uses https://api.github.com; any other host uses https://{host}/api/v3 (GHE).
// Non-positive timeout values use DefaultRequestTimeout.
func NewGitHubClient(repo, token, host string, timeout time.Duration) Interface {
	if host == "" {
		host = "github.com"
	}
	var baseURL string
	if host == "github.com" || host == "api.github.com" {
		baseURL = "https://api.github.com"
	} else {
		baseURL = "https://" + host + "/api/v3"
	}
	return &httpClient{
		repo:    repo,
		token:   token,
		baseURL: baseURL,
		hc: &http.Client{
			Timeout: resolveRequestTimeout(timeout),
			// Do not follow redirects — a redirect means the host requires
			// browser-based auth (e.g. SAML SSO) and is unreachable via token alone.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func resolveRequestTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultRequestTimeout
	}
	return timeout
}

// escapePath escapes each segment of a repo-relative path for use in a URL path.
// Paths are repository file paths, so a literal "/" must stay a separator while
// any other reserved character (space, "#", "?") is escaped.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// refQuery returns the ?ref= query for a branch name. Branch names may contain
// "/", which would otherwise be read as part of the path.
func refQuery(branch string) string {
	return "?ref=" + url.QueryEscape(branch)
}

// readErrorBody returns a bounded snippet of an error response body so failures
// carry GitHub's explanation rather than only a status code.
func readErrorBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return ""
	}
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return ""
	}
	return ": " + snippet
}

// httpError builds the error for an unexpected response. An exhausted quota is
// reported as ErrRateLimited so callers can tell "retry later" from "this will
// never work".
func (c *httpClient) httpError(op, detail string, resp *http.Response) error {
	target := op
	if detail != "" {
		target = op + " " + detail
	}
	rateLimited := resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
	if rateLimited {
		return fmt.Errorf("ghdb: %s: %w (resets at %s)", target, ErrRateLimited, resp.Header.Get("X-RateLimit-Reset"))
	}
	return fmt.Errorf("ghdb: %s: HTTP %d%s", target, resp.StatusCode, readErrorBody(resp))
}

// apiURL returns the full API URL for a repo path.
func (c *httpClient) apiURL(suffix string) string {
	return c.baseURL + "/repos/" + c.repo + suffix
}

func (c *httpClient) do(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

// GetFile fetches a single file's content from a branch.
// Returns ErrNotFound if the file or branch does not exist (HTTP 404).
func (c *httpClient) GetFile(ctx context.Context, branch, path string) ([]byte, string, error) {
	endpoint := c.apiURL("/contents/" + escapePath(path) + refQuery(branch))
	resp, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", c.httpError("GetFile", path, resp)
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Size     *int   `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, "", err
	}
	if payload.Size == nil {
		return nil, "", fmt.Errorf("ghdb: GetFile %s: missing size", path)
	}
	if payload.SHA == "" {
		return nil, "", fmt.Errorf("ghdb: GetFile %s: missing SHA", path)
	}
	if *payload.Size > 0 && (payload.Content == "" || payload.Encoding == "none") {
		blobResp, err := c.do(ctx, http.MethodGet, c.apiURL("/git/blobs/"+payload.SHA), nil)
		if err != nil {
			return nil, "", err
		}
		defer blobResp.Body.Close()
		if blobResp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("ghdb: GetFile blob %s: HTTP %d", payload.SHA, blobResp.StatusCode)
		}
		var blob struct {
			Content *string `json:"content"`
		}
		if err := json.NewDecoder(blobResp.Body).Decode(&blob); err != nil {
			return nil, "", err
		}
		if blob.Content == nil {
			return nil, "", fmt.Errorf("ghdb: GetFile blob %s: missing content", payload.SHA)
		}
		content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(*blob.Content, "\n", ""))
		if err != nil {
			return nil, "", err
		}
		if len(content) != *payload.Size {
			return nil, "", fmt.Errorf("ghdb: GetFile blob %s: decoded size %d does not match declared size %d", payload.SHA, len(content), *payload.Size)
		}
		return content, payload.SHA, nil
	}
	// GitHub returns base64 with newlines; strip them before decoding.
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, "", err
	}
	return content, payload.SHA, nil
}

// PutFile creates or updates a file. currentSHA must be empty for new files.
func (c *httpClient) PutFile(ctx context.Context, branch, path, message string, content []byte, currentSHA string) error {
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	if currentSHA != "" {
		body["sha"] = currentSHA
	}
	b, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPut, c.apiURL("/contents/"+escapePath(path)), bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return c.httpError("PutFile", path, resp)
	}
	return nil
}

// treeEntry is one entry in a git tree listing.
type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// listTree lists the direct children of one git tree. treeRef is a tree SHA or
// a ref such as a branch name, which GitHub resolves before listing.
// A truncated listing is reported as an error: silently dropping entries would
// hide delta segments from replay.
func (c *httpClient) listTree(ctx context.Context, treeRef string) ([]treeEntry, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL("/git/trees/"+url.PathEscape(treeRef)), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.httpError("ListTree", treeRef, resp)
	}
	var payload struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Truncated {
		return nil, fmt.Errorf("ghdb: tree %q is truncated by GitHub; the directory has too many entries to list completely", treeRef)
	}
	return payload.Tree, nil
}

// pathSegments splits a repository path into its components.
func pathSegments(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// ListDir lists the direct child files of a directory path on a branch.
// Returns an empty slice (no error) if the branch or path does not exist.
//
// Listings walk git trees instead of the contents API. The contents endpoint
// stops at 1,000 entries per directory, which a long-lived delta branch
// eventually exceeds; trees have a far higher ceiling, and every level of the
// walk is an immutable object so the listing is self-consistent.
func (c *httpClient) ListDir(ctx context.Context, branch, path string) ([]DirEntry, error) {
	ref := branch
	for _, segment := range pathSegments(path) {
		entries, err := c.listTree(ctx, ref)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		next := ""
		for _, entry := range entries {
			if entry.Type == "tree" && entry.Path == segment {
				next = entry.SHA
				break
			}
		}
		if next == "" {
			return nil, nil
		}
		ref = next
	}
	entries, err := c.listTree(ctx, ref)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type == "blob" {
			out = append(out, DirEntry{Name: entry.Path, SHA: entry.SHA})
		}
	}
	return out, nil
}

// BranchExists returns true if the branch exists in the repo.
func (c *httpClient) BranchExists(ctx context.Context, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL("/branches/"+name), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, c.httpError("BranchExists", name, resp)
	}
	return true, nil
}

// CreateBranch creates a new branch from the given SHA.
func (c *httpClient) CreateBranch(ctx context.Context, name, fromSHA string) error {
	b, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + name, "sha": fromSHA})
	resp, err := c.do(ctx, http.MethodPost, c.apiURL("/git/refs"), bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return c.httpError("CreateBranch", name, resp)
	}
	return nil
}

// DefaultBranch returns the repo's default branch name and its HEAD SHA.
func (c *httpClient) DefaultBranch(ctx context.Context) (string, string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL(""), nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", c.httpError("DefaultBranch", c.repo, resp)
	}
	body, _ := io.ReadAll(resp.Body)
	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &repoInfo); err != nil {
		snippet := body
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return "", "", fmt.Errorf("ghdb: DefaultBranch decode: %w (response: %s)", err, snippet)
	}
	sha, err := c.GetBranchHead(ctx, repoInfo.DefaultBranch)
	if err != nil {
		return "", "", err
	}
	return repoInfo.DefaultBranch, sha, nil
}

// GetBranchHead returns the HEAD commit SHA of the named branch.
// Returns ErrNotFound if the branch does not exist (HTTP 404).
func (c *httpClient) GetBranchHead(ctx context.Context, branch string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL("/branches/"+branch), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.httpError("GetBranchHead", branch, resp)
	}
	var branchInfo struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&branchInfo); err != nil {
		return "", err
	}
	return branchInfo.Commit.SHA, nil
}

// GetRepoInfo returns the authenticated token's push access to this repo, and
// (if it is a GitHub fork) the repo it was forked from.
func (c *httpClient) GetRepoInfo(ctx context.Context) (RepoInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL(""), nil)
	if err != nil {
		return RepoInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RepoInfo{}, c.httpError("GetRepoInfo", c.repo, resp)
	}
	var repoInfo struct {
		Fork        bool `json:"fork"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
		Parent struct {
			FullName string `json:"full_name"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return RepoInfo{}, err
	}
	info := RepoInfo{CanPush: repoInfo.Permissions.Push}
	if repoInfo.Fork {
		info.ForkParent = repoInfo.Parent.FullName
	}
	return info, nil
}

// CreatePR opens a pull request from head into base and returns its URL.
func (c *httpClient) CreatePR(ctx context.Context, title, head, base string) (string, error) {
	b, _ := json.Marshal(map[string]string{"title": title, "head": head, "base": base})
	resp, err := c.do(ctx, http.MethodPost, c.apiURL("/pulls"), bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", c.httpError("CreatePR", "", resp)
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", err
	}
	return pr.HTMLURL, nil
}

// GetAuthenticatedUser returns the authenticated user's name and email.
// Falls back to login if name is empty, and to GitHub's no-reply email if email is hidden.
func (c *httpClient) GetAuthenticatedUser(ctx context.Context) (string, string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.baseURL+"/user", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", c.httpError("GetAuthenticatedUser", "", resp)
	}
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", "", err
	}
	name := u.Name
	if name == "" {
		name = u.Login
	}
	email := u.Email
	if email == "" {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, u.Login)
	}
	return name, email, nil
}

// GetCommitTree returns the git tree SHA of the given commit.
func (c *httpClient) GetCommitTree(ctx context.Context, commitSHA string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, c.apiURL("/git/commits/"+commitSHA), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.httpError("GetCommitTree", commitSHA, resp)
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return "", err
	}
	return commit.Tree.SHA, nil
}

// CreateTree creates a git tree overlaying files on baseTreeSHA.
// files maps repo-relative paths to raw file content.
func (c *httpClient) CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (string, error) {
	type treeEntry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	entries := make([]treeEntry, 0, len(files))
	for p, content := range files {
		entries = append(entries, treeEntry{Path: p, Mode: "100644", Type: "blob", Content: string(content)})
	}
	b, _ := json.Marshal(map[string]any{"base_tree": baseTreeSHA, "tree": entries})
	resp, err := c.do(ctx, http.MethodPost, c.apiURL("/git/trees"), bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", c.httpError("CreateTree", "", resp)
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.SHA, nil
}

// CreateCommit creates a git commit. Pass signature="" for unsigned commits.
// ts is used as both the author and committer timestamp (UTC, second precision).
func (c *httpClient) CreateCommit(ctx context.Context, treeSHA, parentSHA, message, name, email string, ts time.Time, signature string) (string, error) {
	type identity struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Date  string `json:"date"`
	}
	dateStr := ts.UTC().Truncate(time.Second).Format(time.RFC3339)
	id := identity{Name: name, Email: email, Date: dateStr}
	body := map[string]any{
		"message":   message,
		"tree":      treeSHA,
		"parents":   []string{parentSHA},
		"author":    id,
		"committer": id,
	}
	if signature != "" {
		body["signature"] = signature
	}
	b, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPost, c.apiURL("/git/commits"), bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", c.httpError("CreateCommit", "", resp)
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.SHA, nil
}

// UpdateRef force-updates a branch ref to commitSHA.
func (c *httpClient) UpdateRef(ctx context.Context, branch, commitSHA string) error {
	b, _ := json.Marshal(map[string]any{"sha": commitSHA, "force": true})
	resp, err := c.do(ctx, http.MethodPatch, c.apiURL("/git/refs/heads/"+branch), bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.httpError("UpdateRef", branch, resp)
	}
	return nil
}
