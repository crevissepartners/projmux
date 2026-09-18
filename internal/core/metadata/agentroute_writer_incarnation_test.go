package metadata

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// TestAgentRouteIncarnationFollowsClaudeSession pins the written route
// incarnation to the Claude conversation: a new registration generation,
// helper (lease) process, or provider process under the same SessionID keeps
// it, while a new SessionID changes it. The full-authority digest still
// changes, and after the change the base's written value is accepted but its
// full digest is not.
func TestAgentRouteIncarnationFollowsClaudeSession(t *testing.T) {
	t.Parallel()
	reg, _, agentUID, _, registration := claudeRouteFixture(t)
	base, reason := ResolveAgentRoute(reg, agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	if base.Incarnation() == "" || base.Incarnation() == base.FullIncarnation() {
		t.Fatalf("base incarnation %q is not distinct from full digest %q", base.Incarnation(), base.FullIncarnation())
	}
	tests := []struct {
		name   string
		change func(*ClaudeAuthorityRef)
		stable bool
	}{
		{"registration generation", func(a *ClaudeAuthorityRef) { a.RegistrationGeneration = "registration-2" }, true},
		{"lease process birth", func(a *ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:replacement-helper" }, true},
		{"lease process PID", func(a *ClaudeAuthorityRef) { a.LeaseProcess.PID++ }, true},
		{"provider process birth", func(a *ClaudeAuthorityRef) { a.Process.Start = "test:replacement-provider" }, true},
		{"provider process PID", func(a *ClaudeAuthorityRef) { a.Process.PID++ }, true},
		{"session", func(a *ClaudeAuthorityRef) { a.SessionID = "other-session" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := registration.Authority
			test.change(&authority)
			changed := base
			changed.authority = authority
			if got := changed.Incarnation() == base.Incarnation(); got != test.stable {
				t.Fatalf("Incarnation stable=%t, want %t (base %q, changed %q)", got, test.stable, base.Incarnation(), changed.Incarnation())
			}
			if changed.FullIncarnation() == base.FullIncarnation() {
				t.Fatal("full digest ignored the change")
			}
			if changed.AcceptsIncarnation(base.Incarnation()) != test.stable {
				t.Fatalf("base written incarnation accepted=%t, want %t", !test.stable, test.stable)
			}
			if changed.AcceptsIncarnation(base.FullIncarnation()) {
				t.Fatal("base full digest accepted after the authority change")
			}
		})
	}
}

// TestAgentRouteIncarnationSurvivesCodexReconnect pins the written Codex route
// incarnation to one provider-fixed value: no single authority field change
// (state domain, endpoint generation, broker runtime, connection or binding
// epoch) moves it, while the full-authority digest does.
func TestAgentRouteIncarnationSurvivesCodexReconnect(t *testing.T) {
	t.Parallel()
	baseAuthority := CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "endpoint",
		BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1}
	route := func(authority CodexAuthorityRef) AgentRouteRef {
		return AgentRouteRef{AgentUID: "agent-codex", PaneUID: "pane-codex", Generation: "activation-codex",
			authority: CodexRouteAuthority{ThreadID: "thread-1", Authority: authority}}
	}
	base := route(baseAuthority)
	if base.Incarnation() == "" || base.Incarnation() == base.FullIncarnation() {
		t.Fatalf("base incarnation %q is not distinct from full digest %q", base.Incarnation(), base.FullIncarnation())
	}
	tests := []struct {
		name   string
		change func(*CodexAuthorityRef)
	}{
		{"state domain", func(a *CodexAuthorityRef) { a.StateDomainID = "domain-2" }},
		{"endpoint generation", func(a *CodexAuthorityRef) { a.EndpointGenerationID = "endpoint-2" }},
		{"broker runtime", func(a *CodexAuthorityRef) { a.BrokerRuntimeID = "broker-2" }},
		{"connection epoch", func(a *CodexAuthorityRef) { a.ConnectionEpoch = 2 }},
		{"binding epoch", func(a *CodexAuthorityRef) { a.BindingEpoch = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := baseAuthority
			test.change(&authority)
			changed := route(authority)
			if changed.Incarnation() != base.Incarnation() {
				t.Fatalf("Incarnation changed: base %q, changed %q", base.Incarnation(), changed.Incarnation())
			}
			if changed.FullIncarnation() == base.FullIncarnation() {
				t.Fatal("full digest ignored the change")
			}
			if !changed.AcceptsIncarnation(base.Incarnation()) {
				t.Fatal("base written incarnation refused after reconnect")
			}
			if changed.AcceptsIncarnation(base.FullIncarnation()) {
				t.Fatal("base full digest accepted after the authority change")
			}
		})
	}
}

// TestAgentRouteIncarnationKeepsSessionValueBytes pins the written value to
// the exact bytes the session-scoped reader form had before writers switched,
// so helpers built before the switch and after it agree on every value.
func TestAgentRouteIncarnationKeepsSessionValueBytes(t *testing.T) {
	t.Parallel()
	reg, _, agentUID, _, registration := claudeRouteFixture(t)
	claude, reason := ResolveAgentRoute(reg, agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	codex := AgentRouteRef{AgentUID: "agent-codex", PaneUID: "pane-codex", Generation: "activation-codex",
		authority: CodexRouteAuthority{ThreadID: "thread-1", Authority: CodexAuthorityRef{StateDomainID: "domain",
			EndpointGenerationID: "endpoint", BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1}}}
	pinned := func(material string) string {
		digest := sha256.Sum256([]byte(material))
		return fmt.Sprintf("route-%x", digest[:18])
	}
	for name, test := range map[string]struct {
		route AgentRouteRef
		want  string
	}{
		"claude": {claude, pinned("claude-session\x00" + registration.Authority.SessionID)},
		"codex":  {codex, pinned("codex-session")},
	} {
		if got := test.route.Incarnation(); got != test.want {
			t.Fatalf("%s Incarnation = %q, want %q", name, got, test.want)
		}
		if got := test.route.SessionIncarnation(); got != test.want {
			t.Fatalf("%s SessionIncarnation = %q, want %q", name, got, test.want)
		}
	}
}
