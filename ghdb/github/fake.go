package github

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
)

var _ Interface = (*FakeClient)(nil)

// PRRecord holds the details of a PR created via FakeClient.CreatePR.
type PRRecord struct {
	Title string
	Head  string
	Base  string
	URL   string
}

type fileEntry struct {
	content []byte
	sha     string
}

// FakeClient is an in-memory implementation of Interface for use in tests.
type FakeClient struct {
	mu                sync.Mutex
	branches          map[string]string                // branch name → tip SHA
	files             map[string]map[string]*fileEntry // branch → path → entry
	prs               []PRRecord
	DefaultBranchName string // defaults to "main"
	getFileCalls      int
}

// GetFileCallCount returns the total number of GetFile calls made so far.
func (fc *FakeClient) GetFileCallCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.getFileCalls
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func (fc *FakeClient) defaultBranch() string {
	if fc.DefaultBranchName != "" {
		return fc.DefaultBranchName
	}
	return "main"
}

func (fc *FakeClient) ensureMaps() {
	if fc.branches == nil {
		fc.branches = map[string]string{}
	}
	if fc.files == nil {
		fc.files = map[string]map[string]*fileEntry{}
	}
}

// BranchExists reports whether a branch with the given name exists.
func (fc *FakeClient) BranchExists(_ context.Context, name string) (bool, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	_, ok := fc.branches[name]
	return ok, nil
}

// CreateBranch records a new branch rooted at fromSHA.
func (fc *FakeClient) CreateBranch(_ context.Context, name, fromSHA string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	fc.branches[name] = fromSHA
	if fc.files[name] == nil {
		fc.files[name] = map[string]*fileEntry{}
	}
	return nil
}

// GetFile retrieves file content and its SHA. Returns ghdb.ErrNotFound if absent.
func (fc *FakeClient) GetFile(_ context.Context, branch, p string) ([]byte, string, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	fc.getFileCalls++
	bf := fc.files[branch]
	if bf == nil {
		return nil, "", ErrNotFound
	}
	e := bf[p]
	if e == nil {
		return nil, "", ErrNotFound
	}
	cp := make([]byte, len(e.content))
	copy(cp, e.content)
	return cp, e.sha, nil
}

// PutFile writes content to branch/path. currentSHA must match the stored SHA when updating.
func (fc *FakeClient) PutFile(_ context.Context, branch, p, _ string, content []byte, currentSHA string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	if fc.files[branch] == nil {
		fc.files[branch] = map[string]*fileEntry{}
	}
	existing := fc.files[branch][p]
	if currentSHA != "" && (existing == nil || existing.sha != currentSHA) {
		return errors.New("fake: SHA mismatch")
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	fc.files[branch][p] = &fileEntry{content: cp, sha: sha256hex(cp)}
	return nil
}

// ListDir returns the direct children of dir on the given branch.
func (fc *FakeClient) ListDir(_ context.Context, branch, dir string) ([]DirEntry, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	bf := fc.files[branch]
	prefix := strings.TrimRight(dir, "/") + "/"
	var out []DirEntry
	for p, e := range bf {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		if strings.Contains(rest, "/") {
			continue // only direct children
		}
		out = append(out, DirEntry{Name: path.Base(p), SHA: e.sha})
	}
	return out, nil
}

// DefaultBranch returns the default branch name and its tip SHA.
func (fc *FakeClient) DefaultBranch(_ context.Context) (string, string, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	name := fc.defaultBranch()
	sha := fc.branches[name]
	if sha == "" {
		sha = "0000000000000000000000000000000000000000"
	}
	return name, sha, nil
}

// CreatePR records a synthetic pull request and returns its fake URL.
func (fc *FakeClient) CreatePR(_ context.Context, title, head, base string) (string, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	url := fmt.Sprintf("https://github.fake/pulls/%d", len(fc.prs)+1)
	fc.prs = append(fc.prs, PRRecord{Title: title, Head: head, Base: base, URL: url})
	return url, nil
}

// PRs returns all PRs created via CreatePR.
func (fc *FakeClient) PRs() []PRRecord {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	cp := make([]PRRecord, len(fc.prs))
	copy(cp, fc.prs)
	return cp
}
