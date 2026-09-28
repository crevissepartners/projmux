package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// A relaunch into the same conversation keeps the Agent and its provider
// conversation and replaces its Pane and activation generation. These tests
// make that route change on the Registry the CLI, the Claude helper, and the
// store read, and send the reply through each of them.

// relaunchedCodexDialogue is two Codex Agents in one Registry. The original
// goes from alpha to target; relaunch replaces one Agent's Pane and
// activation generation and keeps its thread.
type relaunchedCodexDialogue struct {
	registry   coremetadata.Registry
	store      *messagestore.Store
	command    *agentCommand
	activePane string
}

func newRelaunchedCodexDialogue(t *testing.T) *relaunchedCodexDialogue {
	t.Helper()
	_, registryStore, _ := exactControlCLICommand(t)
	d := &relaunchedCodexDialogue{registry: registryStore.registry.Clone(), store: messagestore.NewStore(t.TempDir())}
	sourceAgent, _ := d.registry.Agent("agt-alpha-codex")
	sourcePane, _ := d.registry.Pane("pan-alpha-codex")
	targetAgent := sourceAgent.Clone()
	targetAgent.Metadata.UID = "agt-target-codex"
	targetAgent.Metadata.Name = "target-codex"
	targetAgent.Status.PaneRef = "pan-target-codex"
	targetAgent.Status.SessionRef.Codex.ThreadID = "thread-target"
	targetPane := sourcePane.Clone()
	targetPane.Metadata.UID = "pan-target-codex"
	targetPane.Metadata.Name = "target-codex-pane"
	targetPane.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: targetAgent.Metadata.UID}
	targetPane.Status.Activation.AgentUID = targetAgent.Metadata.UID
	targetPane.Status.Activation.Generation = "generation-target"
	targetPane.Status.Activation.RuntimeID = "%8"
	targetAuthority := *sourcePane.Status.Activation.Codex.Authority
	targetPane.Status.Activation.Codex = &coremetadata.CodexActivationBinding{ThreadID: "thread-target", TurnID: "turn-target", Authority: &targetAuthority}
	d.registry.Agents = append(d.registry.Agents, targetAgent)
	d.registry.Panes = append(d.registry.Panes, targetPane)
	d.activePane = sourcePane.Metadata.UID
	d.command = &agentCommand{
		activeTarget: func() (activeTargetObserver, bool) {
			return activeTargetObserver{paneID: "%7", paneUID: func() string { return d.activePane }}, true
		},
		messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return d.registry.Clone(), nil }},
		messageStore: d.store,
		messageRoute: liveAgentMessageRouteResolver{},
		messageNow:   messageFixtureNow,
		messageNewRef: func(prefix string) string {
			t.Fatalf("explicit references must not mint %s", prefix)
			return ""
		},
	}
	return d
}

// relaunch gives agentUID a new Pane and activation generation, the way
// `agent relaunch` does. Its conversation (thread) is unchanged.
func (d *relaunchedCodexDialogue) relaunch(t *testing.T, agentUID string) string {
	t.Helper()
	agentIndex, paneIndex := -1, -1
	for i := range d.registry.Agents {
		if d.registry.Agents[i].Metadata.UID == agentUID {
			agentIndex = i
		}
	}
	if agentIndex < 0 {
		t.Fatalf("agent %s not in fixture", agentUID)
	}
	for i := range d.registry.Panes {
		if d.registry.Panes[i].Metadata.UID == d.registry.Agents[agentIndex].Status.PaneRef {
			paneIndex = i
		}
	}
	if paneIndex < 0 {
		t.Fatalf("agent %s has no pane", agentUID)
	}
	pane := &d.registry.Panes[paneIndex]
	relaunched := pane.Metadata.UID + "-relaunched"
	pane.Metadata.UID = relaunched
	pane.Metadata.Name += "-relaunched"
	pane.Status.Activation.Generation += "-relaunched"
	pane.Status.Activation.RuntimeID = "%9"
	d.registry.Agents[agentIndex].Status.PaneRef = relaunched
	return relaunched
}

func (d *relaunchedCodexDialogue) send(t *testing.T, target string, args ...string) error {
	t.Helper()
	_, _, err := runRoute(t, d.command, append([]string{"message", "send", "uid:" + target}, args...)...)
	// The fixture wires no native control seam, so every accepted envelope
	// terminates as codex-native-control-unconfigured. Correlation is decided
	// before that push.
	if err != nil && strings.Contains(err.Error(), "exact Agent native control is not configured") {
		return nil
	}
	return err
}

// C-1 through the CLI and the store: either party relaunched into the same
// conversation, then the reply to the earlier message is accepted, and it is
// addressed to the current routes, not to the Pane the original named.
func TestAgentMessageReplyCorrelatesAfterACodexRelaunch(t *testing.T) {
	for _, relaunched := range []string{"agt-alpha-codex", "agt-target-codex"} {
		t.Run(relaunched, func(t *testing.T) {
			d := newRelaunchedCodexDialogue(t)
			if err := d.send(t, "agt-target-codex", "--message-ref", "message-original", "--", "request"); err != nil {
				t.Fatal(err)
			}
			d.relaunch(t, relaunched)
			replier, _ := d.registry.Agent("agt-target-codex")
			d.activePane = replier.Status.PaneRef
			if err := d.send(t, "agt-alpha-codex", "--message-ref", "message-reply", "--reply-to", "message-original", "--", "response"); err != nil {
				t.Fatalf("reply after relaunch of %s refused: %v", relaunched, err)
			}
			original, _, _ := d.store.Get("message-original")
			reply, found, err := d.store.Get("message-reply")
			if err != nil || !found {
				t.Fatalf("reply not stored: found=%t err=%v", found, err)
			}
			if err := coremessage.ValidateReply(original.Envelope, reply.Envelope); err != nil {
				t.Fatal(err)
			}
			current := func(agent string) coremessage.Route {
				return publicMessageRoute(mustMessageRoute(t, d.registry, agent))
			}
			if reply.Envelope.Source != current("agt-target-codex") || reply.Envelope.Target != current("agt-alpha-codex") {
				t.Fatalf("reply not addressed to the current routes: %+v", reply.Envelope)
			}
		})
	}
}

// relaunchedClaudeReply is the Claude helper path: the live broker's Registry
// fence, its current-route proof, and the store's reply commit. The fixture
// Agent answers itself, so moving the original's source or target off the
// current Pane is the sender's or the receiver's relaunch.
func relaunchedClaudeReply(t *testing.T, relaunch func(*coremessage.Envelope, *claudeCoordinationTestFixture)) (*messagestore.Store, *replyFrameWriter, coremessage.Envelope, string, error) {
	t.Helper()
	f := newClaudeCoordinationTestFixture(t)
	broker, err := newLiveClaudeDialogueBroker(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.registryPath)))
	writer := &replyFrameWriter{}
	f.server.broker, f.server.poster = broker, replyTestPoster{writer}
	original := *dialogueForRoute("original-before-relaunch", f.route, time.Now().UTC()).BrokerEnvelope
	original.Source = original.Target
	relaunch(&original, f)
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef,
		ConversationRef: original.ConversationRef, Target: original.Target, ObservedAt: time.Now().UTC(), Reason: "provider-pipe-full-frame"}); err != nil {
		t.Fatal(err)
	}
	command := &agentCommand{messageStore: store, messageClaude: liveAgentMessageClaudeAdapter{},
		messagePaths: agentMessagePaths{registryPath: f.registryPath, loadRegistry: intmetadata.NewStore(f.registryPath).LoadReadOnly},
		messageRoute: &traceMessageRouteResolver{route: f.route}}
	output, err := runExplicitFixtureCommand(t, command, original, "reply-after-relaunch", "answer")
	return store, writer, original, output, err
}

func previousActivation(route *coremessage.Route) {
	route.PaneUID, route.ActivationGeneration = "pane-before-relaunch", "generation-before-relaunch"
}

// C-1 through the Claude helper: either party relaunched into the same
// session, then the reply commits and is delivered to the current activation.
func TestClaudeExplicitReplyCorrelatesAfterARelaunch(t *testing.T) {
	for _, test := range []struct {
		name     string
		relaunch func(*coremessage.Envelope, *claudeCoordinationTestFixture)
	}{
		{"original sender relaunched", func(e *coremessage.Envelope, _ *claudeCoordinationTestFixture) { previousActivation(&e.Source) }},
		{"replying receiver relaunched", func(e *coremessage.Envelope, _ *claudeCoordinationTestFixture) { previousActivation(&e.Target) }},
		{"both relaunched", func(e *coremessage.Envelope, _ *claudeCoordinationTestFixture) {
			previousActivation(&e.Source)
			previousActivation(&e.Target)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, writer, original, output, err := relaunchedClaudeReply(t, test.relaunch)
			if err != nil {
				t.Fatalf("reply after relaunch refused: %s %v", output, err)
			}
			reply, found, getErr := store.Get("reply-after-relaunch")
			if getErr != nil || !found || reply.Delivery.State != coremessage.StateDelivered || writer.writes != 1 {
				t.Fatalf("reply not delivered: %+v found=%t err=%v writes=%d", reply, found, getErr, writer.writes)
			}
			if reply.Envelope.ReplyTo != original.MessageRef || reply.Envelope.ConversationRef != original.ConversationRef ||
				reply.Envelope.Source.PaneUID == "pane-before-relaunch" || reply.Envelope.Target.PaneUID == "pane-before-relaunch" {
				t.Fatalf("reply not on the current routes of the original conversation: %+v", reply.Envelope)
			}
		})
	}
}

// C-1 Failure.Detection: a relaunch that left the Agent in another session is
// named as such, and nothing is stored or written.
func TestClaudeExplicitReplyAfterAConversationChangeNamesIt(t *testing.T) {
	store, writer, original, output, err := relaunchedClaudeReply(t, func(e *coremessage.Envelope, f *claudeCoordinationTestFixture) {
		previousActivation(&e.Target)
		e.Target.Incarnation = changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "session-before-relaunch" }).SessionIncarnation()
		e.Source = e.Target
	})
	if err == nil || !strings.Contains(err.Error(), coremessage.ReasonExplicitReplyConversationChanged) ||
		strings.Contains(err.Error(), "invalid-explicit-reply-correlation") || !strings.Contains(err.Error(), "without --reply-to") {
		t.Fatalf("conversation change not named: %s %v", output, err)
	}
	if _, found, _ := store.Get("reply-after-relaunch"); found || writer.writes != 0 {
		t.Fatalf("refused reply stored=%t writes=%d", found, writer.writes)
	}
	if _, found, _ := store.Reply(original.MessageRef); found {
		t.Fatal("refused reply recorded against the original")
	}
}

// The Claude helper and its broker name a changed conversation themselves, so
// a caller that reaches the helper without the CLI's check (an older CLI, or
// the reply tool) is told the same cause.
func TestClaudeHelperNamesAChangedConversation(t *testing.T) {
	f := newClaudeCoordinationTestFixture(t)
	store := messagestore.NewStore(t.TempDir())
	broker := &durableReplyTestBroker{store: store, current: true, registryPath: f.registryPath}
	original := *dialogueForRoute("original-other-session", f.route, time.Now().UTC()).BrokerEnvelope
	original.Source = original.Target
	previousActivation(&original.Source)
	original.Source.Incarnation = changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "session-before-relaunch" }).SessionIncarnation()
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef,
		ConversationRef: original.ConversationRef, Target: original.Target, ObservedAt: time.Now().UTC(), Reason: "provider-pipe-full-frame"}); err != nil {
		t.Fatal(err)
	}
	reply := explicitTestReply(original, "answer")
	reply.Target = publicMessageRoute(f.route)
	got := newClaudeCoordinationHub().commitExplicitReply(reply, f.route, broker)
	if got.Kind != "reply-refused" || got.Reason != coremessage.ReasonExplicitReplyConversationChanged {
		t.Fatalf("helper did not name the changed conversation: %+v", got)
	}
	live, err := newLiveClaudeDialogueBroker(f.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := live.CommitReply(original, reply); created || !errors.Is(err, coremessage.ErrReplyConversationChanged) {
		t.Fatalf("broker CommitReply = %t %v, want the changed conversation", created, err)
	}
	if _, found, _ := store.Reply(original.MessageRef); found {
		t.Fatal("refused reply stored")
	}
	if !strings.Contains(explicitReplyRefusal(got).Error(), "send a new message without --reply-to") {
		t.Fatalf("refusal names no action: %v", explicitReplyRefusal(got))
	}
}
