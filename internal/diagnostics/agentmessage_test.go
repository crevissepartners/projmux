package diagnostics

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentMessageForeignSourceRecordsOneOpaqueRecord(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "agent-message-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC) }
	recorder := owner.AgentMessage()
	recorder.RecordForeignSource("agent-abc234", "pane-xyz567")
	// A UID that is not strictly shaped drops the record.
	recorder.RecordForeignSource("agt-alpha", "pane-xyz567")
	recorder.RecordForeignSource("agent-abc234", "%4")
	recorder.RecordForeignSource("", "")
	events, err := store.Read()
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	got := events[0]
	if got.Event != agentMessageForeignSourceEvent || got.Component != "agent" || got.Level != "info" || got.Result != "success" ||
		got.RunID != "agent-message-run" || got.AgentUID != "agent-abc234" || got.PaneUID != "pane-xyz567" ||
		got.Message != "" || got.Code != "" || got.Kind != "" {
		t.Fatalf("event = %+v", got)
	}
	if owner.RecordedOutcome() {
		t.Fatal("foreign-source record claimed the command outcome")
	}
	var nilRecorder *AgentMessageRecorder
	nilRecorder.RecordForeignSource("agent-abc234", "pane-xyz567")
	if (*LifecycleRecorder)(nil).AgentMessage() != nil {
		t.Fatal("nil lifecycle recorder returned an agent message recorder")
	}
}

func TestAgentMessageForeignSourceAppendFailureIsIgnored(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{err: errors.New("fixture journal unavailable")}
	NewLifecycleRecorder(writer, "agent-message-run", "1.0.0", "tmux").AgentMessage().RecordForeignSource("agent-abc234", "pane-xyz567")
	if got := writer.snapshot(); len(got) != 1 {
		t.Fatalf("appends = %d, want one attempt", len(got))
	}
}

func TestAgentMessageFieldsAreRejectedOnEveryOtherFamily(t *testing.T) {
	t.Parallel()
	base := Event{At: "2026-09-25T01:02:03Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux"}
	if _, err := sanitizeEvent(base, ""); err != nil {
		t.Fatalf("base event rejected: %v", err)
	}
	withAgent := base
	withAgent.AgentUID = "agent-abc234"
	agentComponent := base
	agentComponent.Component = "agent"
	teardown := Event{At: base.At, Level: "info", Component: "topology", Event: teardownDecisionEvent, Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Decision: string(TeardownDecisionRetain),
		Code: string(TeardownReasonAwaitingPaneExit), AgentUID: "agent-abc234"}
	foreign := Event{At: base.At, Level: "info", Component: "agent", Event: agentMessageForeignSourceEvent, Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", AgentUID: "agent-abc234", PaneUID: "pane-xyz567", Message: "free text"}
	for name, event := range map[string]Event{"agent uid": withAgent, "agent component": agentComponent,
		"teardown agent uid": teardown, "foreign source message": foreign} {
		if _, err := sanitizeEvent(event, ""); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
