package app

import (
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Every count these projections render is a bucket of some population, and the
// buckets have to partition it.
//
// That is true today by construction: each classifier sends one record down
// exactly one branch and returns, and no rendered number is derived by
// subtracting one count from another. But true-by-construction is not held --
// it is a property of the code as currently written, and the next person to add
// a bucket or a subtraction breaks it silently, because a miscounted total
// still looks like a total.
//
// The failure mode is not hypothetical. Two instrumentation miscounts appeared
// on this track in one day, both of the same shape: a summary line counted as a
// finding, and lines counted as records. Both were in shell around the gates
// rather than inside them, and nothing stops the next one being inside.
//
// So the invariant is asserted rather than noted, and the assertion is checked
// against a fixture that exercises every branch of every classifier. A bucket
// that starts double-counting fails here instead of quietly inflating a
// denominator.

// partitionFixture drives every branch of the three hook projections at once.
func partitionFixture() []aiIngestLogEntry {
	const owned = "conversation-owned"
	return []aiIngestLogEntry{
		// Attributed, and delivered by three different results.
		{Source: "codex-hook", Result: "state", Pane: "%1", ThreadID: owned},
		{Source: "codex-hook", Result: "notify", Pane: "%1", ThreadID: owned},
		{Source: "codex-hook", Result: "quiet", Pane: "%1", ThreadID: owned},
		// Attributed and failed, once opaquely and once with a bounded reason.
		{Source: "codex-hook", Result: "error", Reason: "exit status 1", Pane: "%1", ThreadID: owned},
		{Source: "codex-hook", Result: "error", Reason: "pane option write rejected: -S//tmp/s: no server", Pane: "%1", ThreadID: owned},
		// Unattributed: one mechanism failure, one contractual refusal.
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonNoInventory, ThreadID: owned},
		{Source: "codex-hook", Result: "ignored", Reason: aiPaneMatchReasonConversationUnknown, ThreadID: owned},
		// Another provider: an ordinary record, and one carrying the first
		// provider's conversation.
		{Source: "claude-hook", Result: "state", Pane: "%2", ThreadID: "conversation-claude"},
		{Source: "claude-hook", Result: "state", Pane: "%1", SessionID: owned},
		// Neither an attribution nor a delivery outcome.
		{Source: "codex-hook", Result: "error", Reason: "payload decode failed"},
		{Source: "codex-observer", Result: "invalidating", Reason: "endpoint-suspended", Pane: "%1"},
	}
}

// TestHookProjectionBucketsPartitionTheirPopulation asserts the invariant.
func TestHookProjectionBucketsPartitionTheirPopulation(t *testing.T) {
	entries := partitionFixture()

	attribution := projectAIIngestAttributionHealth(entries)
	attributionTotal := 0
	for _, source := range attribution.Sources {
		if source.Attributed < 0 || source.Unattributed < 0 || source.Refused < 0 {
			t.Fatalf("attribution source %+v carries a negative bucket", source)
		}
		attributionTotal += source.Attributed + source.Unattributed + source.Refused
	}
	if attributionTotal != attribution.Records {
		t.Fatalf("attribution buckets sum to %d over %d records; every counted record must land in exactly one",
			attributionTotal, attribution.Records)
	}

	delivery := projectAIIngestDeliveryHealth(entries)
	deliveryTotal := 0
	for _, source := range delivery.Sources {
		if source.Delivered < 0 || source.Failed < 0 || source.Quiet < 0 || source.Opaque < 0 {
			t.Fatalf("delivery source %+v carries a negative bucket", source)
		}
		if source.Opaque > source.Failed {
			t.Fatalf("delivery source %+v reports more opaque failures than failures", source)
		}
		if source.PathBearing > source.Delivered+source.Failed+source.Quiet {
			t.Fatalf("delivery source %+v reports more path-bearing records than it saw", source)
		}
		deliveryTotal += source.Delivered + source.Failed
	}
	if deliveryTotal != delivery.Records {
		t.Fatalf("delivery buckets sum to %d over %d write attempts; the quiet lane must stay outside both",
			deliveryTotal, delivery.Records)
	}

	registry := registryForPartition()
	ownership := projectAIIngestOwnershipHealth(entries, registry, true)
	if ownership.Foreign > ownership.Classified {
		t.Fatalf("ownership reports %d foreign among %d judged", ownership.Foreign, ownership.Classified)
	}
	if ownership.Classified < 0 || ownership.Unresolved < 0 || ownership.Unrecorded < 0 || ownership.Misrouted < 0 {
		t.Fatalf("ownership carries a negative bucket: %+v", ownership)
	}
	// Misroute is a different axis with its own denominator, so it is checked
	// against that rather than against the attribution population.
	if ownership.Misrouted > 0 && ownership.OwnedConversations == 0 {
		t.Fatalf("ownership found %d misrouted record(s) over no conversation it could check", ownership.Misrouted)
	}
}

// TestAuthorityCensusBucketsPartitionTheirPopulation holds the same property
// for the census, whose population is the managed Agents rather than records.
func TestAuthorityCensusBucketsPartitionTheirPopulation(t *testing.T) {
	diagnostics := map[string]codexLifecycleAuthorityDiagnostic{
		"pane-settled":   {Source: codexAuthorityControlPlane, Epoch: "1-1", Fence: codexAuthorityFenceSettled},
		"pane-torn":      {Source: codexAuthorityControlPlane, Fence: codexAuthorityFenceSettled, Torn: true},
		"pane-contended": {Source: codexAuthorityControlPlane, Fence: codexAuthorityFenceContended},
		"pane-unfenced":  {Source: codexAuthorityHook, Fence: codexAuthorityFenceUnfenced},
	}
	registry := registryWithCodexPanes(t, "pane-settled", "pane-torn", "pane-contended", "pane-unfenced")
	census := censusCodexLifecycleAuthority(registry, func(pane string) codexLifecycleAuthorityDiagnostic {
		return diagnostics[pane]
	})
	if sampled := census.Settled + census.Contended + census.Unfenced; sampled > census.Agents {
		t.Fatalf("census classified %d snapshots over %d Agents", sampled, census.Agents)
	}
	if census.Torn > census.Settled {
		t.Fatalf("census reports %d torn among %d settled; only a settled snapshot can be judged torn",
			census.Torn, census.Settled)
	}
	sources := census.ControlPlane + census.Pending + census.Invalidating +
		census.DeclaredHook + census.UnexplainedHook + census.Unavailable
	if sources != census.Agents {
		t.Fatalf("authority source buckets sum to %d over %d Agents", sources, census.Agents)
	}
}

func registryForPartition() coremetadata.Registry {
	return coremetadata.Registry{Panes: []coremetadata.Pane{
		paneWithActivation("%1", coremetadata.PaneActivation{Codex: &coremetadata.CodexActivationBinding{ThreadID: "conversation-owned"}}),
		paneWithActivation("%2", coremetadata.PaneActivation{Claude: &coremetadata.ClaudeActivationBinding{}}),
	}}
}
