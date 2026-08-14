package diagnostics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAIRecorderClosedWatcherAndIngestTable(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		record   func(*AIRecorder)
		event    string
		result   string
		aiKind   AIKind
		aiResult AIResult
		failure  AIFailure
	}{
		{"watcher start", func(r *AIRecorder) { r.RecordWatcher(AIResultStarted, "", now, false) }, "ai.watcher.transition", "started", AIKindWatcher, AIResultStarted, ""},
		{"watcher stop", func(r *AIRecorder) { r.RecordWatcher(AIResultHookActive, "", now, true) }, "ai.watcher.transition", "success", AIKindWatcher, AIResultHookActive, ""},
		{"watcher error", func(r *AIRecorder) { r.RecordWatcher(AIResultFailed, AIFailureWatcherState, now, false) }, "ai.watcher.transition", "error", AIKindWatcher, AIResultFailed, AIFailureWatcherState},
		{"ingest ignored", func(r *AIRecorder) {
			r.RecordIngest(ProviderClaude, AIKindStop, AIResultIgnored, AIFailureTargetUnmatched, now, false)
		}, "ai.ingest.outcome", "success", AIKindStop, AIResultIgnored, AIFailureTargetUnmatched},
		{"ingest error", func(r *AIRecorder) {
			r.RecordIngest(ProviderCodex, AIKindPayload, AIResultFailed, AIFailurePayloadInvalid, now, true)
		}, "ai.ingest.outcome", "error", AIKindPayload, AIResultFailed, AIFailurePayloadInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &recordingEventWriter{}
			recorder := NewLifecycleRecorder(writer, "ai-safe-run", "0.10.0", "tmux").AI()
			recorder.now = func() time.Time { return now }
			tt.record(recorder)
			events := writer.snapshot()
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one", events)
			}
			event := events[0]
			if event.Event != tt.event || event.Result != tt.result || event.AIKind != string(tt.aiKind) || event.AIResult != string(tt.aiResult) || event.Failure != string(tt.failure) || event.RunID != "ai-safe-run" {
				t.Fatalf("event = %#v", event)
			}
			if _, err := sanitizeEvent(event, "/private/home"); err != nil {
				t.Fatalf("sanitizeEvent() error = %v", err)
			}
		})
	}
}

func TestAIRecorderCoalescesWatcherAndIngestHotPathTuples(t *testing.T) {
	writer := &recordingEventWriter{}
	recorder := NewLifecycleRecorder(writer, "bounded-ai", "0.10.0", "tmux").AI()
	for range 100 {
		recorder.RecordWatcher(AIResultFailed, AIFailureWatcherState, time.Now(), false)
		recorder.RecordIngest(ProviderAntigravity, AIKindUnknown, AIResultIgnored, AIFailureUnsupportedEvent, time.Now(), false)
	}
	events := writer.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want one watcher error and one unsupported ingest tuple", len(events))
	}
}

func TestAIRecorderAppendFailureStillOwnsTopLevel(t *testing.T) {
	writer := &recordingEventWriter{err: errors.New("append denied")}
	lifecycle := NewLifecycleRecorder(writer, "owned-ai", "0.10.0", "tmux")
	lifecycle.AI().RecordIngest(ProviderCodex, AIKindPayload, AIResultFailed, AIFailurePayloadInvalid, time.Now(), true)
	if !lifecycle.RecordedOutcome() {
		t.Fatal("logical AI outcome was lost after append failure")
	}
	fallback := NewStore(t.TempDir() + "/operations.jsonl")
	if err := RecordOutcome(fallback, []string{"ai", "ingest", "codex-hook"}, "owned-ai", "0.10.0", "tmux", time.Now(), errors.New("raw payload failure"), false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	if events, err := fallback.Read(); err != nil || len(events) != 0 {
		t.Fatalf("fallback events = %#v err=%v, want no generic duplicate", events, err)
	}
}

func TestAISchemaRejectsRawAndImpossibleTuples(t *testing.T) {
	base := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime", Provider: "codex",
		AIKind: string(AIKindPayload), AIResult: string(AIResultFailed), Failure: string(AIFailurePayloadInvalid),
	}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"raw provider", func(e *Event) { e.Provider = "codex-/private/path" }},
		{"raw event kind", func(e *Event) { e.AIKind = "Stop-/private/path" }},
		{"raw result", func(e *Event) { e.AIResult = "ignored-secret" }},
		{"raw failure", func(e *Event) { e.Failure = "parse: prompt-secret" }},
		{"message", func(e *Event) { e.Message = "raw hook payload" }},
		{"notify field", func(e *Event) { e.Route = string(RouteQueue) }},
		{"ignored as error", func(e *Event) { e.AIResult, e.Failure = string(AIResultIgnored), string(AIFailureTargetUnmatched) }},
		{"watcher provider", func(e *Event) {
			e.Event, e.AIKind = "ai.watcher.transition", string(AIKindWatcher)
			e.Provider = string(ProviderCodex)
			e.AIResult, e.Failure = string(AIResultFailed), string(AIFailureWatcherState)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := base
			tt.edit(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) accepted unsafe tuple", event)
			}
		})
	}
	raw, _ := json.Marshal(base)
	for _, forbidden := range []string{"prompt", "transcript", "tool_input", "notification_body", "pane", "cwd", "command", "title", "conversation", "session_id", "/private", "uuid"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("safe AI event leaked %q: %s", forbidden, raw)
		}
	}
}

func TestLegacyEventFamiliesRejectAIFields(t *testing.T) {
	bases := []Event{
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Command: "ai", Subcommand: "ingest"},
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.start", Result: "started", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionSwitch)},
	}
	fields := []func(*Event){
		func(e *Event) { e.AIKind = string(AIKindStop) },
		func(e *Event) { e.AIResult = string(AIResultIgnored) },
		func(e *Event) { e.Failure = string(AIFailureTargetUnmatched) },
	}
	for _, base := range bases {
		for _, set := range fields {
			event := base
			set(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) accepted cross-family AI field", event)
			}
		}
	}
}
