package codexhandover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// rewriteJournalQualification replaces the stored receipt without touching
// anything else, which is how a resume can find a journal that no longer says
// what the plan that pinned it read.
func rewriteJournalQualification(t *testing.T, coordinator *Coordinator, qualification *codexgeneration.QualificationResult) {
	t.Helper()
	if _, err := coordinator.Journal.Update(context.Background(), func(journal *codexupgrade.Journal, exists bool) error {
		if !exists {
			t.Fatal("seeded handover journal disappeared")
		}
		journal.Qualification = qualification
		return nil
	}); err != nil {
		t.Fatalf("rewrite stored qualification: %v", err)
	}
}

// TestResumeRefusesAnUnqualifiedPairBeforeAnyHandoverEffect is acceptance 3.
//
// Resume is the second door into the destructive part of a handover, and the
// one Plan does not stand in front of: `agent app-server handover resume` calls
// it directly, and Plan's pinned re-entry never rechecks the version pair. Each
// case here pins a *pinned* operation and then changes what the journal says,
// which is the shape Plan cannot see.
//
// Zero effects is the assertion. A refusal that still fenced admission or
// stopped the old endpoint would have done the damage it refused to authorize.
func TestResumeRefusesAnUnqualifiedPairBeforeAnyHandoverEffect(t *testing.T) {
	forged := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
		})
	refused := codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"},
		codexgeneration.QualificationEvidence{ObservedThreadTurns: 2})

	for _, test := range []struct {
		name          string
		qualification *codexgeneration.QualificationResult
		want          string
	}{
		{name: "receipt removed after the pin", qualification: nil, want: "handover-resume-version-pair-not-qualified"},
		{name: "measured refusal", qualification: &refused, want: "handover-resume-version-pair-not-qualified"},
		{name: "forged evidence counters", qualification: &forged, want: "handover-resume-qualification-evidence-forged"},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator, request, effects := pinnedHandover(t)
			before := len(effects.calls)
			rewriteJournalQualification(t, coordinator, test.qualification)
			_, err := coordinator.Resume(context.Background(), request.OperationRef)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unqualified resume = %v, want %s", err, test.want)
			}
			if len(effects.calls) != before {
				t.Fatalf("refused resume produced effects: %v", effects.calls[before:])
			}
		})
	}
}

// TestResumeStillRetiresAVacantGenerationWithoutAReceipt is the other half of
// the same guard, and the reason it recomputes rather than reading a flag.
//
// An unqualified pair may still retire a generation nothing is bound to. The
// receipt proves a thread survives a cross-version resume; a generation with no
// thread to carry has nothing for it to prove, and Plan encodes exactly that
// exception. A resume guard that refused here would close a lane the planner
// deliberately leaves open and strand any operation pinned through it.
func TestResumeStillRetiresAVacantGenerationWithoutAReceipt(t *testing.T) {
	coordinator, request, effects := pinnedHandover(t)
	// Empty the Registry so the old endpoint census finds no bound Agent, Pane,
	// or thread, enumerate an empty state domain, then remove the receipt.
	coordinator.Registry = staticRegistry{}
	coordinator.EnumerateThreads = func(string) ([]string, error) { return nil, nil }
	rewriteJournalQualification(t, coordinator, nil)
	before := len(effects.calls)
	if _, err := coordinator.Resume(context.Background(), request.OperationRef); err != nil {
		t.Fatalf("vacant unqualified resume = %v", err)
	}
	if len(effects.calls) == before {
		t.Fatal("vacant resume drove no handover action")
	}
	journal, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || journal.Handover == nil || !journal.Handover.Retired {
		t.Fatalf("vacant resume did not retire: journal=%+v exists=%t err=%v", journal.Handover, exists, err)
	}
}

// pinnedHandover seeds a handover that is pinned and part-way through, so a
// later Resume has something left to drive.
//
// Stopping at the first effect is what makes these cases meaningful: a resume
// with no next action returns without entering the gate at all, so an operation
// driven to completion could not show whether the gate is there.
func pinnedHandover(t *testing.T) (*Coordinator, Request, *recordingEffects) {
	t.Helper()
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate,
		codexgeneration.ObligationCompletedPersisted, codexgeneration.ObligationCompletedPersisted)
	fired := false
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterEffect && !fired {
			fired = true
			return errors.New("stop after the first effect")
		}
		return nil
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil || !strings.Contains(err.Error(), "stop after the first effect") {
		t.Fatalf("seed a part-way qualified handover: %v", err)
	}
	coordinator.Failpoint = nil
	if len(effects.calls) == 0 {
		t.Fatal("seeded handover drove no effect")
	}
	return coordinator, request, effects
}
