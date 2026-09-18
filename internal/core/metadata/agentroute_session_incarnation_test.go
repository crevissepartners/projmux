package metadata

import "testing"

// TestAgentRouteAcceptsExactlyFullAndSessionIncarnation pins the reader-side
// predicate: the current full digest and the current session-scoped value are
// accepted, nothing else is.
func TestAgentRouteAcceptsExactlyFullAndSessionIncarnation(t *testing.T) {
	t.Parallel()
	reg, _, agentUID, _, registration := claudeRouteFixture(t)
	claude, reason := ResolveAgentRoute(reg, agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	otherSession := claude
	otherAuthority := registration.Authority
	otherAuthority.SessionID = "other-session"
	otherSession.authority = otherAuthority
	codex := claude
	codex.authority = CodexRouteAuthority{ThreadID: "thread-1", Authority: CodexAuthorityRef{StateDomainID: "domain",
		EndpointGenerationID: "endpoint", BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1}}
	nilAuthority := AgentRouteRef{AgentUID: claude.AgentUID, PaneUID: claude.PaneUID, Generation: claude.Generation}
	invalidClaude := claude
	invalidAuthority := registration.Authority
	invalidAuthority.SessionID = ""
	invalidClaude.authority = invalidAuthority
	emptyThreadCodex := codex
	emptyThreadCodex.authority = CodexRouteAuthority{Authority: codex.authority.(CodexRouteAuthority).Authority}

	tests := []struct {
		name  string
		route AgentRouteRef
		value string
		want  bool
	}{
		{"claude full digest", claude, claude.FullIncarnation(), true},
		{"claude session value", claude, claude.SessionIncarnation(), true},
		{"claude session value of another SessionID", claude, otherSession.SessionIncarnation(), false},
		{"claude full digest of another SessionID", claude, otherSession.FullIncarnation(), false},
		{"claude empty", claude, "", false},
		{"claude arbitrary route value", claude, "route-000000000000000000000000000000000000", false},
		{"claude codex session value", claude, codex.SessionIncarnation(), false},
		{"codex full digest", codex, codex.FullIncarnation(), true},
		{"codex session value", codex, codex.SessionIncarnation(), true},
		{"codex empty", codex, "", false},
		{"codex arbitrary route value", codex, "route-000000000000000000000000000000000000", false},
		{"codex claude session value", codex, claude.SessionIncarnation(), false},
		{"nil authority empty", nilAuthority, "", false},
		{"nil authority claude session value", nilAuthority, claude.SessionIncarnation(), false},
		{"invalid claude authority", invalidClaude, invalidClaude.SessionIncarnation(), false},
		{"codex empty thread", emptyThreadCodex, codex.SessionIncarnation(), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.route.AcceptsIncarnation(test.value); got != test.want {
				t.Fatalf("AcceptsIncarnation(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
	for name, route := range map[string]AgentRouteRef{"nil": nilAuthority, "invalid claude": invalidClaude, "codex empty thread": emptyThreadCodex} {
		if route.SessionIncarnation() != "" || route.FullIncarnation() != "" {
			t.Fatalf("%s authority produced an incarnation", name)
		}
	}
	for name, route := range map[string]AgentRouteRef{"claude": claude, "codex": codex} {
		full, session := route.FullIncarnation(), route.SessionIncarnation()
		if session == "" || session == full || len(session) != len(full) || session[:6] != "route-" {
			t.Fatalf("%s session value %q is not a distinct route value beside %q", name, session, full)
		}
	}
}

// TestAgentRouteSessionIncarnationScope pins what the session value depends
// on: only the Claude SessionID, and nothing at all for Codex.
func TestAgentRouteSessionIncarnationScope(t *testing.T) {
	t.Parallel()
	reg, _, agentUID, _, registration := claudeRouteFixture(t)
	base, reason := ResolveAgentRoute(reg, agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	claudeChanges := []struct {
		name          string
		change        func(*ClaudeAuthorityRef)
		sessionStable bool
	}{
		{"provider process", func(a *ClaudeAuthorityRef) { a.Process.Start = "test:replacement-provider" }, true},
		{"provider PID", func(a *ClaudeAuthorityRef) { a.Process.PID++ }, true},
		{"registration generation", func(a *ClaudeAuthorityRef) { a.RegistrationGeneration = "registration-2" }, true},
		{"lease process", func(a *ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:replacement-helper" }, true},
		{"session", func(a *ClaudeAuthorityRef) { a.SessionID = "other-session" }, false},
	}
	for _, test := range claudeChanges {
		t.Run("claude "+test.name, func(t *testing.T) {
			authority := registration.Authority
			test.change(&authority)
			changed := base
			changed.authority = authority
			if changed.FullIncarnation() == base.FullIncarnation() {
				t.Fatal("full digest ignored the change")
			}
			if stable := changed.SessionIncarnation() == base.SessionIncarnation(); stable != test.sessionStable {
				t.Fatalf("session value stable=%t, want %t", stable, test.sessionStable)
			}
			// The old full digest never reads as current; the session value
			// reads as current exactly when it is unchanged.
			if changed.AcceptsIncarnation(base.FullIncarnation()) {
				t.Fatal("previous full digest accepted after the change")
			}
			if changed.AcceptsIncarnation(base.SessionIncarnation()) != test.sessionStable {
				t.Fatal("previous session value acceptance disagrees with its scope")
			}
		})
	}
	codex := func(thread string, connection, binding uint64, broker string) AgentRouteRef {
		route := base
		route.authority = CodexRouteAuthority{ThreadID: thread, Authority: CodexAuthorityRef{StateDomainID: "domain",
			EndpointGenerationID: "endpoint", BrokerRuntimeID: broker, ConnectionEpoch: connection, BindingEpoch: binding}}
		return route
	}
	first := codex("thread-1", 1, 1, "broker")
	for name, other := range map[string]AgentRouteRef{
		"other thread":           codex("thread-2", 1, 1, "broker"),
		"other connection epoch": codex("thread-1", 2, 1, "broker"),
		"other binding epoch":    codex("thread-1", 1, 2, "broker"),
		"other broker runtime":   codex("thread-1", 1, 1, "restarted"),
	} {
		if other.FullIncarnation() == first.FullIncarnation() {
			t.Fatalf("codex %s: full digest ignored the change", name)
		}
		if other.SessionIncarnation() != first.SessionIncarnation() {
			t.Fatalf("codex %s: provider-fixed session value changed", name)
		}
	}
}
