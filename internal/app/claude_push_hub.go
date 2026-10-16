package app

import (
	"errors"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
)

const claudeNativePeerJoinedReason = "provider-native-peer-joined"

type claudeProviderCoordinationContent struct {
	Kind            string                 `json:"kind"`
	SchemaVersion   int                    `json:"schemaVersion"`
	Authority       string                 `json:"authority"`
	MessageRef      string                 `json:"messageRef"`
	ConversationRef string                 `json:"conversationRef"`
	ReplyTo         string                 `json:"replyTo,omitempty"`
	Source          any                    `json:"source"` // coordinationFrameRoute or coordinationFrameOrigin
	Target          coordinationFrameRoute `json:"target"`
	Payload         string                 `json:"payload"`
	SourceNotice    string                 `json:"sourceNotice"`
	ReplyAction     string                 `json:"replyAction"`
}

func providerCoordinationContent(envelope claudeCoordinationEnvelope, executable ...string) (string, error) {
	toolExecutable := ""
	if len(executable) == 1 {
		toolExecutable = executable[0]
	}
	return renderProviderCoordinationContent(envelope, toolExecutable, false)
}

// renderProviderCoordinationContent renders the frame content. sizeAsPeer
// keeps the peer replyAction on a self-anchored frame; only the sender's size
// pre-check asks for it, and the frame actually sent never does.
func renderProviderCoordinationContent(envelope claudeCoordinationEnvelope, executable string, sizeAsPeer bool) (string, error) {
	if envelope.BrokerEnvelope == nil {
		return "", errors.New("claude coordination broker envelope is unavailable")
	}
	return agentmessages.RenderClaudeContent(*envelope.BrokerEnvelope, executable, sizeAsPeer)
}

// submitPush is immediate. Only a proved busy process Claude reservation
// returns held for durable release; other results are terminal. Broker handoff
// persistence precedes the sole provider write. A known
// zero-byte failure is safe to report as non-ambiguous; any bytes without a
// full helper receipt are ambiguous and never retried.
func (h *claudeCoordinationHub) submitPush(envelope claudeCoordinationEnvelope, broker claudeDialogueBroker,
	poster claudeProviderPoster,
) agentdelivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.messages[envelope.MessageRef]; existing != nil {
		return existing.delivery
	}
	delivery, _ := agentdelivery.Reduce(agentdelivery.Delivery{}, agentdelivery.Event{
		Kind: agentdelivery.EventQueue, MessageRef: envelope.MessageRef,
	})
	message := &claudeCoordinationMessage{envelope: envelope, delivery: delivery}
	h.messages[envelope.MessageRef] = message
	if h.closed {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventStale, MessageRef: envelope.MessageRef, Reason: "helper-stale",
		})
		return message.delivery
	}
	if !envelope.Deadline.After(h.now()) {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventExpire, MessageRef: envelope.MessageRef, Reason: "ttl",
		})
		return message.delivery
	}
	// Delivery no longer waits for an explicit-reply qualification. A live
	// poster is the whole precondition; qualification now governs only the
	// reply path.
	if poster == nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventRefuse, MessageRef: envelope.MessageRef, Reason: "exact-provider-version-unqualified",
		})
		return message.delivery
	}
	content, err := providerCoordinationContent(envelope, h.replyExecutable)
	if err != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventRefuse, MessageRef: envelope.MessageRef, Reason: "provider-frame-unsupported",
		})
		return message.delivery
	}
	// A process reservation must precede durable handoff: a busy host proves
	// zero writes, leaving the durable record safe for held release.
	processPoster, process := poster.(*processClaudeProviderPoster)
	joined := false
	if process {
		// A proof that fails here precedes MarkHandoff, so it journals here.
		prove := func() bool { return broker.Current(*envelope.BrokerEnvelope) }
		if journal, ok := broker.(claudeReservationProver); ok {
			prove = func() bool { return journal.CurrentBeforeReservation(*envelope.BrokerEnvelope) }
		}
		outcome, reserveErr := processPoster.reserve(content, func() bool {
			return envelope.Deadline.After(h.now()) && broker != nil && prove()
		}, true)
		joined = outcome.Joined
		if reserveErr != nil {
			if outcome.Reason == claudeHoldReasonTurnActive {
				delete(h.messages, envelope.MessageRef) // retry only this proven zero-write refusal
				return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateHeld, Reason: outcome.Reason}
			}
			message.delivery = agentdelivery.Delivery{State: agentdelivery.StateFailed, MessageRef: envelope.MessageRef,
				WaiterRef: newCoordinationRef("push"), Reason: outcome.Reason}
			return message.delivery
		}
	}
	if envelope.BrokerEnvelope == nil || broker == nil || broker.MarkHandoff(*envelope.BrokerEnvelope) != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, Reason: "broker-handoff-persist-failed",
			OutcomeKnown: true,
		})
		if process {
			_, _ = processPoster.exchange(content, "finish", claudeProviderPostOutcome{})
		}
		return message.delivery
	}
	// Durable broker work may cross the deadline. Recheck at the final
	// pre-write boundary so an expired message can never reach the provider.
	if !envelope.Deadline.After(h.now()) {
		if process {
			_, _ = processPoster.exchange(content, "finish", claudeProviderPostOutcome{})
		}
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventExpire, MessageRef: envelope.MessageRef, Reason: "ttl-after-durable-handoff",
		})
		return message.delivery
	}
	handoffRef := newCoordinationRef("push")
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
		Kind: agentdelivery.EventBeginHandoff, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
	})
	post := poster.Post
	if process {
		post = processPoster.postReserved
	}
	outcome, postErr := post(content, func() bool {
		return envelope.Deadline.After(h.now()) && broker.Current(*envelope.BrokerEnvelope)
	})
	if postErr != nil || !outcome.FullFrameWritten {
		reason := outcome.Reason
		if outcome.WroteAny || outcome.FullFrameWritten {
			reason = "provider-handoff-outcome-unknown"
			if outcome.Reason == "provider-write-partial" && !outcome.FullFrameWritten {
				reason = outcome.Reason
			}
		} else if !knownClaudeProviderFailureReason(reason) {
			reason = "provider-prewrite-refused"
		}
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
			Reason: reason, OutcomeKnown: !outcome.WroteAny && !outcome.FullFrameWritten,
		})
		return message.delivery
	}
	var reasons []string
	if joined {
		reasons = []string{claudeNativePeerJoinedReason}
	}
	if broker.MarkDelivered(*envelope.BrokerEnvelope, h.now(), reasons...) != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
			Reason: "broker-delivery-persist-failed",
		})
		return message.delivery
	}
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
		Kind: agentdelivery.EventDeliver, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
		FullFrameWritten: true, HelperReceipt: true,
	})
	if joined {
		message.delivery.Reason = claudeNativePeerJoinedReason
	}
	return message.delivery
}
