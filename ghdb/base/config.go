package base

import "time"

const (
	// DefaultMaxDeltaSegmentBytes bounds the default size of an online delta segment.
	DefaultMaxDeltaSegmentBytes = 786_432
	// DefaultMaxDeltaSegmentRecords bounds the default number of records in an online delta segment.
	DefaultMaxDeltaSegmentRecords = 1_000
	// MaxDeltaSegmentBytes is the largest accepted configured delta segment size.
	MaxDeltaSegmentBytes = 917_504
)

type Config struct {
	Name                   string       `json:"name"`
	Version                int          `json:"version"`
	BaselineTime           time.Time    `json:"baseline_time"`
	Mode                   string       `json:"mode"`
	GitHubRepo             string       `json:"github_repo"`
	DeltaBranch            string       `json:"delta_branch"`
	DataRepoPath           string       `json:"data_repo_path"`
	MainBranch             string       `json:"main_branch"`
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
