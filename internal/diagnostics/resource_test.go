package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResourceRecorderClosedOutcomeTable(t *testing.T) {
	now := time.Date(2026, 8, 14, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		source    ResourceSource
		result    ResourceResult
		failure   ResourceFailure
		wantLevel string
	}{
		{"unavailable", ResourceSourceSampler, ResourceResultUnavailable, ResourceFailureSampleUnavailable, "error"},
		{"partial", ResourceSourceSampler, ResourceResultPartial, ResourceFailureSamplePartial, "info"},
		{"stale", ResourceSourceRefresh, ResourceResultStale, ResourceFailureSampleStale, "info"},
		{"inventory error", ResourceSourceInventory, ResourceResultError, ResourceFailureInventory, "error"},
		{"project discovery error", ResourceSourceProjectDiscovery, ResourceResultError, ResourceFailureProjectDiscovery, "error"},
		{"collection error", ResourceSourceSampler, ResourceResultError, ResourceFailureCollection, "error"},
		{"scan budget", ResourceSourceSampler, ResourceResultScanBudgetExceeded, ResourceFailureScanBudget, "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &recordingEventWriter{}
			recorder := NewLifecycleRecorder(writer, "resource-safe-run", "0.10.0", "tmux").Resource()
			recorder.now = func() time.Time { return now.Add(time.Second) }
			recorder.Record(test.source, test.result, test.failure, now)
			events := writer.snapshot()
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one", events)
			}
			event := events[0]
			if event.Component != "resource" || event.Event != "resource.sampler.outcome" || event.Level != test.wantLevel || event.Source != string(test.source) || event.ResourceResult != string(test.result) || event.Failure != string(test.failure) || event.Message != "" || event.RunID != "resource-safe-run" {
				t.Fatalf("event = %#v", event)
			}
			if _, err := sanitizeEvent(event, "/private/home"); err != nil {
				t.Fatalf("sanitizeEvent() error = %v", err)
			}
		})
	}
}

func TestResourceRecorderCoalescesUntilRecoveryAndReentry(t *testing.T) {
	writer := &recordingEventWriter{}
	recorder := NewLifecycleRecorder(writer, "bounded-resource", "0.10.0", "tmux").Resource()
	for range 100 {
		recorder.Record(ResourceSourceSampler, ResourceResultPartial, ResourceFailureSamplePartial, time.Now())
	}
	if events := writer.snapshot(); len(events) != 1 {
		t.Fatalf("persistent anomaly events = %d, want one", len(events))
	}
	recorder.Healthy()
	for range 100 {
		recorder.Record(ResourceSourceSampler, ResourceResultPartial, ResourceFailureSamplePartial, time.Now())
	}
	if events := writer.snapshot(); len(events) != 2 {
		t.Fatalf("re-entered anomaly events = %d, want one new transition", len(events))
	}
}

func TestResourceRecorderAppendFailureIsBestEffort(t *testing.T) {
	writer := &recordingEventWriter{err: errors.New("append denied")}
	recorder := NewLifecycleRecorder(writer, "resource-append-failure", "0.10.0", "tmux").Resource()
	recorder.Record(ResourceSourceSampler, ResourceResultUnavailable, ResourceFailureSampleUnavailable, time.Now())
	recorder.Record(ResourceSourceSampler, ResourceResultUnavailable, ResourceFailureSampleUnavailable, time.Now())
	if events := writer.snapshot(); len(events) != 1 {
		t.Fatalf("append attempts = %d, want one coalesced best-effort attempt", len(events))
	}
}

func TestResourceSchemaRejectsRawImpossibleAndCrossFamilyFields(t *testing.T) {
	base := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "resource", Event: "resource.sampler.outcome", Result: "error",
		RunID: "safe-resource", Version: "0.10.0", MuxBackend: "tmux", Kind: "runtime", Source: string(ResourceSourceSampler),
		ResourceResult: string(ResourceResultUnavailable), Failure: string(ResourceFailureSampleUnavailable),
	}
	tests := []struct {
		name string
		edit func(*Event)
	}{
		{"raw source", func(e *Event) { e.Source = "/private/project" }},
		{"raw result", func(e *Event) { e.ResourceResult = "partial:pid=123" }},
		{"raw failure", func(e *Event) { e.Failure = "scan failed: secret" }},
		{"message", func(e *Event) { e.Message = "arbitrary collector error" }},
		{"provider", func(e *Event) { e.Provider = string(ProviderOther) }},
		{"ai kind", func(e *Event) { e.AIKind = string(AIKindPayload) }},
		{"inventory unavailable", func(e *Event) { e.Source = string(ResourceSourceInventory) }},
		{"partial error level", func(e *Event) {
			e.ResourceResult = string(ResourceResultPartial)
			e.Failure = string(ResourceFailureSamplePartial)
		}},
		{"budget wrong failure", func(e *Event) { e.ResourceResult = string(ResourceResultScanBudgetExceeded) }},
		{"collection wrong source", func(e *Event) {
			e.ResourceResult = string(ResourceResultError)
			e.Failure = string(ResourceFailureCollection)
			e.Source = string(ResourceSourceRefresh)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			test.edit(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("sanitizeEvent(%#v) accepted unsafe tuple", event)
			}
		})
	}
	raw, _ := json.Marshal(base)
	for _, forbidden := range []string{"pid", "cpu", "memory", "command", "cwd", "title", "pane", "project", "session", "uuid", "/private"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("safe resource event leaked %q: %s", forbidden, raw)
		}
	}
}

func TestLegacyFamiliesRejectResourceFields(t *testing.T) {
	bases := []Event{
		{At: "2026-08-14T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Command: "resources"},
		{At: "2026-08-14T00:00:00Z", Level: "error", Component: "ai", Event: "ai.ingest.outcome", Result: "error", Kind: "runtime", RunID: "run", Version: "0.10.0", MuxBackend: "tmux", Provider: "codex", AIKind: "payload", AIResult: "failed", Failure: "payload-invalid"},
	}
	for _, base := range bases {
		base.ResourceResult = string(ResourceResultPartial)
		if _, err := sanitizeEvent(base, ""); err == nil {
			t.Fatalf("sanitizeEvent(%#v) accepted cross-family resource field", base)
		}
	}
}

func TestResourceEventUsesCommonBoundedRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := Event{
		At: "2026-08-14T00:00:00Z", Level: "error", Component: "resource", Event: "resource.sampler.outcome", Result: "error", Kind: "runtime",
		RunID: "old-resource", Version: "0.10.0", MuxBackend: "tmux", Source: string(ResourceSourceSampler), ResourceResult: string(ResourceResultUnavailable), Failure: string(ResourceFailureSampleUnavailable),
	}
	record, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	record = append(record, '\n')
	if err := os.WriteFile(path, bytes.Repeat(record, MaxLogSize/len(record)+20), 0o600); err != nil {
		t.Fatal(err)
	}
	NewLifecycleRecorder(NewStore(path), "new-resource", "0.10.0", "tmux").Resource().Record(ResourceSourceSampler, ResourceResultPartial, ResourceFailureSamplePartial, time.Now())
	events, err := NewStore(path).Read()
	if err != nil || len(events) == 0 {
		t.Fatalf("rotated events = %#v err=%v", events, err)
	}
	last := events[len(events)-1]
	if last.RunID != "new-resource" || last.ResourceResult != "partial" || last.Failure != "sample-partial" {
		t.Fatalf("last rotated event = %#v", last)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > RetainLogSize+int64(len(record)*2) {
		t.Fatalf("rotated size info=%#v err=%v", info, err)
	}
}
