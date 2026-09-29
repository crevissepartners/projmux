package app

import (
	"path/filepath"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// The replying Agent was relaunched after the original was delivered to it.
// The new activation's helper starts with an empty hub, so the original is
// only in the durable store, and its Target names the Pane and activation
// generation the replier had before. The original is a self-message, so the
// sender resolves through the live broker's real Registry.

// replierRelaunchedOriginal stores one original to the fixture Agent's
// previous activation, after change has rewritten it, and marks it delivered
// unless delivered is false. It returns the reply tool argv answering it.
func replierRelaunchedOriginal(t *testing.T, f *claudeCoordinationTestFixture, ref string, delivered bool,
	change func(*coremessage.Envelope),
) (*messagestore.Store, coremessage.Envelope, []string) {
	t.Helper()
	original := *dialogueForRoute(ref, f.route, time.Now().UTC()).BrokerEnvelope
	original.Source = original.Target
	previousActivation(&original.Target)
	change(&original)
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.registryPath)))
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if delivered {
		if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef,
			ConversationRef: original.ConversationRef, Target: original.Target, ObservedAt: time.Now().UTC(), Reason: "provider-pipe-full-frame"}); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"/owned/projmux", "agent", "message", "send", "uid:" + f.route.AgentUID, "--reply-to", ref, "--", "answer"}
	return store, original, argv
}

// replyFromCurrentReplier is the reply the CLI builds for the relaunched
// replier: its current route, in the conversation shape the original used.
func replyFromCurrentReplier(route coremetadata.AgentRouteRef, original coremessage.Envelope, text string) coremessage.Envelope {
	reply := explicitTestReply(original, text)
	reply.Source = publicMessageRoute(route)
	if route.AcceptsIncarnation(original.Target.Incarnation) {
		reply.Source.Incarnation = original.Target.Incarnation
	}
	return reply
}

// C-1 Guarantee: the relaunched replier's Registry-current helper is permitted
// to answer the original its previous activation was delivered, the ticket is
// consumed into the exact reply argv, and the commit path accepts that reply.
func TestClaudeReplyToolPermitsAReplyToAMessageTheReplierReceivedBeforeItsRelaunch(t *testing.T) {
	gate, peer := newClaudeReplyToolTestGate(t)
	f := newClaudeCoordinationTestFixture(t)
	live, err := newLiveClaudeDialogueBroker(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	store, original, argv := replierRelaunchedOriginal(t, f, "original-before-replier-relaunch", true, func(*coremessage.Envelope) {})
	if messageRouteAccepts(f.route, original.Target) {
		t.Fatal("fixture: the original still targets the current activation")
	}
	hub := qualifiedPushHub(time.Now().UTC())
	if !hub.permitsExplicitTool(argv, f.route, live) {
		t.Fatal("reply tool refused an original the replier received before its relaunch")
	}
	input := claudeReplyToolInput{ToolUseID: "tool-replier-relaunched", Directory: gate.policy.Directory,
		Command: "'" + gate.policy.Executable + "' agent message send " + argv[4] + " --reply-to " + argv[6] + " -- '" + argv[8] + "'"}
	permit, err := gate.prepare(input, peer, f.route, hub, live)
	if err != nil || permit.Marker == "" {
		t.Fatalf("no ticket for a reply after the replier's relaunch: %v", err)
	}
	result, err := gate.consume(permit.Marker, peer, f.route, hub, live)
	if err != nil || result.Argv[4] != argv[4] || result.Argv[6] != argv[6] || result.Argv[8] != argv[8] {
		t.Fatalf("ticket not consumed into the reply: %+v %v", result, err)
	}
	if hub.messages[original.MessageRef] != nil {
		t.Fatal("store-found original entered the helper's pushed messages")
	}
	got := hub.commitExplicitReply(replyFromCurrentReplier(f.route, original, argv[8]), f.route, live)
	if got.Kind != "reply-accepted" || !got.ReplyCreated {
		t.Fatalf("commit path refused the permitted reply: %+v", got)
	}
	if _, found, err := store.Reply(original.MessageRef); err != nil || !found {
		t.Fatalf("reply not stored: found=%t err=%v", found, err)
	}
	if hub.permitsExplicitTool(argv, f.route, live) {
		t.Fatal("reply tool permitted a second reply to an answered original")
	}
}

// C-1 Scope and Non-Guarantee: every other fence still holds for an original
// read from the store.
func TestClaudeReplyToolRefusesStoredOriginalsOutsideTheRelaunchedConversation(t *testing.T) {
	for _, test := range []struct {
		name      string
		delivered bool
		change    func(*testing.T, *claudeCoordinationTestFixture, *coremessage.Envelope)
		setup     func(*testing.T, *claudeCoordinationTestFixture, *liveClaudeDialogueBroker, coremessage.Envelope, *claudeCoordinationHub) coremetadata.AgentRouteRef
	}{
		{name: "replier relaunched into another conversation", delivered: true,
			change: func(t *testing.T, f *claudeCoordinationTestFixture, e *coremessage.Envelope) {
				e.Target.Incarnation = changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "session-before-relaunch" }).SessionIncarnation()
			}},
		{name: "original targets another Agent", delivered: true,
			change: func(_ *testing.T, _ *claudeCoordinationTestFixture, e *coremessage.Envelope) {
				e.Target.AgentUID = "agent-other"
			}},
		{name: "original not delivered", delivered: false},
		{name: "original deadline passed", delivered: true,
			setup: func(_ *testing.T, f *claudeCoordinationTestFixture, _ *liveClaudeDialogueBroker, original coremessage.Envelope, hub *claudeCoordinationHub) coremetadata.AgentRouteRef {
				hub.now = func() time.Time { return original.Deadline.Add(time.Second) }
				return f.route
			}},
		{name: "store already holds a reply", delivered: true,
			setup: func(t *testing.T, f *claudeCoordinationTestFixture, live *liveClaudeDialogueBroker, original coremessage.Envelope, _ *claudeCoordinationHub) coremetadata.AgentRouteRef {
				// An earlier helper of the same activation answered it.
				if got := newClaudeCoordinationHub().commitExplicitReply(replyFromCurrentReplier(f.route, original, "earlier answer"), f.route, live); got.Kind != "reply-accepted" {
					t.Fatalf("fixture: earlier reply refused: %+v", got)
				}
				return f.route
			}},
		{name: "helper not Registry-current", delivered: true,
			setup: func(t *testing.T, f *claudeCoordinationTestFixture, _ *liveClaudeDialogueBroker, _ coremessage.Envelope, _ *claudeCoordinationHub) coremetadata.AgentRouteRef {
				return changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:unregistered-helper" })
			}},
		{name: "operator original", delivered: true,
			change: func(_ *testing.T, _ *claudeCoordinationTestFixture, e *coremessage.Envelope) {
				operator := operatorDialogueEnvelope(e.MessageRef, e.Deadline).BrokerEnvelope
				e.Origin, e.Source, e.Authority = operator.Origin, coremessage.Route{}, operator.Authority
			}},
		{name: "unqualified new activation", delivered: true,
			setup: func(_ *testing.T, f *claudeCoordinationTestFixture, _ *liveClaudeDialogueBroker, _ coremessage.Envelope, hub *claudeCoordinationHub) coremetadata.AgentRouteRef {
				hub.qualifiedVersion = ""
				return f.route
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newClaudeCoordinationTestFixture(t)
			live, err := newLiveClaudeDialogueBroker(f.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			_, original, argv := replierRelaunchedOriginal(t, f, "original-before-replier-relaunch", test.delivered, func(e *coremessage.Envelope) {
				if test.change != nil {
					test.change(t, f, e)
				}
			})
			hub := qualifiedPushHub(time.Now().UTC())
			route := f.route
			if test.setup != nil {
				route = test.setup(t, f, live, original, hub)
			}
			if hub.permitsExplicitTool(argv, route, live) {
				t.Fatal("reply tool permitted a reply the relaunch rule does not cover")
			}
		})
	}
}
