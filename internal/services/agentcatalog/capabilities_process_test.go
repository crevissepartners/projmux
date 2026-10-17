package agentcatalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// processWaitFixture is entirely in memory. Its process identities are fixture
// data, never PIDs to probe; a current activation makes no claim about idle.
func processWaitFixture(t *testing.T, provider aiprovider.ID) (coremetadata.Registry, coremetadata.Agent) {
	t.Helper()
	counts := map[coremetadata.Kind]int{}
	mutator := coremetadata.Mutator{
		Now: func() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-%02d", strings.ToLower(string(kind)), counts[kind]), nil
		},
		DirExists: func(path string) (bool, error) { return path == "/fixture/process-wait", nil },
	}
	registry := coremetadata.NewRegistry()
	project, err := mutator.RegisterProject(&registry, coremetadata.RegisterProjectOptions{
		Root: "/fixture/process-wait", OperationID: "op-register",
	})
	if err != nil {
		t.Fatalf("register pure fixture: %v", err)
	}
	agent, err := mutator.CreateAgent(&registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
		Provider: string(provider), OperationID: "op-create",
	})
	if err != nil {
		t.Fatalf("create fixture Agent: %v", err)
	}
	pane, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		CWD: "/fixture/process-wait",
	}, "op-attach")
	if err != nil {
		t.Fatalf("attach fixture Pane: %v", err)
	}
	binding := coremetadata.ProcessBinding{
		HostInstanceID: "host-fixture", ProjectUID: project.Project.Metadata.UID,
		WindowUID: project.Windows[0].Metadata.UID, AgentUID: agent.Metadata.UID,
		PaneUID: pane.Metadata.UID, Generation: "gen-fixture", OperationID: "op-process",
	}
	if err := mutator.ReserveProcessBinding(&registry, binding); err != nil {
		t.Fatalf("reserve fixture process: %v", err)
	}
	activation := coremetadata.ProcessActivation{
		Binding:     binding,
		HostProcess: coremetadata.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "fixture-host-birth"},
		Child:       coremetadata.ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "fixture-child-birth"},
	}
	if err := mutator.RecordProcessChild(&registry, activation); err != nil {
		t.Fatalf("record fixture child: %v", err)
	}
	current, ok := registry.Agent(agent.Metadata.UID)
	if !ok {
		t.Fatal("fixture Agent disappeared")
	}
	return registry, *current
}

func TestProcessWaitAdmissionMatchesCurrentActivation(t *testing.T) {
	for _, provider := range []aiprovider.ID{aiprovider.Codex, aiprovider.Claude} {
		t.Run(string(provider), func(t *testing.T) {
			registry, agent := processWaitFixture(t, provider)
			if err := registry.Validate(); err != nil {
				t.Fatalf("precondition Registry.Validate: %v", err)
			}
			pane, ok := registry.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.Activation.Process == nil {
				t.Fatal("precondition current process Pane is missing")
			}
			activation, gotProvider, current := registry.CurrentProcessActivation(pane.Status.Activation.Process.Binding)
			if !current || gotProvider != string(provider) || activation != *pane.Status.Activation.Process {
				t.Fatalf("precondition CurrentProcessActivation: current=%t provider=%q activation=%+v", current, gotProvider, activation)
			}
			if pane.Status.Activation.RuntimeID != "" {
				t.Fatal("precondition process RuntimeID must be empty")
			}
			before := registry.Clone()
			if reason := ActivationReason(registry, agent); reason != "" {
				t.Errorf("ActivationReason rejected valid current process activation with empty RuntimeID: %s", reason)
			}
			runtime := RuntimeEligibility(registry, agent)
			if !runtime.Ready || runtime.PaneRuntimeID != "" || runtime.LiveVerified || runtime.Evidence != "registry" || runtime.PaneUID != pane.Metadata.UID || runtime.ActivationGeneration != activation.Binding.Generation {
				t.Errorf("RuntimeEligibility must accept Registry-only process activation with empty RuntimeID: %+v", runtime)
			}
			projection := Exact(registry, agent, provider)
			found := false
			for _, capability := range projection.Capabilities {
				if capability.Action == "wait.idle" {
					found = true
					if capability.Available == nil || !*capability.Available || capability.Evidence != "registry" || projection.Runtime == nil || !projection.Runtime.Ready {
						t.Errorf("wait.available and registryReady must accept valid current process activation: capability=%+v runtime=%+v", capability, projection.Runtime)
					}
				}
			}
			if !found {
				t.Error("wait.idle capability missing")
			}
			if !reflect.DeepEqual(registry, before) {
				t.Error("admission/projection mutated Registry")
			}
		})
	}
}

func TestProcessWaitAdmissionRejectsForeignAndRetiredEvidence(t *testing.T) {
	changes := map[string]func(*coremetadata.Registry){
		"pane owner UID":         func(r *coremetadata.Registry) { r.Panes[1].Metadata.OwnerRef.UID = "foreign" },
		"pane owner kind":        func(r *coremetadata.Registry) { r.Panes[1].Metadata.OwnerRef.Kind = coremetadata.KindWindow },
		"agent owner UID":        func(r *coremetadata.Registry) { r.Agents[0].Metadata.OwnerRef.UID = "foreign" },
		"agent owner kind":       func(r *coremetadata.Registry) { r.Agents[0].Metadata.OwnerRef.Kind = coremetadata.KindProject },
		"window owner":           func(r *coremetadata.Registry) { r.Windows[0].Metadata.OwnerRef.UID = "foreign" },
		"activation UID":         func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.AgentUID = "foreign" },
		"generation":             func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Generation = "replacement" },
		"operation":              func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.OperationID = "replacement" },
		"binding Agent":          func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.AgentUID = "foreign" },
		"binding Pane":           func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.PaneUID = "foreign" },
		"binding Project":        func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.ProjectUID = "foreign" },
		"binding Window":         func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.WindowUID = "foreign" },
		"binding host":           func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.HostInstanceID = "" },
		"binding generation":     func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.Generation = "retired" },
		"binding operation":      func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Binding.OperationID = "retired" },
		"missing reservation":    func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession = nil },
		"reserved host":          func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.HostInstanceID = "foreign" },
		"reserved generation":    func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.Generation = "retired" },
		"reserved operation":     func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.OperationID = "retired" },
		"reserved Agent":         func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.AgentUID = "foreign" },
		"reserved Pane":          func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Binding.PaneUID = "foreign" },
		"reserved provider":      func(r *coremetadata.Registry) { r.Panes[1].Status.ProcessSession.Provider = "foreign" },
		"host PID":               func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.HostProcess.PID = 0 },
		"host birth":             func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.HostProcess.Start = "" },
		"child PID":              func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Child.PID = 0 },
		"child birth":            func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process.Child.Start = "" },
		"child free reservation": func(r *coremetadata.Registry) { r.Panes[1].Status.Activation = coremetadata.PaneActivation{} },
		"history only": func(r *coremetadata.Registry) {
			r.Panes[1].Status.Activation = coremetadata.PaneActivation{}
			r.Agents[0].Status.Phase = coremetadata.PhaseOffline
		},
		"missing process":      func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Process = nil },
		"tmux ID mixed in":     func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.RuntimeID = "%42" },
		"activation kind":      func(r *coremetadata.Registry) { r.Panes[1].Status.Activation.Kind = coremetadata.RuntimeTmux },
		"role":                 func(r *coremetadata.Registry) { r.Panes[1].Spec.Role = coremetadata.PaneRoleShell },
		"backref":              func(r *coremetadata.Registry) { r.Agents[0].Status.PaneRef = "foreign" },
		"phase":                func(r *coremetadata.Registry) { r.Agents[0].Status.Phase = coremetadata.PhaseOffline },
		"unsupported provider": func(r *coremetadata.Registry) { r.Agents[0].Spec.Provider = "antigravity" },
	}
	for _, provider := range []aiprovider.ID{aiprovider.Codex, aiprovider.Claude} {
		t.Run(string(provider), func(t *testing.T) {
			base, _ := processWaitFixture(t, provider)
			for name, change := range changes {
				t.Run(name, func(t *testing.T) {
					registry := base.Clone()
					change(&registry)
					agent := registry.Agents[0]
					before := registry.Clone()
					if ActivationReason(registry, agent) == "" || RuntimeEligibility(registry, agent).Ready {
						t.Fatal("invalid process evidence admitted")
					}
					projection := Exact(registry, agent, provider)
					for _, entry := range projection.Capabilities {
						if entry.Action == "wait.idle" && (entry.Available == nil || *entry.Available) {
							t.Fatalf("invalid wait availability: %+v", entry)
						}
					}
					if !reflect.DeepEqual(registry, before) {
						t.Fatal("refusal mutated Registry")
					}
				})
			}
		})
	}
}

func TestProcessWaitAdmissionPreservesTmuxAndStaticSupport(t *testing.T) {
	registry, agent := processWaitFixture(t, aiprovider.Codex)
	pane, _ := registry.Pane(agent.Status.PaneRef)
	pane.Spec.Runtime.Kind = coremetadata.RuntimeTmux
	pane.Status.Activation.Kind = coremetadata.RuntimeTmux
	pane.Status.Activation.Process = nil
	pane.Status.ProcessSession = nil
	if ActivationReason(registry, agent) == "" || RuntimeEligibility(registry, agent).Ready {
		t.Fatal("tmux activation without RuntimeID admitted")
	}
	pane.Status.Activation.RuntimeID = "%42"
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	if ActivationReason(registry, agent) != "" || !RuntimeEligibility(registry, agent).Ready {
		t.Fatal("existing tmux activation rejected")
	}
	registry, agent = processWaitFixture(t, aiprovider.Claude)
	before := registry.Clone()
	static := Static(aiprovider.Claude)
	exact := Exact(registry, agent, aiprovider.Claude)
	for i, entry := range static.Capabilities {
		got := exact.Capabilities[i]
		if entry.Available != nil || entry.Evidence != "" || entry.Mode != got.Mode || entry.Callable != got.Callable || entry.CompletionPrecision != got.CompletionPrecision {
			t.Fatalf("static support changed: static=%+v exact=%+v", entry, got)
		}
		if entry.Mode == aiprovider.SupportUnsupported && (got.Available == nil || *got.Available) {
			t.Fatalf("unsupported action admitted: %+v", got)
		}
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("projection mutated Registry")
	}
}

func TestProcessWaitAdmissionSeparatesCurrentActivationFromHistory(t *testing.T) {
	for _, provider := range []aiprovider.ID{aiprovider.Codex, aiprovider.Claude} {
		t.Run(string(provider), func(t *testing.T) {
			registry, agent := processWaitFixture(t, provider)
			pane, _ := registry.Pane(agent.Status.PaneRef)
			record := pane.Status.ProcessSession
			if provider == aiprovider.Codex {
				record.ThreadID = "fixture-conversation"
			} else {
				record.SessionID = "fixture-conversation"
			}
			old := record.Binding
			old.Generation, old.OperationID = "retired-generation", "retired-operation"
			record.History = &coremetadata.ProcessResumeHistory{Binding: old, SessionID: "fixture-conversation"}
			if err := registry.Validate(); err != nil {
				t.Fatal(err)
			}
			before := registry.Clone()
			if _, _, ok := registry.CurrentProcessActivation(old); ok {
				t.Fatal("retired binding became current")
			}
			if ActivationReason(registry, agent) != "" || !RuntimeEligibility(registry, agent).Ready {
				t.Fatal("history obscured valid current activation")
			}
			if ControlReason(registry, agent) == "" {
				t.Fatal("process wait admission granted native control authority")
			}
			if !reflect.DeepEqual(registry, before) {
				t.Fatal("current/history read mutated Registry")
			}
			pane.Status.Activation = coremetadata.PaneActivation{}
			if err := registry.Validate(); err != nil {
				t.Fatal(err)
			}
			before = registry.Clone()
			if ActivationReason(registry, agent) == "" || RuntimeEligibility(registry, agent).Ready {
				t.Fatal("durable history or child-free reservation became current activation")
			}
			if !reflect.DeepEqual(registry, before) {
				t.Fatal("history refusal mutated Registry")
			}
		})
	}
}
