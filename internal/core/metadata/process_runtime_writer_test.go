package metadata

import (
	"reflect"
	"testing"
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
