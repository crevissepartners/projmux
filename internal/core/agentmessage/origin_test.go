package agentmessage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/operatorclient"
)

// operatorTestClient is the client the operator fixtures name. It is not a
// client any build ships, so a rule that still knew one client by name fails.
const operatorTestClient = "console"

func operatorOriginFixture() Origin {
	origin, err := OperatorOrigin(operatorTestClient)
	if err != nil {
		panic(err)
	}
	return origin
}

func operatorEnvelopeFixture() Envelope {
	envelope := messageEnvelopeFixture()
	envelope.MessageRef = "message-operator"
	envelope.Origin = operatorOriginFixture()
	envelope.Source = Route{}
	envelope.Authority = OperatorAuthority()
	return envelope
}

func TestOperatorOriginEnvelopeValidatesAndMixedShapesAreRefused(t *testing.T) {
	t.Parallel()
	if err := operatorEnvelopeFixture().Validate(); err != nil {
		t.Fatalf("operator envelope: %v", err)
	}
	if !operatorEnvelopeFixture().Operator() || messageEnvelopeFixture().Operator() {
		t.Fatal("Operator() does not separate operator input from an Agent message")
	}
	agent := messageEnvelopeFixture()
	for name, mutate := range map[string]func(*Envelope){
		"operator with a full source route":   func(e *Envelope) { e.Source = agent.Source },
		"operator with one source field":      func(e *Envelope) { e.Source.Provider = "claude" },
		"operator with peer authority":        func(e *Envelope) { e.Authority = PeerAuthority() },
		"operator with human-like permission": func(e *Envelope) { e.Authority.Permission = "turn-start" },
		"operator as a reply":                 func(e *Envelope) { e.ReplyTo = "message-earlier" },
		"explicit agent origin":               func(e *Envelope) { e.Origin = Origin{Kind: "agent"}; e.Source = agent.Source },
		"unknown origin kind":                 func(e *Envelope) { e.Origin = Origin{Kind: "system", Client: operatorTestClient} },
		"operator client outside the rule":    func(e *Envelope) { e.Origin = Origin{Kind: OriginKindOperator, Client: "Console"} },
		"operator without a client":           func(e *Envelope) { e.Origin = Origin{Kind: OriginKindOperator} },
		"empty origin":                        func(e *Envelope) { e.Origin = Origin{} },
		"operator with invalid target":        func(e *Envelope) { e.Target.PaneUID = "" },
	} {
		candidate := operatorEnvelopeFixture()
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: Validate = %v, want ErrInvalidEnvelope", name, err)
		}
	}
	for name, mutate := range map[string]func(*Envelope){
		"agent without a source route":    func(e *Envelope) { e.Source = Route{} },
		"agent with a partial source":     func(e *Envelope) { e.Source.Incarnation = "" },
		"agent with operator authority":   func(e *Envelope) { e.Authority = OperatorAuthority() },
		"agent route with operator label": func(e *Envelope) { e.Origin = operatorOriginFixture() },
	} {
		candidate := messageEnvelopeFixture()
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: Validate = %v, want ErrInvalidEnvelope", name, err)
		}
	}
}

// TestOperatorOriginNamesAnyClientUnderTheRule pins the constructor and the one
// operator judgement to the operatorclient rule: any name under it is an
// operator client, including the one name earlier builds wrote, and a name
// outside it is refused with the rule's token and is never operator input.
func TestOperatorOriginNamesAnyClientUnderTheRule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		client string
		valid  bool
	}{
		{operatorTestClient, true},
		{"ui", true},
		{"client-2", true},
		{"web", true}, // the name earlier builds wrote; their records stay operator input
		{"", false},
		{"Console", false},
		{"2console", false},
		{"con sole", false},
		{strings.Repeat("c", operatorclient.MaxBytes+1), false},
	} {
		origin, err := OperatorOrigin(test.client)
		if test.valid {
			if err != nil || origin != (Origin{Kind: OriginKindOperator, Client: test.client}) || !origin.Operator() {
				t.Errorf("OperatorOrigin(%q) = (%+v, %v), want an operator origin naming it", test.client, origin, err)
			}
		} else if err == nil || !errors.Is(err, operatorclient.ErrInvalid) || origin != (Origin{}) {
			t.Errorf("OperatorOrigin(%q) = (%+v, %v), want the %s refusal", test.client, origin, err, operatorclient.ReasonInvalid)
		}
		if got := IsOperatorOrigin(OriginKindOperator, test.client); got != test.valid {
			t.Errorf("IsOperatorOrigin(operator, %q) = %t, want %t", test.client, got, test.valid)
		}
		if IsOperatorOrigin("agent", test.client) || IsOperatorOrigin("", test.client) {
			t.Errorf("IsOperatorOrigin judged %q operator input without the operator kind", test.client)
		}
	}
}

func TestOperatorOriginForANonClaudeTargetIsRefusedWithItsReasonToken(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"codex", "antigravity"} {
		candidate := operatorEnvelopeFixture()
		candidate.Target.Provider = provider
		err := candidate.Validate()
		if !errors.Is(err, ErrOperatorOriginTargetNotClaude) || !errors.Is(err, ErrInvalidEnvelope) ||
			!strings.Contains(err.Error(), ReasonOperatorOriginTargetNotClaude) {
			t.Errorf("%s target: Validate = %v, want %q", provider, err, ReasonOperatorOriginTargetNotClaude)
		}
	}
	// The token belongs to operator input alone: an Agent message to Codex is
	// an ordinary valid envelope.
	agent := messageEnvelopeFixture()
	agent.Target.Provider = "codex"
	if err := agent.Validate(); err != nil {
		t.Fatalf("agent message to codex: %v", err)
	}
}

// TestAgentEnvelopeWireIsUnchangedByOrigin pins an Agent envelope's bytes to
// the encoding that predates Origin. An older helper decodes the shared store
// with DisallowUnknownFields, so a single new key on an Agent record would make
// it refuse the whole store.
func TestAgentEnvelopeWireIsUnchangedByOrigin(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(messageEnvelopeFixture())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":2,"messageRef":"message-one","conversationRef":"conversation-one",` +
		`"source":{"agentUID":"agent-source","paneUID":"pane-source","activationGeneration":"generation-source","provider":"codex","incarnation":"route-source"},` +
		`"target":{"agentUID":"agent-target","paneUID":"pane-target","activationGeneration":"generation-target","provider":"claude","incarnation":"route-target"},` +
		`"authority":{"kind":"peer","trust":"untrusted","permission":"coordination-only"},` +
		`"payload":"untrusted coordination text","acceptedAt":"2026-09-06T01:02:03Z","deadline":"2026-09-06T01:03:03Z"}`
	if string(data) != want {
		t.Fatalf("agent envelope =\n%s\nwant\n%s", data, want)
	}
}

func TestOperatorEnvelopeWireCarriesOriginAndNoSource(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(operatorEnvelopeFixture())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":2,"messageRef":"message-operator","conversationRef":"conversation-one",` +
		`"origin":{"kind":"operator","client":"` + operatorTestClient + `"},` +
		`"target":{"agentUID":"agent-target","paneUID":"pane-target","activationGeneration":"generation-target","provider":"claude","incarnation":"route-target"},` +
		`"authority":{"kind":"operator","trust":"untrusted","permission":"coordination-only"},` +
		`"payload":"untrusted coordination text","acceptedAt":"2026-09-06T01:02:03Z","deadline":"2026-09-06T01:03:03Z"}`
	if string(data) != want {
		t.Fatalf("operator envelope =\n%s\nwant\n%s", data, want)
	}
	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Validate() != nil || !decoded.SameRetry(operatorEnvelopeFixture()) {
		t.Fatalf("round trip = %+v, %v", decoded, err)
	}
}

func TestSameRetryComparesOrigin(t *testing.T) {
	t.Parallel()
	operator := operatorEnvelopeFixture()
	if !operator.SameRetry(operatorEnvelopeFixture()) {
		t.Fatal("identical operator retry refused")
	}
	agent := operatorEnvelopeFixture()
	agent.Origin, agent.Source, agent.Authority = Origin{}, messageEnvelopeFixture().Source, PeerAuthority()
	if operator.SameRetry(agent) || agent.SameRetry(operator) {
		t.Fatal("an operator message and an Agent message with the same refs compare as one retry")
	}
	other := operatorEnvelopeFixture()
	other.Origin = Origin{Kind: OriginKindOperator, Client: "ui"}
	if operator.SameRetry(other) || other.SameRetry(operator) || !other.SameRetry(other) {
		t.Fatal("operator input from two clients compares as one retry")
	}
	outside := operatorEnvelopeFixture()
	outside.Origin = Origin{Kind: OriginKindOperator, Client: "Console"}
	if operator.SameRetry(outside) || outside.SameRetry(outside) {
		t.Fatal("an origin outside the client rule compares as a retry")
	}
}

func TestReplyToOperatorOriginIsRefusedWithItsReasonToken(t *testing.T) {
	t.Parallel()
	original := operatorEnvelopeFixture()
	reply := messageEnvelopeFixture()
	reply.MessageRef = "message-reply"
	reply.ReplyTo = original.MessageRef
	reply.ConversationRef = original.ConversationRef
	reply.Source = original.Target
	err := ValidateReply(original, reply)
	if !errors.Is(err, ErrOperatorOriginReply) || !errors.Is(err, ErrInvalidEnvelope) ||
		!strings.Contains(err.Error(), ReasonExplicitReplyOperatorOrigin) {
		t.Fatalf("ValidateReply = %v, want %q", err, ReasonExplicitReplyOperatorOrigin)
	}
}
