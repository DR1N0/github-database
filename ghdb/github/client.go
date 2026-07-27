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
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, "", err
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
