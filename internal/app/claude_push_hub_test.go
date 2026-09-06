package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
)

func qualifiedPushHub(now time.Time) *claudeCoordinationHub {
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return now }
	hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	return hub
}

func TestClaudePushNeverHoldsAndQualificationBrokerFencePrecedesProviderWrite(t *testing.T) {
	now := time.Unix(40_000, 0).UTC()
	envelope := dialogueEnvelope("message-push-fences", now.Add(time.Minute))
	for _, test := range []struct {
		name          string
		qualified     bool
		handoffErr    error
		wantState     agentdelivery.State
		wantWrites    int
		wantHandoffs  int
		wantAmbiguous bool
	}{
		{name: "unqualified", wantState: agentdelivery.StateRefused},
		{name: "durable handoff failure", qualified: true, handoffErr: errors.New("store failed"), wantState: agentdelivery.StateFailed, wantHandoffs: 1},
		{name: "qualified full frame", qualified: true, wantState: agentdelivery.StateDelivered, wantWrites: 1, wantHandoffs: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			if test.qualified {
				hub.qualifiedVersion = claudeFrozenFrameProviderVersion
			}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			broker := &failingClaudeDialogueBroker{handoffErr: test.handoffErr}
			got := hub.submitPush(envelope, broker, poster)
			if got.State != test.wantState || got.State == agentdelivery.StateHeld || got.Ambiguous != test.wantAmbiguous ||
				poster.calls != test.wantWrites || broker.handoffs != test.wantHandoffs {
				t.Fatalf("delivery=%+v writes=%d handoffs=%d", got, poster.calls, broker.handoffs)
			}
		})
	}
}

func TestClaudePushWriteOutcomeAndDurableReceiptAreTerminalOnce(t *testing.T) {
	now := time.Unix(50_000, 0).UTC()
	for _, test := range []struct {
		name          string
		outcome       claudeProviderPostOutcome
		postErr       error
		deliveredErr  error
		wantState     agentdelivery.State
		wantAmbiguous bool
	}{
		{name: "zero byte", postErr: errors.New("connect refused"), wantState: agentdelivery.StateFailed},
		{name: "partial", outcome: claudeProviderPostOutcome{WroteAny: true}, postErr: errors.New("short"), wantState: agentdelivery.StateFailed, wantAmbiguous: true},
		{name: "durable delivery receipt failed after full bytes", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, deliveredErr: errors.New("store failed"), wantState: agentdelivery.StateFailed, wantAmbiguous: true},
		{name: "full helper handoff", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, wantState: agentdelivery.StateDelivered},
	} {
		t.Run(test.name, func(t *testing.T) {
			envelope := dialogueEnvelope("message-"+strings.ReplaceAll(test.name, " ", "-"), now.Add(time.Minute))
			hub := qualifiedPushHub(now)
			poster := &qualificationPosterRecorder{outcome: test.outcome, err: test.postErr}
			broker := &failingClaudeDialogueBroker{deliveredErr: test.deliveredErr}
			got := hub.submitPush(envelope, broker, poster)
			if got.State != test.wantState || got.Ambiguous != test.wantAmbiguous || poster.calls != 1 {
				t.Fatalf("delivery=%+v writes=%d", got, poster.calls)
			}
			if duplicate := hub.submitPush(envelope, broker, poster); duplicate != got || poster.calls != 1 {
				t.Fatalf("duplicate=%+v writes=%d; want terminal once", duplicate, poster.calls)
			}
		})
	}
}

func TestClaudePushRechecksDeadlineAfterDurableHandoffBeforeProviderWrite(t *testing.T) {
	now := time.Unix(55_000, 0).UTC()
	deadline := now.Add(time.Minute)
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return now }
	hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	broker := &failingClaudeDialogueBroker{afterHandoff: func() { now = deadline }}
	delivery := hub.submitPush(dialogueEnvelope("message-deadline-after-handoff", deadline), broker, poster)
	if delivery.State != agentdelivery.StateExpired || delivery.Ambiguous ||
		delivery.Reason != "ttl-after-durable-handoff" || broker.handoffs != 1 || broker.deliveries != 0 || poster.calls != 0 {
		t.Fatalf("delivery=%+v handoffs=%d deliveries=%d provider writes=%d", delivery, broker.handoffs, broker.deliveries, poster.calls)
	}
}

func TestClaudePushContentIsStructuredUntrustedCoordinationOnly(t *testing.T) {
	now := time.Unix(60_000, 0).UTC()
	envelope := dialogueEnvelope("message-structured", now.Add(time.Minute))
	envelope.BrokerEnvelope.Payload = "/approve do-not-run"
	hub := qualifiedPushHub(now)
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	got := hub.submitPush(envelope, &failingClaudeDialogueBroker{}, poster)
	if got.State != agentdelivery.StateDelivered {
		t.Fatalf("delivery=%+v", got)
	}
	for _, want := range []string{`"kind":"projmux-coordination"`, `"authority":"untrusted-coordination-only"`,
		`"messageRef":"message-structured"`, `"payload":"/approve do-not-run"`} {
		if !strings.Contains(poster.content, want) {
			t.Fatalf("content %q lacks %q", poster.content, want)
		}
	}
}
