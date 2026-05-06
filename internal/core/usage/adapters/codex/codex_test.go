package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterCollectFromRolloutFixture(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dayDir := filepath.Join(dir, "2026", "05", "06")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"timestamp":"2026-05-06T11:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":1000}}}}` + "\n" +
		`{"timestamp":"2026-05-06T11:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":2500}}}}` + "\n" +
		// Non-token_count event is ignored.
		`{"timestamp":"2026-05-06T11:02:00Z","type":"event_msg","payload":{"type":"agent_message"}}` + "\n"
	file := filepath.Join(dayDir, "rollout-2026-05-06T11-00-00-test.jsonl")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := NewWithRoot(dir)
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Tokens != 1000 || events[1].Tokens != 2500 {
		t.Fatalf("events = %+v, want tokens [1000, 2500]", events)
	}
}

func TestAdapterCollectMissingRootIsBestEffort(t *testing.T) {
	t.Parallel()

	a := NewWithRoot(filepath.Join(t.TempDir(), "missing"))
	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if events != nil {
		t.Fatalf("events = %v, want nil", events)
	}
}

func TestAdapterCollectIgnoresUnknownPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dayDir := filepath.Join(dir, "2026", "05", "06")
	_ = os.MkdirAll(dayDir, 0o755)
	body := `{"timestamp":"2026-05-06T11:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null}}` + "\n" +
		`not-json` + "\n" +
		`{"timestamp":"2026-05-06T11:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":0}}}}` + "\n"
	file := filepath.Join(dayDir, "rollout-empty.jsonl")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := NewWithRoot(dir)
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	events, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0", len(events))
	}
}
