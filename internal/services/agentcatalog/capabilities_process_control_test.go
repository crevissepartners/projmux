package agentcatalog

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func processControlFixture(t *testing.T) (coremetadata.Registry, coremetadata.Agent) {
	t.Helper()
	registry, agent := processWaitFixture(t, aiprovider.Codex)
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: "codex", Codex: &coremetadata.CodexSessionRef{ThreadID: "fixture-thread"},
	}
	pane, _ := registry.Pane(agent.Status.PaneRef)
	pane.Status.ProcessSession.ThreadID = "fixture-thread"
	for i := range registry.Agents {
		if registry.Agents[i].Metadata.UID == agent.Metadata.UID {
			registry.Agents[i] = agent
		}
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return registry, agent
}

func TestCurrentProcessControlCapabilities(t *testing.T) {
	registry, agent := processControlFixture(t)
	before := registry.Clone()
	projection := Exact(registry, agent, aiprovider.Codex)
	if !projection.Runtime.Ready || projection.Runtime.LiveVerified {
		t.Fatalf("want current Registry-only runtime: %+v", projection.Runtime)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("capability read mutated Registry")
	}
	actions := map[string]bool{
		"turn.start": false, "turn.steer": false, "turn.deliver": false,
		"turn.interrupt": false, "approval.review": false,
	}
	for _, capability := range projection.Capabilities {
		if _, wanted := actions[capability.Action]; !wanted {
			continue
		}
		actions[capability.Action] = true
		if capability.Available == nil || !*capability.Available {
			t.Errorf("%s unavailable for current process: %s", capability.Action, capability.Reason)
		}
	}
	for action, seen := range actions {
		if !seen {
			t.Errorf("missing action %s", action)
		}
	}
}

func TestProcessControlCapabilitiesRejectNoncurrentIdentity(t *testing.T) {
	changes := map[string]func(*coremetadata.Registry, *coremetadata.Agent, *coremetadata.Pane){
		"offline": func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.Phase = coremetadata.PhaseOffline
		},
		"pane owner": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Metadata.OwnerRef.UID = "foreign"
		},
		"window owner": func(r *coremetadata.Registry, _ *coremetadata.Agent, _ *coremetadata.Pane) {
			r.Windows[0].Metadata.OwnerRef.UID = "foreign"
		},
		"activation agent": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.AgentUID = "foreign"
		},
		"activation generation": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.Generation = "retired"
		},
		"activation operation": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.OperationID = "retired"
		},
		"retired binding": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.Binding.Generation = "retired"
		},
		"foreign binding": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.Binding.AgentUID = "foreign"
		},
		"missing reservation": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession = nil
		},
		"reserved provider": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.Provider = "claude"
		},
		"declared provider": func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Spec.Provider = "claude"
		},
		"missing thread": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.ThreadID = ""
		},
		"mismatched thread": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.ThreadID = "different-thread"
		},
		"durable provider": func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.SessionRef.Provider = "claude"
		},
		"missing durable ref": func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) { a.Status.SessionRef = nil },
		"missing durable thread": func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.SessionRef.Codex.ThreadID = ""
		},
		"history only": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.ProcessSession.History = &coremetadata.ProcessResumeHistory{Binding: p.Status.ProcessSession.Binding, SessionID: "fixture-thread"}
			p.Status.Activation = coremetadata.PaneActivation{}
		},
		"invalid host": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.Process.HostProcess.PID = 0
		},
		"invalid child": func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.Process.Child.Start = ""
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			registry, initial := processControlFixture(t)
			agent, _ := registry.Agent(initial.Metadata.UID)
			pane, _ := registry.Pane(agent.Status.PaneRef)
			change(&registry, agent, pane)
			before := registry.Clone()
			for _, action := range []string{"turn.start", "turn.steer", "turn.deliver", "turn.interrupt", "approval.review"} {
				if ok, reason := ActionEligibility(registry, *agent, action, aiprovider.SupportNativeExact); ok || reason == "" {
					t.Errorf("%s accepted invalid process identity: ok=%t reason=%q", action, ok, reason)
				}
			}
			if !reflect.DeepEqual(registry, before) {
				t.Fatal("capability read mutated Registry")
			}
		})
	}
}

func TestProcessControlCapabilitiesKeepProviderBoundary(t *testing.T) {
	for _, provider := range []aiprovider.ID{aiprovider.Claude, aiprovider.Antigravity} {
		for _, capability := range Static(provider).Capabilities {
			if capability.Action != "approval.review" && capability.Group != "turn" {
				continue
			}
			registry, agent := processWaitFixture(t, aiprovider.Claude)
			if ok, _ := ActionEligibility(registry, agent, capability.Action, capability.Mode); ok {
				t.Errorf("%s %s became eligible", provider, capability.Action)
			}
		}
	}
}

func TestTmuxControlCapabilitiesStillRequireExactEpochs(t *testing.T) {
	for _, state := range []string{"current", "missing authority", "missing endpoint", "foreign endpoint", "foreign thread"} {
		t.Run(state, func(t *testing.T) {
			registry, agent := processControlFixture(t)
			pane, _ := registry.Pane(agent.Status.PaneRef)
			pane.Spec.Runtime.Kind = coremetadata.RuntimeTmux
			pane.Status.ProcessSession = nil
			pane.Status.Activation.Kind = coremetadata.RuntimeTmux
			pane.Status.Activation.Process = nil
			pane.Status.Activation.RuntimeID = "%42"
			authority := &coremetadata.CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "endpoint", BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1}
			pane.Status.Activation.Codex = &coremetadata.CodexActivationBinding{ThreadID: "fixture-thread", Authority: authority}
			endpoint := authority.Endpoint()
			agent.Status.SessionRef.Codex.Endpoint = &endpoint
			switch state {
			case "missing authority":
				pane.Status.Activation.Codex.Authority = nil
			case "missing endpoint":
				agent.Status.SessionRef.Codex.Endpoint = nil
			case "foreign endpoint":
				endpoint.EndpointGenerationID = "foreign"
			case "foreign thread":
				pane.Status.Activation.Codex.ThreadID = "foreign"
			}
			for _, action := range []string{"turn.start", "turn.steer", "turn.deliver", "turn.interrupt", "approval.review"} {
				ok, reason := ActionEligibility(registry, agent, action, aiprovider.SupportNativeExact)
				if ok != (state == "current") {
					t.Errorf("%s: eligible=%t reason=%q", action, ok, reason)
				}
			}
		})
	}
}
