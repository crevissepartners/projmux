package app

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestDoctorReplacementL3RestorationMovesWhenTheQualificationLaneOpens is
// acceptance 6, and it is a transition rather than a state.
//
// The L3 row's restoration axis has read `not-restorable` for every pool that
// entered draining without a verdict, and until this Phase there was no
// sequence of shipped commands that moved it: the producer wrote a receipt and
// no entry path read one. This walks the pool through the three states the lane
// actually passes through and pins the axis and the reason token at each, so a
// later change that closes the lane again shows up here as a row that stopped
// moving rather than as a silent regression.
func TestDoctorReplacementL3RestorationMovesWhenTheQualificationLaneOpens(t *testing.T) {
	t.Parallel()

	census := doctorProviderSessionCensus{Observed: 1, Running: 1, Live: 1}
	qualified := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
			ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
		})
	generations := []doctorCodexGeneration{
		{GenerationID: "codex-0.152.1", State: codexgeneration.StateDraining},
		{GenerationID: "codex-0.153.0", State: codexgeneration.StateCurrent},
	}

	// Before the pool exists at all: nothing has been observed, which is not
	// the same as a route that is known not to exist.
	absent := projectDoctorReplacementProviderRow(
		&doctorCodexGenerationPool{Status: "absent", Reason: "generation-pool-not-installed"}, census)
	if absent.Restoration != doctorRestorationUnknown || absent.Reason != doctorReplacementReasonPoolNotInstalled {
		t.Fatalf("absent pool row = %+v", absent)
	}

	// Draining with no receipt: the state 2026-09-07 proved has no way back.
	blocked := projectDoctorReplacementProviderRow(&doctorCodexGenerationPool{
		Status: "blocked", Reason: "qualification-missing",
		Action: "run-isolated-version-pair-qualification", Generations: generations,
	}, census)
	if blocked.Restoration != doctorRestorationNotRestorable || blocked.Reason != doctorReplacementReasonQualification {
		t.Fatalf("unqualified pool row = %+v", blocked)
	}
	// C-3: the refusal names the action that resolves it, and it is the same
	// action the entry paths' refusals name.
	if !signalsContain(blocked, doctorReplacementSignalPoolAction, "run-isolated-version-pair-qualification") {
		t.Fatalf("unqualified row carries no recoverable action: %+v", blocked.Signals)
	}

	// After the receipt is produced and installed, the same pool reads back as
	// restorable and the row publishes the verdict it is standing on.
	open := projectDoctorReplacementProviderRow(&doctorCodexGenerationPool{
		Status: "ready", Reason: "qualified", Generations: generations, Qualification: &qualified,
	}, census)
	if open.Restoration != doctorRestorationRestorable || open.Reason != doctorReplacementReasonPoolReady {
		t.Fatalf("qualified pool row = %+v", open)
	}
	if !signalsContain(open, doctorReplacementSignalQualVerdict, string(codexgeneration.VerdictYes)) ||
		!signalsContain(open, doctorReplacementSignalQualReason, string(codexgeneration.ReasonQualified)) {
		t.Fatalf("qualified row does not publish its verdict: %+v", open.Signals)
	}
	if blocked.Restoration == open.Restoration {
		t.Fatal("the restoration axis did not move when the lane opened")
	}
}

// TestDoctorReplacementL3SignalsStayClosedTokensAfterTheSchemaBump keeps the new
// evidence counters out of the report surface. The row publishes the verdict and
// reason tokens and nothing else from the receipt, which is what lets C-3 promise
// a discriminant without promising to print measurements.
func TestDoctorReplacementL3SignalsStayClosedTokensAfterTheSchemaBump(t *testing.T) {
	t.Parallel()

	qualified := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.1", New: "0.153.0"},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
			ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
		})
	row := projectDoctorReplacementProviderRow(&doctorCodexGenerationPool{
		Status: "ready", Reason: "qualified", Qualification: &qualified,
	}, doctorProviderSessionCensus{Observed: 1, Running: 1, Live: 1})
	for _, signal := range row.Signals {
		if strings.Contains(signal.Key, "observed") && strings.Contains(signal.Key, "hread") {
			t.Fatalf("coverage counter leaked onto the report surface: %+v", signal)
		}
	}
}

func signalsContain(row doctorReplacementRow, key, value string) bool {
	for _, signal := range row.Signals {
		if signal.Key == key && signal.Value == value {
			return true
		}
	}
	return false
}

// TestDoctorGenerationPoolAgreesWithTheGateOnForgedEvidence keeps the report and
// the gate from disagreeing.
//
// This was found by running the isolated demonstration rather than by reading
// the code: a pool holding a receipt whose coverage counters back nothing read
// back as `generation-pool-ready` while both entry paths refused it. A surface
// that says the lane is open where the doors say it is shut is worse than
// silence, because the operator has no reason token to act on.
func TestDoctorGenerationPoolAgreesWithTheGateOnForgedEvidence(t *testing.T) {
	t.Parallel()

	forged := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.0", New: "0.153.0"},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
		})
	if forged.Verdict != codexgeneration.VerdictYes {
		t.Fatalf("forgery fixture must read as YES: %+v", forged)
	}
	journal := doctorGenerationJournal(codexgeneration.ObligationCompletedPersisted)
	journal.Qualification = &forged
	report := diagnoseCodexGenerationPool(journal, coremetadata.Registry{}, doctorBundleVerifier)
	if report.Status != "blocked" || report.Reason != "version-pair-evidence-unbacked" {
		t.Fatalf("forged pool report status=%q reason=%q", report.Status, report.Reason)
	}
	if report.Action != "run-isolated-version-pair-qualification" {
		t.Fatalf("forged pool action = %q", report.Action)
	}
	row := projectDoctorReplacementProviderRow(&report, doctorProviderSessionCensus{Observed: 1, Running: 1, Live: 1})
	if row.Restoration != doctorRestorationNotRestorable || row.Reason != doctorReplacementReasonPoolBlocked {
		t.Fatalf("forged pool L3 row = %+v", row)
	}
}
