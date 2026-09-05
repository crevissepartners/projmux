package app

import (
	"strings"
	"testing"
)

// Every count this diagnosis renders has to say what it is a count of.
//
// Four separate corrections were made here in one day, and they were the same
// mistake wearing four faces. Each time the number was arithmetically right and
// what it ranged over was unstated, so a reader supplied the wrong range:
//
//   - a refusal the contract never promised to attribute was counted among the
//     failures it did promise, and a machine behaving to spec read as broken;
//   - a judged count stood without its population, and 805 read as everything
//     when a third of the window had not been judged at all;
//   - a lane that deliberately writes nothing sat in the denominator of a write
//     rate, and one failure in three read as one in twenty-nine;
//   - records written by two different binaries were counted in one window, and
//     a repair that had already landed read as having done nothing.
//
// Each was found by a person, none by a test. This is the test, and what it
// asks of a new surface is the question all four failed to answer: your numbers
// are over what?
//
// It cannot check that a scope is *correct* -- that is the author's judgment,
// and the four above were all authored in good faith. It checks that one was
// declared and that the rendering carries it, which is what turns the question
// from optional into unavoidable.

// codexSurfaceScope names, for each surface, the phrase its rendering must
// carry to say what its counts range over.
//
// Adding a surface without adding a line here fails the completeness check
// below. That is the point: the entry is cheap, and writing it forces the
// author to answer the question before the number reaches an operator.
var codexSurfaceScope = map[string]string{
	// How many endpoints the domain published — the population the reachability
	// verdict was reached over.
	codexSurfaceBrokerDiagnostics: "published endpoints",
	// Split per source, and refusals the contract excludes are named rather
	// than folded into the failures.
	codexSurfaceHookAttribution: "out of contract",
	// Reasons that were captured against reasons that were not.
	codexSurfaceObserverReason: "captured reasons",
	// The reconnect total is over the runtime's life, which the epoch names.
	codexSurfaceConnectionContinuity: "connection epoch",
	// How each snapshot was taken, since only a settled one may be judged.
	codexSurfaceTurnAdmission: "settled",
	// Delivery is over writes that were attempted, not events that arrived.
	codexSurfaceHookDelivery: "made no write",
	// Judged against the whole window, since much of it cannot be judged.
	codexSurfacePaneOwnership: "of",
}

// TestEverySurfaceDeclaresWhatItsNumbersAreOver is the completeness half.
func TestEverySurfaceDeclaresWhatItsNumbersAreOver(t *testing.T) {
	for _, surface := range codexControlPlaneSurfaceOrder {
		if strings.TrimSpace(codexSurfaceScope[surface]) == "" {
			t.Fatalf("surface %q renders counts and declares no scope for them.\n\n"+
				"Say what its numbers range over -- a population, a lane, a time span, a contract "+
				"limit -- and add the phrase its rendering carries. Four verdicts here have already "+
				"been wrong for want of that answer, and every one of them was found by a person.",
				surface)
		}
	}
	for surface := range codexSurfaceScope {
		if _, known := codexControlPlaneSurfaceContract[surface]; !known {
			t.Fatalf("scope declared for %q, which the diagnosis does not render", surface)
		}
	}
}

// TestEverySurfaceRendersTheScopeItDeclares is the rendering half.
//
// A declaration nobody prints is a comment. The verdicts that misled were all
// rendered ones, so the phrase has to reach the line an operator reads.
func TestEverySurfaceRendersTheScopeItDeclares(t *testing.T) {
	report := projectCodexControlPlaneSurfaces(
		&codexBrokerDiagnostic{
			State: codexBrokerStateRunning, Published: 1, Endpoint: "codex-app-server:g1:key",
			ConnectionEpoch: 26500, Reconnects: 26499, Connections: 1,
		},
		&codexAuthorityCensus{
			Agents: 2, Settled: 2,
			Reasons: []codexAuthorityReasonCount{{Reason: string(codexObserverReasonReady), Count: 2}},
		},
		codexHookHealth{
			Attribution: aiIngestAttributionHealth{Observed: true, Records: 3, Sources: []aiIngestAttributionSource{
				{Source: "codex-hook", Attributed: 2, Refused: 1, RefusalReasons: []aiIngestAttributionReason{
					{Reason: aiPaneMatchReasonConversationUnknown, Count: 1},
				}},
			}},
			Delivery: aiIngestDeliveryHealth{Observed: true, Records: 2, Sources: []aiIngestDeliverySource{
				{Source: "codex-hook", Delivered: 2, Quiet: 7},
			}},
			Ownership: aiIngestOwnershipHealth{Observed: true, Classified: 2, Unrecorded: 1},
			From:      "2026-09-05T08:44:30Z",
			To:        "2026-09-05T09:54:28Z",
		},
		codexControlPlaneVintage{Supported: true},
	)
	for _, surface := range report.Surfaces {
		scope := codexSurfaceScope[surface.Surface]
		if surface.Status == codexSurfaceStatusUnobserved {
			t.Fatalf("surface %q read as unobserved on populated input; the fixture no longer exercises it", surface.Surface)
		}
		if !strings.Contains(surface.Detail, scope) {
			t.Fatalf("surface %q renders %q, which does not carry its declared scope %q",
				surface.Surface, surface.Detail, scope)
		}
	}
	// The hook rows share one time span, and it is rendered once above them
	// rather than repeated on each.
	if report.HookWindow == "" {
		t.Fatal("the hook rows carry counts over a time span and the section does not name it")
	}
}
