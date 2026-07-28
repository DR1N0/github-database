package base

import (
	"testing"
	"time"
)

func TestBuildCommitObject(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	got := string(buildCommitObject(
		"abc123tree", "def456parent",
		"Alice", "alice@example.com",
		ts, "chore: checkpoint testdb to v2",
	))
	want := "tree abc123tree\n" +
		"parent def456parent\n" +
		"author Alice <alice@example.com> 1700000000 +0000\n" +
		"committer Alice <alice@example.com> 1700000000 +0000\n" +
		"\n" +
		"chore: checkpoint testdb to v2"
	if got != want {
		t.Errorf("buildCommitObject:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}
