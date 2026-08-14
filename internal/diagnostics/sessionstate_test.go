package diagnostics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type sessionStateEventWriter struct {
	events []Event
	err    error
}

func (w *sessionStateEventWriter) Append(event Event) error {
	w.events = append(w.events, event)
	return w.err
}

func TestSessionStateRecorderClosedOutcomeTable(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		operation Operation
		source    SessionStateSource
		err       error
		counts    SessionStateCounts
		wantCode  Code
	}{
		{"save success", OperationSessionStateSave, SessionStateSourceManual, nil, SessionStateCounts{1, 3, 1, 1, 1, 0}, ""},
		{"save error", OperationSessionStateSave, SessionStateSourceSettingsLatest, errors.New("/private/project raw"), SessionStateCounts{}, CodeSessionStateSaveFailed},
		{"autosave error", OperationSessionStateAutosave, SessionStateSourceAutosave, errors.New("raw command"), SessionStateCounts{}, CodeSessionStateAutosaveFailed},
		{"restore success", OperationSessionStateRestore, SessionStateSourceStartupNamed, nil, SessionStateCounts{2, 4, 2, 1, 1, 0}, ""},
		{"restore error", OperationSessionStateRestore, SessionStateSourceStartupLatest, errors.New("raw session id"), SessionStateCounts{}, CodeSessionStateRestoreFailed},
		{"delete success", OperationSessionStateDelete, SessionStateSourcePrune, nil, SessionStateCounts{ItemCount: 2}, ""},
		{"delete error", OperationSessionStateDelete, SessionStateSourceSettingsLatest, errors.New("raw path"), SessionStateCounts{}, CodeSessionStateDeleteFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &sessionStateEventWriter{}
			lifecycle := NewLifecycleRecorder(writer, "same-run", "0.10.0", "tmux")
			recorder := lifecycle.SessionState()
			recorder.now = func() time.Time { return now }
			recorder.Record(tt.operation, tt.source, now.Add(-7*time.Millisecond), tt.counts, tt.err)
			if len(writer.events) != 1 {
				t.Fatalf("events = %#v, want one", writer.events)
			}
			event := writer.events[0]
			if event.Event != "session-state.outcome" || event.Component != "session-state" || event.Operation != string(tt.operation) || event.Source != string(tt.source) || event.RunID != "same-run" || event.DurationMS != 7 {
				t.Fatalf("event = %#v", event)
			}
			if tt.err == nil {
				if event.Level != "info" || event.Result != "success" || event.Kind != "" || event.Code != "" {
					t.Fatalf("success shape = %#v", event)
				}
			} else if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" || event.Code != string(tt.wantCode) || event.Message != "" || event.hasCounts() {
				t.Fatalf("error shape = %#v", event)
			}
			if _, err := sanitizeEvent(event, "/home/private"); err != nil {
				t.Fatalf("sanitizeEvent() error = %v", err)
			}
			raw, _ := json.Marshal(event)
			for _, forbidden := range []string{"/private/project", "raw command", "raw session id", "raw path"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("raw event leaked %q: %s", forbidden, raw)
				}
			}
		})
	}
}

func TestSessionStateRecorderAutosaveSuccessAndNoOpVolumeIsZero(t *testing.T) {
	writer := &sessionStateEventWriter{}
	recorder := NewLifecycleRecorder(writer, "run", "0.10.0", "tmux").SessionState()
	for range 100 {
		recorder.Record(OperationSessionStateAutosave, SessionStateSourceAutosave, time.Now(), SessionStateCounts{WindowCount: 99}, nil)
	}
	if len(writer.events) != 0 {
		t.Fatalf("autosave success events = %d, want zero", len(writer.events))
	}
}

func TestSessionStateRecorderAppendFailureStillOwnsEachAttempt(t *testing.T) {
	writer := &sessionStateEventWriter{err: errors.New("append failed")}
	lifecycle := NewLifecycleRecorder(writer, "run", "0.10.0", "tmux")
	recorder := lifecycle.SessionState()
	recorder.Record(OperationSessionStateDelete, SessionStateSourceManual, time.Now(), SessionStateCounts{ItemCount: 1}, nil)
	recorder.Record(OperationSessionStateSave, SessionStateSourceManual, time.Now(), SessionStateCounts{}, errors.New("mutation failed"))
	if !lifecycle.RecordedOutcome() || lifecycle.outcomes.Load() != 2 {
		t.Fatalf("logical outcomes = %d, want two owned attempts", lifecycle.outcomes.Load())
	}
}

func TestSessionStateOutcomeRejectsOpenSchemaShapes(t *testing.T) {
	zero, one := 0, 1
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", DurationMS: 1, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionStateSave), Source: string(SessionStateSourceManual), WindowCount: &one, PaneCount: &one, ShellRecipeCount: &one, AgentRecipeCount: &zero, StartupRecipeCount: &zero}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"wrong component", func(e *Event) { e.Component = "runtime" }},
		{"unknown source", func(e *Event) { e.Source = "popup-raw" }},
		{"wrong code", func(e *Event) {
			e.Result, e.Level, e.Kind, e.Code = "error", "error", "runtime", string(CodeSessionStateDeleteFailed)
			e.WindowCount, e.PaneCount, e.ShellRecipeCount, e.AgentRecipeCount, e.StartupRecipeCount = nil, nil, nil, nil, nil
		}},
		{"negative count", func(e *Event) { negative := -1; e.WindowCount = &negative }},
		{"delete snapshot count", func(e *Event) { e.Operation = string(OperationSessionStateDelete); e.ItemCount = &one }},
		{"message", func(e *Event) { e.Message = "raw" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := base
			tt.edit(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) succeeded", event)
			}
		})
	}
}

func TestOutcomeEventFamiliesRejectCrossFamilyOperations(t *testing.T) {
	base := Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.start", Result: "started", DurationMS: 0, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionStateSave)}
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("lifecycle.start accepted Session State operation")
	}
	base.Event, base.Result = "lifecycle.outcome", "success"
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("lifecycle.outcome accepted Session State operation")
	}
	zero := 0
	base = Event{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", DurationMS: 0, RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionCreate), Source: string(SessionStateSourceManual), WindowCount: &zero, PaneCount: &zero, ShellRecipeCount: &zero, AgentRecipeCount: &zero, StartupRecipeCount: &zero}
	if _, err := sanitizeEvent(base, ""); err == nil {
		t.Fatal("session-state.outcome accepted runtime lifecycle operation")
	}
}
