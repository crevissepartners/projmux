package codexupgrade

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
)

func qualifiedManagedActivation(t *testing.T) (*Coordinator, ManagedCurrentActivation) {
	t.Helper()
	request := testRollingRequest(t)
	newLease := testRollingLease(t, filepath.Join(filepath.Dir(request.Target.LeaseRoot), "managed-0.153.0"), "0.153.0")
	request.Target.Endpoint.EndpointGenerationID = "codex-0.153.0"
	request.Target.LeaseRoot = newLease.Root
	request.TargetBundleID = newLease.ID
	request.TargetTUIPath = newLease.Paths(codexbundle.RoleTUI)[0]
	coordinator, _, _ := testRollingCoordinator(t, request)
	return coordinator, ManagedCurrentActivation{
		OperationRef:   "managed-activation-qualification",
		OldEndpoint:    request.Current.Generation.Endpoint,
		OldOwner:       codexgeneration.OwnerUnmanaged,
		OldVersion:     "0.151.0",
		Target:         request.Target,
		TargetBundleID: request.TargetBundleID,
		TargetTUIPath:  request.TargetTUIPath,
		TargetVersion:  "0.153.0",
		Qualification:  testActivationQualification("0.151.0", "0.153.0"),
	}
}

// TestActivateManagedCurrentRefusesEveryUnqualifiedRequestBeforeDraining is the
// whole of acceptance 2. Each case checks two things that are easy to conflate:
// the refusal token, and that the journal was never written.
//
// The second matters more than the first. A gate that refuses after the
// prewrite would leave the old route in `draining` with no receipt to run a
// handover under, which is the exact state 2026-09-07 proved has no way back.
// So "no journal exists" is the assertion, not "an error was returned".
func TestActivateManagedCurrentRefusesEveryUnqualifiedRequestBeforeDraining(t *testing.T) {
	forged := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.151.0", New: "0.153.0"},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
		})
	refused := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.151.0", New: "0.153.0"},
		codexgeneration.QualificationEvidence{ObservedThreadTurns: 2})

	for _, test := range []struct {
		name   string
		mutate func(*ManagedCurrentActivation)
		want   string
	}{
		{
			name:   "no receipt at all",
			mutate: func(a *ManagedCurrentActivation) { a.Qualification = codexgeneration.QualificationResult{} },
			want:   "managed-current-activation-qualification-invalid",
		},
		{
			name:   "receipt for another pair",
			mutate: func(a *ManagedCurrentActivation) { a.Qualification = testActivationQualification("0.151.0", "0.152.9") },
			want:   "managed-current-activation-qualification-version-pair-mismatch",
		},
		{
			name:   "measured refusal",
			mutate: func(a *ManagedCurrentActivation) { a.Qualification = refused },
			want:   "managed-current-activation-version-pair-not-qualified",
		},
		{
			name:   "forged evidence counters",
			mutate: func(a *ManagedCurrentActivation) { a.Qualification = forged },
			want:   "managed-current-activation-qualification-evidence-forged",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator, activation := qualifiedManagedActivation(t)
			test.mutate(&activation)
			_, err := coordinator.ActivateManagedCurrent(context.Background(), activation)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unqualified activation = %v, want %s", err, test.want)
			}
			if _, exists, loadErr := coordinator.Journal.Load(); exists || loadErr != nil {
				t.Fatalf("refused activation wrote a journal: exists=%t err=%v", exists, loadErr)
			}
		})
	}
}

// TestActivateManagedCurrentPublishesTheQualificationItRanUnder closes the other
// half of the same guarantee: the receipt does not only authorize the switch, it
// stays in the journal. Without that the diagnosis after the fact could not say
// which pair the pool was qualified for, only that it was qualified once.
func TestActivateManagedCurrentPublishesTheQualificationItRanUnder(t *testing.T) {
	coordinator, activation := qualifiedManagedActivation(t)
	journal, err := coordinator.ActivateManagedCurrent(context.Background(), activation)
	if err != nil {
		t.Fatalf("qualified activation: %v", err)
	}
	if journal.Qualification == nil || *journal.Qualification != activation.Qualification {
		t.Fatalf("journal qualification = %+v", journal.Qualification)
	}
	reloaded, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || reloaded.Qualification == nil || *reloaded.Qualification != activation.Qualification {
		t.Fatalf("reloaded qualification = %+v exists=%t err=%v", reloaded.Qualification, exists, err)
	}
	if gate := codexgeneration.GateQualification(*reloaded.Qualification); !gate.Phase2Ready {
		t.Fatalf("stored qualification no longer opens the lane: %+v", gate)
	}
}

// TestActivateManagedCurrentRefusesAnExistingPoolWithNoReceipt covers the pool
// this change inherits rather than creates: one written before the gate
// existed. Re-entry is refused because the journal is the only record of what
// authorized that pool, and an absent receipt there means nothing did.
func TestActivateManagedCurrentRefusesAnExistingPoolWithNoReceipt(t *testing.T) {
	coordinator, activation := qualifiedManagedActivation(t)
	if _, err := coordinator.ActivateManagedCurrent(context.Background(), activation); err != nil {
		t.Fatalf("seed qualified activation: %v", err)
	}
	if _, err := coordinator.Journal.Update(context.Background(), func(journal *Journal, exists bool) error {
		if !exists {
			t.Fatal("seeded journal disappeared")
		}
		journal.Qualification = nil
		return nil
	}); err != nil {
		t.Fatalf("strip the stored receipt: %v", err)
	}
	_, err := coordinator.ActivateManagedCurrent(context.Background(), activation)
	if err == nil || !strings.Contains(err.Error(), "managed-current-activation-existing-pool-not-qualified") {
		t.Fatalf("re-entry into an unqualified pool = %v", err)
	}
}
