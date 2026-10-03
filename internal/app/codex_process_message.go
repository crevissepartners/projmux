package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The foreground owner supplies the exact endpoint set. There is no ambient
// daemon lookup or fallback to the shared broker. Delivered witnesses a typed
// turn/start acceptance, separately from the provider's later turn result.
type codexProcessMessages struct {
	mu        sync.Mutex
	store     *messagestore.Store
	endpoints map[string]*codexProcessEndpoint
	outcomes  map[string]coremessage.Event
	now       func() time.Time
}
type codexProcessReceipt struct{ Delivery coremessage.Delivery }

func processCodexMessageRoute(ctx context.Context, e *codexProcessEndpoint) (coremessage.Route, error) {
	if e == nil {
		return coremessage.Route{}, processhost.ErrStale
	}
	route, err := e.route(ctx)
	if err != nil {
		return coremessage.Route{}, err
	}
	return coremessage.Route{AgentUID: route.AgentUID, PaneUID: route.PaneUID, ActivationGeneration: route.Generation, Provider: "codex", Incarnation: route.Incarnation()}, nil
}
func (m *codexProcessMessages) send(ctx context.Context, source, target, messageRef, conversationRef, payload string, now, deadline time.Time) (codexProcessReceipt, error) {
	from, err := processCodexMessageRoute(ctx, m.endpoints[source])
	if err != nil {
		return codexProcessReceipt{}, err
	}
	to, err := processCodexMessageRoute(ctx, m.endpoints[target])
	if err != nil {
		return codexProcessReceipt{}, err
	}
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: messageRef, ConversationRef: conversationRef, Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: payload, AcceptedAt: now, Deadline: deadline}
	record, _, err := m.store.PutAccepted(envelope, "codex-inbox")
	if err != nil {
		return codexProcessReceipt{}, err
	}
	return m.deliver(ctx, record)
}
func (m *codexProcessMessages) reply(ctx context.Context, source, target, originalRef, messageRef, payload string, now, deadline time.Time) (codexProcessReceipt, error) {
	from, err := processCodexMessageRoute(ctx, m.endpoints[source])
	if err != nil {
		return codexProcessReceipt{}, err
	}
	to, err := processCodexMessageRoute(ctx, m.endpoints[target])
	if err != nil {
		return codexProcessReceipt{}, err
	}
	record, _, err := m.store.PutReply(originalRef, messageRef, payload, from, to, now, deadline)
	if err != nil {
		return codexProcessReceipt{}, err
	}
	return m.deliver(ctx, record)
}
func (m *codexProcessMessages) deliver(ctx context.Context, record messagestore.Record) (codexProcessReceipt, error) {
	if record.Delivery.State.Terminal() {
		m.mu.Lock()
		delete(m.outcomes, record.Envelope.MessageRef)
		m.mu.Unlock()
		return codexProcessReceipt{Delivery: record.Delivery}, nil
	}
	e := m.endpoints[record.Envelope.Target.AgentUID]
	if e == nil {
		return codexProcessReceipt{}, processhost.ErrStale
	}
	result, err := e.call(ctx, record.Envelope.MessageRef)
	if err != nil {
		return codexProcessReceipt{}, err
	}
	if result.Receipt == nil {
		return codexProcessReceipt{}, processhost.ErrStale
	}
	return *result.Receipt, nil
}
func (m *codexProcessMessages) receive(ctx context.Context, target *codexProcessEndpoint, messageRef string) (codexProcessReceipt, error) {
	// One claim covers the read, host admission and terminal receipt. A failed
	// receipt commit cannot cause a replay: the host consumes the operation ID.
	m.mu.Lock()
	defer m.mu.Unlock()
	record, found, err := m.store.Get(messageRef)
	if err != nil {
		return codexProcessReceipt{}, err
	}
	if !found {
		return codexProcessReceipt{}, messagestore.ErrNotFound
	}
	if record.Delivery.State.Terminal() {
		delete(m.outcomes, messageRef)
		return codexProcessReceipt{Delivery: record.Delivery}, nil
	}
	envelope := record.Envelope
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	observedAt := now().UTC()
	if envelope.AcceptedAt.After(observedAt) {
		return codexProcessReceipt{}, coremessage.EnvelopeRefusal(coremessage.ReasonQualificationInvalid, "message acceptance is in the future")
	}
	to, err := processCodexMessageRoute(ctx, target)
	kind, reason, unknown := coremessage.EventDeliver, "host-turn-accepted", false
	from, sourceErr := processCodexMessageRoute(ctx, m.endpoints[envelope.Source.AgentUID])
	switch {
	case !envelope.Deadline.After(observedAt):
		kind, reason = coremessage.EventExpire, "deadline-expired"
	case err != nil || sourceErr != nil || !to.Same(envelope.Target) || !from.Same(envelope.Source):
		kind, reason = coremessage.EventStale, "stale-binding"
	default:
		content, encodeErr := json.Marshal(envelope)
		if encodeErr != nil {
			return codexProcessReceipt{}, encodeErr
		}
		if previous, known := m.outcomes[messageRef]; known {
			kind, reason, unknown = previous.Kind, previous.Reason, previous.OutcomeUnknown
			break
		}
		// The bounded inbox and host operation ledger forbid replay. Retain
		// the witnessed outcome if its disk receipt commit fails, so a later
		// query settles that receipt without another turn/start.
		if len(m.outcomes) >= 256 {
			kind, reason = coremessage.EventRefuse, "host-busy"
			break
		}
		operation := fmt.Sprintf("message-%x", sha256.Sum256([]byte(messageRef)))
		turnErr := target.handle.Turn(ctx, target.authority(), operation, string(content))
		switch {
		case turnErr == nil:
		case errors.Is(turnErr, processhost.ErrBusy):
			kind, reason = coremessage.EventRefuse, "host-busy"
		case errors.Is(turnErr, processhost.ErrStale):
			kind, reason = coremessage.EventStale, "stale-binding"
		case codexappserver.IsResponseError(turnErr):
			kind, reason = coremessage.EventRefuse, "provider-refused"
		default:
			kind, reason, unknown = coremessage.EventFail, "delivery-outcome-unknown", true
		}
		if m.outcomes == nil {
			m.outcomes = make(map[string]coremessage.Event)
		}
		m.outcomes[messageRef] = coremessage.Event{Kind: kind, Reason: reason, OutcomeUnknown: unknown}
	}
	record, _, err = m.store.ApplyMatching(envelope, "codex-inbox", coremessage.Event{Kind: kind, MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, Reason: reason, ObservedAt: now().UTC(), OutcomeUnknown: unknown})
	if err == nil {
		delete(m.outcomes, messageRef)
	}
	return codexProcessReceipt{Delivery: record.Delivery}, err
}
