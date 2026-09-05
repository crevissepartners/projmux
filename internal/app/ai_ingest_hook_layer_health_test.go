package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestDeliveryHealthCountsOnlyAttributedEvents keeps the two hook layers apart.
//
// An event that never found its Pane failed at attribution and cannot also have
// failed to change one. Folding the two together would report one defect twice
// and make the delivery number move whenever attribution did.
func TestDeliveryHealthCountsOnlyAttributedEvents(t *testing.T) {
	delivery := projectAIIngestDeliveryHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Event: "Stop", Result: "state", Pane: "%1"},
		{Source: "codex-hook", Event: "Stop", Result: "error", Reason: "exit status 1", Pane: "%1"},
		{Source: "codex-hook", Event: "Stop", Result: "error", Reason: "pane option write refused", Pane: "%1"},
		// Attribution failures belong to the other layer.
		{Source: "codex-hook", Event: "Stop", Result: "ignored", Reason: aiPaneMatchReasonNoInventory},
		// A payload error that never reached a Pane is neither layer.
		{Source: "codex-hook", Result: "error", Reason: "payload decode failed"},
		// An observer transition is not a hook event.
		{Source: "codex-observer", Result: "invalidating", Reason: "endpoint-suspended", Pane: "%1"},
	})
	if delivery.Records != 3 {
		t.Fatalf("records = %d, want the 3 attributed hook events", delivery.Records)
	}
	if delivery.Sources[0].Quiet != 0 {
		t.Fatalf("quiet = %d, want none in this fixture", delivery.Sources[0].Quiet)
	}
	if delivery.Failed() != 2 || delivery.Opaque() != 1 {
		t.Fatalf("failed = %d, opaque = %d, want 2 and 1", delivery.Failed(), delivery.Opaque())
	}
}

// TestOpaqueDeliveryReasonsNeverReachTheDiagnosis pins both halves of what
// "opaque" buys.
//
// A raw process-exit string names a process that ended and nothing else, so it
// is counted rather than repeated. A reason carrying a path is opaque for a
// second reason as well: this track's change boundary keeps paths out of these
// records, so one appearing here is a leak as much as a non-answer, and
// rendering it would carry the leak onto a diagnostics surface.
func TestOpaqueDeliveryReasonsNeverReachTheDiagnosis(t *testing.T) {
	for _, test := range []struct {
		reason string
		opaque bool
	}{
		{reason: "exit status 1", opaque: true},
		{reason: "exit status 127", opaque: true},
		{reason: "signal: killed", opaque: true},
		{reason: "", opaque: true},
		{reason: "open /home/user/.codex/config.toml: no such file", opaque: true},
		{reason: "pane option write refused"},
		{reason: "tmux-socket-unavailable"},
		// The three shapes the reflection layer's own vocabulary takes. They
		// are listed as shapes, not as a binding: the gate that requires a
		// failure reason to be one of those tokens has to reference the
		// declared constants, or a rename walks past it. What is pinned here
		// is only that a bounded kebab-or-prose token of this shape is not
		// mistaken for a raw process detail, so the layer's own vocabulary
		// cannot trip the opaque verdict the moment it arrives.
		{reason: "pane runtime route unavailable"},
		{reason: "pane runtime route does not hold this pane"},
		{reason: "pane option write rejected"},
	} {
		if got := aiIngestReasonIsOpaque(test.reason); got != test.opaque {
			t.Fatalf("aiIngestReasonIsOpaque(%q) = %v, want %v", test.reason, got, test.opaque)
		}
	}
	delivery := projectAIIngestDeliveryHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Result: "error", Reason: "exit status 1", Pane: "%1"},
		{Source: "codex-hook", Result: "error", Reason: "open /home/user/secret/rollout.jsonl: denied", Pane: "%1"},
	})
	for _, source := range delivery.Sources {
		for _, reason := range source.Reasons {
			if reason.Reason != aiIngestOpaqueDeliveryReason {
				t.Fatalf("reason %q reached the diagnosis verbatim", reason.Reason)
			}
		}
	}
	if delivery.Opaque() != 2 {
		t.Fatalf("opaque = %d, want both counted", delivery.Opaque())
	}
}

// TestHookDeliverySurfaceTreatsAnOpaqueFailureAsBroken is the ⑥ verdict.
//
// This is the property the reflection layer owes regardless of which tokens its
// vocabulary ends up carrying: a write that cannot happen leaves an answer. A
// failure with a bounded reason is a fault an operator acts on; one reporting
// only that a process exited is the state this surface was opened on.
func TestHookDeliverySurfaceTreatsAnOpaqueFailureAsBroken(t *testing.T) {
	for _, test := range []struct {
		name       string
		delivery   aiIngestDeliveryHealth
		wantStatus string
	}{
		{
			name:       "no readable log is unobserved, not healthy",
			delivery:   aiIngestDeliveryHealth{},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name:       "no attributed event answers nothing",
			delivery:   aiIngestDeliveryHealth{Observed: true},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name: "a failure that answers nothing is broken",
			delivery: aiIngestDeliveryHealth{Observed: true, Records: 60, Sources: []aiIngestDeliverySource{
				{Source: "codex-hook", Delivered: 52, Failed: 8, Opaque: 8, Reasons: []aiIngestAttributionReason{
					{Reason: aiIngestOpaqueDeliveryReason, Count: 8},
				}},
			}},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name: "a failure that names its cause is a degradation",
			delivery: aiIngestDeliveryHealth{Observed: true, Records: 60, Sources: []aiIngestDeliverySource{
				{Source: "codex-hook", Delivered: 52, Failed: 8, Reasons: []aiIngestAttributionReason{
					{Reason: "pane option write refused", Count: 8},
				}},
			}},
			wantStatus: codexSurfaceStatusDegraded,
		},
		{
			name: "every attributed event landing is healthy",
			delivery: aiIngestDeliveryHealth{Observed: true, Records: 23, Sources: []aiIngestDeliverySource{
				{Source: "codex-hook", Delivered: 23},
			}},
			wantStatus: codexSurfaceStatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexHookDeliverySurface(test.delivery)
			if surface.Surface != codexSurfaceHookDelivery {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfaceHookDelivery)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
		})
	}
}

func paneWithActivation(runtimeID string, activation coremetadata.PaneActivation) coremetadata.Pane {
	activation.RuntimeID = runtimeID
	return coremetadata.Pane{Status: coremetadata.PaneStatus{Activation: activation}}
}

// TestOwnershipHealthCallsOnlyAPositivelyForeignPaneForeign pins the predicate.
//
// The judgment is one-sided on purpose. An attribution is foreign only when the
// Registry positively records a different provider for that Pane; a Pane that
// records no provider is not a contradiction. Calling it one would turn every
// ordinary shell Pane into a finding and bury the case that matters, and it
// would break the neutrality the attribution contract is written under — which
// is also why the predicate is a table rather than a branch on one provider.
func TestOwnershipHealthCallsOnlyAPositivelyForeignPaneForeign(t *testing.T) {
	registry := coremetadata.Registry{Panes: []coremetadata.Pane{
		paneWithActivation("%1", coremetadata.PaneActivation{Codex: &coremetadata.CodexActivationBinding{ThreadID: "t"}}),
		paneWithActivation("%2", coremetadata.PaneActivation{Claude: &coremetadata.ClaudeActivationBinding{}}),
		paneWithActivation("%3", coremetadata.PaneActivation{}),
	}}
	ownership := projectAIIngestOwnershipHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Result: "state", Pane: "%1"},
		{Source: "claude-hook", Result: "state", Pane: "%2"},
		// The latent leak: a Codex hook landing on a Pane the Registry records
		// as Claude's.
		{Source: "codex-hook", Result: "state", Pane: "%2"},
		// And the mirror, so the judgment cannot be one provider's special case.
		{Source: "claude-hook", Result: "state", Pane: "%1"},
		// A Pane recording no provider is not another provider.
		{Source: "codex-hook", Result: "state", Pane: "%3"},
		// A Pane the Registry no longer holds is outside the contract.
		{Source: "codex-hook", Result: "state", Pane: "%99"},
		// A source with no provider binding cannot be judged either way.
		{Source: "antigravity-hook", Result: "state", Pane: "%2"},
	}, registry, true)
	if ownership.Foreign != 2 {
		t.Fatalf("foreign = %d, want both directions of the mismatch counted", ownership.Foreign)
	}
	if ownership.Classified != 4 {
		t.Fatalf("classified = %d, want the four judgeable attributions", ownership.Classified)
	}
	if ownership.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want the attribution to a Pane the Registry lost", ownership.Unresolved)
	}
	// The verdict may not certify past what it can judge. A Pane the Registry
	// holds without a provider resolves the old way, so an attribution landing
	// there is neither proven right nor proven wrong, and a zero foreign count
	// beside a hidden pile of these would read as a guarantee nobody gave.
	if ownership.Unrecorded != 1 {
		t.Fatalf("unrecorded = %d, want the attribution onto a Pane recording no provider counted apart", ownership.Unrecorded)
	}
	surface := codexPaneOwnershipSurface(ownership)
	if !strings.Contains(surface.Detail, "provider unrecorded 1") {
		t.Fatalf("detail = %q, want the unjudgeable attribution visible beside the verdict", surface.Detail)
	}
	// The denominator has to lead. A judged count standing alone reads as the
	// whole population, and on a live machine more than a third of the window
	// has fallen outside it.
	if !strings.Contains(surface.Detail, "judged 4 of 6 attributions") {
		t.Fatalf("detail = %q, want the judged count carried against the whole window", surface.Detail)
	}
	// The two directions have different causes and must stay separable: one is
	// a hook landing on the Pane that launched its host, the other an event
	// that arrived under the wrong source before attribution ran.
	directions := map[string]int{}
	for _, direction := range ownership.Directions {
		directions[direction.Reason] = direction.Count
	}
	if directions["codex-hook onto claude"] != 1 || directions["claude-hook onto codex"] != 1 {
		t.Fatalf("directions = %+v, want each mismatch reported by which way it runs", ownership.Directions)
	}
	unread := projectAIIngestOwnershipHealth(nil, coremetadata.Registry{}, false)
	if unread.Observed {
		t.Fatal("an unreadable log and Registry pair reported itself as observed")
	}
}

// TestPaneOwnershipSurfaceReportsAForeignAttributionAsBroken is the ⑦ verdict.
//
// A foreign attribution looks like success from every other angle: the event
// was attributed, the write landed, and the Pane it moved belongs to someone
// else. No other surface in this diagnosis can see it.
func TestPaneOwnershipSurfaceReportsAForeignAttributionAsBroken(t *testing.T) {
	for _, test := range []struct {
		name       string
		ownership  aiIngestOwnershipHealth
		wantStatus string
	}{
		{
			name:       "no log and Registry pair is unobserved, not healthy",
			ownership:  aiIngestOwnershipHealth{},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name:       "nothing judgeable answers nothing",
			ownership:  aiIngestOwnershipHealth{Observed: true, Unresolved: 4},
			wantStatus: codexSurfaceStatusUnobserved,
		},
		{
			name:       "one foreign attribution is broken",
			ownership:  aiIngestOwnershipHealth{Observed: true, Classified: 23, Foreign: 1},
			wantStatus: codexSurfaceStatusBroken,
		},
		{
			name:       "every attribution on its own provider is healthy",
			ownership:  aiIngestOwnershipHealth{Observed: true, Classified: 23},
			wantStatus: codexSurfaceStatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := codexPaneOwnershipSurface(test.ownership)
			if surface.Surface != codexSurfacePaneOwnership {
				t.Fatalf("surface = %q, want %q", surface.Surface, codexSurfacePaneOwnership)
			}
			if surface.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", surface.Status, test.wantStatus, surface.Detail)
			}
			if strings.TrimSpace(surface.Detail) == "" {
				t.Fatal("a verdict with no evidence behind it")
			}
		})
	}
}

// PathBearing is how many delivery failures carry a filesystem path in their
// explanation. The surface renders the count per source, so this aggregate is
// only ever a test's summary of a fixture.
func (h aiIngestDeliveryHealth) PathBearing() int {
	total := 0
	for _, source := range h.Sources {
		total += source.PathBearing
	}
	return total
}

// TestDeliveryHealthKeepsTheQuietLaneOutOfTheRate is the denominator gate.
//
// A quiet event reached its Pane and deliberately wrote nothing, so it can
// neither succeed nor fail at delivery. On a live machine that lane was 989 of
// 1028 attributed records, and counting it as delivered turned one failure out
// of three real write attempts into one out of twenty-nine — a path that fails
// a third of the time reading as a healthy path with an incident. Widening a
// denominator until the rate disappears is a shape this diagnosis has met more
// than once, and the fix each time was to name what the number is over.
func TestDeliveryHealthKeepsTheQuietLaneOutOfTheRate(t *testing.T) {
	entries := []aiIngestLogEntry{
		{Source: "codex-hook", Event: "Stop", Result: "error", Reason: "exit status 1", Pane: "%1"},
		{Source: "codex-hook", Event: "SessionStart", Result: "state", Pane: "%1"},
		{Source: "codex-hook", Event: "PreCompact", Result: "notify", Pane: "%1"},
	}
	for range 82 {
		entries = append(entries, aiIngestLogEntry{Source: "codex-hook", Event: "PreToolUse", Result: "quiet", Pane: "%1"})
	}
	delivery := projectAIIngestDeliveryHealth(entries)
	source := delivery.Sources[0]
	if source.Delivered != 2 || source.Failed != 1 || source.Quiet != 82 {
		t.Fatalf("delivered=%d failed=%d quiet=%d, want 2/1/82", source.Delivered, source.Failed, source.Quiet)
	}
	if delivery.Records != 3 {
		t.Fatalf("records = %d, want only the write attempts", delivery.Records)
	}
	surface := codexHookDeliverySurface(delivery)
	if !strings.Contains(surface.Detail, "82 made no write") {
		t.Fatalf("detail = %q, want the quiet lane reported beside the rate", surface.Detail)
	}
	if strings.Contains(surface.Detail, "84 delivered") {
		t.Fatalf("detail = %q, want the quiet lane out of the delivered count", surface.Detail)
	}
	// A window in which nothing attempted a write answers nothing about
	// delivery, and must not answer well.
	onlyQuiet := projectAIIngestDeliveryHealth([]aiIngestLogEntry{
		{Source: "codex-hook", Result: "quiet", Pane: "%1"},
	})
	if got := codexHookDeliverySurface(onlyQuiet); got.Status != codexSurfaceStatusUnobserved {
		t.Fatalf("status = %q (detail %q), want %q when no write was attempted", got.Status, got.Detail, codexSurfaceStatusUnobserved)
	}
}

// TestPaneOwnershipVerdictIgnoresForeignLifecycleChurn pins that neither
// unjudgeable count can move the verdict on its own.
//
// Both counts change for reasons this track does not cause. A Pane recording
// no provider is an ordinary Pane; a Pane the Registry no longer holds is one
// somebody else retired — on this machine a neighbouring track reclaimed a
// worker and a single Pane carrying 134 records in the window went absent
// between two readings of unchanged code, taking the unresolved count from
// zero to two hundred odd. A row that reddens and greens on a neighbour's
// housekeeping is a signal nobody can act on, and this diagnosis has already
// had to learn once what an unactionable red costs.
func TestPaneOwnershipVerdictIgnoresForeignLifecycleChurn(t *testing.T) {
	settled := aiIngestOwnershipHealth{Observed: true, Classified: 805}
	churned := aiIngestOwnershipHealth{Observed: true, Classified: 559, Unresolved: 234, Unrecorded: 235}
	if before, after := codexPaneOwnershipSurface(settled), codexPaneOwnershipSurface(churned); before.Status != after.Status {
		t.Fatalf("verdict moved from %q to %q on churn alone (%q → %q)",
			before.Status, after.Status, before.Detail, after.Detail)
	}
	// It must still be visible, or the verdict silently narrows.
	detail := codexPaneOwnershipSurface(churned).Detail
	for _, want := range []string{"judged 559 of 1028", "unresolved 234", "provider unrecorded 235"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail = %q, want it to carry %q", detail, want)
		}
	}
	// A foreign attribution is the one thing that does move it.
	foreign := churned
	foreign.Foreign = 1
	if got := codexPaneOwnershipSurface(foreign); got.Status != codexSurfaceStatusBroken {
		t.Fatalf("status = %q, want %q once an attribution is provably foreign", got.Status, codexSurfaceStatusBroken)
	}
}
