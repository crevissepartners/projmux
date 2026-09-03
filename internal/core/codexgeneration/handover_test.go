package codexgeneration

import (
	"math/rand"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

func handoverTarget(id string) HandoverTarget {
	return HandoverTarget{
		AgentUID: "agent-" + id, PaneUID: "pane-" + id, PaneRuntimeID: "%" + id,
		PaneGeneration: "pane-generation-" + id, RelaunchGeneration: "handover-generation-" + id,
		ThreadID: "thread-" + id,
	}
}

func noTurnChoice(agent string, decision NoTurnDecision, replacement string) NoTurnChoice {
	return NoTurnChoice{AgentUID: agent, Decision: decision, ReplacementAgentUID: replacement,
		PaneUID: "pane-" + agent, PaneRuntimeID: "%" + agent, PaneGeneration: "generation-" + agent}
}

func TestGenerationHandoverOrdersStopResumeSnapshotCASRelaunchTerminalLease(t *testing.T) {
	op, err := NewHandoverOperation("handover-one", "upgrade-one", "domain-one", "old", "new", OwnerProjmuxPrivate,
		[]HandoverTarget{handoverTarget("one"), handoverTarget("two")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []HandoverAction{HandoverActionAdmissionFence, HandoverActionBindingFence, HandoverActionCheckAbsent, HandoverActionCheckAbsent, HandoverActionStopOld,
		HandoverActionResumeTarget, HandoverActionResumeTarget, HandoverActionSnapshotTarget, HandoverActionSnapshotTarget,
		HandoverActionCASTarget, HandoverActionRelaunchTarget, HandoverActionCASTarget, HandoverActionRelaunchTarget,
		HandoverActionRetire, HandoverActionReleaseLease}
	for step, action := range want {
		got, index := op.NextAction()
		if got != action {
			t.Fatalf("step %d action=%s want=%s operation=%+v", step, got, action, op)
		}
		op, err = op.RecordIntent(action, index)
		if err != nil {
			t.Fatalf("intent %s: %v", action, err)
		}
		op, err = op.RecordAction(action, index)
		if err != nil {
			t.Fatalf("record %s: %v", action, err)
		}
	}
	if action, _ := op.NextAction(); action != HandoverActionNone || op.Phase != HandoverComplete || !op.LeaseReleased {
		t.Fatalf("terminal operation = %+v action=%s", op, action)
	}
}

func TestGenerationHandoverCannotCASUntilEverySnapshot(t *testing.T) {
	op, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerProjmuxPrivate,
		[]HandoverTarget{handoverTarget("1"), handoverTarget("2")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []HandoverAction{HandoverActionAdmissionFence, HandoverActionBindingFence, HandoverActionCheckAbsent, HandoverActionCheckAbsent, HandoverActionStopOld,
		HandoverActionResumeTarget, HandoverActionResumeTarget, HandoverActionSnapshotTarget} {
		got, index := op.NextAction()
		if got != action {
			t.Fatalf("got %s want %s", got, action)
		}
		op, err = op.RecordIntent(action, index)
		if err != nil {
			t.Fatal(err)
		}
		op, err = op.RecordAction(action, index)
		if err != nil {
			t.Fatal(err)
		}
	}
	if action, _ := op.NextAction(); action != HandoverActionSnapshotTarget {
		t.Fatalf("CAS escaped global snapshot barrier: %s", action)
	}
	if _, err := op.RecordAction(HandoverActionCASTarget, 0); err == nil {
		t.Fatal("CAS accepted before every snapshot")
	}
}

func TestGenerationHandoverForeignWaitsForExactUserStopReceiptWithZeroLifecycle(t *testing.T) {
	op, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerUnmanaged,
		[]HandoverTarget{handoverTarget("1")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != HandoverAwaitingOwnerStop || op.Mutations.ForeignLifecycle != 0 {
		t.Fatalf("unmanaged initial=%+v", op)
	}
	for _, action := range []HandoverAction{HandoverActionAdmissionFence, HandoverActionBindingFence, HandoverActionCheckAbsent} {
		var index int
		action, index = op.NextAction()
		op, err = op.RecordIntent(action, index)
		if err != nil {
			t.Fatal(err)
		}
		op, err = op.RecordAction(action, index)
		if err != nil {
			t.Fatal(err)
		}
	}
	if action, _ := op.NextAction(); action != HandoverActionAwaitOwnerStop {
		t.Fatalf("unmanaged action=%s", action)
	}
	receipt := OwnerStopReceipt{ReceiptID: "receipt-one", Endpoint: metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}}
	op, err = op.WithExternalStopReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	action, index := op.NextAction()
	if action != HandoverActionStopOld {
		t.Fatalf("after receipt action=%s", action)
	}
	op, err = op.RecordIntent(action, index)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.RecordAction(action, index)
	if err != nil {
		t.Fatal(err)
	}
	if op.Mutations.OldEndpointStop != 0 || op.Mutations.ForeignLifecycle != 0 || !op.OldStopped {
		t.Fatalf("foreign lifecycle escaped: %+v", op)
	}
}

func TestGenerationHandoverAbortOnlyBeforeOldStop(t *testing.T) {
	op, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerProjmuxPrivate, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []HandoverAction{HandoverActionAdmissionFence, HandoverActionBindingFence} {
		got, index := op.NextAction()
		if got != action {
			t.Fatalf("got %s", got)
		}
		op, err = op.RecordIntent(got, index)
		if err != nil {
			t.Fatal(err)
		}
		op, err = op.RecordAction(got, index)
		if err != nil {
			t.Fatal(err)
		}
	}
	aborted, err := op.RequestAbort()
	if err == nil {
		aborted, err = aborted.Abort()
	}
	if err != nil || !aborted.Aborted || aborted.AdmissionFenced || aborted.BindingFenced {
		t.Fatalf("abort=%+v err=%v", aborted, err)
	}
	op, err = op.RecordIntent(HandoverActionStopOld, -1)
	if err != nil {
		t.Fatal(err)
	}
	op, err = op.RecordAction(HandoverActionStopOld, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Abort(); err == nil {
		t.Fatal("post-stop abort accepted")
	}
}

func TestGenerationHandoverNoTurnChoicesAreExplicitAndIdentityDistinct(t *testing.T) {
	choices := []NoTurnChoice{noTurnChoice("agent-close", NoTurnClose, ""),
		noTurnChoice("agent-old", NoTurnReplacement, "agent-new")}
	op, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerProjmuxPrivate, nil, choices, nil)
	if err != nil {
		t.Fatal(err)
	}
	if op.Mutations.NoTurnChoice != 0 {
		t.Fatal("no-turn choice was applied automatically")
	}
	for range choices {
		action, index := op.NextAction()
		if action != HandoverActionNoTurnChoice {
			t.Fatalf("choice action=%s", action)
		}
		op, err = op.RecordIntent(action, index)
		if err != nil {
			t.Fatal(err)
		}
		op, err = op.RecordAction(action, index)
		if err != nil {
			t.Fatal(err)
		}
	}
	if op.Mutations.NoTurnChoice != 2 || op.Choices[1].AgentUID == op.Choices[1].ReplacementAgentUID {
		t.Fatalf("choices=%+v", op.Choices)
	}
}

func TestGenerationHandoverJournalRejectsDuplicateChoiceAndTargetIdentities(t *testing.T) {
	target := handoverTarget("one")
	for _, choices := range [][]NoTurnChoice{
		{noTurnChoice("choice", NoTurnClose, ""), noTurnChoice("choice", NoTurnClose, "")},
		{noTurnChoice("choice-a", NoTurnReplacement, "replacement"), noTurnChoice("choice-b", NoTurnReplacement, "replacement")},
		{noTurnChoice(target.AgentUID, NoTurnClose, "")},
		{noTurnChoice("choice", NoTurnReplacement, target.AgentUID)},
	} {
		if _, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerProjmuxPrivate,
			[]HandoverTarget{target}, choices, nil); err == nil {
			t.Fatalf("accepted duplicate/overlapping identities: %+v", choices)
		}
	}
}

func FuzzGenerationHandoverNeverAcceptsUnsafeReceiptOrder(f *testing.F) {
	f.Add(uint64(0))
	f.Add(^uint64(0))
	f.Fuzz(func(t *testing.T, bits uint64) {
		r := rand.New(rand.NewSource(int64(bits)))
		op, err := NewHandoverOperation("handover", "upgrade", "domain", "old", "new", OwnerProjmuxPrivate,
			[]HandoverTarget{handoverTarget("1")}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		op.AdmissionFenced = r.Intn(2) == 1
		op.BindingFenced = r.Intn(2) == 1
		op.Targets[0].SuccessorAbsentObserved = r.Intn(2) == 1
		op.StopIntended = r.Intn(2) == 1
		op.OldStopped = r.Intn(2) == 1
		op.Targets[0].ResumeIntended = r.Intn(2) == 1
		op.Targets[0].Resumed = r.Intn(2) == 1
		op.Targets[0].SnapshotObserved = r.Intn(2) == 1
		op.Targets[0].EndpointCAS = r.Intn(2) == 1
		op.Targets[0].PaneRelaunched = r.Intn(2) == 1
		op.Retired = r.Intn(2) == 1
		op.LeaseReleased = r.Intn(2) == 1
		op.Mutations.AdmissionFence = boolCount(op.AdmissionFenced)
		op.Mutations.BindingFence = boolCount(op.BindingFenced)
		op.Mutations.SuccessorAbsence = boolCount(op.Targets[0].SuccessorAbsentObserved)
		op.Mutations.OldEndpointStop = boolCount(op.OldStopped)
		op.Mutations.SuccessorResume = boolCount(op.Targets[0].Resumed)
		op.Mutations.SuccessorSnapshot = boolCount(op.Targets[0].SnapshotObserved)
		op.Mutations.EndpointRefCAS = boolCount(op.Targets[0].EndpointCAS)
		op.Mutations.PaneRelaunch = boolCount(op.Targets[0].PaneRelaunched)
		op.Mutations.Retirement = boolCount(op.Retired)
		op.Mutations.LeaseRelease = boolCount(op.LeaseReleased)
		if op.Validate() == nil {
			if op.BindingFenced && !op.AdmissionFenced || op.OldStopped && !op.BindingFenced || op.Targets[0].Resumed && !op.OldStopped ||
				op.Targets[0].EndpointCAS && !op.Targets[0].SnapshotObserved || op.Targets[0].PaneRelaunched && !op.Targets[0].EndpointCAS || op.LeaseReleased && !op.Retired {
				t.Fatalf("accepted unsafe state: %+v", op)
			}
		}
	})
}
