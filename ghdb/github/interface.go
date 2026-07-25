package github

import "context"

// Interface is the GitHub API surface used by ghdb. Tests inject a fake.
type Interface interface {
	GetFile(ctx context.Context, branch, path string) (content []byte, sha string, err error)
	PutFile(ctx context.Context, branch, path, message string, content []byte, currentSHA string) error
	ListDir(ctx context.Context, branch, path string) ([]DirEntry, error)
	BranchExists(ctx context.Context, name string) (bool, error)
	CreateBranch(ctx context.Context, name, fromSHA string) error
	DefaultBranch(ctx context.Context) (name string, sha string, err error)
	CreatePR(ctx context.Context, title, head, base string) (prURL string, err error)
}
