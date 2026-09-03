package base

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

func TestUnmarshalJSONLAccepts32MiBLine(t *testing.T) {
	const prefix = `{"value":"`
	const suffix = `"}`
	line := append([]byte(prefix), bytes.Repeat([]byte("x"), MaxSingleMutationBytes-len(prefix)-len(suffix))...)
	line = append(line, suffix...)
	if got := len(line); got != MaxSingleMutationBytes {
		t.Fatalf("line size = %d, want %d", got, MaxSingleMutationBytes)
	}

	recs, err := UnmarshalJSONL(append(line, '\n'))
	if err != nil {
		t.Fatalf("UnmarshalJSONL() error = %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
}

func TestDecodeJSONLRecoversPerLineAndPreservesLineNumbers(t *testing.T) {
	valid, err := MarshalJSONL([]MutationRecord{{TS: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), Op: "delete", Table: "things", Key: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	oversized := bytes.Repeat([]byte("x"), MaxSingleMutationBytes+1)
	data := append([]byte("not JSON\n"), oversized...)
	data = append(data, '\n')
	data = append(data, valid...)

	recs, errs := decodeJSONL(data)
	if len(errs) != 2 {
		t.Fatalf("error count = %d, want 2", len(errs))
	}
	if errs[0].Line != 1 || errs[1].Line != 2 {
		t.Fatalf("error lines = [%d %d], want [1 2]", errs[0].Line, errs[1].Line)
	}
	if len(recs) != 1 || recs[0].line != 3 || recs[0].record.Key != "ok" {
		t.Fatalf("decoded records = %#v, want valid record from line 3", recs)
	}

	if _, err := UnmarshalJSONL(data); err == nil {
		t.Fatal("UnmarshalJSONL() succeeded for malformed JSONL")
	}
}

type replayTestGitHub struct {
	entries map[string][]github.DirEntry
	files   map[string][]byte
}

func (f *replayTestGitHub) GetFile(_ context.Context, _, path string) ([]byte, string, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return data, "content-sha", nil
}

func (f *replayTestGitHub) PutFile(context.Context, string, string, string, []byte, string) error {
	return nil
}

func (f *replayTestGitHub) ListDir(_ context.Context, _, path string) ([]github.DirEntry, error) {
	return f.entries[path], nil
}

func (f *replayTestGitHub) BranchExists(context.Context, string) (bool, error) { return false, nil }
func (f *replayTestGitHub) CreateBranch(context.Context, string, string) error { return nil }
func (f *replayTestGitHub) DefaultBranch(context.Context) (string, string, error) {
	return "", "", nil
}
func (f *replayTestGitHub) CreatePR(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *replayTestGitHub) GetAuthenticatedUser(context.Context) (string, string, error) {
	return "", "", nil
}
func (f *replayTestGitHub) GetCommitTree(context.Context, string) (string, error) { return "", nil }
func (f *replayTestGitHub) CreateTree(context.Context, string, map[string][]byte) (string, error) {
	return "", nil
}
func (f *replayTestGitHub) CreateCommit(context.Context, string, string, string, string, string, time.Time, string) (string, error) {
	return "", nil
}
func (f *replayTestGitHub) UpdateRef(context.Context, string, string) error { return nil }

func TestReplayVersionAcknowledgesBadSegmentAndWatermarksSuccesses(t *testing.T) {
	cutoff := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	t1 := cutoff.Add(time.Second)
	t2 := t1.Add(time.Second)
	t3 := t2.Add(time.Second)
	records, err := MarshalJSONL([]MutationRecord{
		{TS: t1, Key: "fail-first"},
		{TS: t2, Key: "success"},
		{TS: t3, Key: "fail-last"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "data/v1/segment.jsonl"
	client := &replayTestGitHub{
		entries: map[string][]github.DirEntry{"data/v1": {{Name: "segment.jsonl", SHA: "segment-sha"}}},
		files:   map[string][]byte{path: append([]byte("not JSON\n"), records...)},
	}
	var logs bytes.Buffer
	b := &baseDB{
		cfg:       Config{DeltaBranch: "delta"},
		gh:        client,
		logger:    log.New(&logs, "", 0),
		syncSHAs:  map[string]string{},
		syncTimes: map[string]time.Time{},
		applyFn: func(r MutationRecord) error {
			if r.Key != "success" {
				return errors.New("rejected")
			}
			return nil
		},
	}

	if err := b.replayVersion(context.Background(), b.cfg, "data/v1", cutoff); err != nil {
		t.Fatal(err)
	}
	if got := b.syncSHAs[path]; got != "segment-sha" {
		t.Fatalf("acknowledged SHA = %q, want segment-sha", got)
	}
	if got := b.syncTimes[path]; !got.Equal(t2) {
		t.Fatalf("watermark = %s, want last successful timestamp %s", got, t2)
	}
	for _, want := range []string{
		"data/v1/segment.jsonl (sha segment-sha) line 1",
		"data/v1/segment.jsonl (sha segment-sha) line 2: invalid mutation",
		"data/v1/segment.jsonl (sha segment-sha) line 4: invalid mutation",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("warning log missing %q:\n%s", want, logs.String())
		}
	}
}

func TestReplayWarningsDoNotLogMutationContents(t *testing.T) {
	const secret = "top-secret-remote-value"
	cutoff := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	valid, err := MarshalJSONL([]MutationRecord{{TS: cutoff.Add(time.Second), Op: "set", Key: secret}})
	if err != nil {
		t.Fatal(err)
	}
	path := "data/v1/segment.jsonl"
	client := &replayTestGitHub{
		entries: map[string][]github.DirEntry{"data/v1": {{Name: "segment.jsonl", SHA: "segment-sha"}}},
		files: map[string][]byte{path: append(
			[]byte(`{"ts":"`+secret+`","op":"set","key":"`+secret+`"}`+"\n"),
			valid...,
		)},
	}
	var logs bytes.Buffer
	b := &baseDB{
		cfg:       Config{DeltaBranch: "delta"},
		gh:        client,
		logger:    log.New(&logs, "", 0),
		syncSHAs:  map[string]string{},
		syncTimes: map[string]time.Time{},
		applyFn: func(r MutationRecord) error {
			return errors.New("invalid mutation for " + r.Key)
		},
	}

	if err := b.replayVersion(context.Background(), b.cfg, "data/v1", cutoff); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if strings.Contains(got, secret) {
		t.Fatalf("warning log exposes remote mutation content:\n%s", got)
	}
	for _, want := range []string{
		"data/v1/segment.jsonl (sha segment-sha) line 1: malformed JSONL record",
		"data/v1/segment.jsonl (sha segment-sha) line 2: invalid mutation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning log missing %q:\n%s", want, got)
		}
	}
}

func TestPollAcknowledgesBadSegmentAndWatermarksSuccesses(t *testing.T) {
	cutoff := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	t1 := cutoff.Add(time.Second)
	t2 := t1.Add(time.Second)
	t3 := t2.Add(time.Second)
	records, err := MarshalJSONL([]MutationRecord{
		{TS: t1, Key: "fail-first"},
		{TS: t2, Key: "success"},
		{TS: t3, Key: "fail-last"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "data/v1/segment.jsonl"
	client := &replayTestGitHub{
		entries: map[string][]github.DirEntry{"data/v1": {{Name: "segment.jsonl", SHA: "segment-sha"}}},
		files:   map[string][]byte{path: records},
	}
	b := &baseDB{
		cfg:       Config{Name: "data", Version: 1, DeltaBranch: "delta"},
		gh:        client,
		syncSHAs:  map[string]string{},
		syncTimes: map[string]time.Time{},
		applyFn: func(r MutationRecord) error {
			if r.Key != "success" {
				return errors.New("rejected")
			}
			return nil
		},
	}

	if err := b.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.syncSHAs[path]; got != "segment-sha" {
		t.Fatalf("acknowledged SHA = %q, want segment-sha", got)
	}
	if got := b.syncTimes[path]; !got.Equal(t2) {
		t.Fatalf("watermark = %s, want last successful timestamp %s", got, t2)
	}
}
