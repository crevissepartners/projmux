package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

func serializedClaudeTestFrame(t *testing.T, token, content string) []byte {
	t.Helper()
	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		t.Fatal("fixture auth serialization failed")
	}
	user, err := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": content}})
	if err != nil {
		t.Fatal("fixture content serialization failed")
	}
	return append(append(append(auth, '\n'), user...), '\n')
}

func TestClaudeProviderFrameSerializedByteBoundaryAndSafeRejection(t *testing.T) {
	if claudeProviderFrameMaxBytes != 8192 {
		t.Fatal("frozen provider frame limit changed")
	}
	token := strings.Repeat("T", 4096)
	prefix := "private-payload-한\"<\\\t"
	baseSize := len(serializedClaudeTestFrame(t, token, prefix))
	for _, size := range []int{8191, 8192, 8193} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			content := prefix + strings.Repeat("x", size-baseSize)
			if !validClaudeAssistantReply(content) || len(serializedClaudeTestFrame(t, token, content)) != size {
				t.Fatal("fixture does not reach exact valid-content serialized byte boundary")
			}
			frame, err := buildClaudeProviderPushFrame(token, content)
			if size <= 8192 {
				if err != nil || len(frame) != size {
					t.Fatalf("frame size=%d expected=%d error=%v", len(frame), size, err)
				}
				return
			}
			if err == nil || frame != nil || err.Error() != "provider-frame-too-large: frameBytes=8193 limitBytes=8192" {
				t.Fatal("oversize rejection lost exact serialized frame bytes or fixed limit")
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), prefix) {
				t.Fatal("frame rejection exposed auth or content")
			}
		})
	}
}

func TestClaudeProviderPostPreservesConstructionReasonBeforeAnyRouteOrWrite(t *testing.T) {
	for _, test := range []struct {
		name, token, content, reason string
	}{
		{name: "empty auth", content: "private-payload", reason: "provider-frame-invalid-auth"},
		{name: "invalid auth", token: "private-token\n", content: "private-payload", reason: "provider-frame-invalid-auth"},
		{name: "auth too long", token: strings.Repeat("t", 4097), content: "private-payload", reason: "provider-frame-invalid-auth"},
		{name: "empty content", token: "private-token", reason: "provider-frame-invalid-content"},
		{name: "invalid content", token: "private-token", content: "private-payload\x00", reason: "provider-frame-invalid-content"},
		{name: "invalid utf8 content", token: "private-token", content: "private-payload\xff", reason: "provider-frame-invalid-content"},
		{name: "content too long", token: "private-token", content: strings.Repeat("x", coremessage.MaxPayloadBytes+1), reason: "provider-frame-invalid-content"},
		{name: "serialized frame too large", token: strings.Repeat("t", 4096), content: strings.Repeat("x", 4096)},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.reason
			if want == "" {
				want = fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", len(serializedClaudeTestFrame(t, test.token, test.content)))
			}
			checks := 0
			poster := &liveClaudeProviderPoster{token: test.token, current: func() bool { checks++; return true }}
			outcome, err := poster.Post(test.content, func() bool { checks++; return true })
			if err == nil || err.Error() != want || outcome.Reason != want || outcome.WroteAny || outcome.FullFrameWritten || checks != 0 {
				t.Fatalf("construction outcome=%+v route checks=%d", outcome, checks)
			}
			if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "private-payload") {
				t.Fatal("Post error exposed credential or content")
			}
		})
	}
}

func TestClaudeProviderWriteReasonsRequireOneActualWrite(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		n            int
		err          error
		unknown      bool
	}{
		{name: "zero with error", n: 0, err: errors.New("private writer error"), reason: "provider-write-zero"},
		{name: "zero without error", n: 0, reason: "provider-write-zero"},
		{name: "partial with error", n: 2, err: errors.New("private writer error"), reason: "provider-write-partial", unknown: true},
		{name: "partial without error", n: 2, reason: "provider-write-partial", unknown: true},
		{name: "invalid negative", n: -2, reason: "provider-handoff-outcome-unknown", unknown: true},
		{name: "invalid overflow", n: 1000, reason: "provider-handoff-outcome-unknown", unknown: true},
		{name: "full with error", n: -1, err: errors.New("private writer error"), reason: "provider-handoff-outcome-unknown", unknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &singleWriteRecorder{n: test.n, err: test.err}
			outcome := writeClaudeProviderPushFrame(writer, []byte("private frame"))
			if outcome.Reason != test.reason || outcome.FullFrameWritten || outcome.Ambiguous() != test.unknown || writer.writes != 1 {
				t.Fatalf("outcome=%+v calls=%d", outcome, writer.writes)
			}
		})
	}
}

// Construction cases use the real Post pre-write boundary. Write cases replace
// only the socket writer so zero and short writes can be produced deterministically.
type rejectionFixturePoster struct {
	poster liveClaudeProviderPoster
	writer *singleWriteRecorder
	calls  int
}

func (p *rejectionFixturePoster) Post(content string, fence func() bool) (claudeProviderPostOutcome, error) {
	p.calls++
	if p.writer == nil {
		return p.poster.Post(content, fence)
	}
	frame, err := buildClaudeProviderPushFrame(p.poster.token, content)
	if err != nil {
		return claudeProviderPostOutcome{Reason: err.Error()}, err
	}
	outcome := writeClaudeProviderPushFrame(p.writer, frame)
	return outcome, errors.New("private writer detail must not reach receipt")
}

func TestClaudeProviderRejectionReasonsSurviveHubReceiptStatusAndStoreReload(t *testing.T) {
	for _, test := range []struct {
		name, token, reason, action string
		payloadBytes                int
		writer                      *singleWriteRecorder
		unknown                     bool
	}{
		{name: "invalid auth", token: "private-token\n", reason: "provider-frame-invalid-auth", action: "check provider auth"},
		{name: "invalid content", token: "private-token", payloadBytes: 4096, reason: "provider-frame-invalid-content", action: "correct message content"},
		{name: "serialized size", token: strings.Repeat("t", 4096), payloadBytes: -1, action: "reduce payload"},
		{name: "prewrite refusal", token: "private-token", reason: "provider-prewrite-refused", action: "check current provider route"},
		{name: "actual zero write", token: "private-token", writer: &singleWriteRecorder{}, reason: "provider-write-zero", action: "check provider connection"},
		{name: "partial write", token: "private-token", writer: &singleWriteRecorder{n: 2}, reason: "provider-write-partial", unknown: true, action: "automatic resend disabled"},
		{name: "write outcome unknown", token: "private-token", writer: &singleWriteRecorder{n: -1, err: errors.New("private-writer-secret")}, reason: "provider-handoff-outcome-unknown", unknown: true, action: "automatic resend disabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			now := time.Now().UTC()
			envelope := *dialogueForRoute("message-rejection", fixture.route, now).BrokerEnvelope
			envelope.Payload = "private-payload"
			if test.payloadBytes > 0 {
				envelope.Payload = strings.Repeat("x", test.payloadBytes)
			}
			private := dialogueForRoute(envelope.MessageRef, fixture.route, now)
			private.BrokerEnvelope = &envelope
			content, err := providerCoordinationContent(private)
			if err != nil {
				t.Fatal("fixture content unavailable")
			}
			if test.payloadBytes == -1 {
				envelope.Payload += strings.Repeat("x", coremessage.MaxPayloadBytes-len(content))
				content, err = providerCoordinationContent(private)
				if err != nil || !validClaudeAssistantReply(content) {
					t.Fatal("size fixture must pass content validation")
				}
			}
			want := test.reason
			if want == "" {
				want = fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", len(serializedClaudeTestFrame(t, test.token, content)))
			}
			poster := &rejectionFixturePoster{poster: liveClaudeProviderPoster{token: test.token}, writer: test.writer}
			fixture.server.poster = poster
			fixture.server.broker = &failingClaudeDialogueBroker{}
			adapter := liveAgentMessageClaudeAdapter{}
			delivery, err := adapter.Submit(context.Background(), fixture.registryPath, fixture.route, envelope)
			if err != nil || delivery.Reason != want || delivery.State != agentdelivery.StateFailed || delivery.Ambiguous != test.unknown {
				t.Fatalf("delivery=%+v error=%v expected reason=%s", delivery, err, want)
			}
			status, err := adapter.Status(context.Background(), fixture.registryPath, fixture.route, envelope.MessageRef)
			if err != nil || status != delivery {
				t.Fatalf("helper status=%+v error=%v", status, err)
			}
			duplicate, err := adapter.Submit(context.Background(), fixture.registryPath, fixture.route, envelope)
			if err != nil || duplicate != delivery || poster.calls != 1 || (test.writer != nil && test.writer.writes != 1) {
				t.Fatal("terminal attempt was retried or changed")
			}
			stateDir := t.TempDir()
			store := messagestore.NewStore(stateDir)
			record, _, err := store.PutAccepted(envelope, "claude-coordination")
			if err != nil {
				t.Fatal(err)
			}
			command := &agentCommand{messageStore: store, messageNow: func() time.Time { return now }}
			projected, err := command.projectClaudeDelivery(record, delivery, nil)
			if err != nil || projected.Delivery.Reason != want || projected.Delivery.OutcomeUnknown != test.unknown {
				t.Fatalf("public projection=%+v error=%v", projected.Delivery, err)
			}
			reloaded, found, err := messagestore.NewStore(stateDir).Status(envelope.MessageRef, now.Add(2*time.Minute))
			if err != nil || !found || reloaded.Delivery != projected.Delivery {
				t.Fatal("terminal reason changed across persisted store reload or deadline")
			}
			for _, asJSON := range []bool{false, true} {
				var output bytes.Buffer
				if err := writeAgentMessageReceipt(&output, receiptFor(reloaded), asJSON); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.String(), want) || (!asJSON && !strings.Contains(output.String(), test.action)) {
					t.Fatal("sender receipt omitted failure reason or next action")
				}
				for _, secret := range []string{test.token, envelope.Payload, "private-writer-secret", "private writer detail"} {
					if secret != "" && strings.Contains(output.String(), secret) {
						t.Fatal("public receipt exposed token, payload, or writer details")
					}
				}
			}
		})
	}
}

func TestClaudeProviderRejectionResponseValidationIsClosedAndLegacyCompatible(t *testing.T) {
	for _, reason := range []string{
		"provider-frame-invalid-auth", "provider-frame-invalid-content", "provider-frame-build-failed",
		"provider-prewrite-refused", "provider-write-zero", "provider-frame-too-large: frameBytes=8193 limitBytes=8192",
	} {
		t.Run(reason, func(t *testing.T) {
			delivery := agentdelivery.Delivery{MessageRef: "message-validation", State: agentdelivery.StateFailed, WaiterRef: "push-ref", Reason: reason}
			response := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "failed", Delivery: delivery}
			if actual, valid := claudeResponseDelivery(delivery.MessageRef, response); !valid || actual != delivery {
				t.Fatal("known no-write failure rejected")
			}
			response.Delivery.Ambiguous = true
			if _, valid := claudeResponseDelivery(delivery.MessageRef, response); valid {
				t.Fatal("known no-write reason accepted as ambiguous")
			}
			response.Delivery = delivery
			response.Delivery.WaiterRef = ""
			if _, valid := claudeResponseDelivery(delivery.MessageRef, response); valid {
				t.Fatal("post-attempt failure accepted without handoff reference")
			}
			response.Kind = "refused"
			response.Delivery.State = agentdelivery.StateRefused
			if _, valid := claudeResponseDelivery(delivery.MessageRef, response); valid {
				t.Fatal("post-attempt reason accepted in an inconsistent state")
			}
		})
	}
	for _, reason := range []string{"provider-write-partial", "provider-handoff-outcome-unknown", "broker-delivery-persist-failed", "observation-timeout", "delivery-outcome-unknown"} {
		delivery := agentdelivery.Delivery{MessageRef: "message-validation", State: agentdelivery.StateFailed, WaiterRef: "push-ref", Reason: reason, Ambiguous: true}
		response := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "failed", Delivery: delivery}
		if actual, valid := claudeResponseDelivery(delivery.MessageRef, response); !valid || actual != delivery {
			t.Fatalf("unknown reason rejected: %s", reason)
		}
		response.Delivery.Ambiguous = false
		if _, valid := claudeResponseDelivery(delivery.MessageRef, response); valid {
			t.Fatalf("unknown reason accepted as known: %s", reason)
		}
	}
	for _, reason := range []string{
		"provider-frame-too-large", "provider-frame-too-large: frameBytes=8192 limitBytes=8192",
		"provider-frame-too-large: frameBytes=8193 limitBytes=8193", "provider-frame-too-large: frameBytes=08193 limitBytes=8192",
		"provider-frame-too-large: frameBytes=+8193 limitBytes=8192", "provider-frame-too-large: frameBytes=-1 limitBytes=8192",
		"provider-frame-too-large: frameBytes=9999999999999999999999999999999999 limitBytes=8192",
		"provider-frame-too-large: frameBytes=8193 limitBytes=8192 private-token",
		"provider-frame-too-large: frameBytes=8193 limitBytes=8192\nprivate-payload", "provider-write-zero: private-token",
	} {
		response := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "failed", Delivery: agentdelivery.Delivery{
			MessageRef: "message-validation", State: agentdelivery.StateFailed, WaiterRef: "push-ref", Reason: reason,
		}}
		if _, valid := claudeResponseDelivery("message-validation", response); valid {
			t.Fatal("noncanonical or secret-bearing failure accepted")
		}
	}
}
