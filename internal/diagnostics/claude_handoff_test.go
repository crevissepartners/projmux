package diagnostics

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeHandoffRouteRecordsOneClosedRecord(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "claude-helper-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC) }
	recorder := owner.ClaudeRegistration()
	recorder.RecordHandoffRoute(ClaudeHandoffRouteRecord{Side: ClaudeHandoffSourceUnproven, Peer: ClaudeHandoffPeerProcessCodex, AgentUID: "agent-abc234"})
	// A malformed UID is omitted, not projected.
	recorder.RecordHandoffRoute(ClaudeHandoffRouteRecord{Side: ClaudeHandoffTargetUnproven, Peer: ClaudeHandoffPeerUnknown, AgentUID: "%4"})
	// A side or peer outside the closed sets drops the record.
	recorder.RecordHandoffRoute(ClaudeHandoffRouteRecord{Side: "payload-chosen", Peer: ClaudeHandoffPeerTmuxClaude, AgentUID: "agent-abc234"})
	recorder.RecordHandoffRoute(ClaudeHandoffRouteRecord{Side: ClaudeHandoffSourceUnproven, Peer: "claimed-provider", AgentUID: "agent-abc234"})
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	got := events[0]
	if got.Event != claudeHandoffRouteEvent || got.Component != "agent" || got.Level != "error" || got.Result != "error" || got.Kind != "runtime" ||
		got.RunID != "claude-helper-run" || got.Code != "claude.handoff.source-route-unproven" || got.Source != "process-codex" ||
		got.AgentUID != "agent-abc234" || got.PaneUID != "" || got.Message != "" {
		t.Fatalf("event = %+v", got)
	}
	if events[1].Code != "claude.handoff.target-route-unproven" || events[1].Source != "unknown" || events[1].AgentUID != "" {
		t.Fatalf("second event = %+v", events[1])
	}
	if owner.RecordedOutcome() {
		t.Fatal("handoff route record claimed the command outcome")
	}
	var nilRecorder *ClaudeRegistrationRecorder
	nilRecorder.RecordHandoffRoute(ClaudeHandoffRouteRecord{Side: ClaudeHandoffSourceUnproven, Peer: ClaudeHandoffPeerTmuxCodex})
}

func TestClaudeHandoffRouteShapeIsClosed(t *testing.T) {
	t.Parallel()
	valid := Event{At: "2026-10-08T01:02:03Z", Level: "error", Component: "agent", Event: claudeHandoffRouteEvent, Result: "error", Kind: "runtime",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Code: "claude.handoff.source-route-unproven", Source: "tmux-claude", AgentUID: "agent-abc234"}
	if _, err := sanitizeEvent(valid, ""); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	cases := map[string]func(*Event){
		"message":      func(e *Event) { e.Message = "free text" },
		"pane uid":     func(e *Event) { e.PaneUID = "pane-xyz567" },
		"info level":   func(e *Event) { e.Level, e.Result = "info", "success" },
		"unknown side": func(e *Event) { e.Code = "claude.handoff.other" },
		"foreign code": func(e *Event) { e.Code = "claude.registration.ready" },
		"unknown peer": func(e *Event) { e.Source = "helper" },
		"bad agent":    func(e *Event) { e.AgentUID = "agt-alpha" },
		"provider":     func(e *Event) { e.Provider = "claude" },
	}
	for name, change := range cases {
		event := valid
		change(&event)
		if _, err := sanitizeEvent(event, ""); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
