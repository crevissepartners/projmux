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
		{"watcher error", func(r *AIRecorder) { r.RecordWatcher(AIResultFailed, AIFailureWatcherLaunch, now, false) }, "ai.watcher.transition", "error", AIKindWatcher, AIResultFailed, AIFailureWatcherLaunch},
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
		recorder.RecordWatcher(AIResultFailed, AIFailureWatcherLaunch, time.Now(), false)
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
	failedPayload := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime", Provider: "codex",
		AIKind: string(AIKindPayload), AIResult: string(AIResultFailed), Failure: string(AIFailurePayloadInvalid),
	}
	ignoredStop := Event{
		At: "2026-08-14T00:00:00Z", Level: "info", Component: "ai", Event: "ai.ingest.outcome", Result: "success",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Provider: "claude",
		AIKind: string(AIKindStop), AIResult: string(AIResultIgnored), Failure: string(AIFailureTargetUnmatched),
	}
	failedRoute := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime", Provider: "antigravity",
		AIKind: string(AIKindInvocation), AIResult: string(AIResultFailed), Failure: string(AIFailureRoute),
	}
	unsupported := Event{
		At: "2026-08-14T00:00:00Z", Level: "info", Component: "ai", Event: "ai.ingest.outcome", Result: "success",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Provider: "codex",
		AIKind: string(AIKindUnknown), AIResult: string(AIResultIgnored), Failure: string(AIFailureUnsupportedEvent),
	}
	watcherFailed := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.watcher.transition", Result: "error",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime", Provider: "ai",
		AIKind: string(AIKindWatcher), AIResult: string(AIResultFailed), Failure: string(AIFailureWatcherLaunch),
	}
	tests := []struct {
		name  string
		event Event
	}{
		{"raw provider", editAIEvent(failedPayload, func(e *Event) { e.Provider = "codex-/private/path" })},
		{"raw event kind", editAIEvent(failedPayload, func(e *Event) { e.AIKind = "Stop-/private/path" })},
		{"raw result", editAIEvent(failedPayload, func(e *Event) { e.AIResult = "ignored-secret" })},
		{"raw failure", editAIEvent(failedPayload, func(e *Event) { e.Failure = "parse: prompt-secret" })},
		{"message", editAIEvent(failedPayload, func(e *Event) { e.Message = "raw hook payload" })},
		{"notify field", editAIEvent(failedPayload, func(e *Event) { e.Route = string(RouteQueue) })},
		{"other provider", editAIEvent(unsupported, func(e *Event) { e.Provider = string(ProviderOther) })},
		{"generic ai ingest provider", editAIEvent(unsupported, func(e *Event) { e.Provider = string(ProviderAI) })},
		{"tmux bell prompt", editAIEvent(ignoredStop, func(e *Event) { e.Provider = string(ProviderTmuxBell); e.AIKind = string(AIKindPrompt) })},
		{"codex bell", editAIEvent(ignoredStop, func(e *Event) { e.Provider = string(ProviderCodex); e.AIKind = string(AIKindBell) })},
		{"codex notification kind", editAIEvent(ignoredStop, func(e *Event) { e.Provider = string(ProviderCodex); e.AIKind = string(AIKindNotification) })},
		{"claude invocation kind", editAIEvent(ignoredStop, func(e *Event) { e.AIKind = string(AIKindInvocation) })},
		{"antigravity notification kind", editAIEvent(failedRoute, func(e *Event) { e.AIKind = string(AIKindNotification) })},
		{"tmux bell payload failure", editAIEvent(failedPayload, func(e *Event) { e.Provider = string(ProviderTmuxBell) })},
		{"payload route failure", editAIEvent(failedPayload, func(e *Event) { e.Failure = string(AIFailureRoute) })},
		{"codex prompt route failure", editAIEvent(failedRoute, func(e *Event) { e.Provider = string(ProviderCodex); e.AIKind = string(AIKindPrompt) })},
		{"claude stop route failure", editAIEvent(failedRoute, func(e *Event) { e.Provider = string(ProviderClaude); e.AIKind = string(AIKindStop) })},
		{"antigravity invocation route failure", failedRoute},
		{"known kind unsupported", editAIEvent(unsupported, func(e *Event) { e.AIKind = string(AIKindPrompt) })},
		{"tmux bell unsupported", editAIEvent(unsupported, func(e *Event) { e.Provider = string(ProviderTmuxBell); e.AIKind = string(AIKindBell) })},
		{"hook target invalid", editAIEvent(ignoredStop, func(e *Event) { e.Failure = string(AIFailureTargetInvalid) })},
		{"payload target unmatched", editAIEvent(ignoredStop, func(e *Event) { e.Provider = string(ProviderCodex); e.AIKind = string(AIKindPayload) })},
		{"ignored route failure", editAIEvent(ignoredStop, func(e *Event) { e.Failure = string(AIFailureRoute) })},
		{"failed target unmatched", editAIEvent(failedRoute, func(e *Event) { e.Failure = string(AIFailureTargetUnmatched) })},
		{"failed without failure", editAIEvent(failedRoute, func(e *Event) { e.Failure = "" })},
		{"watcher provider", editAIEvent(watcherFailed, func(e *Event) { e.Provider = string(ProviderCodex) })},
		{"watcher ingest kind", editAIEvent(watcherFailed, func(e *Event) { e.AIKind = string(AIKindPrompt) })},
		{"watcher route failure", editAIEvent(watcherFailed, func(e *Event) { e.Failure = string(AIFailureRoute) })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sanitizeEvent(tt.event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) accepted unsafe tuple", tt.event)
			}
		})
	}
	raw, _ := json.Marshal(failedPayload)
	for _, forbidden := range []string{"prompt", "transcript", "tool_input", "notification_body", "pane", "cwd", "command", "title", "conversation", "session_id", "/private", "uuid"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("safe AI event leaked %q: %s", forbidden, raw)
		}
	}
}

func TestAISchemaAcceptsProductionProviderKindTuples(t *testing.T) {
	providerKinds := map[Provider][]AIKind{
		ProviderCodex:       {AIKindPrompt, AIKindPermission, AIKindStop, AIKindTool, AIKindSession, AIKindCompact, AIKindUnknown},
		ProviderClaude:      {AIKindPrompt, AIKindPermission, AIKindStop, AIKindNotification, AIKindTool, AIKindSession, AIKindCompact, AIKindSubagent, AIKindTeammate, AIKindLifecycle, AIKindUnknown},
		ProviderAntigravity: {AIKindStop, AIKindStatusline, AIKindInvocation, AIKindTool, AIKindUnknown},
	}
	for provider, kinds := range providerKinds {
		for _, kind := range kinds {
			failure := AIFailureTargetUnmatched
			if kind == AIKindUnknown {
				failure = AIFailureUnsupportedEvent
			}
			event := Event{
				At: "2026-08-14T00:00:00Z", Level: "info", Component: "ai", Event: "ai.ingest.outcome", Result: "success",
				RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Provider: string(provider),
				AIKind: string(kind), AIResult: string(AIResultIgnored), Failure: string(failure),
			}
			if _, err := sanitizeEvent(event, ""); err != nil {
				t.Fatalf("valid %s/%s tuple rejected: %v", provider, kind, err)
			}
		}
		payload := Event{
			At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error", Kind: "runtime",
			RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Provider: string(provider),
			AIKind: string(AIKindPayload), AIResult: string(AIResultFailed), Failure: string(AIFailurePayloadInvalid),
		}
		if _, err := sanitizeEvent(payload, ""); err != nil {
			t.Fatalf("valid %s payload tuple rejected: %v", provider, err)
		}
	}
	bellRoute := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error", Kind: "runtime",
		RunID: "safe-ai", Version: "0.10.0", MuxBackend: "tmux", Provider: string(ProviderTmuxBell),
		AIKind: string(AIKindBell), AIResult: string(AIResultFailed), Failure: string(AIFailureRoute),
	}
	if _, err := sanitizeEvent(bellRoute, ""); err != nil {
		t.Fatalf("valid bell route tuple rejected: %v", err)
	}
	antigravityRoute := bellRoute
	antigravityRoute.Provider = string(ProviderAntigravity)
	antigravityRoute.AIKind = string(AIKindTool)
	if _, err := sanitizeEvent(antigravityRoute, ""); err != nil {
		t.Fatalf("valid Antigravity response route tuple rejected: %v", err)
	}
}

func editAIEvent(base Event, edit func(*Event)) Event {
	edit(&base)
	return base
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
