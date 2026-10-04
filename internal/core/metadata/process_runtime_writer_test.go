package metadata

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestProcessActivationWriterRequiresReservedGeneration(t *testing.T) {
	base := processSchemaFixture(t)
	pane := &base.Panes[1]
	activation := *pane.Status.Activation.Process
	agent, _ := base.Agent(activation.Binding.AgentUID)
	agent.Spec.Provider = "claude"
	agent.Status.Phase = PhaseRunning
	pane.Status.Activation = PaneActivation{}
	pane.Status.ProcessSession = &ProcessSessionRecord{Provider: "claude", Binding: activation.Binding, ResumeState: ProcessResumeUnknown}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	reg := base.Clone()
	if err := (Mutator{}).RecordProcessActivation(&reg, activation, "verified-hook-session"); err != nil {
		t.Fatal(err)
	}
	current, provider, ok := reg.CurrentProcessActivation(activation.Binding)
	if !ok || current != activation || provider != "claude" {
		t.Fatalf("activation: %+v %q %v", current, provider, ok)
	}
	target, _ := reg.Pane(activation.Binding.PaneUID)
	if target.Status.Activation.Claude != nil || target.Status.ProcessSession.ResumeState != ProcessResumeUnknown || target.Status.ProcessSession.SessionID != "verified-hook-session" {
		t.Fatal("hook promoted stream readiness or stored tmux registration")
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	retryBefore := reg.Clone()
	if err := (Mutator{}).RecordProcessActivation(&reg, activation, "verified-hook-session"); err != nil {
		t.Fatalf("same binding retry: %v", err)
	}
	if !reflect.DeepEqual(reg, retryBefore) {
		t.Fatal("same activation retry rewrote Registry")
	}
	for name, change := range map[string]func(*ProcessActivation){
		"generation":    func(a *ProcessActivation) { a.Binding.Generation = "replacement" },
		"operation":     func(a *ProcessActivation) { a.Binding.OperationID = "replacement" },
		"host":          func(a *ProcessActivation) { a.HostProcess.Start = "replacement" },
		"child":         func(a *ProcessActivation) { a.Child.Start = "replacement" },
		"invalid child": func(a *ProcessActivation) { a.Child.PID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			before := reg.Clone()
			wrong := activation
			change(&wrong)
			if err := (Mutator{}).RecordProcessActivation(&reg, wrong, "verified-hook-session"); err == nil {
				t.Fatal("foreign child/generation accepted")
			}
			if !reflect.DeepEqual(reg, before) {
				t.Fatal("refusal mutated Registry")
			}
		})
	}
	before := reg.Clone()
	if err := (Mutator{}).RecordProcessActivation(&reg, activation, "different-session"); err == nil || !reflect.DeepEqual(reg, before) {
		t.Fatal("session replacement accepted or mutated Registry")
	}
}

func TestProcessWaitProjectsOnlyExactSupervisorReceiptAndRetainsBinding(t *testing.T) {
	reg := processSchemaFixture(t)
	activation := *reg.Panes[1].Status.Activation.Process
	code := 0
	receipt := TerminationEvidence{Source: TerminationSourceSupervisor, Classification: TerminationNormal, ObservedAt: time.Now().UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, ExitCode: &code}
	before := reg.Clone()
	for name, change := range map[string]func(*TerminationEvidence){
		"operation":   func(r *TerminationEvidence) { r.OperationID = "foreign" },
		"generation":  func(r *TerminationEvidence) { r.Generation = "foreign" },
		"unobserved":  func(r *TerminationEvidence) { r.ObservedAt = time.Time{} },
		"absent Wait": func(r *TerminationEvidence) { r.ExitCode = nil },
		"control":     func(r *TerminationEvidence) { r.Source = TerminationSourceControlAction },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := receipt
			change(&wrong)
			if err := (Mutator{}).RecordProcessWait(&reg, activation, wrong); err == nil || !reflect.DeepEqual(reg, before) {
				t.Fatalf("invalid receipt mutated state: %v", err)
			}
		})
	}
	if err := (Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(activation.Binding.AgentUID)
	pane, _ := reg.Pane(activation.Binding.PaneUID)
	if !pane.Status.Activation.IsZero() || agent.Status.Phase != PhaseOffline || agent.Status.PaneRef != activation.Binding.PaneUID || pane.Status.LastTermination == nil || !sameEvidence(pane.Status.LastTermination, &receipt) {
		t.Fatal("Wait did not retain exact offline binding/receipt")
	}
	after := reg.Clone()
	if err := (Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil || !reflect.DeepEqual(reg, after) {
		t.Fatalf("Wait retry changed state: %v", err)
	}
}

func TestProcessWaitRetirementCanComposeWithResumableWriter(t *testing.T) {
	reg, activation, receipt := resumableWriterFixture(t)
	pane, _ := reg.Pane(activation.Binding.PaneUID)
	pane.Status.Activation = PaneActivation{Kind: RuntimeProcess, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, Process: &activation}
	pane.Status.LastTermination = nil
	agent, _ := reg.Agent(activation.Binding.AgentUID)
	agent.Status.Phase, agent.Status.LastTermination = PhaseRunning, nil
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	mutator := Mutator{}
	if err := mutator.RecordProcessWait(&reg, activation, receipt); err != nil {
		t.Fatal(err)
	}
	if err := mutator.RecordProcessResumable(&reg, activation.Binding, &receipt); err != nil {
		t.Fatal("same-transaction composition", err)
	}
	pane, _ = reg.Pane(activation.Binding.PaneUID)
	if !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.ResumeState != ProcessResumable {
		t.Fatal("retirement/resume evidence missing")
	}
	before := reg.Clone()
	if err := mutator.RecordProcessWait(&reg, activation, receipt); err != nil || !reflect.DeepEqual(reg, before) {
		t.Fatal("duplicate Wait changed resumable evidence", err)
	}
	different := receipt
	different.ObservedAt = receipt.ObservedAt.Add(time.Second)
	if err := mutator.RecordProcessWait(&reg, activation, different); !errors.Is(err, ErrInvalidRegistry) || !reflect.DeepEqual(reg, before) {
		t.Fatal("different retired receipt accepted or mutated state", err)
	}
}

func TestProcessWaitDifferentExitReceiptIsRejectedAfterRetirement(t *testing.T) {
	reg, a, r := resumableWriterFixture(t)
	before := reg.Clone()
	code := 1
	r.ExitCode = &code
	r.Classification = ClassifyProcessExit(code, "")
	if err := (Mutator{}).RecordProcessWait(&reg, a, r); !errors.Is(err, ErrInvalidRegistry) || !reflect.DeepEqual(reg, before) {
		t.Fatal("different exit receipt changed retired state", err)
	}
}
