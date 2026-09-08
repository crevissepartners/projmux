package codexgeneration

import (
	"strings"
	"testing"
)

// TestQualificationGateRefusesEvidenceCountersNoObservationBacks is the forgery
// fixture. It is deliberately built from a receipt that Validate accepts.
//
// The forgery is not a wrong verdict. A YES verdict is fully determined by the
// acceptance booleans and the violation counters, so recomputing it -- the only
// thing Validate does -- cannot tell a measured YES from a hand-written one.
// What separates them is coverage: a run that never happened observed nothing.
func TestQualificationGateRefusesEvidenceCountersNoObservationBacks(t *testing.T) {
	forged := EvaluateQualification(VersionPair{Old: "0.152.0", New: "0.152.1"}, QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	// Everything the older gate looked at says this receipt qualifies.
	if forged.Verdict != VerdictYes || forged.Reason != ReasonQualified || forged.Validate() != nil {
		t.Fatalf("forgery fixture must be a self-consistent YES: %+v validate=%v", forged, forged.Validate())
	}
	raw, err := forged.JSON()
	if err != nil {
		t.Fatalf("a self-consistent receipt must encode: %v", err)
	}
	if _, err := DecodeQualificationResult(raw); err != nil {
		t.Fatalf("a self-consistent receipt must decode: %v", err)
	}
	gate := GateQualification(forged)
	if gate.Phase2Ready || !gate.EvidenceForged || gate.Lane != FollowupSingleEndpointJournaledHandover {
		t.Fatalf("forged evidence gate=%+v", gate)
	}
	if gate.Discriminant != EvidenceUnbackedThreadCreateTurn || !strings.Contains(gate.Blocker, "unbacked") {
		t.Fatalf("forged refusal must carry a recoverable discriminant: %+v", gate)
	}
}

// TestQualificationEvidenceIntegrityIsFixedByCounterAndClaim pins every
// integrity outcome, so a later evidence field cannot quietly join the schema
// without deciding whether coverage backs it.
func TestQualificationEvidenceIntegrityIsFixedByCounterAndClaim(t *testing.T) {
	measured := QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
		ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
	}
	if got := QualificationEvidenceIntegrity(measured); got != EvidenceIntact {
		t.Fatalf("measured evidence integrity = %q", got)
	}
	for _, test := range []struct {
		name   string
		mutate func(*QualificationEvidence)
		want   string
	}{
		{"turns unobserved", func(e *QualificationEvidence) { e.ObservedThreadTurns = 0 }, EvidenceUnbackedThreadCreateTurn},
		{"reads unobserved", func(e *QualificationEvidence) { e.ObservedThreadReads = 0 }, EvidenceUnbackedThreadReadList},
		{"restarts unobserved", func(e *QualificationEvidence) { e.ObservedCrashRestarts = 0 }, EvidenceUnbackedCrashRestart},
		{"launches unobserved", func(e *QualificationEvidence) { e.ObservedBundleLaunches = 0 }, EvidenceUnbackedBundleSourceLaunch},
		{"negative cross-thread writes", func(e *QualificationEvidence) { e.CrossThreadWrites = -1 }, EvidenceNegativeCrossThreadWrites},
		{"negative store corruptions", func(e *QualificationEvidence) { e.StoreCorruptions = -1 }, EvidenceNegativeStoreCorruptions},
		{"negative resume writes", func(e *QualificationEvidence) { e.LiveOwnerResumeWrites = -1 }, EvidenceNegativeResumeWrites},
		{"negative ambient mutations", func(e *QualificationEvidence) { e.AmbientMutations = -1 }, EvidenceNegativeAmbientMutations},
		{"negative turns", func(e *QualificationEvidence) { e.ObservedThreadTurns = -1 }, EvidenceNegativeThreadTurns},
		{"negative reads", func(e *QualificationEvidence) { e.ObservedThreadReads = -1 }, EvidenceNegativeThreadReads},
		{"negative restarts", func(e *QualificationEvidence) { e.ObservedCrashRestarts = -1 }, EvidenceNegativeCrashRestarts},
		{"negative launches", func(e *QualificationEvidence) { e.ObservedBundleLaunches = -1 }, EvidenceNegativeBundleLaunches},
		// Coverage is only required where the claim is made. A probe that
		// reports failure has nothing to back.
		{"unclaimed probe needs no coverage", func(e *QualificationEvidence) {
			e.CrashRestart, e.ObservedCrashRestarts = false, 0
		}, EvidenceIntact},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := measured
			test.mutate(&evidence)
			if got := QualificationEvidenceIntegrity(evidence); got != test.want {
				t.Fatalf("integrity = %q, want %q", got, test.want)
			}
		})
	}
}

// TestQualificationSchemaVersionRefusesTheCoverageFreeReceipt fixes the reason a
// version-1 receipt cannot be read forward: it has no coverage to check, so
// accepting it would reopen exactly the hole the coverage counters close.
func TestQualificationSchemaVersionRefusesTheCoverageFreeReceipt(t *testing.T) {
	if QualificationSchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", QualificationSchemaVersion)
	}
	legacy := []byte(`{"schemaVersion":1,"versions":{"old":"0.152.0","new":"0.152.1"},"verdict":"no",` +
		`"reason":"distinct-thread-concurrency-failed","evidence":{"sharedStateDomain":false,` +
		`"distinctPrivateEndpoints":false,"distinctThreadCreateTurn":false,"distinctThreadReadList":false,` +
		`"crashRestart":false,"crossThreadWrites":0,"storeCorruptions":0,"liveOwnerResumeWrites":0,` +
		`"oldStoppedBeforeResume":false,"persistedResumeSnapshot":false,"sharedAuthConfigPrivate":false,` +
		`"bundleSourceRemovalLaunch":false,"bundleDriftRefused":false,"protocolMismatchRefused":false,` +
		`"ambientMutations":0}}`)
	if _, err := DecodeQualificationResult(legacy); err == nil {
		t.Fatal("a coverage-free receipt must not decode under the current schema")
	}
}
