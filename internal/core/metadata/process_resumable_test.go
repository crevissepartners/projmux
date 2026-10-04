package metadata

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func resumableWriterFixture(t *testing.T) (Registry, ProcessActivation, TerminationEvidence) {
	t.Helper()
	reg := processSchemaFixture(t)
	pane := &reg.Panes[1]
	activation := *pane.Status.Activation.Process
	pane.Status.ProcessSession.ConnectionID = activation.Binding.OperationID
	pane.Status.ProcessSession.Pending[0].ConnectionID = activation.Binding.OperationID
	pane.Status.ProcessSession.ResumeState = ProcessResumeUnknown
	code := 0
	receipt := TerminationEvidence{Source: TerminationSourceSupervisor, Classification: TerminationNormal, ObservedAt: time.Unix(100, 0).UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, ExitCode: &code}
	pane.Status.Activation = PaneActivation{}
	pane.Status.LastTermination = receipt.Clone()
	agent, _ := reg.Agent(activation.Binding.AgentUID)
	agent.Status.Phase = PhaseOffline
	agent.Status.LastTermination = receipt.Clone()
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, activation, receipt
}

func TestProcessSupportedResumableWriterPreservesInterruptedGeneration(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			reg, activation, receipt := resumableWriterFixture(t)
			pane, _ := reg.Pane(activation.Binding.PaneUID)
			agent, _ := reg.Agent(activation.Binding.AgentUID)
			agent.Spec.Provider = provider
			if provider == "claude" {
				pane.Status.ProcessSession.SessionID, pane.Status.ProcessSession.ThreadID = pane.Status.ProcessSession.ThreadID, ""
			}
			pane.Status.ProcessSession.Provider = provider
			before := reg.Clone()
			m := Mutator{Now: func() time.Time { return time.Unix(200, 0) }}
			if err := m.RecordProcessResumable(&reg, activation.Binding, &receipt); err != nil {
				t.Fatal(err)
			}
			if err := reg.Validate(); err != nil {
				t.Fatal("preserved interrupted turn/pending rejected", err)
			}
			want := before.Clone()
			target, _ := want.Pane(activation.Binding.PaneUID)
			target.Status.ProcessSession.ResumeState = ProcessResumable
			want.UpdatedAt = time.Unix(200, 0).UTC()
			if !reflect.DeepEqual(reg, want) {
				t.Fatal("writer changed termination, phase, activation or interrupted evidence")
			}
			retry := mustJSON(t, reg)
			if err := m.RecordProcessResumable(&reg, activation.Binding, &receipt); err != nil || mustJSON(t, reg) != retry {
				t.Fatal("retry changed registry", err)
			}
		})
	}
}

func TestProcessSupportedResumableWriterRefusesWithoutMutation(t *testing.T) {
	for name, change := range map[string]func(*Registry, *ProcessActivation, **TerminationEvidence){
		"no Wait":                 func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { *r = nil },
		"no wait status":          func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { (*r).ExitCode = nil },
		"conflicting wait status": func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { (*r).Signal = "TERM" },
		"wrong generation":        func(_ *Registry, a *ProcessActivation, _ **TerminationEvidence) { a.Binding.Generation = "other" },
		"wrong operation":         func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { (*r).OperationID = "other" },
		"wrong agent":             func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { (*r).AgentUID = "other" },
		"wrong pane":              func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) { (*r).PaneUID = "other" },
		"wrong host instance":     func(_ *Registry, a *ProcessActivation, _ **TerminationEvidence) { a.Binding.HostInstanceID = "other" },
		"live activation": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			p, _ := r.Pane(a.Binding.PaneUID)
			p.Status.Activation = PaneActivation{Kind: RuntimeProcess, Process: a, Generation: a.Binding.Generation, AgentUID: a.Binding.AgentUID, OperationID: a.Binding.OperationID}
		},
		"running owner": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			agent, _ := r.Agent(a.Binding.AgentUID)
			agent.Status.Phase = PhaseRunning
		},
		"missing pane receipt": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			p, _ := r.Pane(a.Binding.PaneUID)
			p.Status.LastTermination = nil
		},
		"missing agent receipt": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			agent, _ := r.Agent(a.Binding.AgentUID)
			agent.Status.LastTermination = nil
		},
		"conversation absent": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			p, _ := r.Pane(a.Binding.PaneUID)
			p.Status.ProcessSession.ThreadID = ""
		},
		"connection mismatch": func(r *Registry, a *ProcessActivation, _ **TerminationEvidence) {
			p, _ := r.Pane(a.Binding.PaneUID)
			p.Status.ProcessSession.ConnectionID = "other"
		},
		"not a Wait source": func(_ *Registry, _ *ProcessActivation, r **TerminationEvidence) {
			(*r).Source = TerminationSourceControlAction
			(*r).Classification = TerminationIntentional
		},
	} {
		t.Run(name, func(t *testing.T) {
			reg, activation, receipt := resumableWriterFixture(t)
			ptr := &receipt
			change(&reg, &activation, &ptr)
			before := reg.Clone()
			if err := (Mutator{}).RecordProcessResumable(&reg, activation.Binding, ptr); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("got %v, want ErrInvalidRegistry", err)
			}
			if !reflect.DeepEqual(reg, before) {
				t.Fatal("refusal mutated registry; unknown must remain unknown")
			}
		})
	}
}

func TestProcessSupportedResumableWaitPromotesInRetirementTransaction(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			reg, activation, receipt := resumableWriterFixture(t)
			pane, _ := reg.Pane(activation.Binding.PaneUID)
			agent, _ := reg.Agent(activation.Binding.AgentUID)
			agent.Spec.Provider = provider
			pane.Status.ProcessSession.Provider = provider
			if provider == "claude" {
				pane.Status.ProcessSession.SessionID, pane.Status.ProcessSession.ThreadID = pane.Status.ProcessSession.ThreadID, ""
			}
			interrupted := pane.Status.ProcessSession.Clone()
			pane.Status.Activation = PaneActivation{Kind: RuntimeProcess, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, Process: &activation}
			pane.Status.LastTermination = nil
			agent.Status.Phase, agent.Status.LastTermination = PhaseRunning, nil
			if err := (Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(activation.Binding.PaneUID)
			if !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.ResumeState != ProcessResumable || pane.Status.ProcessSession.TurnID != interrupted.TurnID || !reflect.DeepEqual(pane.Status.ProcessSession.Pending, interrupted.Pending) {
				t.Fatal("Wait did not retire and preserve a resumable interrupted conversation atomically")
			}
			before := reg.Clone()
			if err := (Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil || !reflect.DeepEqual(before, reg) {
				t.Fatal("duplicate Wait changed resumable record", err)
			}
		})
	}
}

func TestProcessSupportedResumableReserveArchivesOnlyRetiredGeneration(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			reg, activation, receipt := resumableWriterFixture(t)
			pane, _ := reg.Pane(activation.Binding.PaneUID)
			agent, _ := reg.Agent(activation.Binding.AgentUID)
			agent.Spec.Provider, pane.Status.ProcessSession.Provider = provider, provider
			if provider == "claude" {
				pane.Status.ProcessSession.SessionID, pane.Status.ProcessSession.ThreadID = pane.Status.ProcessSession.ThreadID, ""
			}
			m := Mutator{}
			if err := m.RecordProcessResumable(&reg, activation.Binding, &receipt); err != nil {
				t.Fatal(err)
			}
			// Mutators commit cloned Registries, so re-read the selected record.
			pane, _ = reg.Pane(activation.Binding.PaneUID)
			old := pane.Status.ProcessSession.Clone()
			binding := activation.Binding
			binding.HostInstanceID, binding.Generation, binding.OperationID = "new-host", "new-generation", "new-operation"
			if err := m.ReserveProcessResume(&reg, activation.Binding, binding); err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(binding.PaneUID)
			current := pane.Status.ProcessSession
			if current.Binding != binding || current.TurnID != "" || len(current.Pending) != 0 || current.ResumeState != ProcessResumeUnknown || current.History == nil || current.History.Binding != old.Binding || current.History.InterruptedTurnID != old.TurnID || !reflect.DeepEqual(current.History.Expired, old.Pending) {
				t.Fatal("resume lost retired work or carried it into current authority")
			}
			if _, _, ok := reg.CurrentProcessActivation(activation.Binding); ok {
				t.Fatal("old generation retained control authority")
			}
			before := reg.Clone()
			if err := m.ReserveProcessResume(&reg, activation.Binding, binding); err == nil || !reflect.DeepEqual(reg, before) {
				t.Fatal("stale resume reservation mutated state")
			}
			next := activation
			next.Binding = binding
			if err := m.RecordProcessChild(&reg, next); err != nil {
				t.Fatal(err)
			}
			snapshot := *current.Clone()
			snapshot.History = nil
			snapshot.TurnID = "new-turn"
			if err := m.RecordProcessSession(&reg, next, snapshot); err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(binding.PaneUID)
			if !reflect.DeepEqual(pane.Status.ProcessSession.History, current.History) {
				t.Fatal("new snapshot erased retired history")
			}
		})
	}
}
