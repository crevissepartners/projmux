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
	route, reason := ResolveProcessClaudeRoute(reg, agent, evidence, verify)
	if reason != "" || route.AgentUID != agent || route.PaneUID != pane || route.Authority() != registration.Authority {
		t.Fatalf("valid process route: %+v %s", route, reason)
	}
	for _, change := range []func(*ClaudeProcessRouteEvidence){
		func(e *ClaudeProcessRouteEvidence) { e.HostInstance = "foreign-host" },
		func(e *ClaudeProcessRouteEvidence) { e.Generation = "old-generation" },
		func(e *ClaudeProcessRouteEvidence) { e.Process.PID++ },
		func(e *ClaudeProcessRouteEvidence) { e.Process.Start = "reused-pid-birth" },
		func(e *ClaudeProcessRouteEvidence) { e.SessionID = "foreign-session" },
		func(e *ClaudeProcessRouteEvidence) { e.PaneUID = "foreign-pane" },
	} {
		wrong := evidence
		change(&wrong)
		if r, reason := ResolveProcessClaudeRoute(reg, agent, wrong, verify); reason == "" || r.Authority() != nil {
			t.Fatalf("self assertion routed: %+v %s", r, reason)
		}
	}
	for _, v := range []func(ClaudeProcessRouteEvidence) bool{nil, func(ClaudeProcessRouteEvidence) bool { return false }} {
		if _, reason := ResolveProcessClaudeRoute(reg, agent, evidence, v); reason == "" {
			t.Fatal("unverified payload routed")
		}
	}
}
