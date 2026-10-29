package base

import "time"

const (
	MaxSingleMutationBytes = 32 * 1024 * 1024
	// DefaultMaxDeltaSegmentBytes bounds the default size of an online delta segment.
	DefaultMaxDeltaSegmentBytes = 786_432
	// DefaultMaxDeltaSegmentRecords bounds the default number of records in an online delta segment.
	DefaultMaxDeltaSegmentRecords = 1_000
	// MaxDeltaSegmentBytes is the largest accepted configured delta segment size.
	MaxDeltaSegmentBytes = 917_504
)

type Config struct {
	Name         string    `json:"name"`
	Version      int       `json:"version"`
	BaselineTime time.Time `json:"baseline_time"`
	Mode         string    `json:"mode"`
	// BaseRepo hosts SourceBranch (the checkpoint tree basis) and TargetBranch
	// (the checkpoint PR's destination).
	BaseRepo     string `json:"base_repo"`
	DeltaBranch  string `json:"delta_branch"`
	DataRepoPath string `json:"data_repo_path"`
	// ForkRepo, if set, is used for delta_branch and checkpoint commits instead
	// of BaseRepo, for tokens that can read BaseRepo but cannot write to it.
	// It must be an actual GitHub fork of BaseRepo — forks share the underlying
	// git object store, which is required for creating commits/trees/branches
	// here that reference BaseRepo SHAs. Empty means use BaseRepo for everything
	// (today's single-repo behavior).
	ForkRepo string `json:"fork_repo,omitempty"`
	// SourceBranch is the branch (on BaseRepo) checkpoint snapshots are built
	// from. Empty means use the repo's actual default branch (via the GitHub API).
	SourceBranch string `json:"source_branch,omitempty"`
	// TargetBranch is the PR base (on BaseRepo) for checkpoint commits. Empty
	// means use whichever branch SourceBranch resolved to.
	TargetBranch           string       `json:"target_branch,omitempty"`
	FlushIntervalSec       int          `json:"flush_interval_sec"`
	SyncIntervalSec        int          `json:"sync_interval_sec"`
	MaxDeltaSegmentBytes   int          `json:"max_delta_segment_bytes"`
	MaxDeltaSegmentRecords int          `json:"max_delta_segment_records"`
	Tables                 []TableSpec  `json:"tables,omitempty"`
	Vertices               []VertexSpec `json:"vertices,omitempty"`
	Edges                  []EdgeSpec   `json:"edges,omitempty"`
}

func deltaSegmentBytes(cfg Config) int {
	if cfg.MaxDeltaSegmentBytes == 0 {
		return DefaultMaxDeltaSegmentBytes
	}
	return cfg.MaxDeltaSegmentBytes
}

func deltaSegmentRecords(cfg Config) int {
	if cfg.MaxDeltaSegmentRecords == 0 {
		return DefaultMaxDeltaSegmentRecords
	}
	return cfg.MaxDeltaSegmentRecords
}

type TableSpec struct {
	Name    string       `json:"name"`
	Key     string       `json:"key"`
	Columns []ColumnSpec `json:"columns,omitempty"`
}
type ColumnSpec struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
}
type VertexSpec struct {
	Label      string         `json:"label"`
	Properties []PropertySpec `json:"properties,omitempty"`
}
type PropertySpec struct {
	Name string `json:"name"`
}
type EdgeSpec struct {
	Label      string         `json:"label"`
	Properties []PropertySpec `json:"properties,omitempty"`
}

func GetDataRepoPath(cfg Config) string {
	if cfg.DataRepoPath != "" {
		return cfg.DataRepoPath
	}
	return cfg.Name
}
