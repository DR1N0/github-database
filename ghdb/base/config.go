package base

import "time"

type Config struct {
	Name             string       `json:"name"`
	Version          int          `json:"version"`
	BaselineTime     time.Time    `json:"baseline_time"`
	Mode             string       `json:"mode"`
	GitHubRepo       string       `json:"github_repo"`
	DeltaBranch      string       `json:"delta_branch"`
	DataRepoPath     string       `json:"data_repo_path"`
	MainBranch       string       `json:"main_branch"`
	FlushIntervalSec int          `json:"flush_interval_sec"`
	SyncIntervalSec  int          `json:"sync_interval_sec"`
	Tables           []TableSpec  `json:"tables,omitempty"`
	Vertices         []VertexSpec `json:"vertices,omitempty"`
	Edges            []EdgeSpec   `json:"edges,omitempty"`
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
	Label string `json:"label"`
}
func GetDataRepoPath(cfg Config) string {
	if cfg.DataRepoPath != "" {
		return cfg.DataRepoPath
	}
	return cfg.Name
}
