package diagnostics

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCodexObserverFallbackRecordsOneClosedReasonRecord(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "observer-fallback-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC) }
	owner.CodexObserver().RecordFallback("agent-abc234", "pane-xyz567", "observer-exited")
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{{At: "2026-10-31T09:00:00Z", Level: "info", Component: "agent", Event: "codex.observer.fallback", Result: "success",
		RunID: "observer-fallback-run", Version: "1.0.0", MuxBackend: "tmux", Source: "sdk", Operation: "observer-start", Code: "observer-exited",
		AgentUID: "agent-abc234", PaneUID: "pane-xyz567"}}
	if !slices.Equal(events, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", events, want)
	}
	if owner.RecordedOutcome() {
		t.Fatal("fallback record claimed the command outcome")
	}
	owner.CodexObserver().RecordFallback("agent-abc234", "pane-xyz567", "observer-reported")
	if events, err = store.Read(); err != nil || len(events) != 2 || events[1].Code != "observer-reported" {
		t.Fatalf("observer-reported record = %+v err = %v", events, err)
	}
	var nilRecorder *CodexObserverRecorder
	nilRecorder.RecordFallback("agent-abc234", "pane-xyz567", "observer-exited")
	if (*LifecycleRecorder)(nil).CodexObserver() != nil {
		t.Fatal("nil lifecycle recorder returned an observer recorder")
	}
}

func TestCodexObserverFallbackDropsEveryValueOutsideItsClosedSet(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][3]string{
		"free text reason":    {"agent-abc234", "pane-xyz567", "socket /tmp/x refused"},
		"child wording":       {"agent-abc234", "pane-xyz567", "unavailable"},
		"empty reason":        {"agent-abc234", "pane-xyz567", ""},
		"tmux handle as pane": {"agent-abc234", "%7", "observer-exited"},
		"path as agent":       {"/tmp/agent", "pane-xyz567", "observer-exited"},
		"wrong uid kind":      {"pane-xyz567", "agent-abc234", "observer-exited"},
	} {
		store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
		NewLifecycleRecorder(store, "observer-fallback-run", "1.0.0", "tmux").CodexObserver().RecordFallback(args[0], args[1], args[2])
		if events, err := store.Read(); err == nil && len(events) != 0 {
			t.Fatalf("%s recorded %+v", name, events)
		}
	}
}
