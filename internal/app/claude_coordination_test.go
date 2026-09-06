package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func coordinationEnvelope(ref string, deadline time.Time) claudeCoordinationEnvelope {
	return claudeCoordinationEnvelope{
		Version: claudeCoordinationVersion, MessageRef: ref,
		Source:  claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"},
		Payload: "semantic marker", Deadline: deadline,
	}
}

func dialogueEnvelope(ref string, deadline time.Time) claudeCoordinationEnvelope {
	public := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: "conversation-" + ref,
		Source: coremessage.Route{AgentUID: "codex-agent", PaneUID: "codex-pane", ActivationGeneration: "codex-generation",
			Provider: "codex", Incarnation: "codex-incarnation"},
		Target: coremessage.Route{AgentUID: "claude-agent", PaneUID: "claude-pane", ActivationGeneration: "claude-generation",
			Provider: "claude", Incarnation: "claude-incarnation"},
		Authority: coremessage.PeerAuthority(), Payload: "semantic marker", AcceptedAt: deadline.Add(-time.Minute), Deadline: deadline}
	private := coordinationEnvelope(ref, deadline)
	private.Payload = ""
	private.BrokerEnvelope = &public
	return private
}

type failingClaudeDialogueBroker struct {
	handoffErr   error
	deliveredErr error
	replyErr     error
	afterHandoff func()
	current      func() bool
	handoffs     int
	deliveries   int
	replies      int
}

func (b *failingClaudeDialogueBroker) Current(coremessage.Envelope) bool {
	return b.current == nil || b.current()
}

func (b *failingClaudeDialogueBroker) MarkHandoff(coremessage.Envelope) error {
	b.handoffs++
	if b.afterHandoff != nil {
		b.afterHandoff()
	}
	return b.handoffErr
}

func (b *failingClaudeDialogueBroker) MarkDelivered(coremessage.Envelope, time.Time) error {
	b.deliveries++
	return b.deliveredErr
}

func (b *failingClaudeDialogueBroker) Reply(original coremessage.Envelope, _ coremetadata.AgentRouteRef, payload string, _ time.Time) (string, error) {
	b.replies++
	if b.replyErr != nil {
		return "", b.replyErr
	}
	return "reply-" + original.MessageRef + "-" + strings.ReplaceAll(payload, " ", "-"), nil
}

type claudeCoordinationTestFixture struct {
	registryPath string
	route        coremetadata.AgentRouteRef
	target       claudeCoordinationTarget
	server       *claudeCoordinationServer
	sessionID    string
	paneUID      string
	generation   string
}

func newClaudeCoordinationTestFixture(t *testing.T) *claudeCoordinationTestFixture {
	t.Helper()
	h := newSessionRefHarness(t, aiModeClaude)
	provider, _, err := localipc.Process(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	helper, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	mutator := intmetadata.DefaultMutator()
	if err := mutator.RecordClaudeProcess(h.registry, h.paneUID, h.agentUID, h.envGeneration, provider); err != nil {
		t.Fatal(err)
	}
	authority := coremetadata.ClaudeAuthorityRef{SessionID: "synthetic-session", Process: provider,
		RegistrationGeneration: "synthetic-registration", LeaseProcess: helper}
	if err := mutator.BeginClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration, authority); err != nil {
		t.Fatal(err)
	}
	if err := mutator.RecordClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration,
		coremetadata.ClaudeRegistration{Authority: authority}); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "pmx-coordination-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	registryPath := intmetadata.PathFor(filepath.Join(root, "state"))
	store := intmetadata.NewStore(registryPath)
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	route, reason := coremetadata.ResolveAgentRoute(*h.registry, h.agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		t.Fatal("exact Claude target unavailable")
	}
	listener, err := localipc.Listen(claudeCoordinationSocket(registryPath, target))
	if err != nil {
		t.Fatal(err)
	}
	server := startClaudeCoordinationServerWithPoster(listener, route, func() bool { return true }, nil, nil)
	t.Cleanup(func() {
		server.Close()
		_ = os.RemoveAll(claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration))
	})
	return &claudeCoordinationTestFixture{registryPath: registryPath, route: route, target: target, server: server,
		sessionID: authority.SessionID, paneUID: h.paneUID, generation: h.envGeneration}
}

func (f *claudeCoordinationTestFixture) call(t *testing.T, request claudeCoordinationRequest) claudeCoordinationResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := callClaudeCoordination(ctx, f.registryPath, f.route, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestClaudeCoordinationPrivateBridgeRequiresExactV3Route(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	envelope := dialogueEnvelope("message-private-route", now.Add(time.Minute))
	envelope.Target = fixture.target
	envelope.BrokerEnvelope.Target = publicMessageRoute(fixture.route)
	if !envelope.valid(now, fixture.route) {
		t.Fatal("exact v3 route refused")
	}
	envelope.Version = 2
	if envelope.valid(now, fixture.route) {
		t.Fatal("old private protocol crossed v3 helper")
	}
	envelope.Version = claudeCoordinationVersion
	envelope.BrokerEnvelope.Target.Incarnation = "route-replaced"
	if envelope.valid(now, fixture.route) {
		t.Fatal("replaced endpoint incarnation crossed private bridge")
	}
}

func TestClaudeCoordinationServerHasNoWaiterIngressOperations(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	for _, operation := range []string{"wait", "waiter-ready", "begin-handoff", "receipt"} {
		response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: operation, Target: fixture.target, SessionID: fixture.sessionID})
		if response.Kind != "refused" {
			t.Fatalf("%s response=%+v, want refused", operation, response)
		}
	}
}

func TestClaudePushReplyCorrelationIsExactAndConcurrentUserTurnFailsClosed(t *testing.T) {
	now := time.Unix(80_000, 0).UTC()
	newDelivered := func(ref string) (*claudeCoordinationHub, *failingClaudeDialogueBroker) {
		hub := qualifiedPushHub(now)
		broker := &failingClaudeDialogueBroker{}
		poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
		if got := hub.submitPush(dialogueEnvelope(ref, now.Add(time.Minute)), broker, poster); got.State != agentdelivery.StateDelivered {
			t.Fatalf("delivery=%+v", got)
		}
		return hub, broker
	}
	t.Run("ordinary push Stop", func(t *testing.T) {
		hub, _ := newDelivered("message-reply")
		original, reason := hub.reserveReply(false)
		if original == nil || reason != "" || original.MessageRef != "message-reply" {
			t.Fatalf("original=%+v reason=%q", original, reason)
		}
		hub.finishReply(original.MessageRef, "reply-one", true)
		if duplicate, _ := hub.reserveReply(false); duplicate != nil {
			t.Fatal("reply terminal completed twice")
		}
	})
	t.Run("recursive Stop", func(t *testing.T) {
		hub, _ := newDelivered("message-recursive")
		if original, reason := hub.reserveReply(true); original != nil || reason != "stop-origin-mismatch" {
			t.Fatalf("original=%+v reason=%q", original, reason)
		}
	})
	t.Run("human prompt after push", func(t *testing.T) {
		hub, _ := newDelivered("message-human")
		hub.userPrompt()
		if original, reason := hub.reserveReply(false); original != nil || reason != "concurrent-user-turn-ambiguous" {
			t.Fatalf("original=%+v reason=%q", original, reason)
		}
	})
}

func TestClaudeCoordinationQualificationRequiresExplicitOptIn(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	fixture.server.poster = &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	now := time.Now().UTC()
	fixture.server.hub.now = func() time.Time { return now }
	evidence := exactQualificationEvidence(fixture.route, now)
	response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "qualify", Target: fixture.target, Qualification: &evidence})
	if response.Kind != "qualification-refused" {
		t.Fatalf("unconfirmed qualification=%+v", response)
	}
	response = fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "qualify", Target: fixture.target, Qualification: &evidence, ExplicitOptIn: true})
	if response.Kind != "qualification-pending" || response.QualificationRef == "" {
		t.Fatalf("confirmed qualification=%+v", response)
	}
}

func TestClaudeCoordinationEnvelopeJSONContainsNoProviderSecretFields(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	envelope := dialogueEnvelope("message-secret-shape", time.Now().Add(time.Minute))
	envelope.Target = fixture.target
	envelope.BrokerEnvelope.Target = publicMessageRoute(fixture.route)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CLAUDE_CODE_MESSAGING", "provider.sock", "\"token\"", "opaque-token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("private envelope contains %q: %s", forbidden, data)
		}
	}
}

func TestClaudeDialogueBrokerFailureDoesNotCreateReply(t *testing.T) {
	broker := &failingClaudeDialogueBroker{replyErr: errors.New("durable failure")}
	if ref, err := broker.Reply(coremessage.Envelope{MessageRef: "message"}, coremetadata.AgentRouteRef{}, "reply", time.Now()); err == nil || ref != "" || broker.replies != 1 {
		t.Fatalf("ref=%q err=%v replies=%d", ref, err, broker.replies)
	}
}

func TestClaudeDialogueReplyCorrelationExpiresWithoutChangingDelivery(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	broker := &failingClaudeDialogueBroker{}
	fixture.server.broker = broker
	fixture.server.poster = &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	fixture.server.hub.now = func() time.Time { return now }
	envelope := dialogueForRoute("message-expired-correlation", fixture.route, now)
	response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: fixture.target, Envelope: &envelope})
	if response.Delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("push delivery=%+v", response.Delivery)
	}
	now = envelope.Deadline.Add(time.Nanosecond)
	response = fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "stop-reply", Target: fixture.target, SessionID: fixture.sessionID,
		AssistantMessage: "unrelated later assistant text", StopHookActive: false})
	if response.Kind != "reply-refused" || response.Reason != "reply-correlation-expired" || broker.replies != 0 {
		t.Fatalf("expired response=%+v broker replies=%d", response, broker.replies)
	}
	if delivery := fixture.server.hub.status(envelope.MessageRef); delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("delivery terminal changed after reply TTL: %+v", delivery)
	}
}

func TestClaudeDialogueBrokerReplyFailureNeverRetriesOnLaterStop(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	broker := &failingClaudeDialogueBroker{replyErr: errors.New("durable reply outcome unknown")}
	fixture.server.broker = broker
	fixture.server.poster = &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	fixture.server.hub.now = func() time.Time { return now }
	envelope := dialogueForRoute("message-reply-persist-failure", fixture.route, now)
	if response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: fixture.target, Envelope: &envelope}); response.Delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("push delivery=%+v", response.Delivery)
	}
	first := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "stop-reply", Target: fixture.target, SessionID: fixture.sessionID,
		AssistantMessage: "first assistant text", StopHookActive: false})
	if first.Kind != "reply-refused" || first.Reason != "broker-reply-refused" || broker.replies != 1 {
		t.Fatalf("first response=%+v broker replies=%d", first, broker.replies)
	}
	second := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "stop-reply", Target: fixture.target, SessionID: fixture.sessionID,
		AssistantMessage: "unrelated second assistant text", StopHookActive: false})
	if second.Kind != "reply-refused" || second.Reason != "broker-reply-outcome-unknown" || broker.replies != 1 {
		t.Fatalf("second response=%+v broker replies=%d", second, broker.replies)
	}
}

func TestClaudeReplyHookRequiresExplicitStopHookActiveField(t *testing.T) {
	for _, input := range []string{
		`{"hook_event_name":"Stop","session_id":"session","last_assistant_message":"answer"}`,
		`{"hook_event_name":"Stop","session_id":"session","stop_hook_active":null,"last_assistant_message":"answer"}`,
		`{"hook_event_name":"Stop","session_id":"session","stop_hook_active":"false","last_assistant_message":"answer"}`,
	} {
		lookups := 0
		if err := runClaudeMessageReplyInput(nil, strings.NewReader(input), func(string) string { lookups++; return "" }); err != nil {
			t.Fatal(err)
		}
		if lookups != 0 {
			t.Fatalf("malformed Stop reached coordination route: input=%s lookups=%d", input, lookups)
		}
	}

	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	broker := &failingClaudeDialogueBroker{}
	fixture.server.broker = broker
	fixture.server.poster = &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	fixture.server.hub.now = func() time.Time { return now }
	envelope := dialogueForRoute("message-stop-field", fixture.route, now)
	if response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: fixture.target, Envelope: &envelope}); response.Delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("push delivery=%+v", response.Delivery)
	}
	getenv := func(key string) string {
		switch key {
		case internalClaudeRegistryPathEnv:
			return fixture.registryPath
		case internalActivationPaneUIDEnv:
			return fixture.paneUID
		case internalActivationGenerationEnv:
			return fixture.generation
		default:
			return ""
		}
	}
	input := `{"hook_event_name":"Stop","session_id":"` + fixture.sessionID +
		`","stop_hook_active":false,"last_assistant_message":"answer","permission_mode":"default"}`
	if err := runClaudeMessageReplyInput(nil, strings.NewReader(input), getenv); err != nil {
		t.Fatal(err)
	}
	if broker.replies != 1 {
		t.Fatalf("documented Stop with extra fields broker replies=%d, want 1", broker.replies)
	}
}

// userPrompt applies a complete boundary in tests; the live hook announces it
// before attempting its bounded serialization lock.
func (h *claudeCoordinationHub) userPrompt() {
	h.userPromptAt(h.boundaryAnnouncements.Add(1))
}
