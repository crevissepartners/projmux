package metadata

import (
	"reflect"
	"testing"
)

func nativeReservationFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	agent.Status.Phase, agent.Status.PaneRef = PhaseOffline, ""
	beforeSession := agent.Status.SessionRef.Clone()
	pane, err := lifecycleMutator().ReserveNativeAgentPane(reg, lifecycleAgentUID, BootstrapPane{CWD: "/srv/alpha"}, "transfer-op")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lifecycleMutator().RecordPaneActivation(reg, pane.Metadata.UID, PaneActivationOptions{Generation: "transfer-gen", OperationID: "transfer-op", AgentUID: lifecycleAgentUID}); err != nil {
		t.Fatal(err)
	}
	agent, _ = reg.Agent(lifecycleAgentUID)
	if agent.Status.Phase != PhasePending || agent.Status.PaneRef != pane.Metadata.UID || !reflect.DeepEqual(agent.Status.SessionRef, beforeSession) {
		t.Fatalf("reservation changed conversation or phase: %+v", agent.Status)
	}
	if err = reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, pane.Metadata.UID
}

func TestNativeReservationAbsenceIsNotTermination(t *testing.T) {
	reg, paneUID := nativeReservationFixture(t)
	before := reg.Clone()
	if NeedsTerminationProjection(*reg, paneUID) {
		t.Fatal("unstarted native reservation needs exit projection")
	}
	out, err := lifecycleMutator().ProjectTermination(reg, TerminationProjectionInput{PaneUID: paneUID, Generation: "transfer-gen"})
	if err != nil || out.Changed || !reflect.DeepEqual(*reg, before) {
		t.Fatalf("unstarted reservation was projected: %+v %v", out, err)
	}
	pane, _ := reg.Pane(paneUID)
	if pane.Status.Activation.RuntimeID != "" || pane.Status.Activation.Process != nil || pane.Status.Activation.Codex != nil || pane.Status.ProcessSession != nil || pane.Status.LastTermination != nil {
		t.Fatal("reservation granted runtime/authority/termination evidence")
	}
}

func TestNativeReservationDoesNotMaskOrdinaryOrStartedAbsence(t *testing.T) {
	cases := map[string]func(*Registry, string){
		"ordinary pending":  func(reg *Registry, _ string) { agent, _ := reg.Agent(lifecycleAgentUID); agent.Status.Reason = "" },
		"actual runtime":    func(reg *Registry, uid string) { pane, _ := reg.Pane(uid); pane.Status.Activation.RuntimeID = "%99" },
		"missing operation": func(reg *Registry, uid string) { pane, _ := reg.Pane(uid); pane.Status.Activation.OperationID = "" },
		"provider binding": func(reg *Registry, uid string) {
			pane, _ := reg.Pane(uid)
			pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: "thr-9"}
		},
		"teardown evidence": func(reg *Registry, uid string) {
			pane, _ := reg.Pane(uid)
			pane.Status.Teardown = &PaneTeardownEvidence{}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			reg, uid := nativeReservationFixture(t)
			change(reg, uid)
			if !NeedsTerminationProjection(*reg, uid) {
				t.Fatal("absence incorrectly hidden")
			}
			out, err := lifecycleMutator().ProjectTermination(reg, TerminationProjectionInput{PaneUID: uid})
			if err != nil || !out.Changed {
				t.Fatalf("ordinary projection suppressed: %+v %v", out, err)
			}
		})
	}
}
