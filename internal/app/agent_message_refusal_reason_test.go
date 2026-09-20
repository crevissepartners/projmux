package app

import (
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

func refusalReasonEnvelopeFixture() coremessage.Envelope {
	now := time.Now().UTC()
	return coremessage.Envelope{
		Version: coremessage.Version, MessageRef: "message-reply", ConversationRef: "conversation-one",
		ReplyTo: "message-original",
		Source: coremessage.Route{AgentUID: "agent-source", PaneUID: "pane-source",
			ActivationGeneration: "generation-source", Provider: "claude", Incarnation: "route-source"},
		Target: coremessage.Route{AgentUID: "agent-target", PaneUID: "pane-target",
			ActivationGeneration: "generation-target", Provider: "claude", Incarnation: "route-target"},
		Authority: coremessage.PeerAuthority(), Payload: "answer", AcceptedAt: now,
		Deadline: now.Add(time.Minute),
	}
}

// TestExplicitReplyRefusalReasonSeparatesTheEnvelopeFromCorrelation pins the
// guard that made --reply-to the difference between a wrong diagnosis and
// none. A reply whose payload is over the envelope limit fails on size, and
// naming correlation sends the reader to --reply-to and previousRef, where
// nothing is wrong.
func TestExplicitReplyRefusalReasonSeparatesTheEnvelopeFromCorrelation(t *testing.T) {
	t.Parallel()
	correlating := refusalReasonEnvelopeFixture()
	if err := correlating.Validate(); err != nil {
		t.Fatalf("the fixture must be a valid envelope: %v", err)
	}
	if got := explicitReplyRefusalReason(correlating); got != "invalid-explicit-reply-correlation" {
		t.Fatalf("a well-formed reply keeps the correlation reason, got %q", got)
	}
	oversized := refusalReasonEnvelopeFixture()
	oversized.Payload = strings.Repeat("x", coremessage.MaxPayloadBytes+1)
	if got := explicitReplyRefusalReason(oversized); got != "invalid-explicit-reply-envelope" {
		t.Fatalf("an oversized reply must not be named a correlation failure, got %q", got)
	}
}

// TestClaudeProviderFrameLimitStaysAboveThePayloadLimit records why the
// numbered frame refusal cannot be what an oversized body meets. The envelope
// limit is the tighter of the two and is judged first, so it is the one that
// has to carry numbers.
func TestClaudeProviderFrameLimitStaysAboveThePayloadLimit(t *testing.T) {
	t.Parallel()
	if coremessage.MaxPayloadBytes != 4<<10 {
		t.Fatalf("MaxPayloadBytes = %d, want the unchanged 4096", coremessage.MaxPayloadBytes)
	}
	if claudeProviderFrameMaxBytes != 8<<10 {
		t.Fatalf("claudeProviderFrameMaxBytes = %d, want the unchanged 8192", claudeProviderFrameMaxBytes)
	}
	if claudeProviderFrameMaxBytes <= coremessage.MaxPayloadBytes {
		t.Fatal("the frame limit must stay above the payload limit for this ordering to hold")
	}
}
