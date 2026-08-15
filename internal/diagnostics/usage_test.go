package diagnostics

import (
	"errors"
	"testing"
	"time"
)

func TestUsageRecorderClosedOutcomeTable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		provider   Provider
		failure    UsageFailure
		wantLevel  string
		wantResult string
		wantKind   string
	}{
		{"claude hard failure", ProviderClaude, UsageFailureCollect, "error", "error", "runtime"},
		{"codex hard failure", ProviderCodex, UsageFailureCollect, "error", "error", "runtime"},
		{"antigravity rows skipped", ProviderAntigravity, UsageFailureRowsSkipped, "info", "success", ""},
		{"unknown provider projection", ProviderOther, UsageFailureRowsSkipped, "info", "success", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			writer := &recordingEventWriter{}
			recorder := NewUsageRecorder(writer, "usage-safe-run", "0.10.0", MuxBackend())
			recorder.now = func() time.Time { return now.Add(2 * time.Second) }
			recorder.RecordCollectFailure(test.provider, test.failure, now)

			events := writer.snapshot()
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one", events)
			}
			event := events[0]
			if event.Component != "usage" || event.Event != "usage.collect.outcome" {
				t.Fatalf("event family = %q/%q", event.Component, event.Event)
			}
			if event.Provider != string(test.provider) || event.Failure != string(test.failure) {
				t.Fatalf("event tuple = %q/%q", event.Provider, event.Failure)
			}
			if event.Level != test.wantLevel || event.Result != test.wantResult || event.Kind != test.wantKind {
				t.Fatalf("event severity = %q/%q/%q, want %q/%q/%q", event.Level, event.Result, event.Kind, test.wantLevel, test.wantResult, test.wantKind)
			}
			if event.Message != "" || event.Code != "" || event.Operation != "" || event.Source != "" {
				t.Fatalf("event carried free-form or borrowed fields: %#v", event)
			}
			if event.DurationMS != 2000 {
				t.Fatalf("duration = %d, want 2000", event.DurationMS)
			}
			if _, err := sanitizeEvent(event, "/private/home"); err != nil {
				t.Fatalf("sanitizeEvent() error = %v", err)
			}
		})
	}
}

// TestUsageEventShapeRejectsUnsafeVariants pins the closed schema: anything
// outside the (provider, failure) tuple, or a severity that does not match the
// failure class, is refused before it can be written.
func TestUsageEventShapeRejectsUnsafeVariants(t *testing.T) {
	t.Parallel()

	base := func() Event {
		return Event{
			At: "2026-08-15T09:00:00Z", Level: "error", Component: "usage", Event: "usage.collect.outcome",
			Result: "error", DurationMS: 1, RunID: "usage-safe-run", Version: "0.10.0", MuxBackend: "tmux",
			Kind: "runtime", Provider: string(ProviderClaude), Failure: string(UsageFailureCollect),
		}
	}
	if _, err := sanitizeEvent(base(), ""); err != nil {
		t.Fatalf("baseline usage event rejected: %v", err)
	}

	cases := map[string]func(*Event){
		"free-form message":     func(e *Event) { e.Message = "claude: 401 for token sk-ant-xyz" },
		"borrowed code":         func(e *Event) { e.Code = string(CodeSessionCreateFailed) },
		"borrowed operation":    func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"borrowed source":       func(e *Event) { e.Source = string(ResourceSourceSampler) },
		"borrowed route":        func(e *Event) { e.Route = string(RouteQueue) },
		"borrowed ai kind":      func(e *Event) { e.AIKind = string(AIKindPayload) },
		"borrowed counts":       func(e *Event) { count := 1; e.ItemCount = &count },
		"unknown provider":      func(e *Event) { e.Provider = "future-provider" },
		"unknown failure":       func(e *Event) { e.Failure = "something-went-wrong" },
		"missing failure":       func(e *Event) { e.Failure = "" },
		"wrong component":       func(e *Event) { e.Component = "cli" },
		"hard failure as info":  func(e *Event) { e.Level, e.Result, e.Kind = "info", "success", "" },
		"partial as error":      func(e *Event) { e.Failure = string(UsageFailureRowsSkipped) },
		"ai provider not usage": func(e *Event) { e.Provider = string(ProviderTmuxBell) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			event := base()
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent accepted unsafe usage event: %#v", event)
			}
		})
	}
}

// TestUsageRecorderSuppressesRepeatedIdenticalTuples follows the notify/focus
// suppression contract: a persistent failure cannot fill the bounded journal,
// while a genuinely different tuple is still observable.
func TestUsageRecorderSuppressesRepeatedIdenticalTuples(t *testing.T) {
	t.Parallel()

	writer := &recordingEventWriter{}
	recorder := NewUsageRecorder(writer, "bounded-usage", "0.10.0", MuxBackend())
	for range 100 {
		recorder.RecordCollectFailure(ProviderClaude, UsageFailureCollect, time.Now())
	}
	if events := writer.snapshot(); len(events) != 1 {
		t.Fatalf("persistent failure events = %d, want one", len(events))
	}
	recorder.RecordCollectFailure(ProviderClaude, UsageFailureRowsSkipped, time.Now())
	recorder.RecordCollectFailure(ProviderCodex, UsageFailureCollect, time.Now())
	if events := writer.snapshot(); len(events) != 3 {
		t.Fatalf("distinct tuples = %d, want three total", len(events))
	}
}

// TestUsageRecorderAppendFailureIsBestEffort pins the lifecycle rule that a
// failing journal write never propagates into the observed command.
func TestUsageRecorderAppendFailureIsBestEffort(t *testing.T) {
	t.Parallel()

	writer := &recordingEventWriter{err: errors.New("journal unavailable")}
	recorder := NewUsageRecorder(writer, "failing-usage", "0.10.0", MuxBackend())
	recorder.RecordCollectFailure(ProviderClaude, UsageFailureCollect, time.Now())
	if events := writer.snapshot(); len(events) != 1 {
		t.Fatalf("append attempts = %d, want one", len(events))
	}

	var nilRecorder *UsageRecorder
	nilRecorder.RecordCollectFailure(ProviderClaude, UsageFailureCollect, time.Now())

	noWriter := NewUsageRecorder(nil, "no-writer", "0.10.0", MuxBackend())
	noWriter.RecordCollectFailure(ProviderCodex, UsageFailureCollect, time.Now())
}
