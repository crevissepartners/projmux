package app

import (
	"encoding/json"
	"errors"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

type claudeProviderCoordinationContent struct {
	Kind            string            `json:"kind"`
	Authority       string            `json:"authority"`
	MessageRef      string            `json:"messageRef"`
	ConversationRef string            `json:"conversationRef"`
	ReplyTo         string            `json:"replyTo,omitempty"`
	Source          coremessage.Route `json:"source"`
	Target          coremessage.Route `json:"target"`
	Payload         string            `json:"payload"`
}

func providerCoordinationContent(envelope claudeCoordinationEnvelope) (string, error) {
	if envelope.BrokerEnvelope == nil {
		return "", errors.New("claude coordination broker envelope is unavailable")
	}
	broker := envelope.BrokerEnvelope
	content, err := json.Marshal(claudeProviderCoordinationContent{
		Kind: "projmux-coordination", Authority: "untrusted-coordination-only",
		MessageRef: broker.MessageRef, ConversationRef: broker.ConversationRef, ReplyTo: broker.ReplyTo,
		Source: broker.Source, Target: broker.Target, Payload: broker.Payload,
	})
	if err != nil || len(content) > claudeProviderFrameMaxBytes {
		return "", errors.New("claude coordination provider content is unavailable")
	}
	return string(content), nil
}

// submitPush is immediate and terminal: it never creates a held/no-waiter
// state. Broker handoff persistence precedes the sole provider write. A known
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
	message := &claudeCoordinationMessage{envelope: envelope, delivery: delivery, boundary: h.boundary,
		dialogueAmbiguous: h.humanTurnOpen}
	if message.dialogueAmbiguous {
		message.dialogueReason = "concurrent-user-turn-ambiguous"
	}
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
	if h.qualifiedVersion != claudeFrozenFrameProviderVersion || poster == nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventRefuse, MessageRef: envelope.MessageRef, Reason: "exact-provider-version-unqualified",
		})
		return message.delivery
	}
	content, err := providerCoordinationContent(envelope)
	if err != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventRefuse, MessageRef: envelope.MessageRef, Reason: "provider-frame-unsupported",
		})
		return message.delivery
	}
	if envelope.BrokerEnvelope == nil || broker == nil || broker.MarkHandoff(*envelope.BrokerEnvelope) != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, Reason: "broker-handoff-persist-failed",
			OutcomeKnown: true,
		})
		return message.delivery
	}
	// Durable broker work may cross the deadline. Recheck at the final
	// pre-write boundary so an expired message can never reach the provider.
	if !envelope.Deadline.After(h.now()) {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventExpire, MessageRef: envelope.MessageRef, Reason: "ttl-after-durable-handoff",
		})
		return message.delivery
	}
	handoffRef := newCoordinationRef("push")
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
		Kind: agentdelivery.EventBeginHandoff, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
	})
	outcome, postErr := poster.Post(content, func() bool {
		return envelope.Deadline.After(h.now()) && broker.Current(*envelope.BrokerEnvelope)
	})
	if postErr != nil || !outcome.FullFrameWritten {
		reason := "provider-write-zero"
		if outcome.WroteAny {
			h.replyCorrelationReason = "provider-handoff-outcome-unknown"
			reason = "provider-handoff-outcome-unknown"
		}
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
			Reason: reason, OutcomeKnown: !outcome.WroteAny,
		})
		return message.delivery
	}
	if broker.MarkDelivered(*envelope.BrokerEnvelope, h.now()) != nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
			Kind: agentdelivery.EventFail, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
			Reason: "broker-delivery-persist-failed",
		})
		message.dialogueAmbiguous = true
		message.dialogueReason = "broker-delivery-persist-failed"
		h.replyCorrelationReason = message.dialogueReason
		return message.delivery
	}
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{
		Kind: agentdelivery.EventDeliver, MessageRef: envelope.MessageRef, WaiterRef: handoffRef,
		FullFrameWritten: true, HelperReceipt: true,
	})
	if !message.dialogueAmbiguous && message.boundary == h.boundary {
		message.dialogueReady = true
	} else {
		message.dialogueAmbiguous = true
		message.dialogueReason = "concurrent-user-turn-ambiguous"
		h.replyCorrelationReason = message.dialogueReason
	}
	return message.delivery
}
