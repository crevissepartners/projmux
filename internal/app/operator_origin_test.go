package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// operatorDialogueEnvelope is dialogueEnvelope as operator input: the same
// Claude target, an operator origin, and no source route.
func operatorDialogueEnvelope(ref string, deadline time.Time) claudeCoordinationEnvelope {
	private := dialogueEnvelope(ref, deadline)
	private.BrokerEnvelope.Origin = coremessage.OperatorWebOrigin()
	private.BrokerEnvelope.Source = coremessage.Route{}
	private.BrokerEnvelope.Authority = coremessage.OperatorAuthority()
	private.Source = claudeCoordinationSourceOf(coremessage.OperatorAuthority())
	return private
}

func TestClaudeOperatorFrameShapeIsPinned(t *testing.T) {
	now := time.Unix(71_000, 0).UTC()
	envelope := operatorDialogueEnvelope("message-frame-operator", now.Add(time.Minute))
	if err := envelope.BrokerEnvelope.Validate(); err != nil {
		t.Fatalf("operator fixture: %v", err)
	}
	content, err := providerCoordinationContent(envelope, "/usr/bin/projmux")
	if err != nil {
		t.Fatalf("provider content: %v", err)
	}
	const want = `{"kind":"projmux-coordination","schemaVersion":2,` +
		`"authority":"untrusted-coordination-only","messageRef":"message-frame-operator",` +
		`"conversationRef":"conversation-message-frame-operator",` +
		`"source":{"kind":"operator","client":"web"},` +
		`"target":{"agentUID":"claude-agent","provider":"claude"},` +
		`"payload":"semantic marker",` +
		`"sourceNotice":"Operator input that arrived through the projmux web client; projmux did not verify the person.",` +
		`"replyAction":""}`
	if content != want {
		t.Fatalf("operator frame =\n%s\nwant\n%s", content, want)
	}
	// The sender's size pre-check renders the peer replyAction for Agent
	// frames; operator input has no reply at all, so it sizes the frame sent.
	sized, err := renderProviderCoordinationContent(envelope, "/usr/bin/projmux", true)
	if err != nil || sized != want {
		t.Fatalf("sized operator frame = %s, %v", sized, err)
	}
}

func TestClaudePushHubDeliversOperatorInputWithoutASourceRoute(t *testing.T) {
	now := time.Unix(72_000, 0).UTC()
	hub := qualifiedPushHub(now)
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	envelope := operatorDialogueEnvelope("message-operator-push", now.Add(time.Minute))
	got := hub.submitPush(envelope, broker, poster)
	if got.State != agentdelivery.StateDelivered || poster.calls != 1 || broker.handoffs != 1 || broker.deliveries != 1 {
		t.Fatalf("delivery=%+v writes=%d handoffs=%d deliveries=%d", got, poster.calls, broker.handoffs, broker.deliveries)
	}
	for _, want := range []string{`"source":{"kind":"operator","client":"web"}`, `"replyAction":""`, coordinationOperatorSourceNotice} {
		if !strings.Contains(poster.content, want) {
			t.Fatalf("content %s lacks %s", poster.content, want)
		}
	}
	if strings.Contains(poster.content, "uid:") || strings.Contains(poster.content, "agentUID\":\"\"") {
		t.Fatalf("operator frame names an empty Agent: %s", poster.content)
	}
}

func TestClaudePrivateEnvelopeSourceMustRestateItsDurableAuthority(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	operator := *operatorDialogueEnvelope("message-operator-private", now.Add(time.Minute)).BrokerEnvelope
	operator.Target = publicMessageRoute(fixture.route)
	private := claudePrivateCoordinationEnvelope(fixture.target, operator)
	if !private.valid(now, fixture.route) {
		t.Fatal("operator private envelope refused")
	}
	private.Source = claudeCoordinationSourceOf(coremessage.PeerAuthority())
	if private.valid(now, fixture.route) {
		t.Fatal("operator durable envelope crossed with a peer private source")
	}
	peer := dialogueForRoute("message-peer-private", fixture.route, now)
	if peer.Source != claudeCoordinationSourceOf(coremessage.PeerAuthority()) || !peer.valid(now, fixture.route) {
		t.Fatalf("peer private source = %+v", peer.Source)
	}
	peer.Source = claudeCoordinationSourceOf(coremessage.OperatorAuthority())
	if peer.valid(now, fixture.route) {
		t.Fatal("peer durable envelope crossed with an operator private source")
	}
}

// TestLiveClaudeBrokerHandsOffOperatorInputOnItsTargetAlone runs the live
// broker's handoff and delivery records over a real store: with no source
// route, only the target is proved current.
func TestLiveClaudeBrokerHandsOffOperatorInputOnItsTargetAlone(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envelope := *operatorDialogueEnvelope("message-operator-broker", now.Add(time.Minute)).BrokerEnvelope
	envelope.AcceptedAt = now
	envelope.Target = publicMessageRoute(fixture.route)
	if !broker.Current(envelope) {
		t.Fatal("operator input to a live target is not current")
	}
	stale := envelope
	stale.Target.Incarnation = "route-replaced"
	if broker.Current(stale) {
		t.Fatal("operator input to a replaced target is current")
	}
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(fixture.registryPath)))
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if err := broker.MarkHandoff(envelope); err != nil {
		t.Fatalf("MarkHandoff: %v", err)
	}
	if err := broker.MarkDelivered(envelope, now); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	record, found, err := store.Get(envelope.MessageRef)
	if err != nil || !found || record.Delivery.State != coremessage.StateDelivered || !record.HandoffObserved || !record.Envelope.Operator() {
		t.Fatalf("record = (%+v, %t, %v)", record, found, err)
	}
}

func TestClaudeExplicitReplyToOperatorInputIsRefusedWithItsReasonToken(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	hub := qualifiedPushHub(now)
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	private := operatorDialogueEnvelope("message-operator-reply", now.Add(time.Minute))
	private.Target = fixture.target
	private.BrokerEnvelope.Target = publicMessageRoute(fixture.route)
	if got := hub.submitPush(private, broker, poster); got.State != agentdelivery.StateDelivered {
		t.Fatalf("operator push = %+v", got)
	}
	original := *private.BrokerEnvelope
	reply := coremessage.Envelope{Version: coremessage.Version, MessageRef: "reply-" + original.MessageRef,
		ConversationRef: original.ConversationRef, ReplyTo: original.MessageRef, Source: original.Target,
		Target: coremessage.Route{AgentUID: "codex-agent", PaneUID: "codex-pane", ActivationGeneration: "codex-generation",
			Provider: "codex", Incarnation: "codex-incarnation"},
		Authority: coremessage.PeerAuthority(), Payload: "answer", AcceptedAt: now, Deadline: original.Deadline}
	response := hub.commitExplicitReply(reply, fixture.route, broker)
	if response.Kind != "reply-refused" || response.Reason != coremessage.ReasonExplicitReplyOperatorOrigin || broker.replies != 0 {
		t.Fatalf("reply response = %+v replies=%d", response, broker.replies)
	}
	argv := []string{"/usr/bin/projmux", "agent", "message", "send", "uid:", "--reply-to", original.MessageRef, "--", "answer"}
	if hub.permitsExplicitTool(argv, fixture.route, broker) {
		t.Fatal("reply tool permitted a reply to operator input")
	}
}

func operatorStatusRecord(t *testing.T, store *messagestore.Store, ref string, now time.Time) messagestore.Record {
	t.Helper()
	envelope := *operatorDialogueEnvelope(ref, now.Add(time.Minute)).BrokerEnvelope
	envelope.AcceptedAt = now
	if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	record, _, err := store.Apply(ref, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: ref,
		ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAgentMessageSendReplyToOperatorInputIsRefusedWithItsReasonToken(t *testing.T) {
	now := time.Now().UTC()
	store := messagestore.NewStore(t.TempDir())
	original := operatorStatusRecord(t, store, "message-operator-original", now)
	cmd := &agentCommand{messageStore: store, messageNow: func() time.Time { return now }}
	stdout, _, err := runRoute(t, cmd, "message", "send", "uid:any-agent", "--reply-to", original.Envelope.MessageRef, "--", "answer")
	if err == nil || !strings.Contains(err.Error(), coremessage.ReasonExplicitReplyOperatorOrigin) ||
		!strings.Contains(err.Error(), "replyTo="+original.Envelope.MessageRef) || stdout != "" {
		t.Fatalf("reply to operator input: stdout=%q err=%v", stdout, err)
	}
	if reply, found, err := store.Reply(original.Envelope.MessageRef); err != nil || found {
		t.Fatalf("refused reply stored an attempt: (%+v, %t, %v)", reply, found, err)
	}
}

// TestAgentMessageStatusLabelsOperatorInputAndKeepsAgentOutput pins status for
// both origins. The Agent receipt is the exact text and JSON it was before
// Origin; operator input gains its label and loses the source it never had.
func TestAgentMessageStatusLabelsOperatorInputAndKeepsAgentOutput(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store := messagestore.NewStore(t.TempDir())
	operatorStatusRecord(t, store, "message-operator-status", now)
	agent := *dialogueEnvelope("message-agent-status", now.Add(time.Minute)).BrokerEnvelope
	agent.AcceptedAt = now
	if _, _, err := store.PutAccepted(agent, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(agent.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: agent.MessageRef,
		ConversationRef: agent.ConversationRef, Target: agent.Target, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	cmd := &agentCommand{messageStore: store, messageNow: func() time.Time { return now }}
	status := func(args ...string) string {
		t.Helper()
		stdout, _, err := runRoute(t, cmd, append([]string{"message", "status"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return stdout
	}
	if got := status("message-agent-status"); got != "message-agent-status\tdelivered\n" {
		t.Fatalf("agent text status = %q", got)
	}
	stamp, deadline := now.Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339)
	agentJSON := `{"version":2,"messageRef":"message-agent-status","conversationRef":"conversation-message-agent-status",` +
		`"source":{"agentUID":"codex-agent","paneUID":"codex-pane","activationGeneration":"codex-generation","provider":"codex","incarnation":"codex-incarnation"},` +
		`"target":{"agentUID":"claude-agent","paneUID":"claude-pane","activationGeneration":"claude-generation","provider":"claude","incarnation":"claude-incarnation"},` +
		`"delivery":{"messageRef":"message-agent-status","conversationRef":"conversation-message-agent-status","state":"delivered","reason":"unspecified","acceptedAt":"` + stamp + `","terminalAt":"` + stamp + `"},` +
		`"deadline":"` + deadline + `"}` + "\n"
	if got := status("message-agent-status", "-o", "json"); got != agentJSON {
		t.Fatalf("agent JSON status =\n%s\nwant\n%s", got, agentJSON)
	}
	if got := status("message-operator-status"); got != "message-operator-status\tdelivered\tsource=operator (web)\n" {
		t.Fatalf("operator text status = %q", got)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(status("message-operator-status", "-o", "json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if _, ok := receipt["source"]; ok || string(receipt["origin"]) != `{"kind":"operator","client":"web"}` {
		t.Fatalf("operator JSON status source=%s origin=%s", receipt["source"], receipt["origin"])
	}
}

// TestAgentMessageSendFlagSetCannotNameAnOrigin pins every flag agent message
// send accepts. None of them sets an origin, and no Envelope the send path
// builds names one, so no flag combination produces operator input: a new flag
// has to be added here on purpose.
func TestAgentMessageSendFlagSetCannotNameAnOrigin(t *testing.T) {
	_, stderr, err := runRoute(t, &agentCommand{}, "message", "send", "-h", "--", "text")
	if err == nil {
		t.Fatal("send -h did not stop at usage")
	}
	var flags []string
	for _, match := range regexp.MustCompile(`(?m)^\s+-([a-z-]+)`).FindAllStringSubmatch(stderr, -1) {
		flags = append(flags, match[1])
	}
	slices.Sort(flags)
	if want := []string{"message-ref", "reply-to", "source", "ttl"}; !slices.Equal(flags, want) {
		t.Fatalf("agent message send flags = %v, want %v\n%s", flags, want, stderr)
	}

	source, err := os.ReadFile("agent_message.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "agent_message.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CompositeLit:
			selector, ok := node.Type.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Envelope" {
				return true
			}
			envelopes++
			for _, element := range node.Elts {
				if pair, ok := element.(*ast.KeyValueExpr); ok {
					if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "Origin" {
						t.Errorf("agent_message.go builds an Envelope with an Origin")
					}
				}
			}
		case *ast.AssignStmt:
			for _, left := range node.Lhs {
				if selector, ok := left.(*ast.SelectorExpr); ok && selector.Sel.Name == "Origin" {
					t.Errorf("agent_message.go assigns an Origin")
				}
			}
		}
		return true
	})
	if envelopes == 0 {
		t.Fatal("audit found no Envelope literal in agent_message.go, so it proves nothing")
	}
}
