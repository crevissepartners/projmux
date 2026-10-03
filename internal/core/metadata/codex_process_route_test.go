package metadata

import "testing"

func TestProcessCodexRouteRequiresOwnedEvidenceAndPreservesTmuxResolver(t *testing.T) {
	reg, _, agent, pane, _ := claudeRouteFixture(t)
	a, _ := reg.Agent(agent)
	a.Spec.Provider = "codex"
	e := CodexProcessRouteEvidence{HostInstance: "host", PaneUID: pane, Generation: "activation-1", ThreadID: "thread", Connection: "connection", Process: ProcessIdentity{PID: 100, OwnerUID: 1000, Start: "test:child"}, HostProcess: ProcessIdentity{PID: 99, OwnerUID: 1000, Start: "test:host"}}
	verify := func(p CodexProcessRouteEvidence) bool { return p == e }
	if _, reason := ResolveProcessCodexRoute(reg, agent, e, verify); reason == "" {
		t.Fatal("adopted tmux")
	}
	p, _ := reg.Pane(pane)
	p.Status.Activation.RuntimeID = ""
	p.Status.Activation.Claude = nil
	if _, reason := ResolveAgentRoute(reg, agent); reason == "" {
		t.Fatal("public route changed")
	}
	route, reason := ResolveProcessCodexRoute(reg, agent, e, verify)
	if reason != "" || route.Authority() != e || route.Incarnation() == "" || route.FullIncarnation() == "" {
		t.Fatalf("route %+v %s", route, reason)
	}
	for _, change := range []func(*CodexProcessRouteEvidence){func(e *CodexProcessRouteEvidence) { e.HostInstance = "foreign" }, func(e *CodexProcessRouteEvidence) { e.ThreadID = "foreign" }, func(e *CodexProcessRouteEvidence) { e.Connection = "old" }, func(e *CodexProcessRouteEvidence) { e.Generation = "old" }, func(e *CodexProcessRouteEvidence) { e.Process.Start = "reused" }, func(e *CodexProcessRouteEvidence) { e.HostProcess.Start = "reused" }} {
		wrong := e
		change(&wrong)
		if r, reason := ResolveProcessCodexRoute(reg, agent, wrong, verify); reason == "" || r.Authority() != nil {
			t.Fatal("forged route")
		}
		if route.Same(AgentRouteRef{AgentUID: agent, PaneUID: pane, Generation: e.Generation, authority: wrong}) {
			t.Fatal("same ignores proof")
		}
	}
	if _, reason := ResolveProcessCodexRoute(reg, agent, e, nil); reason == "" {
		t.Fatal("missing verifier")
	}
	a.Metadata.OwnerRef.UID = "foreign"
	if _, reason := ResolveProcessCodexRoute(reg, agent, e, verify); reason == "" {
		t.Fatal("foreign owner")
	}
}
