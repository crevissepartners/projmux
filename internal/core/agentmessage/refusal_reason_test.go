package agentmessage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeRefusalNamesWhichRuleItBroke is the positive half of the
// diagnostic contract: every refused shape says which class of rule it broke,
// and every one of them stays an ErrInvalidEnvelope for the callers that
// already judge refusals with errors.Is.
func TestEnvelopeRefusalNamesWhichRuleItBroke(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		mutate func(*Envelope)
		reason string
	}{
		"unsupported version":   {func(e *Envelope) { e.Version = Version + 1 }, ReasonQualificationInvalid},
		"unset acceptedAt":      {func(e *Envelope) { e.AcceptedAt = time.Time{} }, ReasonQualificationInvalid},
		"unset deadline":        {func(e *Envelope) { e.Deadline = time.Time{} }, ReasonQualificationInvalid},
		"deadline before start": {func(e *Envelope) { e.Deadline = e.AcceptedAt.Add(-time.Second) }, ReasonQualificationInvalid},
		"ttl over the maximum":  {func(e *Envelope) { e.Deadline = e.AcceptedAt.Add(MaxTTL + time.Second) }, ReasonQualificationInvalid},
		"peer authority lost":   {func(e *Envelope) { e.Authority = OperatorAuthority() }, ReasonQualificationInvalid},
		"blank messageRef":      {func(e *Envelope) { e.MessageRef = "" }, ReasonCorrelationInvalid},
		"blank conversationRef": {func(e *Envelope) { e.ConversationRef = "" }, ReasonCorrelationInvalid},
		"control rune replyTo":  {func(e *Envelope) { e.ReplyTo = "message\x01earlier" }, ReasonCorrelationInvalid},
		"target route gap":      {func(e *Envelope) { e.Target.PaneUID = "" }, ReasonRouteInvalid},
		"source route gap":      {func(e *Envelope) { e.Source.AgentUID = "" }, ReasonRouteInvalid},
		"unknown provider":      {func(e *Envelope) { e.Target.Provider = "gemini" }, ReasonRouteInvalid},
		"empty payload":         {func(e *Envelope) { e.Payload = "" }, ReasonPayloadInvalid},
		"payload with a NUL":    {func(e *Envelope) { e.Payload = "text\x00more" }, ReasonPayloadInvalid},
		"payload not UTF-8":     {func(e *Envelope) { e.Payload = "text\xff" }, ReasonPayloadInvalid},
		"payload over the cap":  {func(e *Envelope) { e.Payload = strings.Repeat("x", MaxPayloadBytes+1) }, ReasonPayloadTooLarge},
	} {
		candidate := messageEnvelopeFixture()
		test.mutate(&candidate)
		err := candidate.Validate()
		if !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: Validate = %v, want an ErrInvalidEnvelope", name, err)
			continue
		}
		if !strings.Contains(err.Error(), test.reason) {
			t.Errorf("%s: Validate = %q, want the reason %q", name, err, test.reason)
		}
	}
}

// TestOversizedPayloadRefusalCarriesTheLimitAndTheActualValue pins the one
// refusal a sender can act on by itself. Without both numbers the only way to
// find the limit is to bisect the body, which is what this replaces.
func TestOversizedPayloadRefusalCarriesTheLimitAndTheActualValue(t *testing.T) {
	t.Parallel()
	if MaxPayloadBytes != 4<<10 {
		t.Fatalf("MaxPayloadBytes = %d, want the unchanged 4096", MaxPayloadBytes)
	}
	candidate := messageEnvelopeFixture()
	candidate.Payload = strings.Repeat("x", MaxPayloadBytes+512)
	err := candidate.Validate()
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("Validate = %v, want an ErrInvalidEnvelope", err)
	}
	want := fmt.Sprintf("payloadBytes=%d limitBytes=%d", MaxPayloadBytes+512, MaxPayloadBytes)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate = %q, want it to contain %q", err, want)
	}
}

// TestReplyRefusalNamesTheEnvelopeRuleRatherThanCorrelation covers the case
// that sent two readers looking at the wrong thing: a reply whose own payload
// is too large fails on size, and ValidateReply must say so rather than report
// the correlation it never got to judge.
func TestReplyRefusalNamesTheEnvelopeRuleRatherThanCorrelation(t *testing.T) {
	t.Parallel()
	original := messageEnvelopeFixture()
	reply := messageEnvelopeFixture()
	reply.MessageRef = "message-two"
	reply.ReplyTo = original.MessageRef
	reply.Source, reply.Target = original.Target, original.Source
	if err := ValidateReply(original, reply); err != nil {
		t.Fatalf("a correlating reply must validate: %v", err)
	}
	reply.Payload = strings.Repeat("x", MaxPayloadBytes+1)
	err := ValidateReply(original, reply)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("ValidateReply = %v, want an ErrInvalidEnvelope", err)
	}
	if !strings.Contains(err.Error(), ReasonPayloadTooLarge) {
		t.Fatalf("ValidateReply = %q, want the payload size reason", err)
	}
	if strings.Contains(err.Error(), "correlation") {
		t.Fatalf("ValidateReply = %q, want it not to blame correlation for a size failure", err)
	}
}

// TestValidateStillRefusesEveryShapeItRefusedBefore holds the boundary this
// change must not move: naming the broken rule may not make a refused envelope
// acceptable, nor a valid one refused.
func TestValidateStillRefusesEveryShapeItRefusedBefore(t *testing.T) {
	t.Parallel()
	if err := messageEnvelopeFixture().Validate(); err != nil {
		t.Fatalf("the valid fixture must stay valid: %v", err)
	}
	if err := operatorEnvelopeFixture().Validate(); err != nil {
		t.Fatalf("the valid operator fixture must stay valid: %v", err)
	}
	operator := operatorEnvelopeFixture()
	operator.Target.Provider = "codex"
	err := operator.Validate()
	if !errors.Is(err, ErrOperatorOriginTargetNotClaude) || !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("operator input for a non-Claude target = %v, want the unchanged sentinel", err)
	}
}
