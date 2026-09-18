package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Writers emit the route incarnation scoped to the provider conversation, so
// a same-session SessionStart re-registration or a Codex reconnect leaves the
// records written before it current. The tests below drive the real writer
// (publicMessageRoute) and the real readers.

// reregisterClaudeAgent performs a SessionStart re-registration of the
// fixture's Pane, Agent, and activation generation through the Registry
// mutators, the way a compacted conversation's new helper does: same provider
// process, a new registration generation, and a new lease process.
func reregisterClaudeAgent(t *testing.T, f *claudeCoordinationTestFixture, sessionID string) coremetadata.AgentRouteRef {
	t.Helper()
	current, ok := f.route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok {
		t.Fatal("fixture route has no Claude authority")
	}
	authority := current
	authority.SessionID = sessionID
	authority.RegistrationGeneration = "registration-after-session-start"
	authority.LeaseProcess.Start = "test:session-start-helper"
	mutator := intmetadata.DefaultMutator()
	reg, err := intmetadata.NewStore(f.registryPath).Update(func(reg *coremetadata.Registry) error {
		if err := mutator.BeginClaudeRegistration(reg, f.paneUID, f.route.AgentUID, f.generation, authority); err != nil {
			return err
		}
		return mutator.RecordClaudeRegistration(reg, f.paneUID, f.route.AgentUID, f.generation, coremetadata.ClaudeRegistration{Authority: authority})
	})
	if err != nil {
		t.Fatal(err)
	}
	route, reason := coremetadata.ResolveAgentRoute(reg, f.route.AgentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	if route.Generation != f.route.Generation || route.Same(f.route) || route.FullIncarnation() == f.route.FullIncarnation() {
		t.Fatal("re-registration did not replace the helper authority within the activation generation")
	}
	return route
}

// reregisteredClaudeAdapter is a stand-in for the helper serving the new
// registration, not the real helper. Its explicit reply goes straight to the
// store's exact PutReply comparison. The real current helper, after a
// same-session re-registration, reads an original it did not deliver from the
// durable store (readRegistryCurrentOriginal) and commits through the same
// correlation; this test does not exercise that path.
type reregisteredClaudeAdapter struct {
	store    *messagestore.Store
	statuses int
	replies  int
	routes   []coremetadata.AgentRouteRef
}

func (a *reregisteredClaudeAdapter) Submit(_ context.Context, _ string, route coremetadata.AgentRouteRef, envelope coremessage.Envelope) (agentdelivery.Delivery, error) {
	a.routes = append(a.routes, route)
	return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateDelivered, Reason: "provider-handoff-acknowledged"}, nil
}

func (a *reregisteredClaudeAdapter) Status(_ context.Context, _ string, route coremetadata.AgentRouteRef, _ string) (agentdelivery.Delivery, error) {
	a.statuses++
	a.routes = append(a.routes, route)
	return agentdelivery.Delivery{}, nil
}

func (a *reregisteredClaudeAdapter) ExplicitReply(_ context.Context, _ string, source coremetadata.AgentRouteRef, reply coremessage.Envelope) (string, bool, error) {
	a.replies++
	a.routes = append(a.routes, source)
	_, created, err := a.store.PutReply(reply.ReplyTo, reply.MessageRef, reply.Payload, reply.Source, reply.Target, reply.AcceptedAt, reply.Deadline)
	return reply.MessageRef, created, err
}

// readyLeaseResolver resolves routes from the Registry file and treats the
// re-registered helper's lease and coordination eligibility as proved; those
// probes are the authority fences, which these tests do not exercise.
func readyLeaseResolver(registryPath string) liveAgentMessageRouteResolver {
	ready := func(string, coremetadata.AgentRouteRef) claudeProbeOutcome { return claudeProbeReady }
	return liveAgentMessageRouteResolver{registryPath: registryPath, leaseProbe: ready, eligibilityProbe: ready}
}

// TestAgentMessageStatusAndReplyCorrelationSurviveSameSessionReregistrationWithStandInHelper:
// a message written to a Claude target before a same-session SessionStart
// re-registration (the compact shape) stays current after it. Status runs the
// real reader. The reply leg proves only that the reply envelope's route
// correlation (the CLI `--reply-to` coremessage.ValidateReply and the store's
// exact PutReply comparison) accepts it. The helper is a stand-in whose reply
// goes straight to PutReply; the real current helper's durable-store read of
// an original it did not deliver is not exercised here (see
// reregisteredClaudeAdapter). A re-registration under another SessionID is a
// new conversation and stays stale.
func TestAgentMessageStatusAndReplyCorrelationSurviveSameSessionReregistrationWithStandInHelper(t *testing.T) {
	for _, test := range []struct {
		name    string
		session func(*claudeCoordinationTestFixture) string
		current bool
	}{
		{"same session", func(f *claudeCoordinationTestFixture) string { return f.sessionID }, true},
		{"other session", func(*claudeCoordinationTestFixture) string { return "session-after-clear" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newClaudeCoordinationTestFixture(t)
			store := messagestore.NewStore(t.TempDir())
			now := time.Now().UTC()
			pending := *dialogueForRoute("original-pending", f.route, now).BrokerEnvelope
			pending.Source = pending.Target
			answered := *dialogueForRoute("original-answered", f.route, now).BrokerEnvelope
			answered.Source = answered.Target
			if answered.Target != publicMessageRoute(f.route) || answered.Target.Incarnation != f.route.Incarnation() {
				t.Fatal("original target was not written by the current writer")
			}
			for _, envelope := range []coremessage.Envelope{pending, answered} {
				if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := store.Apply(answered.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: answered.MessageRef,
				ConversationRef: answered.ConversationRef, Target: answered.Target, ObservedAt: now, Reason: "provider-pipe-full-frame"}); err != nil {
				t.Fatal(err)
			}

			reregistered := reregisterClaudeAgent(t, f, test.session(f))
			adapter := &reregisteredClaudeAdapter{store: store}
			command := &agentCommand{messageStore: store, messageClaude: adapter,
				messagePaths: agentMessagePaths{registryPath: f.registryPath, loadRegistry: intmetadata.NewStore(f.registryPath).LoadReadOnly},
				messageRoute: readyLeaseResolver(f.registryPath)}

			var stdout bytes.Buffer
			if err := command.runMessageStatus([]string{pending.MessageRef, "-o", "json"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			var receipt agentMessageReceipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			stale := receipt.Delivery.State == coremessage.StateStale && receipt.Delivery.Reason == "target-activation-stale"
			if stale == test.current || (adapter.statuses == 1) != test.current {
				t.Fatalf("status delivery=%+v helper statuses=%d, want current=%t", receipt.Delivery, adapter.statuses, test.current)
			}
			if test.current && !adapter.routes[0].Same(reregistered) {
				t.Fatal("status did not ask the re-registered route")
			}

			output, err := runExplicitFixtureCommand(t, command, answered, "reply-after-session-start", "answer")
			reply, found, getErr := store.Reply(answered.MessageRef)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if !test.current {
				if err == nil || !strings.Contains(err.Error(), "invalid-explicit-reply-correlation") || found || adapter.replies != 0 {
					t.Fatalf("reply across a new session committed: %s %v found=%t replies=%d", output, err, found, adapter.replies)
				}
				return
			}
			if err != nil || strings.Contains(output, "invalid-explicit-reply-correlation") || !found || adapter.replies != 1 {
				t.Fatalf("reply after same-session re-registration refused: %s %v found=%t replies=%d", output, err, found, adapter.replies)
			}
			if reply.Envelope.Source != answered.Target || reply.Envelope.Target != answered.Source ||
				reply.Envelope.Source != publicMessageRoute(reregistered) || coremessage.ValidateReply(answered, reply.Envelope) != nil {
				t.Fatalf("reply routes do not answer the original: %+v", reply.Envelope)
			}
		})
	}
}

// twoCodexAgentsCommand is the two-Agent Codex fixture of
// TestAgentMessageReplyReturnsOnlyToOriginalSourceConversation. registry is
// read on every command, so a test may replace an authority between sends.
func twoCodexAgentsCommand(t *testing.T) (*agentCommand, *coremetadata.Registry, *string, *messagestore.Store) {
	t.Helper()
	_, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	sourceAgent, _ := registry.Agent("agt-alpha-codex")
	sourcePane, _ := registry.Pane("pan-alpha-codex")
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
	registry.Agents = append(registry.Agents, targetAgent)
	registry.Panes = append(registry.Panes, targetPane)

	private := messagestore.NewStore(t.TempDir())
	activePane := sourcePane.Metadata.UID
	active := func() (activeTargetObserver, bool) {
		return activeTargetObserver{paneID: "%7", paneUID: func() string { return activePane }}, true
	}
	cmd := &agentCommand{
		activeTarget: active,
		messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }},
		messageStore: private,
		messageRoute: liveAgentMessageRouteResolver{},
		messageNow:   messageFixtureNow,
		messageNewRef: func(prefix string) string {
			t.Fatalf("explicit references must not mint %s", prefix)
			return ""
		},
	}
	return cmd, &registry, &activePane, private
}

// TestAgentMessageReplySurvivesCodexReconnect: a Codex reconnect (a new
// connection or binding epoch) on either side between a message and its
// `--reply-to` answer leaves the correlation intact.
func TestAgentMessageReplySurvivesCodexReconnect(t *testing.T) {
	for _, test := range []struct {
		name   string
		pane   string
		change func(*coremetadata.CodexAuthorityRef)
	}{
		{"original sender connection epoch", "pan-alpha-codex", func(a *coremetadata.CodexAuthorityRef) { a.ConnectionEpoch++ }},
		{"original sender binding epoch", "pan-alpha-codex", func(a *coremetadata.CodexAuthorityRef) { a.BindingEpoch++ }},
		{"replier connection epoch", "pan-target-codex", func(a *coremetadata.CodexAuthorityRef) { a.ConnectionEpoch++ }},
		{"replier binding epoch", "pan-target-codex", func(a *coremetadata.CodexAuthorityRef) { a.BindingEpoch++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, registry, activePane, private := twoCodexAgentsCommand(t)
			// No native control seam is wired, so each accepted envelope ends
			// codex-native-control-unconfigured after the correlation decision.
			unconfigured := func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "exact Agent native control is not configured")
			}
			if _, _, err := runRoute(t, cmd, "message", "send", "uid:agt-target-codex",
				"--message-ref", "message-original", "--", "request"); !unconfigured(err) {
				t.Fatal(err)
			}
			original, _, _ := private.Get("message-original")
			agentUID := map[string]string{"pan-alpha-codex": "agt-alpha-codex", "pan-target-codex": "agt-target-codex"}[test.pane]
			before := mustMessageRoute(t, *registry, agentUID)
			pane, _ := registry.Pane(test.pane)
			authority := *pane.Status.Activation.Codex.Authority
			test.change(&authority)
			pane.Status.Activation.Codex.Authority = &authority
			after := mustMessageRoute(t, *registry, agentUID)
			if before.Same(after) || before.FullIncarnation() == after.FullIncarnation() || before.Generation != after.Generation {
				t.Fatal("fixture did not reconnect the Codex authority within the activation generation")
			}

			*activePane = "pan-target-codex"
			output, _, err := runRoute(t, cmd, "message", "send", "uid:agt-alpha-codex",
				"--message-ref", "message-reply", "--reply-to", "message-original", "--", "response")
			if !unconfigured(err) || strings.Contains(err.Error(), "invalid-explicit-reply-correlation") {
				t.Fatalf("reply after reconnect: %s %v", output, err)
			}
			reply, found, getErr := private.Get("message-reply")
			if getErr != nil || !found {
				t.Fatalf("reply after reconnect not stored: found=%t err=%v", found, getErr)
			}
			if err := coremessage.ValidateReply(original.Envelope, reply.Envelope); err != nil ||
				reply.Envelope.Source != original.Envelope.Target || reply.Envelope.Target != original.Envelope.Source {
				t.Fatalf("reply correlation original=%+v reply=%+v err=%v", original.Envelope, reply.Envelope, err)
			}
			if reply.Envelope.Source.Incarnation != publicMessageRoute(mustMessageRoute(t, *registry, "agt-target-codex")).Incarnation ||
				reply.Envelope.Target.Incarnation != publicMessageRoute(mustMessageRoute(t, *registry, "agt-alpha-codex")).Incarnation {
				t.Fatal("reply routes are not the current writer's values")
			}
		})
	}
}

// TestAgentMessageReplyToKeepsOriginalIncarnationForm: an original written by
// an earlier build carries the full digest. After the writer switch it is
// still current for `agent message status`, and a `--reply-to` answer carries
// that full digest back rather than the value the writer now emits; an
// original carrying the session value is answered with the session value.
func TestAgentMessageReplyToKeepsOriginalIncarnationForm(t *testing.T) {
	for _, test := range []struct {
		name        string
		incarnation func(*claudeCoordinationTestFixture) string
	}{
		{"full digest from an earlier build", func(f *claudeCoordinationTestFixture) string { return f.route.FullIncarnation() }},
		{"session value", func(f *claudeCoordinationTestFixture) string { return f.route.Incarnation() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, command, broker, writer, original := sessionReplyCommandFixture(t, test.incarnation, true)
			written := publicMessageRoute(f.route).Incarnation
			if written != f.route.Incarnation() || written == f.route.FullIncarnation() {
				t.Fatal("the writer still emits the full digest")
			}

			// Status reads a non-terminal original carrying the same form.
			pending := original
			pending.MessageRef, pending.ConversationRef = "original-pending", "conversation-original-pending"
			if _, _, err := broker.store.PutAccepted(pending, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			status := &statusOnlyClaudeAdapter{}
			statusCommand := &agentCommand{messageStore: broker.store, messageClaude: status,
				messagePaths: command.messagePaths, messageRoute: command.messageRoute}
			var stdout bytes.Buffer
			if err := statusCommand.runMessageStatus([]string{pending.MessageRef, "-o", "json"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			var receipt agentMessageReceipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Delivery.Reason == "target-activation-stale" || status.statuses != 1 {
				t.Fatalf("original carrying %s read as stale: %+v", test.name, receipt.Delivery)
			}

			writes := writer.writes
			output, err := runExplicitFixtureCommand(t, command, original, "reply-form", "answer")
			reply, found, getErr := broker.store.Get("reply-form")
			if getErr != nil {
				t.Fatal(getErr)
			}
			if err != nil || strings.Contains(output, "invalid-explicit-reply-correlation") || !found || writer.writes != writes+1 {
				t.Fatalf("reply refused: %s %v found=%t writes=%d", output, err, found, writer.writes-writes)
			}
			want := test.incarnation(f)
			if reply.Envelope.Source.Incarnation != want || reply.Envelope.Target.Incarnation != want ||
				reply.Envelope.Source != original.Target || reply.Envelope.Target != original.Source ||
				coremessage.ValidateReply(original, reply.Envelope) != nil {
				t.Fatalf("reply did not keep the original's incarnation form %q: %+v", want, reply.Envelope)
			}
		})
	}
}
