package codexgeneration

import (
	"reflect"
	"testing"
)

func TestRollingUpgradeStateMachineKeepsPhase5EffectsAtZeroAndReceiptsAtMostOnce(t *testing.T) {
	op, err := NewRollingUpgradeOperation("upgrade-one", "domain-one", "generation-old", "generation-new")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordCandidateLaunchIntent()
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordCandidateStart()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ProjectDrainLedger("generation-old", []AgentObligation{
		{AgentUID: "agent-active", EndpointGenerationID: "generation-old", State: ObligationActive},
		{AgentUID: "agent-durable", EndpointGenerationID: "generation-old", State: ObligationCompletedPersisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		action RollingAction
		phase  RollingPhase
	}{
		{RollingActionPrepareCandidate, RollingCandidateReady},
		{RollingActionCommitAdmission, RollingAdmissionCurrent},
		{RollingActionPublishDrain, RollingDraining},
	} {
		var changed bool
		op, changed, err = op.RecordAction(step.action, ledger)
		if err != nil || !changed || op.Phase != step.phase {
			t.Fatalf("record %s = (%+v,%t,%v)", step.action, op, changed, err)
		}
		unchanged, changed, err := op.RecordAction(step.action, ledger)
		if err != nil || changed || !reflect.DeepEqual(unchanged, op) {
			t.Fatalf("repeat %s changed receipt: (%+v,%t,%v)", step.action, unchanged, changed, err)
		}
	}
	op, changed, err := op.RequestGenerationHandover()
	if err != nil || !changed || op.Phase != RollingHandoverPending {
		t.Fatalf("handover request = (%+v,%t,%v)", op, changed, err)
	}
	ref := op.OperationRef
	op, changed, err = op.RequestGenerationHandover()
	if err != nil || changed || op.OperationRef != ref || op.Mutations.HandoverRequest != 1 {
		t.Fatalf("repeated handover request = (%+v,%t,%v)", op, changed, err)
	}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	if op.Mutations.OldEndpointStop != 0 || op.Mutations.SuccessorResume != 0 ||
		op.Mutations.EndpointRefCAS != 0 || op.Mutations.PaneRelaunch != 0 ||
		op.Mutations.Retirement != 0 || op.Mutations.LeaseRelease != 0 ||
		op.Mutations.ForeignAdoption != 0 {
		t.Fatalf("Phase 5 effect escaped into Phase 4: %+v", op.Mutations)
	}
}

func TestDrainLedgerIsContentFreeAndClosesBlockerMatrix(t *testing.T) {
	states := []ObligationState{
		ObligationActive, ObligationApprovalPending, ObligationNoTurn,
		ObligationUnknown, ObligationCompletedPersisted, ObligationClosed,
	}
	obligations := make([]AgentObligation, 0, len(states))
	for i, state := range states {
		obligations = append(obligations, AgentObligation{
			AgentUID: "agent-" + string(rune('a'+i)), EndpointGenerationID: "old", State: state,
		})
	}
	ledger, err := ProjectDrainLedger("old", obligations)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range ledger {
		wantBlocker := i < 4
		if row.BlocksHandover != wantBlocker {
			t.Fatalf("%s blocks=%t, want %t", row.State, row.BlocksHandover, wantBlocker)
		}
	}
	if _, err := ProjectDrainLedger("old", append(obligations, obligations[0])); err == nil {
		t.Fatal("duplicate exact Agent obligation accepted")
	}
}

func TestRollingUpgradeAbortIsNonDestructiveAndCannotRevertCommittedAdmission(t *testing.T) {
	op, err := NewRollingUpgradeOperation("upgrade-one", "domain-one", "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RequestAbort()
	if err != nil {
		t.Fatal(err)
	}
	aborted, changed, err := op.Abort()
	if err != nil || !changed || !aborted.Aborted || aborted.Phase != RollingAborted || aborted.Mutations != (RollingOperationMutations{}) {
		t.Fatalf("abort = (%+v,%t,%v)", aborted, changed, err)
	}
	op, err = NewRollingUpgradeOperation("upgrade-two", "domain-one", "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordCandidateLaunchIntent()
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordCandidateStart()
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordAction(RollingActionPrepareCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := op.Abort(); err == nil || changed {
		t.Fatalf("prepared candidate abort = changed=%t err=%v", changed, err)
	}
	op, _, err = op.RequestAbort()
	if err != nil {
		t.Fatal(err)
	}
	aborted, changed, err = op.AbortPreparedCandidate()
	if err != nil || !changed || !aborted.Aborted || aborted.Mutations.CandidateCleanup != 1 || aborted.Mutations.LeaseRelease != 0 {
		t.Fatalf("exact prepared-candidate abort = (%+v,%t,%v)", aborted, changed, err)
	}
}

func TestLaunchBeforeProofCrashCanBeAbortedWithCandidateCleanupOnly(t *testing.T) {
	op, err := NewRollingUpgradeOperation("upgrade-crash", "domain", "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RecordCandidateLaunchIntent()
	if err != nil {
		t.Fatal(err)
	}
	op, _, err = op.RequestAbort()
	if err != nil {
		t.Fatal(err)
	}
	aborted, changed, err := op.AbortRecoveredCandidate()
	if err != nil || !changed || !aborted.Aborted || !aborted.CandidateStarted ||
		aborted.Mutations.CandidateStart != 1 || aborted.Mutations.CandidateCleanup != 1 {
		t.Fatalf("recovered abort = (%+v,%t,%v)", aborted, changed, err)
	}
	if aborted.Mutations.OldEndpointStop != 0 || aborted.Mutations.SuccessorResume != 0 ||
		aborted.Mutations.EndpointRefCAS != 0 || aborted.Mutations.PaneRelaunch != 0 ||
		aborted.Mutations.Retirement != 0 || aborted.Mutations.LeaseRelease != 0 || aborted.Mutations.ForeignAdoption != 0 {
		t.Fatalf("recovered abort crossed Phase 5 boundary: %+v", aborted.Mutations)
	}
}

func TestRollingUpgradeStateSpaceProperty(t *testing.T) {
	for mask := range 1 << 14 {
		op, err := NewRollingUpgradeOperation("upgrade", "domain", "old", "new")
		if err != nil {
			t.Fatal(err)
		}
		op.CandidateLaunchIntended = mask&(1<<0) != 0
		op.CandidateStarted = mask&(1<<1) != 0
		op.CandidateReady = mask&(1<<2) != 0
		op.AdmissionCommitted = mask&(1<<3) != 0
		op.DrainPublished = mask&(1<<4) != 0
		op.HandoverRequested = mask&(1<<5) != 0
		op.Mutations.CandidateLaunchIntent = boolCount(mask&(1<<6) != 0)
		op.Mutations.CandidateStart = boolCount(mask&(1<<7) != 0)
		op.Mutations.AdmissionCommit = boolCount(mask&(1<<8) != 0)
		op.Mutations.DrainPublish = boolCount(mask&(1<<9) != 0)
		op.Mutations.HandoverRequest = boolCount(mask&(1<<10) != 0)
		op.Mutations.OldEndpointStop = boolCount(mask&(1<<11) != 0)
		op.Mutations.EndpointRefCAS = boolCount(mask&(1<<12) != 0)
		op.Mutations.LeaseRelease = boolCount(mask&(1<<13) != 0)
		op.Phase = phaseForReceipts(op)
		if op.Validate() == nil {
			if op.Mutations.OldEndpointStop != 0 || op.Mutations.EndpointRefCAS != 0 || op.Mutations.LeaseRelease != 0 ||
				op.Mutations.CandidateStart > 1 || op.Mutations.AdmissionCommit > 1 ||
				op.Mutations.DrainPublish > 1 || op.Mutations.HandoverRequest > 1 {
				t.Fatalf("accepted unsafe state: %+v", op)
			}
		}
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func phaseForReceipts(op RollingUpgradeOperation) RollingPhase {
	switch {
	case op.HandoverRequested:
		return RollingHandoverPending
	case op.DrainPublished:
		return RollingDraining
	case op.AdmissionCommitted:
		return RollingAdmissionCurrent
	case op.CandidateReady:
		return RollingCandidateReady
	default:
		return RollingPlanned
	}
}

func FuzzRollingUpgradeValidStatesNeverContainPhase5Effects(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(255))
	f.Fuzz(func(t *testing.T, bits uint8) {
		op, err := NewRollingUpgradeOperation("upgrade", "domain", "old", "new")
		if err != nil {
			t.Fatal(err)
		}
		op.Mutations.OldEndpointStop = int(bits & 1)
		op.Mutations.SuccessorResume = int((bits >> 1) & 1)
		op.Mutations.EndpointRefCAS = int((bits >> 2) & 1)
		op.Mutations.PaneRelaunch = int((bits >> 3) & 1)
		op.Mutations.Retirement = int((bits >> 4) & 1)
		op.Mutations.LeaseRelease = int((bits >> 5) & 1)
		op.Mutations.ForeignAdoption = int((bits >> 6) & 1)
		if op.Validate() == nil && bits&0x7f != 0 {
			t.Fatalf("accepted Phase 5 effect bits %07b", bits&0x7f)
		}
	})
}
