package metadata

import (
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
)

func claudeRouteFixture(t *testing.T) (Registry, Mutator, string, string, ClaudeRegistration) {
	t.Helper()
	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	project, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := m.CreateAgent(&reg, project.Windows[0].Metadata.UID, CreateAgentOptions{Name: "claude-one", Provider: "claude", OperationID: "create"})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{Name: "claude-pane", Command: "claude", CWD: "/src/projmux"}, "create")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.RecordPaneActivation(&reg, pane.Metadata.UID, PaneActivationOptions{Generation: "activation-1", RuntimeID: "%7", AgentUID: agent.Metadata.UID, OperationID: "create"})
	if err != nil {
		t.Fatal(err)
	}
	process := ProcessIdentity{PID: 100, OwnerUID: 1000, Start: "test:provider-birth"}
	if err := m.RecordClaudeProcess(&reg, pane.Metadata.UID, agent.Metadata.UID, "activation-1", process); err != nil {
		t.Fatal(err)
	}
	registration := ClaudeRegistration{Authority: ClaudeAuthorityRef{SessionID: "actual-session", Process: process, RegistrationGeneration: "registration-1",
		LeaseProcess: ProcessIdentity{PID: 101, OwnerUID: 1000, Start: "test:helper-birth"}}, Ready: true}
	if err := m.BeginClaudeRegistration(&reg, pane.Metadata.UID, agent.Metadata.UID, "activation-1", registration.Authority); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordClaudeRegistration(&reg, pane.Metadata.UID, agent.Metadata.UID, "activation-1", registration); err != nil {
		t.Fatal(err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return reg, m, agent.Metadata.UID, pane.Metadata.UID, registration
}

func TestAgentRouteClaudeIdentityTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*Registry, Mutator, string, string, ClaudeRegistration)
	}{
		{"generation", func(reg *Registry, m Mutator, a, p string, _ ClaudeRegistration) {
			_, _ = m.RecordPaneActivation(reg, p, PaneActivationOptions{Generation: "activation-2", AgentUID: a, RuntimeID: "%7"})
		}},
		{"runtime absent", func(reg *Registry, _ Mutator, _, p string, _ ClaudeRegistration) {
			pane, _ := reg.Pane(p)
			pane.Status.Activation.RuntimeID = ""
		}},
		{"wrong owner kind", func(reg *Registry, _ Mutator, _, p string, _ ClaudeRegistration) {
			pane, _ := reg.Pane(p)
			pane.Metadata.OwnerRef.Kind = KindWindow
		}},
		{"owner mismatch", func(reg *Registry, _ Mutator, _, p string, _ ClaudeRegistration) {
			pane, _ := reg.Pane(p)
			pane.Metadata.OwnerRef.UID = "foreign"
		}},
		{"process restart same PID", func(reg *Registry, _ Mutator, _, p string, _ ClaudeRegistration) {
			pane, _ := reg.Pane(p)
			pane.Status.Activation.Claude.Process.Start = "test:new-birth"
		}},
		{"stop", func(reg *Registry, m Mutator, a, _ string, _ ClaudeRegistration) {
			_, _ = m.TransitionAgent(reg, a, PhaseOffline, "exit")
		}},
		{"termination", func(reg *Registry, m Mutator, a, p string, _ ClaudeRegistration) {
			code := 0
			_, _ = m.RecordTermination(reg, TerminationEvidence{Source: TerminationSourceSupervisor, Classification: TerminationNormal, PaneUID: p, AgentUID: a, Generation: "activation-1", ExitCode: &code})
		}},
		{"new SessionStart pending", func(reg *Registry, m Mutator, a, p string, r ClaudeRegistration) {
			r.Authority.RegistrationGeneration = "registration-2"
			_ = m.BeginClaudeRegistration(reg, p, a, "activation-1", r.Authority)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reg, m, a, p, r := claudeRouteFixture(t)
			before, reason := ResolveAgentRoute(reg, a)
			if reason != "" {
				t.Fatal(reason)
			}
			test.change(&reg, m, a, p, r)
			after, reason := ResolveAgentRoute(reg, a)
			if reason == "" || before.Same(after) {
				t.Fatal("stale route remained eligible")
			}
		})
	}
}

func TestClaudeRegistrationAdmissionAndCleanupCAS(t *testing.T) {
	t.Parallel()
	reg, m, a, p, old := claudeRouteFixture(t)
	newer := old
	newer.Authority.RegistrationGeneration = "registration-2"
	newer.Authority.LeaseProcess.Start = "test:new-helper"
	if err := m.BeginClaudeRegistration(&reg, p, a, "activation-1", newer.Authority); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordClaudeRegistration(&reg, p, a, "activation-1", old); err == nil {
		t.Fatal("delayed old helper overwrote newer SessionStart claim")
	}
	if err := m.RecordClaudeRegistration(&reg, p, a, "activation-1", newer); err != nil {
		t.Fatal(err)
	}
	before := reg.Clone()
	if m.ClearClaudeRegistration(&reg, p, a, "activation-1", old.Authority) || !reflect.DeepEqual(before, reg) {
		t.Fatal("old helper cleared new lease")
	}
	for _, change := range []func(*Registry){
		func(r *Registry) { pane, _ := r.Pane(p); pane.Metadata.OwnerRef.Kind = KindWindow },
		func(r *Registry) { agent, _ := r.Agent(a); agent.Status.PaneRef = "foreign-pane" },
		func(r *Registry) { pane, _ := r.Pane(p); pane.Spec.Role = PaneRoleShell },
		func(r *Registry) {
			pane, _ := r.Pane(p)
			pane.Status.Activation.Claude.RegistrationGeneration = "newer-claim"
		},
	} {
		foreign := reg.Clone()
		change(&foreign)
		before = foreign.Clone()
		if m.ClearClaudeRegistration(&foreign, p, a, "activation-1", newer.Authority) || !reflect.DeepEqual(before, foreign) {
			t.Fatal("foreign owner or claim cleanup wrote Registry")
		}
	}
}

func TestAgentRouteNamesAreNotAuthority(t *testing.T) {
	t.Parallel()
	reg, m, a, p, _ := claudeRouteFixture(t)
	before, reason := ResolveAgentRoute(reg, a)
	if reason != "" {
		t.Fatal(reason)
	}
	if _, err := m.RenameAgent(&reg, a, "renamed-agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RenamePane(&reg, p, "renamed-pane"); err != nil {
		t.Fatal(err)
	}
	after, reason := ResolveAgentRoute(reg, a)
	if reason != "" || !before.Same(after) {
		t.Fatal("same-UID rename changed endpoint authority")
	}
}

func TestClaudeRegistrationRejectsCompetingClaims(t *testing.T) {
	t.Parallel()
	for _, change := range []func(*ClaudeRegistration){
		func(r *ClaudeRegistration) { r.Authority.SessionID = "competing-session" },
		func(r *ClaudeRegistration) { r.Authority.Process.Start = "foreign-process" },
		func(r *ClaudeRegistration) { r.Authority.LeaseProcess.Start = "competing-helper" },
	} {
		reg, m, a, p, r := claudeRouteFixture(t)
		before := reg.Clone()
		change(&r)
		if m.RecordClaudeRegistration(&reg, p, a, "activation-1", r) == nil || !reflect.DeepEqual(before, reg) {
			t.Fatal("competing claim changed Registry")
		}
	}
}

func TestAgentRouteProviderAuthoritiesNeverCrossCompare(t *testing.T) {
	t.Parallel()
	reg, _, a, p, r := claudeRouteFixture(t)
	claude, reason := ResolveAgentRoute(reg, a)
	if reason != "" {
		t.Fatal(reason)
	}
	codex := claude
	codex.authority = CodexRouteAuthority{ThreadID: r.Authority.SessionID, Authority: CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "endpoint", BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1}}
	if claude.Same(codex) || codex.Same(claude) {
		t.Fatal("cross-provider authority compared equal")
	}
	agent, _ := reg.Agent(a)
	agent.Spec.Provider = "codex"
	pane, _ := reg.Pane(p)
	pane.Status.Activation.Claude = nil
	authority := codex.authority.(CodexRouteAuthority)
	pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: authority.ThreadID, Authority: &authority.Authority}
	endpoint := authority.Authority.Endpoint()
	agent.Status.SessionRef = &AgentSessionRef{Provider: "codex", Codex: &CodexSessionRef{ThreadID: authority.ThreadID, Endpoint: &endpoint}}
	current, reason := ResolveAgentRoute(reg, a)
	if reason != "" || !current.Same(codex) {
		t.Fatal("existing Codex composite was not consumed exactly")
	}
	pane.Status.Activation.Codex.Authority.BrokerRuntimeID = "restarted"
	changed, _ := ResolveAgentRoute(reg, a)
	if current.Same(changed) {
		t.Fatal("foreign Codex broker numeric epochs matched")
	}
}

func TestAgentRouteStaleReferenceModel(t *testing.T) {
	t.Parallel()
	// Model: every observed SessionStart replaces the current registration;
	// every activation replacement destroys it. A historical reference never
	// authorizes the current route, even when names, PID, and runtime are reused.
	property := func(events []uint8) bool {
		reg, m, a, p, registration := claudeRouteFixture(t)
		generation := "activation-1"
		var history []AgentRouteRef
		type oldLease struct {
			generation   string
			registration ClaudeRegistration
		}
		var leases []oldLease
		for step, event := range events {
			current, reason := ResolveAgentRoute(reg, a)
			if reason == "" {
				history = append(history, current)
				leases = append(leases, oldLease{generation, registration})
			}
			if event%2 == 0 {
				generation = fmt.Sprintf("activation-%d", step+2)
				_, _ = m.RecordPaneActivation(&reg, p, PaneActivationOptions{Generation: generation, AgentUID: a, RuntimeID: "%7"})
				if m.RecordClaudeProcess(&reg, p, a, generation, registration.Authority.Process) != nil {
					return false
				}
			}
			registration.Authority.RegistrationGeneration = fmt.Sprintf("registration-step-%d", step)
			if m.BeginClaudeRegistration(&reg, p, a, generation, registration.Authority) != nil || m.RecordClaudeRegistration(&reg, p, a, generation, registration) != nil {
				return false
			}
			now, reason := ResolveAgentRoute(reg, a)
			if reason != "" {
				return false
			}
			for _, old := range history {
				if old.Same(now) {
					return false
				}
			}
			for _, old := range leases {
				before := reg.Clone()
				if m.RecordClaudeRegistration(&reg, p, a, old.generation, old.registration) == nil ||
					m.ClearClaudeRegistration(&reg, p, a, old.generation, old.registration.Authority) || !reflect.DeepEqual(before, reg) {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal("stale-reference model failed")
	}
}
