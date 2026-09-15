package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// frameBudgetTestToken has the length of the e2e provider fixture token: 24
// random bytes in hex.
var frameBudgetTestToken = strings.Repeat("9f", 24)

const frameBudgetTestExecutable = "/home/user/go/bin/projmux"

// claudeFrameBudgetSymbolBody is a payload-limit body that still fits the
// private coordination envelope but not the provider frame: its quotes and
// newlines are escaped once in the envelope and again in the frame.
func claudeFrameBudgetSymbolBody() string {
	body := strings.Repeat("\"x", 1200) + strings.Repeat("<>&\n", 48)
	return body + strings.Repeat("y", coremessage.MaxPayloadBytes-len(body))
}

type frameBudgetRecordingWriter struct{ frames [][]byte }

func (w *frameBudgetRecordingWriter) Write(frame []byte) (int, error) {
	w.frames = append(w.frames, append([]byte(nil), frame...))
	return len(frame), nil
}

// frameBudgetRecordingPoster keeps the real frame construction and the single
// write, replacing only the provider socket with a recorder.
type frameBudgetRecordingPoster struct {
	token  string
	calls  int
	writer frameBudgetRecordingWriter
}

func (p *frameBudgetRecordingPoster) Post(content string, _ func() bool) (claudeProviderPostOutcome, error) {
	p.calls++
	frame, err := buildClaudeProviderPushFrame(p.token, content)
	if err != nil {
		return claudeProviderPostOutcome{Reason: err.Error()}, err
	}
	return writeClaudeProviderPushFrame(&p.writer, frame), nil
}

func setFrameBudgetReplyExecutable(hub *claudeCoordinationHub) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.replyExecutable = frameBudgetTestExecutable
}

// frameBudgetEnvelope carries route UIDs and refs of production length so the
// envelope overhead around the body is realistic.
func frameBudgetEnvelope(route coremetadata.AgentRouteRef, payload string) claudeCoordinationEnvelope {
	envelope := dialogueForRoute(newCoordinationRef("message"), route, time.Now().UTC())
	broker := envelope.BrokerEnvelope
	broker.ConversationRef = newCoordinationRef("conversation")
	broker.Source = coremessage.Route{AgentUID: "agent-01k2v7q9m3x8c4n5b6t0r1y2z3", PaneUID: "pane-01k2v7q9m3x8c4n5b6t0r1y2z4",
		ActivationGeneration: "gen-01k2v7q9m3x8c4n5b6t0r1y2z5", Provider: "codex", Incarnation: "incarnation-01k2v7q9m3x8c4n5b6t0r1y2z6"}
	broker.Payload = payload
	return envelope
}

func TestClaudePushAcceptedPayloadLimitBodyIsDeliveredInOneFrameWithinBudget(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{name: "ascii 4096 bytes", body: strings.Repeat("x", coremessage.MaxPayloadBytes)},
		{name: "korean 4095 bytes", body: strings.Repeat("한", 1365)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.body) > coremessage.MaxPayloadBytes || coremessage.MaxPayloadBytes-len(test.body) > 1 {
				t.Fatalf("fixture body is %d bytes", len(test.body))
			}
			fixture := newClaudeCoordinationTestFixture(t)
			setFrameBudgetReplyExecutable(fixture.server.hub)
			poster := &frameBudgetRecordingPoster{token: frameBudgetTestToken}
			broker := &failingClaudeDialogueBroker{}
			fixture.server.poster, fixture.server.broker = poster, broker
			envelope := frameBudgetEnvelope(fixture.route, test.body)
			content, err := providerCoordinationContent(envelope, frameBudgetTestExecutable)
			if err != nil || !strings.Contains(content, "execute "+frameBudgetTestExecutable+" with argv") {
				t.Fatal("real coordination envelope unavailable")
			}
			delivery, err := liveAgentMessageClaudeAdapter{}.Submit(context.Background(), fixture.registryPath, fixture.route, *envelope.BrokerEnvelope)
			if err != nil || delivery.State != agentdelivery.StateDelivered || delivery.Ambiguous {
				t.Fatalf("delivery=%+v error=%v content bytes=%d", delivery, err, len(content))
			}
			if poster.calls != 1 || len(poster.writer.frames) != 1 || broker.handoffs != 1 || broker.deliveries != 1 {
				t.Fatalf("posts=%d frames=%d handoffs=%d deliveries=%d", poster.calls, len(poster.writer.frames), broker.handoffs, broker.deliveries)
			}
			frame := poster.writer.frames[0]
			independent := len(serializedClaudeTestFrame(t, frameBudgetTestToken, content))
			if len(frame) != independent || len(frame) > claudeProviderFrameMaxBytes {
				t.Fatalf("frame bytes=%d independent=%d limit=%d", len(frame), independent, claudeProviderFrameMaxBytes)
			}
			lines := bytes.Split(bytes.TrimSuffix(frame, []byte("\n")), []byte("\n"))
			var user claudeProviderUserFrame
			if len(lines) != 2 || json.Unmarshal(lines[1], &user) != nil || user.Message.Content != content {
				t.Fatal("written frame is not the full real coordination envelope")
			}
			var delivered claudeProviderCoordinationContent
			if json.Unmarshal([]byte(user.Message.Content), &delivered) != nil || delivered.Payload != test.body {
				t.Fatal("delivered payload changed")
			}
		})
	}
}

func TestClaudePushSymbolHeavyPayloadLimitBodyReportsSizedFrameRefusalThroughReceipt(t *testing.T) {
	body := claudeFrameBudgetSymbolBody()
	if len(body) != coremessage.MaxPayloadBytes || !utf8.ValidString(body) {
		t.Fatalf("fixture body is %d bytes", len(body))
	}
	fixture := newClaudeCoordinationTestFixture(t)
	setFrameBudgetReplyExecutable(fixture.server.hub)
	poster := &frameBudgetRecordingPoster{token: frameBudgetTestToken}
	broker := &failingClaudeDialogueBroker{}
	fixture.server.poster, fixture.server.broker = poster, broker
	envelope := frameBudgetEnvelope(fixture.route, body)
	if private, _ := json.Marshal(envelope); !envelope.valid(time.Now(), fixture.route) {
		t.Fatalf("fixture must fit the private coordination envelope: %d bytes", len(private)+1)
	}
	content, err := providerCoordinationContent(envelope, frameBudgetTestExecutable)
	if err != nil {
		t.Fatal("real coordination envelope unavailable")
	}
	frameBytes := len(serializedClaudeTestFrame(t, frameBudgetTestToken, content))
	if frameBytes <= claudeProviderFrameMaxBytes {
		t.Fatalf("fixture frame %d bytes does not exceed the budget", frameBytes)
	}
	want := fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", frameBytes)
	adapter := liveAgentMessageClaudeAdapter{}
	delivery, err := adapter.Submit(context.Background(), fixture.registryPath, fixture.route, *envelope.BrokerEnvelope)
	if err != nil || delivery.State != agentdelivery.StateFailed || delivery.Reason != want || delivery.Ambiguous {
		t.Fatalf("delivery=%+v error=%v expected reason=%s", delivery, err, want)
	}
	if poster.calls != 1 || len(poster.writer.frames) != 0 || broker.deliveries != 0 {
		t.Fatalf("posts=%d provider writes=%d deliveries=%d", poster.calls, len(poster.writer.frames), broker.deliveries)
	}
	status, err := adapter.Status(context.Background(), fixture.registryPath, fixture.route, envelope.MessageRef)
	if err != nil || status != delivery {
		t.Fatalf("helper status=%+v error=%v", status, err)
	}
	duplicate, err := adapter.Submit(context.Background(), fixture.registryPath, fixture.route, *envelope.BrokerEnvelope)
	if err != nil || duplicate != delivery || poster.calls != 1 || len(poster.writer.frames) != 0 {
		t.Fatal("terminal size refusal was retried or changed")
	}
	now := time.Now().UTC()
	stateDir := t.TempDir()
	store := messagestore.NewStore(stateDir)
	record, _, err := store.PutAccepted(*envelope.BrokerEnvelope, "claude-coordination")
	if err != nil {
		t.Fatal(err)
	}
	command := &agentCommand{messageStore: store, messageNow: func() time.Time { return now }}
	projected, err := command.projectClaudeDelivery(record, delivery, nil)
	if err != nil || projected.Delivery.State != coremessage.StateFailed || projected.Delivery.Reason != want || projected.Delivery.OutcomeUnknown {
		t.Fatalf("public projection=%+v error=%v", projected.Delivery, err)
	}
	reloaded, found, err := messagestore.NewStore(stateDir).Status(envelope.MessageRef, now.Add(2*time.Minute))
	if err != nil || !found || reloaded.Delivery != projected.Delivery {
		t.Fatal("sized refusal changed across persisted store reload")
	}
	for _, asJSON := range []bool{false, true} {
		var output bytes.Buffer
		if err := writeAgentMessageReceipt(&output, receiptFor(reloaded), asJSON); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), want) || (!asJSON && !strings.Contains(output.String(), "reduce payload")) {
			t.Fatalf("public receipt omitted sized reason or next action: %s", output.String())
		}
		if strings.Contains(output.String(), frameBudgetTestToken) || strings.Contains(output.String(), body) {
			t.Fatal("public receipt exposed token or payload")
		}
	}
}

func TestClaudePushSizeExcessEndsSizedNeverInvalidContentOrUnsupported(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	for _, test := range []struct {
		name, token string
		payload     func(baseContentBytes int) string
		fits        func(content string) bool
	}{
		{name: "content just above reply payload limit via long token", token: strings.Repeat("t", 4096),
			payload: func(base int) string { return strings.Repeat("x", 1+coremessage.MaxPayloadBytes+1-base) },
			fits:    func(content string) bool { return len(content) == coremessage.MaxPayloadBytes+1 }},
		{name: "content above frame budget", token: frameBudgetTestToken,
			payload: func(int) string { return strings.Repeat("<", coremessage.MaxPayloadBytes) },
			fits:    func(content string) bool { return len(content) > claudeProviderFrameMaxBytes }},
		{name: "symbol-heavy payload limit body", token: frameBudgetTestToken,
			payload: func(int) string { return claudeFrameBudgetSymbolBody() },
			fits:    func(content string) bool { return len(content) > coremessage.MaxPayloadBytes }},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			setFrameBudgetReplyExecutable(hub)
			envelope := frameBudgetEnvelope(fixture.route, "x")
			base, err := providerCoordinationContent(envelope, frameBudgetTestExecutable)
			if err != nil {
				t.Fatal("base coordination envelope unavailable")
			}
			envelope.BrokerEnvelope.Payload = test.payload(len(base))
			content, err := providerCoordinationContent(envelope, frameBudgetTestExecutable)
			if err != nil || !test.fits(content) {
				t.Fatalf("fixture content bytes=%d error=%v", len(content), err)
			}
			want := fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", len(serializedClaudeTestFrame(t, test.token, content)))
			poster := &frameBudgetRecordingPoster{token: test.token}
			broker := &failingClaudeDialogueBroker{}
			delivery := hub.submitPush(envelope, broker, poster)
			if delivery.State != agentdelivery.StateFailed || delivery.Reason != want || delivery.Ambiguous ||
				delivery.Reason == "provider-frame-invalid-content" || delivery.Reason == "provider-frame-unsupported" ||
				!isClaudeProviderFrameSizeReason(delivery.Reason) {
				t.Fatalf("delivery=%+v expected reason=%s", delivery, want)
			}
			if poster.calls != 1 || len(poster.writer.frames) != 0 || broker.handoffs != 1 || broker.deliveries != 0 {
				t.Fatalf("posts=%d provider writes=%d handoffs=%d deliveries=%d", poster.calls, len(poster.writer.frames), broker.handoffs, broker.deliveries)
			}
		})
	}
	for _, test := range []struct{ name, content string }{
		{name: "empty content"},
		{name: "invalid utf8 content", content: "payload\xff"},
		{name: "nul content", content: "payload\x00"},
		{name: "nul content above frame budget", content: strings.Repeat("x", 2*claudeProviderFrameMaxBytes) + "\x00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			poster := &frameBudgetRecordingPoster{token: frameBudgetTestToken}
			outcome, err := poster.Post(test.content, nil)
			if err == nil || outcome.Reason != "provider-frame-invalid-content" || outcome.WroteAny || len(poster.writer.frames) != 0 {
				t.Fatalf("outcome=%+v error=%v", outcome, err)
			}
		})
	}
}

func TestClaudeReplyToolArgvBodyKeepsPayloadByteLimit(t *testing.T) {
	executable := "/owned/projmux"
	command := func(body string) string {
		return executable + " agent message send uid:codex-agent --reply-to message-a -- '" + body + "'"
	}
	accepted := strings.Repeat("x", coremessage.MaxPayloadBytes)
	if argv, err := parseClaudeReplyCommand(command(accepted), executable); err != nil || len(argv) != 9 || argv[8] != accepted {
		t.Fatalf("4096-byte reply argv body refused: %v", err)
	}
	rejected := strings.Repeat("x", coremessage.MaxPayloadBytes+1)
	if len(command(rejected)) > coremessage.MaxPayloadBytes+1024 {
		t.Fatal("fixture must reach the argv body check, not the command length bound")
	}
	if _, err := parseClaudeReplyCommand(command(rejected), executable); err == nil {
		t.Fatal("4097-byte reply argv body accepted")
	}
	if validClaudeAssistantReply(rejected) || !validClaudeProviderPushContent(rejected) {
		t.Fatal("reply argv body limit leaked into push content judgment")
	}
}
