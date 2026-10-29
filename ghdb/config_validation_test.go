package ghdb

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DR1N0/github-database/ghdb/github"
)

// baselineWith replaces db_meta.json in a minimal baseline.
func baselineWith(meta string) fstest.MapFS {
	return fstest.MapFS{"db_meta.json": {Data: []byte(meta)}}
}

func TestOpenRejectsMetaWithoutMode(t *testing.T) {
	baseline := baselineWith(`{"name":"db","version":1,"base_repo":"owner/repo","delta_branch":"d","tables":[{"name":"things","key":"id"}]}`)
	_, err := NewOption(baseline).Open(context.Background())
	if err == nil {
		t.Fatal("Open error = nil, want a missing-mode error")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v, want it to mention mode", err)
	}
}

func TestOpenRejectsMetaWithoutRepositoryPath(t *testing.T) {
	// Both name and data_repo_path are empty, so data would land at the repo root.
	baseline := baselineWith(`{"version":1,"mode":"table","base_repo":"owner/repo","delta_branch":"d","tables":[{"name":"things","key":"id"}]}`)
	_, err := NewOption(baseline).Open(context.Background())
	if err == nil {
		t.Fatal("Open error = nil, want a missing-repository-path error")
	}
	if !strings.Contains(err.Error(), "data_repo_path") {
		t.Fatalf("error = %v, want it to mention data_repo_path", err)
	}
}

func TestOnlineRequiresBaseRepo(t *testing.T) {
	// A token/client is configured, so startup goes online, but base_repo is empty.
	baseline := baselineWith(`{"name":"db","version":1,"mode":"table","delta_branch":"d","tables":[{"name":"things","key":"id"}]}`)
	if _, err := OpenWithClient(baseline, &github.FakeClient{}); err == nil {
		t.Fatal("online Open with empty base_repo succeeded")
	} else if !strings.Contains(err.Error(), "base_repo") {
		t.Fatalf("error = %v, want it to mention base_repo", err)
	}
}
