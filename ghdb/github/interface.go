package github

import (
	"context"
	"time"
)

// Interface is the GitHub API surface used by ghdb. Tests inject a fake.
type Interface interface {
	GetFile(ctx context.Context, branch, path string) (content []byte, sha string, err error)
	PutFile(ctx context.Context, branch, path, message string, content []byte, currentSHA string) error
	ListDir(ctx context.Context, branch, path string) ([]DirEntry, error)
	BranchExists(ctx context.Context, name string) (bool, error)
	CreateBranch(ctx context.Context, name, fromSHA string) error
	DefaultBranch(ctx context.Context) (name string, sha string, err error)
	CreatePR(ctx context.Context, title, head, base string) (prURL string, err error)

	// GetAuthenticatedUser returns the name and email of the authenticated token owner.
	GetAuthenticatedUser(ctx context.Context) (name, email string, err error)

	// GetCommitTree returns the git tree SHA of the given commit SHA.
	GetCommitTree(ctx context.Context, commitSHA string) (treeSHA string, err error)

	// CreateTree creates a new git tree rooted at baseTreeSHA with files overlaid.
	// files maps repo-relative paths to raw file content.
	CreateTree(ctx context.Context, baseTreeSHA string, files map[string][]byte) (treeSHA string, err error)

	// CreateCommit creates a commit. signature is an ASCII-armored PGP detached
	// signature over the raw commit object; pass "" for an unsigned commit.
	CreateCommit(ctx context.Context, treeSHA, parentSHA, message, name, email string, ts time.Time, signature string) (commitSHA string, err error)

	// UpdateRef force-updates the branch ref to commitSHA.
	UpdateRef(ctx context.Context, branch, commitSHA string) error
}
