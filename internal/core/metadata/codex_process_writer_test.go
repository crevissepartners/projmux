package metadata

import (
	"reflect"
	"testing"
)

func TestProcessActivationWriterCodexThreadReservation(t *testing.T) {
	base := processSchemaFixture(t)
	pane := &base.Panes[1]
	activation := *pane.Status.Activation.Process
	agent, _ := base.Agent(activation.Binding.AgentUID)
	agent.Spec.Provider = "codex"
	agent.Status.Phase = PhaseRunning
	pane.Status.Activation = PaneActivation{}
	pane.Status.ProcessSession = &ProcessSessionRecord{Provider: "codex", Binding: activation.Binding, ResumeState: ProcessResumeUnknown}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	reg := base.Clone()
	if err := (Mutator{}).RecordProcessActivation(&reg, activation, "verified-thread"); err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Pane(activation.Binding.PaneUID)
	if target.Status.Activation.Codex != nil || target.Status.ProcessSession.ThreadID != "verified-thread" || target.Status.ProcessSession.SessionID != "" || target.Status.ProcessSession.ConnectionID != activation.Binding.OperationID || target.Status.ProcessSession.ResumeState != ProcessResumeUnknown {
		t.Fatal("wrong Codex process evidence", target.Status)
	}
	before := reg.Clone()
	if err := (Mutator{}).RecordProcessActivation(&reg, activation, "verified-thread"); err != nil || !reflect.DeepEqual(before, reg) {
		t.Fatal("retry rewrote evidence", err)
	}
	for name, change := range map[string]func(*Registry){
		"foreign connection": func(r *Registry) {
			p, _ := r.Pane(activation.Binding.PaneUID)
			p.Status.ProcessSession.ConnectionID = "foreign"
		},
		"foreign thread": func(r *Registry) {
			p, _ := r.Pane(activation.Binding.PaneUID)
			p.Status.ProcessSession.ThreadID = "foreign"
		},
		"provider session": func(r *Registry) {
			p, _ := r.Pane(activation.Binding.PaneUID)
			p.Status.ProcessSession.SessionID = "foreign"
		},
		"provider": func(r *Registry) {
			p, _ := r.Pane(activation.Binding.PaneUID)
			p.Status.ProcessSession.Provider = "claude"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := base.Clone()
			change(&bad)
			prior := bad.Clone()
			if err := (Mutator{}).RecordProcessActivation(&bad, activation, "verified-thread"); err == nil || !reflect.DeepEqual(prior, bad) {
				t.Fatal("foreign reservation accepted or changed", err)
			}
		})
	}
}
