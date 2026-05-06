package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterCollectFromTranscriptFixture(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	projDir := filepath.Join(dir, "-home-test-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	jsonl := `{"type":"user","timestamp":"2026-05-06T11:00:00Z"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-05-06T11:01:00Z","requestId":"req-A","message":{"id":"m1","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":5,"cache_read_input_tokens":1000}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-05-06T11:02:00Z","requestId":"req-A","message":{"id":"m1","usage":{"input_tokens":10,"output_tokens":20}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-05-06T11:03:00Z","requestId":"req-B","message":{"id":"m2","usage":{"input_tokens":7,"output_tokens":3}}}` + "\n"
	file := filepath.Join(projDir, "session.jsonl")
	if err := os.WriteFile(file, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := NewWithRoot(dir)
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (deduped on requestId)", len(events))
	}
	// First: 10+20+5 = 35.
	if events[0].Tokens != 35 {
		t.Fatalf("event[0].Tokens = %d, want 35", events[0].Tokens)
	}
	// Second: 7+3 = 10.
	if events[1].Tokens != 10 {
		t.Fatalf("event[1].Tokens = %d, want 10", events[1].Tokens)
	}
}

func TestAdapterCollectMissingRootIsBestEffort(t *testing.T) {
	t.Parallel()

	a := NewWithRoot(filepath.Join(t.TempDir(), "does-not-exist"))
	a.now = func() time.Time { return time.Now() }

	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if events != nil {
		t.Fatalf("events = %v, want nil", events)
	}
}

func TestAdapterCollectSkipsOldFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	projDir := filepath.Join(dir, "old-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := filepath.Join(projDir, "ancient.jsonl")
	body := `{"type":"assistant","timestamp":"2025-01-01T00:00:00Z","requestId":"old","message":{"id":"x","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	a := NewWithRoot(dir)
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0 (file mtime predates window)", len(events))
	}
}
