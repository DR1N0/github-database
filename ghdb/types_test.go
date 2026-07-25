package ghdb

import (
	"encoding/json"
	"testing"
)

func TestConfigUnmarshal(t *testing.T) {
	raw := `{"name":"mydb","version":3,"mode":"table","github_repo":"owner/repo",
	         "delta_branch":"ghdb-data","data_repo_path":"data/mydb","main_branch":"",
	         "flush_interval_sec":300,"sync_interval_sec":30,
	         "tables":[{"name":"imagerepos","key":"host",
	           "columns":[{"name":"host","required":true},{"name":"tag"}]}]}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "mydb" {
		t.Errorf("Name=%q want mydb", cfg.Name)
	}
	if cfg.Version != 3 {
		t.Errorf("Version=%d want 3", cfg.Version)
	}
	if len(cfg.Tables[0].Columns) != 2 {
		t.Errorf("Columns len=%d want 2", len(cfg.Tables[0].Columns))
	}
	if !cfg.Tables[0].Columns[0].Required {
		t.Errorf("Columns[0].Required=false want true")
	}
}

func TestMutationRecordRoundTrip(t *testing.T) {
	raw := `{"ts":"2026-07-22T10:05:00.123Z","op":"set","table":"components","key":"svc","value":{"name":"svc"}}`
	var r MutationRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.Op != "set" {
		t.Errorf("Op=%q want set", r.Op)
	}
	if r.Table != "components" {
		t.Errorf("Table=%q want components", r.Table)
	}
}

func TestJSONLRoundTrip(t *testing.T) {
	recs := []MutationRecord{
		{Op: "set", Table: "components", Key: "svc"},
		{Op: "delete", Table: "components", Key: "old"},
	}
	data, err := marshalJSONL(recs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalJSONL(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[1].Op != "delete" {
		t.Errorf("got[1].Op=%q want delete", got[1].Op)
	}
}
