package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeAttributionLog(t *testing.T, entries []aiIngestLogEntry, padding int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai-ingest.log")
	var builder strings.Builder
	for range padding {
		builder.WriteString(strings.Repeat("x", 512) + "\n")
	}
	for _, entry := range entries {
		payload, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		builder.Write(payload)
		builder.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

// TestAttributionHealthCountsOnlyAttributionOutcomes pins what the number
// means.
//
// A payload error, a quiet event, and an observer transition are not failed
// attributions, and folding them in would move the number that answers whether
// hooks find their Pane. That is the same discipline that separated `no
// matching pane` from `pane inventory unavailable`: a bucket wide enough to
// hold everything answers nothing.
func TestAttributionHealthCountsOnlyAttributionOutcomes(t *testing.T) {
	health := projectAIIngestAttributionHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Event: "Stop", Result: "state", Pane: "%1"},
		{Source: "codex-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonNoInventory},
		{Source: "claude-hook", Event: "Stop", Result: "state", Pane: "%2"},
		{Source: "claude-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonExplicitStale},
		{Source: "claude-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonExplicitStale},
		// Neither of these is an attribution outcome.
		{Source: "codex-hook", Result: "error", Reason: "payload decode failed"},
		{Source: "codex-observer", Event: "observer.disconnected", Result: "invalidating", Reason: "endpoint-suspended", Pane: "%1"},
		// A sibling source that carries its pane through another route.
		{Source: "tmux-bell", Result: "ignored", Reason: aiPaneMatchReasonNoMatch},
	})
	want := aiIngestAttributionHealth{
		Observed: true,
		Records:  5,
		Sources: []aiIngestAttributionSource{
			{Source: "codex-hook", Attributed: 1, Unattributed: 1, Reasons: []aiIngestAttributionReason{
				{Reason: aiPaneMatchReasonNoInventory, Count: 1},
			}},
			{Source: "claude-hook", Attributed: 1, Unattributed: 2, Reasons: []aiIngestAttributionReason{
				{Reason: aiPaneMatchReasonExplicitStale, Count: 2},
			}},
		},
	}
	if !reflect.DeepEqual(health, want) {
		t.Fatalf("health = %+v, want %+v", health, want)
	}
	if health.Attributed() != 2 || health.Unattributed() != 3 {
		t.Fatalf("attributed = %d, unattributed = %d, want 2 and 3", health.Attributed(), health.Unattributed())
	}
}

// TestAttributionHealthSeparatesAnUnreadableLogFromAQuietOne pins that an
// absent log is not a healthy one.
//
// A machine that never ran a hook and a machine whose log this reader cannot
// open produce the same counts, and only the first is good news. Reporting
// both as zero failures is the shape of silence this section replaced.
func TestAttributionHealthSeparatesAnUnreadableLogFromAQuietOne(t *testing.T) {
	missing := readAIIngestAttributionHealth(filepath.Join(t.TempDir(), "absent.log"))
	if missing.Observed {
		t.Fatalf("health = %+v, want an unreadable log reported as unobserved", missing)
	}
	quiet := readAIIngestAttributionHealth(writeAttributionLog(t, nil, 0))
	if !quiet.Observed || quiet.Records != 0 {
		t.Fatalf("health = %+v, want an empty log observed with no records", quiet)
	}
	if surface := codexHookAttributionSurface(missing); surface.Status != codexSurfaceStatusUnobserved {
		t.Fatalf("surface = %q, want %q for an unreadable log", surface.Status, codexSurfaceStatusUnobserved)
	}
}

// TestAttributionHealthReadsTheTailAndDropsThePartialRecord pins the window.
//
// The read is a tail because attribution health is a statement about what hooks
// are doing now; a window reaching the file's first line would keep reporting an
// install-day failure long after it was fixed. A byte-aligned tail almost always
// lands inside a record, and half a record is not one this projection may count.
func TestAttributionHealthReadsTheTailAndDropsThePartialRecord(t *testing.T) {
	entries := make([]aiIngestLogEntry, 0, 64)
	for range 64 {
		entries = append(entries, aiIngestLogEntry{Source: "codex-hook", Event: "Stop", Result: "state", Pane: "%1"})
	}
	path := writeAttributionLog(t, entries, 64)
	all, ok := readAIIngestLogTail(path, 1<<20, 4096)
	if !ok || len(all) != 64 {
		t.Fatalf("full read = %d records (ok=%v), want 64", len(all), ok)
	}
	windowed, ok := readAIIngestLogTail(path, 2<<10, 4096)
	if !ok {
		t.Fatal("windowed read failed")
	}
	if len(windowed) == 0 || len(windowed) >= 64 {
		t.Fatalf("windowed read = %d records, want a strict tail of the 64", len(windowed))
	}
	for _, entry := range windowed {
		if entry.Source != "codex-hook" || entry.Pane != "%1" {
			t.Fatalf("windowed read produced a partial record: %+v", entry)
		}
	}
}

// TestAttributionHealthIgnoresAReasonOutsideTheClosedMatchVocabulary pins that
// the failure tally is bounded by the vocabulary the matcher actually speaks,
// so a free-text reason cannot enter the diagnosis through this reader.
func TestAttributionHealthIgnoresAReasonOutsideTheClosedMatchVocabulary(t *testing.T) {
	health := projectAIIngestAttributionHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Result: "ignored", Reason: "/home/user/secret/rollout.jsonl"},
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonRegistryUnavailable},
	})
	if health.Unattributed() != 1 {
		t.Fatalf("unattributed = %d, want only the closed-vocabulary failure counted", health.Unattributed())
	}
	for _, source := range health.Sources {
		for _, reason := range source.Reasons {
			if reason.Reason != aiPaneMatchReasonRegistryUnavailable {
				t.Fatalf("reason %q reached the diagnosis from outside the closed vocabulary", reason.Reason)
			}
		}
	}
}
