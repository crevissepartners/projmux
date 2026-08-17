package diagnostics

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNotifyFocusRecorderClosedTransitionTable(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		record      func(*NotifyFocusRecorder)
		component   string
		event       string
		transition  Transition
		disposition Disposition
		code        Code
	}{
		{"enqueue", func(r *NotifyFocusRecorder) {
			r.RecordNotify(TransitionNotifyEnqueue, DispositionQueued, ProviderCodex, CategoryResponseComplete, RouteQueue, "", now.Add(-3*time.Millisecond), true)
		}, "notify", "notify.transition", TransitionNotifyEnqueue, DispositionQueued, ""},
		{"deduplicated", func(r *NotifyFocusRecorder) {
			r.RecordNotify(TransitionNotifyEnqueue, DispositionDeduplicated, ProviderClaude, CategoryApprovalRequired, RouteQueue, "", now, true)
		}, "notify", "notify.transition", TransitionNotifyEnqueue, DispositionDeduplicated, ""},
		{"delivery failure", func(r *NotifyFocusRecorder) {
			r.RecordNotify(TransitionNotifyDelivery, DispositionFailed, ProviderAntigravity, CategoryError, RouteNotifySend, CodeNotifyDeliveryUnavailable, now, true)
		}, "notify", "notify.transition", TransitionNotifyDelivery, DispositionFailed, CodeNotifyDeliveryUnavailable},
		{"focus", func(r *NotifyFocusRecorder) {
			r.RecordFocus(DispositionFocused, ProviderProjmux, CategorySegmentClick, RouteFocusQueue, "", now)
		}, "focus", "focus.transition", TransitionFocusRequest, DispositionFocused, ""},
		{"focus failure", func(r *NotifyFocusRecorder) {
			r.RecordFocus(DispositionFailed, ProviderProjmux, CategoryToastClick, RouteFocusToast, CodeFocusPaneFailed, now)
		}, "focus", "focus.transition", TransitionFocusRequest, DispositionFailed, CodeFocusPaneFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &recordingEventWriter{}
			lifecycle := NewLifecycleRecorder(writer, "safe-run", "0.10.0", "tmux")
			recorder := lifecycle.NotifyFocus()
			recorder.now = func() time.Time { return now }
			tt.record(recorder)
			events := writer.snapshot()
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one", events)
			}
			event := events[0]
			if event.Component != tt.component || event.Event != tt.event || event.Transition != string(tt.transition) || event.Disposition != string(tt.disposition) || event.Code != string(tt.code) || event.RunID != "safe-run" {
				t.Fatalf("event = %#v", event)
			}
			if tt.disposition == DispositionFailed {
				if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" {
					t.Fatalf("error shape = %#v", event)
				}
			} else if event.Level != "info" || event.Result != "success" || event.Kind != "" {
				t.Fatalf("success shape = %#v", event)
			}
			if _, err := sanitizeEvent(event, "/seed/home"); err != nil {
				t.Fatalf("sanitizeEvent() error = %v", err)
			}
		})
	}
}

func TestNotifyFocusRecorderCoalescesIdenticalHotPathTuplesPerRun(t *testing.T) {
	writer := &recordingEventWriter{}
	recorder := NewLifecycleRecorder(writer, "bounded-run", "0.10.0", "tmux").NotifyFocus()
	for range 100 {
		recorder.RecordNotify(TransitionNotifyEnqueue, DispositionDeduplicated, ProviderCodex, CategoryResponseComplete, RouteQueue, "", time.Now(), false)
		recorder.RecordNotify(TransitionNotifyDelivery, DispositionSuppressed, ProviderCodex, CategoryResponseComplete, RouteDedupe, "", time.Now(), false)
	}
	events := writer.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want one enqueue dedupe and one delivery suppression", len(events))
	}
}

func TestNotifyFocusSecondaryEventDoesNotOwnUnrelatedTopLevelOutcome(t *testing.T) {
	writer := &recordingEventWriter{}
	lifecycle := NewLifecycleRecorder(writer, "secondary-run", "0.10.0", "tmux")
	recorder := lifecycle.NotifyFocus()
	recorder.RecordNotify(TransitionNotifyEnqueue, DispositionFailed, ProviderAI, CategoryOther, RouteQueue, CodeNotifyEnqueueFailed, time.Now(), false)
	if lifecycle.RecordedOutcome() {
		t.Fatal("secondary automatic notify event claimed the outer command")
	}
	store := NewStore(t.TempDir() + "/operations.jsonl")
	if err := RecordOutcome(store, []string{"internal", "agent-hook", "ingest", "codex-hook"}, "secondary-run", "0.10.0", "tmux", time.Now(), errors.New("unrelated ingest failure"), false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 1 || events[0].Event != "command.outcome" {
		t.Fatalf("top-level events = %#v err=%v, want unrelated command outcome", events, err)
	}
}

func TestNotifyFocusRecorderAppendFailureStillOwnsTopLevel(t *testing.T) {
	writer := &recordingEventWriter{err: errors.New("append denied")}
	lifecycle := NewLifecycleRecorder(writer, "owned-run", "0.10.0", "tmux")
	recorder := lifecycle.NotifyFocus()
	recorder.RecordFocus(DispositionFailed, ProviderProjmux, CategoryRowSelect, RouteFocusQueue, CodeFocusResolveFailed, time.Now())
	if !lifecycle.RecordedOutcome() {
		t.Fatal("logical focus outcome was lost after append failure")
	}
	fallback := NewStore(t.TempDir() + "/operations.jsonl")
	if err := RecordOutcome(fallback, []string{"focus", "--target", "raw-session"}, "owned-run", "0.10.0", "tmux", time.Now(), errors.New("raw failure"), false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	if events, err := fallback.Read(); err != nil || len(events) != 0 {
		t.Fatalf("fallback events = %#v err=%v, want none", events, err)
	}
}

func TestNotifyFocusSchemaRejectsRawAndCrossFamilyFields(t *testing.T) {
	base := Event{
		At: "2026-08-14T00:00:00Z", Level: "info", Component: "notify", Event: "notify.transition", Result: "success",
		RunID: "safe", Version: "0.10.0", MuxBackend: "tmux", Transition: "enqueue", Disposition: "queued",
		Provider: "codex", Category: "response_complete", Route: "queue",
	}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"raw provider", func(e *Event) { e.Provider = "codex /seed/private/path" }},
		{"raw category", func(e *Event) { e.Category = "summary-secret" }},
		{"message", func(e *Event) { e.Message = "raw body" }},
		{"wrong family", func(e *Event) { e.Event, e.Component, e.Transition = "focus.transition", "focus", "enqueue" }},
		{"success code", func(e *Event) { e.Code = string(CodeNotifyEnqueueFailed) }},
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
	raw, _ := json.Marshal(base)
	for _, forbidden := range []string{"summary", "body", "tag", "group", "title", "/seed/private", "uuid"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("safe event leaked %q: %s", forbidden, raw)
		}
	}
}

func TestNotifyFocusSchemaRejectsImpossibleTransitionTuples(t *testing.T) {
	base := Event{
		At: "2026-08-14T00:00:00Z", Level: "info", Component: "notify", Event: "notify.transition", Result: "success",
		RunID: "safe", Version: "0.10.0", MuxBackend: "tmux", Transition: "enqueue", Disposition: "queued",
		Provider: "codex", Category: "response_complete", Route: "queue",
	}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"enqueue with focus disposition", func(e *Event) { e.Disposition = string(DispositionFocused) }},
		{"enqueue with delivery route", func(e *Event) { e.Route = string(RouteHook) }},
		{"delivery with enqueue disposition", func(e *Event) {
			e.Transition, e.Disposition, e.Route = string(TransitionNotifyDelivery), string(DispositionQueued), string(RouteHook)
		}},
		{"delivery suppression on sender", func(e *Event) {
			e.Transition, e.Disposition, e.Route = string(TransitionNotifyDelivery), string(DispositionSuppressed), string(RouteNotifySend)
		}},
		{"delivery success on suppression route", func(e *Event) {
			e.Transition, e.Disposition, e.Route = string(TransitionNotifyDelivery), string(DispositionDelivered), string(RouteDedupe)
		}},
		{"unavailable code on hook route", func(e *Event) {
			e.Level, e.Result, e.Kind = "error", "error", "runtime"
			e.Transition, e.Disposition, e.Route, e.Code = string(TransitionNotifyDelivery), string(DispositionFailed), string(RouteHook), string(CodeNotifyDeliveryUnavailable)
		}},
		{"focus with enqueue disposition", func(e *Event) {
			e.Event, e.Component, e.Transition = "focus.transition", "focus", string(TransitionFocusRequest)
			e.Disposition, e.Route = string(DispositionQueued), string(RouteFocusDirect)
		}},
		{"focus with delivery route", func(e *Event) {
			e.Event, e.Component, e.Transition = "focus.transition", "focus", string(TransitionFocusRequest)
			e.Disposition, e.Route = string(DispositionFocused), string(RouteHook)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := base
			tt.edit(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) accepted impossible tuple", event)
			}
		})
	}
}

func TestLegacyEventFamiliesRejectNotifyFocusFields(t *testing.T) {
	one := 1
	bases := []Event{
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Command: "notify", Subcommand: "push"},
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "runtime", Event: "lifecycle.start", Result: "started", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionSwitch)},
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "session-state", Event: "session-state.outcome", Result: "success", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Operation: string(OperationSessionStateDelete), Source: string(SessionStateSourceManual), ItemCount: &one},
	}
	fields := []struct {
		name string
		set  func(*Event)
	}{
		{"allowlisted transition", func(e *Event) { e.Transition = string(TransitionNotifyEnqueue) }},
		{"allowlisted disposition", func(e *Event) { e.Disposition = string(DispositionQueued) }},
		{"allowlisted provider", func(e *Event) { e.Provider = string(ProviderCodex) }},
		{"allowlisted category", func(e *Event) { e.Category = string(CategoryResponseComplete) }},
		{"allowlisted route", func(e *Event) { e.Route = string(RouteQueue) }},
		{"raw transition", func(e *Event) { e.Transition = "raw-transition" }},
		{"raw disposition", func(e *Event) { e.Disposition = "raw-disposition" }},
		{"raw provider", func(e *Event) { e.Provider = "raw-provider" }},
		{"raw category", func(e *Event) { e.Category = "raw-category" }},
		{"raw route", func(e *Event) { e.Route = "/raw/private/route" }},
	}
	for _, base := range bases {
		for _, field := range fields {
			t.Run(base.Event+"/"+field.name, func(t *testing.T) {
				event := base
				field.set(&event)
				if _, err := sanitizeEvent(event, ""); err == nil {
					t.Fatalf("sanitizeEvent(%#v) accepted cross-family field", event)
				}
			})
		}
	}
}
