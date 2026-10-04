package metadata

import "testing"

func TestProcessClaudeRouteRequiresLiveHostEvidenceAndNeverAdoptsTmux(t *testing.T) {
	reg, _, agent, pane, registration := claudeRouteFixture(t)
	evidence := ClaudeProcessRouteEvidence{HostInstance: "host-instance", PaneUID: pane, Generation: "activation-1", SessionID: registration.Authority.SessionID, Process: registration.Authority.Process}
	verify := func(e ClaudeProcessRouteEvidence) bool { return e == evidence }
	if _, reason := ResolveProcessClaudeRoute(reg, agent, evidence, verify); reason == "" {
		t.Fatal("tmux adopted by process resolver")
	}
	p, _ := reg.Pane(pane)
	p.Status.Activation.RuntimeID = ""
	if _, reason := ResolveAgentRoute(reg, agent); reason == "" {
		t.Fatal("public resolver changed")
	}
	if _, reason := ResolveProcessClaudeRoute(reg, agent, evidence, verify); reason == "" {
		t.Fatal("legacy tmux evidence adopted as a v5 process activation")
	}

}

func TestProcessClaudeRouteUsesV5BindingAndLiveRegistration(t *testing.T) {
	reg := processSchemaFixture(t)
	pane := &reg.Panes[1]
	agent, _ := reg.Agent(pane.Metadata.OwnerUID())
	agent.Spec.Provider = "claude"
	agent.Status.Phase = PhaseRunning
	activation := pane.Status.Activation.Process
	pane.Status.ProcessSession = &ProcessSessionRecord{Provider: "claude", Binding: activation.Binding, SessionID: "session-current", ConnectionID: "connection-current", ResumeState: ProcessResumeUnknown}
	authority := ClaudeAuthorityRef{SessionID: "session-current", Process: activation.Child, LeaseProcess: ProcessIdentity{PID: 13, OwnerUID: 1000, Start: "helper-birth"}, RegistrationGeneration: "registration-current"}
	evidence := ClaudeProcessRouteEvidence{HostInstance: activation.Binding.HostInstanceID, PaneUID: pane.Metadata.UID, Generation: activation.Binding.Generation, SessionID: authority.SessionID, Process: activation.Child, HostProcess: activation.HostProcess, Registration: ClaudeRegistration{Authority: authority, Ready: true}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	route, reason := ResolveProcessClaudeRoute(reg, agent.Metadata.UID, evidence, func(e ClaudeProcessRouteEvidence) bool { return e == evidence })
	if reason != "" || route.Authority() != authority {
		t.Fatalf("v5 process authority: %+v %s", route, reason)
	}
	for name, change := range map[string]func(*Registry){
		"generation": func(r *Registry) { r.Panes[1].Status.Activation.Generation = "replacement" },
		"host":       func(r *Registry) { r.Panes[1].Status.Activation.Process.HostProcess.Start = "replacement" },
		"session":    func(r *Registry) { r.Panes[1].Status.ProcessSession.SessionID = "replacement" },
		"tmux":       func(r *Registry) { r.Panes[1].Spec.Runtime.Kind = RuntimeTmux },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := reg.Clone()
			change(&wrong)
			if _, reason := ResolveProcessClaudeRoute(wrong, agent.Metadata.UID, evidence, func(ClaudeProcessRouteEvidence) bool { return true }); reason == "" {
				t.Fatal("stale Registry accepted")
			}
		})
	}
	for name, change := range map[string]func(*ClaudeProcessRouteEvidence){
		"child-birth":    func(e *ClaudeProcessRouteEvidence) { e.Process.Start = "reused" },
		"host-birth":     func(e *ClaudeProcessRouteEvidence) { e.HostProcess.Start = "reused" },
		"helper-session": func(e *ClaudeProcessRouteEvidence) { e.Registration.Authority.SessionID = "other" },
		"not-ready":      func(e *ClaudeProcessRouteEvidence) { e.Registration.Ready = false },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := evidence
			change(&wrong)
			if _, reason := ResolveProcessClaudeRoute(reg, agent.Metadata.UID, wrong, func(ClaudeProcessRouteEvidence) bool { return true }); reason == "" {
				t.Fatal("mismatched live evidence accepted")
			}
		})
	}
	for name, verify := range map[string]func(ClaudeProcessRouteEvidence) bool{"missing-verifier": nil, "failed-verifier": func(ClaudeProcessRouteEvidence) bool { return false }} {
		t.Run(name, func(t *testing.T) {
			if _, reason := ResolveProcessClaudeRoute(reg, agent.Metadata.UID, evidence, verify); reason == "" {
				t.Fatal("unverified process route accepted")
			}
		})
	}

}
