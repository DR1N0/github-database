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
	"strings"
	"time"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("ghdb: not found")

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
func NewGitHubClient(repo, token, host string) Interface {
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
			Timeout: 30 * time.Second,
			// Do not follow redirects — a redirect means the host requires
			// browser-based auth (e.g. SAML SSO) and is unreachable via token alone.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// apiURL returns the full API URL for a repo path.
func (c *httpClient) apiURL(suffix string) string {
	return c.baseURL + "/repos/" + c.repo + suffix
}

func (c *httpClient) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

// GetFile fetches a single file's content from a branch.
// Returns ErrNotFound if the file or branch does not exist (HTTP 404).
func (c *httpClient) GetFile(ctx context.Context, branch, path string) ([]byte, string, error) {
	url := c.apiURL("/contents/" + path + "?ref=" + branch)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("ghdb: GetFile %s: HTTP %d", path, resp.StatusCode)
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
	resp, err := c.do(ctx, http.MethodPut, c.apiURL("/contents/"+path), bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("ghdb: PutFile %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// ListDir lists direct children of a directory path on a branch.
// Returns an empty slice (no error) if the path does not exist.
func (c *httpClient) ListDir(ctx context.Context, branch, path string) ([]DirEntry, error) {
	url := c.apiURL("/contents/" + path + "?ref=" + branch)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ghdb: ListDir %s: HTTP %d", path, resp.StatusCode)
	}
	var items []struct {
		Name string `json:"name"`
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	var out []DirEntry
	for _, it := range items {
		if it.Type == "file" {
			out = append(out, DirEntry{Name: it.Name, SHA: it.SHA})
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
		return false, fmt.Errorf("ghdb: BranchExists %s: HTTP %d", name, resp.StatusCode)
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
		return fmt.Errorf("ghdb: CreateBranch %s: HTTP %d", name, resp.StatusCode)
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
		return "", "", fmt.Errorf("ghdb: DefaultBranch: HTTP %d", resp.StatusCode)
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
	// Resolve branch HEAD SHA.
	resp2, err := c.do(ctx, http.MethodGet, c.apiURL("/branches/"+repoInfo.DefaultBranch), nil)
	if err != nil {
		return "", "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("ghdb: DefaultBranch resolve SHA %q: HTTP %d", repoInfo.DefaultBranch, resp2.StatusCode)
	}
	var branchInfo struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&branchInfo); err != nil {
		return "", "", err
	}
	return repoInfo.DefaultBranch, branchInfo.Commit.SHA, nil
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
		return "", fmt.Errorf("ghdb: CreatePR: HTTP %d", resp.StatusCode)
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
		return "", "", fmt.Errorf("ghdb: GetAuthenticatedUser: HTTP %d", resp.StatusCode)
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
		return "", fmt.Errorf("ghdb: GetCommitTree %s: HTTP %d", commitSHA, resp.StatusCode)
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
		return "", fmt.Errorf("ghdb: CreateTree: HTTP %d", resp.StatusCode)
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
		return "", fmt.Errorf("ghdb: CreateCommit: HTTP %d", resp.StatusCode)
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
		return fmt.Errorf("ghdb: UpdateRef %s: HTTP %d", branch, resp.StatusCode)
	}
	return nil
}
