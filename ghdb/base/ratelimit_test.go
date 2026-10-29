package base

import (
	"errors"
	"testing"
	"time"

	"github.com/DR1N0/github-database/ghdb/github"
)

func TestNoteRateLimitSchedulesBackoff(t *testing.T) {
	b := NewBaseDB(Config{}).(*baseDB)
	b.noteRateLimit(&github.RateLimitError{Operation: "GetFile", ResetAt: time.Now().Add(2 * time.Minute)})
	if !b.backingOff() {
		t.Fatal("backingOff() = false, want true inside the quota window")
	}
}

func TestNoteRateLimitIgnoresUnusableWindows(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"not a rate limit", errors.New("boom")},
		{"no reset time", &github.RateLimitError{Operation: "GetFile"}},
		{"already reset", &github.RateLimitError{ResetAt: time.Now().Add(-time.Minute)}},
		{"beyond the backoff cap", &github.RateLimitError{ResetAt: time.Now().Add(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBaseDB(Config{}).(*baseDB)
			b.noteRateLimit(tc.err)
			if b.backingOff() {
				t.Fatal("backingOff() = true, want false")
			}
		})
	}
}
