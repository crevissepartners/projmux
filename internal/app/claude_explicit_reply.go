package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// The public CLI caller must descend from the exact registered provider birth.
// Same UID, inherited environment, a session ID, or a reply ref alone is not
// authority. Every parent identity is observed again before accepting a chain.
func claudeProviderDescendant(peer, provider coremetadata.ProcessIdentity) bool {
	current := peer
	for range 16 {
		actual, parent, err := localipc.Process(current.PID)
		if err != nil || actual != current || actual.OwnerUID != provider.OwnerUID {
			return false
		}
		if actual == provider {
			return current != peer
		}
		if parent <= 1 || parent == current.PID {
			return false
		}
		current, _, err = localipc.Process(parent)
		if err != nil {
			return false
		}
	}
	return false
}

func (a liveAgentMessageClaudeAdapter) ExplicitReply(ctx context.Context, registryPath string,
	source coremetadata.AgentRouteRef, reply coremessage.Envelope,
) (string, error) {
	target, ok := claudeTargetForRoute(source)
	if !ok {
		return "", coremessage.ErrInvalidEnvelope
	}
	ctx, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, source, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "explicit-reply", Target: target,
		SessionID: target.Authority.SessionID, ReplyEnvelope: &reply,
	})
	if err != nil || response.Kind != "reply-accepted" || response.ReplyRef != reply.MessageRef || response.AutoResend {
		return "", errors.New("exact explicit reply was not acknowledged; do not resend")
	}
	return response.ReplyRef, nil
}

func (b *liveClaudeDialogueBroker) CommitReply(original, reply coremessage.Envelope) error {
	// The reply target is no longer pinned to Codex. Plain homogeneous sends
	// already succeed through the broker, so refusing only their replies left a
	// lane that half worked and reported no reason for the half that did not.
	if b == nil || b.store == nil || coremessage.ValidateReply(original, reply) != nil ||
		!original.Deadline.After(time.Now()) || !b.Current(reply) {
		return coremessage.ErrInvalidEnvelope
	}
	_, _, err := b.store.PutReply(original.MessageRef, reply.MessageRef, reply.Payload, reply.Source,
		reply.Target, reply.AcceptedAt, reply.Deadline)
	return err
}

// commitExplicitReply selects the broker's original request by ref, never by
// payload, Stop text, arrival order, or number of outstanding messages. A human
// turn does not invalidate an otherwise current explicit reply.
func (h *claudeCoordinationHub) commitExplicitReply(reply coremessage.Envelope,
	source coremetadata.AgentRouteRef, broker claudeDialogueBroker,
) claudeCoordinationResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	refuse := func(reason string) claudeCoordinationResponse {
		return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: reason}
	}
	h.expireQualificationLocked(h.now())
	message := h.messages[reply.ReplyTo]
	if h.closed || broker == nil || message == nil || message.envelope.BrokerEnvelope == nil ||
		message.delivery.State != agentdelivery.StateDelivered || !message.envelope.Deadline.After(h.now()) ||
		reply.Source != publicMessageRoute(source) || coremessage.ValidateReply(*message.envelope.BrokerEnvelope, reply) != nil {
		return refuse("invalid-explicit-reply-correlation")
	}
	// A reply is a qualification answer only when it answers the pending
	// challenge. Ordinary replies no longer have to wait for qualification;
	// the challenge itself still has to match its marker exactly.
	state := h.qualification
	qualification := state != nil && state.state == "pending" && state.ref == reply.ReplyTo
	if qualification && (!state.frameComplete || reply.Payload != state.marker) {
		return refuse("qualification-challenge-only")
	}
	if message.replyReserved {
		return refuse("broker-reply-outcome-unknown")
	}
	if message.replyRef != "" && message.replyRef != reply.MessageRef {
		return refuse("reply-already-committed")
	}
	if !broker.Current(reply) {
		return refuse("explicit-reply-route-stale")
	}
	// Reserve before the durable call. A failed/ambiguous commit is never
	// retried automatically, including by a later unrelated Stop.
	message.replyReserved = true
	if broker.CommitReply(*message.envelope.BrokerEnvelope, reply) != nil {
		return refuse("broker-reply-outcome-unknown")
	}
	message.replyReserved = false
	message.replyRef = reply.MessageRef
	if qualification {
		h.qualification.state = "qualified"
		h.qualification.reason = "exact-public-init-and-explicit-reply"
		h.qualification.ambiguous = false
		h.qualifiedVersion = claudeFrozenFrameProviderVersion
	}
	return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-accepted", ReplyRef: reply.MessageRef}
}

func (e claudeQualificationEvidence) validExplicit(now time.Time, route coremetadata.AgentRouteRef) bool {
	if len(e.Tools) != 1 || e.Tools[0] != "Bash" || !e.ReplyExecutionGate {
		return false
	}
	// All existing exact version/process/lease and no MCP/plugin/effect checks
	// remain mandatory; tools=[] is replaced by the bounded explicit tool.
	e.Tools = []string{}
	return e.valid(now, route)
}

func (h *claudeCoordinationHub) beginExplicitQualification(evidence claudeQualificationEvidence,
	route coremetadata.AgentRouteRef, envelope *claudeCoordinationEnvelope, broker claudeDialogueBroker,
	poster claudeProviderPoster,
) claudeCoordinationResponse {
	h.mu.Lock()
	now := h.now()
	refuse := claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-refused",
		Reason: "invalid-public-init-or-broker-challenge"}
	if h.closed || !evidence.validExplicit(now, route) || envelope == nil || !envelope.valid(now, route) ||
		envelope.BrokerEnvelope == nil || envelope.BrokerEnvelope.Source.Provider != "codex" || envelope.BrokerEnvelope.ReplyTo != "" ||
		!strings.HasPrefix(envelope.MessageRef, "qualification-") || broker == nil || poster == nil {
		h.mu.Unlock()
		return refuse
	}
	h.expireQualificationLocked(now)
	if h.qualification != nil {
		// One attempt for this exact helper incarnation. An uncertain write or
		// timeout cannot silently create another challenge and resend.
		response := h.qualificationResponseLocked()
		h.mu.Unlock()
		return response
	}
	state := &claudeQualificationState{ref: envelope.MessageRef, marker: claudeQualificationMarkerPrefix + envelope.MessageRef,
		state: "writing", expiresAt: envelope.Deadline}
	h.qualification = state
	content, err := providerCoordinationContent(*envelope, h.replyExecutable)
	if err != nil || broker.MarkHandoff(*envelope.BrokerEnvelope) != nil {
		state.state, state.reason = "failed", "qualification-provider-write-zero"
		response := h.qualificationResponseLocked()
		h.mu.Unlock()
		return response
	}
	// Match ordinary submitPush: publish the durable original and delivered
	// message before any explicit reply can enter. The provider write has its
	// existing bounded deadline; reply speed cannot race this publication.
	defer h.mu.Unlock()
	outcome, postErr := poster.Post(content, func() bool {
		return envelope.Deadline.After(h.now()) && broker.Current(*envelope.BrokerEnvelope)
	})
	if state.state != "writing" || h.closed {
		state.ambiguous = outcome.WroteAny
		return qualificationResponseForState(state)
	}
	if postErr != nil || !outcome.FullFrameWritten {
		state.state, state.reason = "failed", "qualification-provider-write-zero"
		if outcome.WroteAny {
			state.reason, state.ambiguous = "qualification-provider-outcome-unknown", true
		}
		return h.qualificationResponseLocked()
	}
	state.frameComplete = true
	if broker.MarkDelivered(*envelope.BrokerEnvelope, h.now()) != nil {
		state.state, state.reason, state.ambiguous = "failed", "qualification-provider-outcome-unknown", true
		return h.qualificationResponseLocked()
	}
	h.messages[envelope.MessageRef] = &claudeCoordinationMessage{envelope: *envelope,
		delivery: agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateDelivered}}
	state.state = "pending"
	return h.qualificationResponseLocked()
}
