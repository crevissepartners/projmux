package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
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
		{Source: "claude-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonNoMatch},
		{Source: "claude-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonNoMatch},
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
				{Reason: aiPaneMatchReasonNoMatch, Count: 2},
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

// Refused is how many hook events were declined for naming a Pane that no
// longer exists. Like Attributed, the verdict works per source and this
// aggregate is only ever a test's summary of a fixture.
func (h aiIngestAttributionHealth) Refused() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Refused
	}
	return total
}

// Attributed is how many hook events over the window reached a Pane. The
// verdict is reached per source rather than over this total, so the aggregate
// is only ever a test's summary of a fixture.
func (h aiIngestAttributionHealth) Attributed() int {
	total := 0
	for _, source := range h.Sources {
		total += source.Attributed
	}
	return total
}

// readAIIngestAttributionHealth reads the tail of ai-ingest.log and projects
// attribution health from it in one call.
//
// Doctor takes the tail once and derives all three hook projections from it, so
// this single-projection path exists for the tests that drive the reader end to
// end against a real file: an absent log must report itself unobserved rather
// than as an empty healthy result.
func readAIIngestAttributionHealth(path string) aiIngestAttributionHealth {
	entries, ok := readAIIngestLogTail(path, aiIngestAttributionWindow, aiIngestAttributionRecords)
	if !ok {
		return aiIngestAttributionHealth{}
	}
	return projectAIIngestAttributionHealth(entries)
}

// TestAttributionHealthSeparatesAContractualRefusalFromAFailure is the
// correction that keeps this gate honest.
//
// The contract's scope excludes a Pane that is already gone, and a hook still
// firing from a retired conversation is exactly that: its Agent and Pane were
// cleaned up and the thread kept talking. Refusing it is the mechanism working.
// Counting it as a failure made `doctor` report a machine behaving to spec as
// broken, and a gate that cries wolf gets switched off — which is how the
// defects this whole section exists to catch survived eight phases next door.
//
// What stays a failure is the mechanism itself not answering: an inventory or
// Registry it could not read, or the ladder running over readable data and
// finding nothing, which is the shape a re-broken hook identity would take.
func TestAttributionHealthSeparatesAContractualRefusalFromAFailure(t *testing.T) {
	health := projectAIIngestAttributionHealth([]aiIngestLogEntry{
		// Out of contract: the Pane these name is gone.
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonConversationUnknown},
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonExplicitUnknown},
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonExplicitNoRuntime},
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonExplicitStale},
		// Failures: the mechanism owed an answer and had none.
		{Source: "claude-hook", Result: "ignored", Reason: aiPaneMatchReasonNoInventory},
		{Source: "claude-hook", Result: "ignored", Reason: aiPaneMatchReasonRegistryUnavailable},
		{Source: "claude-hook", Result: "ignored", Reason: aiPaneMatchReasonNoMatch},
		{Source: "claude-hook", Result: "ignored", Reason: aiPaneMatchReasonConversationShared},
	})
	if health.Refused() != 4 || health.Unattributed() != 4 {
		t.Fatalf("refused = %d, unattributed = %d, want 4 and 4", health.Refused(), health.Unattributed())
	}
	for _, source := range health.Sources {
		if source.Source == "codex-hook" && source.Unattributed != 0 {
			t.Fatalf("a contractual refusal was counted as a failure: %+v", source)
		}
		if source.Source == "claude-hook" && source.Refused != 0 {
			t.Fatalf("a mechanism failure was excused as a refusal: %+v", source)
		}
	}
	// A source that only ever refused is not a source that failed.
	refusedOnly := projectAIIngestAttributionHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonConversationUnknown},
	})
	if surface := codexHookAttributionSurface(refusedOnly); surface.Status != codexSurfaceStatusOK {
		t.Fatalf("surface = %q (detail %q), want %q for a retired conversation", surface.Status, surface.Detail, codexSurfaceStatusOK)
	} else if !strings.Contains(surface.Detail, "out of contract") {
		t.Fatalf("detail = %q, want the refusal reported rather than hidden", surface.Detail)
	}
}

// TestAttributionVocabularyCoversEveryDeclaredMatchReason keeps this reader
// from going deaf as the matcher learns new answers.
//
// The reader classifies a record by comparing its reason against a list
// restated here, and a token the list does not know is not counted as anything
// — not a failure, not a refusal. So a new answer added to the matcher would
// quietly shrink both numbers, and the surface would report improvement. That
// is this track's recurring shape once more: a check that does not check its
// own premise. The declarations are read out of the source, so the list cannot
// fall behind them without this failing.
func TestAttributionVocabularyCoversEveryDeclaredMatchReason(t *testing.T) {
	root := repoRootForGate(t)
	path := filepath.Join(root, "internal", "app", "ai_ingest.go")
	payload, err := os.ReadFile(path) // #nosec G304 -- repository source under test.
	if err != nil {
		t.Fatalf("read matcher source: %v", err)
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, payload, 0)
	if err != nil {
		t.Fatalf("parse matcher source: %v", err)
	}
	declared := map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "aiPaneMatchReason") || index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr == nil {
				declared[value] = true
			}
		}
		return true
	})
	if len(declared) == 0 {
		t.Fatal("no attribution reason declarations found; the scan is looking in the wrong place")
	}
	var missing []string
	for value := range declared {
		if !slices.Contains(aiPaneMatchReasons, value) {
			missing = append(missing, value)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d declared attribution reason(s) this reader would silently ignore:\n  %q\n\n"+
			"Add each to aiPaneMatchReasons, and decide whether it is a contractual refusal "+
			"(the Pane it names is gone) or an attribution failure (the mechanism owed an answer).",
			len(missing), missing)
	}
	for _, known := range aiPaneMatchReasons {
		if !declared[known] {
			t.Fatalf("aiPaneMatchReasons carries %q, which the matcher no longer declares", known)
		}
	}
}
