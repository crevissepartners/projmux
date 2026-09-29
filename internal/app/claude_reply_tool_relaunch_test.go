package app

import (
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The reply tool proves the original sender through the live broker, which
// reads the fixture's real Registry. The original is a self-message so both
// routes resolve; a relaunch moves the original's Source to the Pane and
// generation it had before, the way the CLI and store relaunch tests do.

// relaunchedSenderToolHub holds one delivered original from the fixture's
// Agent to itself, after change has rewritten its Source.
func relaunchedSenderToolHub(t *testing.T, f *claudeCoordinationTestFixture, ref string,
	change func(*coremessage.Envelope),
) (*claudeCoordinationHub, []string) {
	t.Helper()
	now := time.Now().UTC()
	envelope := dialogueForRoute(ref, f.route, now)
	envelope.BrokerEnvelope.Source = envelope.BrokerEnvelope.Target
	change(envelope.BrokerEnvelope)
	hub := qualifiedPushHub(now)
	hub.messages[ref] = &claudeCoordinationMessage{envelope: envelope, delivery: agentdelivery.Delivery{State: agentdelivery.StateDelivered}}
	argv := []string{"/owned/projmux", "agent", "message", "send", "uid:" + envelope.BrokerEnvelope.Source.AgentUID, "--reply-to", ref, "--", "answer"}
	return hub, argv
}

// C-2 Guarantee: the tool is permitted while the sender Agent's current route
// is live and in the original's conversation, whatever Pane and activation
// generation the original named. Another conversation, an Agent the Registry
// no longer resolves, or a sender that is not live is refused.
func TestClaudeReplyToolPermitsAReplyToASenderRelaunchedIntoTheSameConversation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *claudeCoordinationTestFixture, *coremessage.Envelope)
		permit bool
	}{
		{"sender unchanged", func(*testing.T, *claudeCoordinationTestFixture, *coremessage.Envelope) {}, true},
		{"sender relaunched into the same conversation", func(_ *testing.T, _ *claudeCoordinationTestFixture, e *coremessage.Envelope) {
			previousActivation(&e.Source)
		}, true},
		{"sender relaunched into another conversation", func(t *testing.T, f *claudeCoordinationTestFixture, e *coremessage.Envelope) {
			previousActivation(&e.Source)
			e.Source.Incarnation = changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "session-before-relaunch" }).SessionIncarnation()
		}, false},
		{"sender the Registry no longer resolves", func(_ *testing.T, _ *claudeCoordinationTestFixture, e *coremessage.Envelope) {
			previousActivation(&e.Source)
			e.Source.AgentUID = "agent-gone"
		}, false},
		{"sender not live", func(t *testing.T, f *claudeCoordinationTestFixture, e *coremessage.Envelope) {
			previousActivation(&e.Source)
			changedClaudeRoute(t, f, true, func(a *coremetadata.ClaudeAuthorityRef) { a.Process.Start += "-replaced" })
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newClaudeCoordinationTestFixture(t)
			live, err := newLiveClaudeDialogueBroker(f.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			hub, argv := relaunchedSenderToolHub(t, f, "original-before-relaunch", func(e *coremessage.Envelope) { test.change(t, f, e) })
			if got := hub.permitsExplicitTool(argv, f.route, live); got != test.permit {
				t.Fatalf("permitsExplicitTool = %t, want %t", got, test.permit)
			}
		})
	}
}

// C-2 through the tool gate: a sender relaunched into the same conversation
// gets a ticket, and the ticket is consumed into the exact reply argv.
func TestClaudeReplyToolIssuesATicketForASenderRelaunchedIntoTheSameConversation(t *testing.T) {
	gate, peer := newClaudeReplyToolTestGate(t)
	f := newClaudeCoordinationTestFixture(t)
	live, err := newLiveClaudeDialogueBroker(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	hub, argv := relaunchedSenderToolHub(t, f, "original-before-relaunch", func(e *coremessage.Envelope) { previousActivation(&e.Source) })
	input := claudeReplyToolInput{ToolUseID: "tool-relaunched", Directory: gate.policy.Directory,
		Command: "'" + gate.policy.Executable + "' agent message send " + argv[4] + " --reply-to " + argv[6] + " -- '" + argv[8] + "'"}
	permit, err := gate.prepare(input, peer, f.route, hub, live)
	if err != nil || permit.Marker == "" {
		t.Fatalf("no ticket for a reply to a relaunched sender: %v", err)
	}
	result, err := gate.consume(permit.Marker, peer, f.route, hub, live)
	if err != nil || result.Argv[4] != argv[4] || result.Argv[6] != argv[6] || result.Argv[8] != argv[8] {
		t.Fatalf("ticket not consumed into the reply: %+v %v", result, err)
	}
}
