package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed.UTC()
}

func TestStoreLoadMissingReturnsNil(t *testing.T) {
	t.Parallel()

	s := NewStore(t.TempDir())
	got, err := s.Load("claude")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != nil {
		t.Fatalf("Load() = %v, want nil", got)
	}
}

func TestStoreAppendAggregatesBucketsByMinute(t *testing.T) {
	t.Parallel()

	s := NewStore(t.TempDir())
	now := mustTime(t, "2026-05-06T12:30:00Z")
	events := []TokenEvent{
		{At: mustTime(t, "2026-05-06T12:00:10Z"), Tokens: 100},
		{At: mustTime(t, "2026-05-06T12:00:55Z"), Tokens: 200},
		{At: mustTime(t, "2026-05-06T12:05:00Z"), Tokens: 50},
	}
	merged, err := s.Append("claude", events, now)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	if merged[0].Minute != mustTime(t, "2026-05-06T12:00:00Z") || merged[0].Tokens != 300 {
		t.Fatalf("bucket[0] = %+v, want minute=12:00 tokens=300", merged[0])
	}
	if merged[1].Minute != mustTime(t, "2026-05-06T12:05:00Z") || merged[1].Tokens != 50 {
		t.Fatalf("bucket[1] = %+v, want minute=12:05 tokens=50", merged[1])
	}
}

func TestStoreAppendPersistsAndReloads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := NewStore(dir)
	now := mustTime(t, "2026-05-06T12:30:00Z")

	if _, err := s.Append("codex", []TokenEvent{{At: mustTime(t, "2026-05-06T12:00:10Z"), Tokens: 7}}, now); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := s.Append("codex", []TokenEvent{{At: mustTime(t, "2026-05-06T12:00:50Z"), Tokens: 3}}, now); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	reloaded, err := s.Load("codex")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("len(reloaded) = %d, want 1", len(reloaded))
	}
	if reloaded[0].Tokens != 10 {
		t.Fatalf("tokens = %d, want 10 (merged 7+3)", reloaded[0].Tokens)
	}
	// Sanity: file lives where FilePath says.
	if got := s.FilePath("codex"); filepath.Dir(got) != dir {
		t.Fatalf("FilePath dir = %q, want %q", filepath.Dir(got), dir)
	}
}

func TestStoreAppendTrimsBeyondRetention(t *testing.T) {
	t.Parallel()

	s := NewStore(t.TempDir())
	now := mustTime(t, "2026-05-10T00:00:00Z")
	events := []TokenEvent{
		{At: mustTime(t, "2026-05-01T00:00:00Z"), Tokens: 1}, // 9 days ago — drop.
		{At: mustTime(t, "2026-05-04T00:00:00Z"), Tokens: 2}, // 6 days ago — keep.
		{At: mustTime(t, "2026-05-09T00:00:00Z"), Tokens: 4}, // 1 day ago — keep.
	}
	merged, err := s.Append("claude", events, now)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2 (after trim)", len(merged))
	}
	if merged[0].Tokens != 2 || merged[1].Tokens != 4 {
		t.Fatalf("merged = %+v, want [{tokens:2}, {tokens:4}]", merged)
	}
}

func TestStoreFilePathSanitizesModel(t *testing.T) {
	t.Parallel()

	s := NewStore("/tmp/usage")
	got := s.FilePath("claude/dangerous..name")
	want := "/tmp/usage/claude_dangerous__name.json"
	if got != want {
		t.Fatalf("FilePath = %q, want %q", got, want)
	}
}
