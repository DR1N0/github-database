package github

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

var _ Interface = (*FakeClient)(nil)

// PRRecord holds the details of a PR created via FakeClient.CreatePR.
type PRRecord struct {
	Title string
	Head  string
	Base  string
	URL   string
}

// CommitRecord holds the details of the most recent CreateCommit call.
type CommitRecord struct {
	TreeSHA   string
	ParentSHA string
	Message   string
	Name      string
	Email     string
	Signature string
}

type fileEntry struct {
	content []byte
	sha     string
}

// FakeClient is an in-memory implementation of Interface for use in tests.
type FakeClient struct {
	mu                 sync.Mutex
	branches           map[string]string                // branch name → tip SHA
	files              map[string]map[string]*fileEntry // branch → path → entry
	prs                []PRRecord
	DefaultBranchName  string // defaults to "main"
	getFileCalls       int
	putFileCalls       int
	nextGetFileErr     error
	nextPutFileErr     error
	putFileErrAfter    int
	lastCommit         CommitRecord
	AuthenticatedName  string // returned by GetAuthenticatedUser; defaults to "Test User"
	AuthenticatedEmail string // returned by GetAuthenticatedUser; defaults to "<EMAIL_ADDRESS>"
}

// GetFileCallCount returns the total number of GetFile calls made so far.
func (fc *FakeClient) GetFileCallCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.getFileCalls
}

// SetGetFileError makes the next GetFile call return err before reading any file.
func (fc *FakeClient) SetGetFileError(err error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.nextGetFileErr = err
}

// SetPutFileError makes the next PutFile call return err before writing a file.
func (fc *FakeClient) SetPutFileError(err error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.putFileErrAfter = 0
	fc.nextPutFileErr = err
}

// SetPutFileErrorAfter makes PutFile return err after successfulCalls writes succeed.
func (fc *FakeClient) SetPutFileErrorAfter(successfulCalls int, err error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.putFileErrAfter = successfulCalls
	fc.nextPutFileErr = err
}

// PutFileCallCount returns the total number of PutFile calls made so far.
func (fc *FakeClient) PutFileCallCount() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.putFileCalls
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
	if fc.nextGetFileErr != nil {
		err := fc.nextGetFileErr
		fc.nextGetFileErr = nil
		return nil, "", err
	}
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
	fc.putFileCalls++
	if fc.nextPutFileErr != nil {
		if fc.putFileErrAfter == 0 {
			err := fc.nextPutFileErr
			fc.nextPutFileErr = nil
			return err
		}
		fc.putFileErrAfter--
	}
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

// GetAuthenticatedUser returns AuthenticatedName/Email (with sensible defaults).
func (fc *FakeClient) GetAuthenticatedUser(_ context.Context) (string, string, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	name := fc.AuthenticatedName
	if name == "" {
		name = "Test User"
	}
	email := fc.AuthenticatedEmail
	if email == "" {
		email = "<EMAIL_ADDRESS>"
	}
	return name, email, nil
}

// GetCommitTree returns a fixed deterministic tree SHA for use in tests.
func (fc *FakeClient) GetCommitTree(_ context.Context, _ string) (string, error) {
	return "basetree0000000000000000000000000000000000", nil
}

// CreateTree records the call and returns a deterministic SHA derived from the content.
func (fc *FakeClient) CreateTree(_ context.Context, _ string, files map[string][]byte) (string, error) {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write(files[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:20]), nil
}

// CreateCommit records the commit details for later inspection via LastCommit().
func (fc *FakeClient) CreateCommit(_ context.Context, treeSHA, parentSHA, message, name, email string, _ time.Time, signature string) (string, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	record := CommitRecord{
		TreeSHA:   treeSHA,
		ParentSHA: parentSHA,
		Message:   message,
		Name:      name,
		Email:     email,
		Signature: signature,
	}
	fc.lastCommit = record
	suffix := treeSHA
	if len(suffix) > 10 {
		suffix = suffix[:10]
	}
	return "commitsha-" + suffix, nil
}

// UpdateRef updates the branch tip to commitSHA.
func (fc *FakeClient) UpdateRef(_ context.Context, branch, commitSHA string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ensureMaps()
	fc.branches[branch] = commitSHA
	return nil
}

// LastCommit returns the most recent CreateCommit record.
func (fc *FakeClient) LastCommit() CommitRecord {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.lastCommit
}
