// Package agentmessages executes peer coordination for CLI and SDK callers.
// Its transport adapters carry exact routes; no active Pane or actor is inferred.
package agentmessages

import (
	"context"
	"fmt"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// Receipt is the existing redacted CLI projection. Delivery.OutcomeUnknown is
// independent of State. Neither a successful call nor delivered means that a
// model consumed the coordination or completed its work.
type Receipt struct {
	Version         int              `json:"version"`
	MessageRef      string           `json:"messageRef"`
	ConversationRef string           `json:"conversationRef"`
	ReplyTo         string           `json:"replyTo,omitempty"`
	Origin          message.Origin   `json:"origin,omitzero"`
	Source          message.Route    `json:"source,omitzero"`
	Target          message.Route    `json:"target"`
	Delivery        message.Delivery `json:"delivery"`
	Deadline        time.Time        `json:"deadline"`
}

func ReceiptFor(r store.Record) Receipt {
	return Receipt{Version: r.Envelope.Version, MessageRef: r.Envelope.MessageRef,
		ConversationRef: r.Envelope.ConversationRef, ReplyTo: r.Envelope.ReplyTo,
		Origin: r.Envelope.Origin, Source: r.Envelope.Source, Target: r.Envelope.Target,
		Delivery: r.Delivery, Deadline: r.Envelope.Deadline}
}

type Store interface {
	Get(string) (store.Record, bool, error)
	PutAccepted(message.Envelope, string) (store.Record, bool, error)
	Apply(string, message.Event) (store.Record, bool, error)
	MarkHandoff(string) (store.Record, bool, error)
	Status(string, time.Time) (store.Record, bool, error)
	Claim(message.Route, time.Time) (store.Record, bool, error)
}

// Service owns receipt transitions. Adapters only resolve exact authorities and
// exchange bounded provider frames. Status never dispatches a message.
type Service struct {
	CorrelationRefusal func(string, string) error
	// Only the CLI adapter sets this; SDK requests always require Source.
	AllowImplicitSource bool
	RegistryPath        string
	ResolveSource       func(context.Context, metadata.Registry, string, string) (metadata.Agent, error)
	ResolveAgent        func(context.Context, metadata.Registry, string, string) (metadata.Agent, error)
	ResolveTarget       func(context.Context, metadata.Registry, metadata.Agent) (metadata.AgentRouteRef, error)
	Deferred            func(context.Context, metadata.Registry, metadata.Agent, metadata.Agent, metadata.AgentRouteRef, string, string, string, time.Duration, func(Receipt, int) error) (bool, error)
	Render              func(context.Context, metadata.AgentRouteRef, message.Envelope) (Render, error)
	Deliver             func(context.Context, store.Record, metadata.Agent, metadata.AgentRouteRef, message.Envelope) (store.Record, error)
	NewRef              func(string) string
	Claude              any
	RouteRefusalPrefix  func(string, error) string
	ResumeRecovery      func(error, metadata.Agent) error
	FailureAction       func(Receipt, int) string
	MapRegistryError    func(error) error
	Accepted            func(metadata.Registry, metadata.Agent, metadata.AgentRouteRef)
	Store               Store
	LoadRegistry        func() (metadata.Registry, error)
	Now                 func() time.Time
	ResolveRoute        func(context.Context, metadata.Registry, metadata.Agent) (metadata.AgentRouteRef, error)
	RequireCapability   func(string, metadata.Agent) error
	ClaudeStatus        func(context.Context, metadata.AgentRouteRef, string) (agentdelivery.Delivery, error)
	DeferredHoldReason  string
}

func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) event(r store.Record, kind message.EventKind, reason string, unknown bool) message.Event {
	return message.Event{Kind: kind, MessageRef: r.Envelope.MessageRef,
		ConversationRef: r.Envelope.ConversationRef, Target: r.Envelope.Target,
		Reason: reason, ObservedAt: s.clock(), OutcomeUnknown: unknown}
}

func RouteAccepts(route metadata.AgentRouteRef, stored message.Route) bool {
	expected := PublicRoute(route)
	expected.Incarnation = stored.Incarnation
	return expected == stored && route.AcceptsIncarnation(stored.Incarnation)
}

// Status reads the original receipt and refreshes only the nonterminal
// projections the CLI already maintains. It never submits or replays input.
func (s Service) Status(ctx context.Context, ref string) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	r, found, err := s.Store.Get(ref)
	if err != nil || !found {
		if err == nil {
			err = store.ErrNotFound
		}
		return Receipt{}, fmt.Errorf("agent message status: %w", err)
	}
	lastKnown := r
	if r.Delivery.State.Terminal() {
		return ReceiptFor(r), nil
	}
	if err := ctx.Err(); err != nil {
		return ReceiptFor(r), err
	}
	reg, err := s.LoadRegistry()
	if err != nil {
		return ReceiptFor(r), err
	}
	target, ok := reg.Agent(r.Envelope.Target.AgentUID)
	stale := func(reason string) (store.Record, bool, error) {
		kind, unknown := message.EventStale, false
		if r.HandoffObserved {
			kind, reason, unknown = message.EventFail, "provider-handoff-outcome-unknown", true
		}
		return s.Store.Apply(r.Envelope.MessageRef, s.event(r, kind, reason, unknown))
	}
	if !ok {
		r, _, err = stale("target-removed")
	} else if capabilityErr := s.RequireCapability("message.status", *target); capabilityErr != nil {
		return ReceiptFor(r), capabilityErr
	} else if r.Delivery.State == message.StateHeld && r.Delivery.Reason == s.DeferredHoldReason {
		r, found, err = s.Store.Status(ref, s.clock())
		if err == nil && !found {
			err = store.ErrNotFound
		}
	} else if route, routeErr := s.ResolveRoute(ctx, reg, *target); routeErr != nil || !RouteAccepts(route, r.Envelope.Target) {
		if ctx.Err() != nil {
			return ReceiptFor(r), ctx.Err()
		}
		r, _, err = stale("target-activation-stale")
	} else if r.Delivery.State == message.StateHeld {
		r, found, err = s.Store.Status(ref, s.clock())
		if err == nil && !found {
			err = store.ErrNotFound
		}
	} else if r.Adapter == "claude-coordination" {
		private, statusErr := s.ClaudeStatus(ctx, route, r.Envelope.MessageRef)
		r, err = s.ProjectClaudeDelivery(r, private, statusErr)
	} else if r.Adapter == "codex-inbox" {
		r, found, err = s.Store.Status(ref, s.clock())
		if err == nil && !found {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		// Failed refresh operations can return a zero record. Retain the last
		// confirmed receipt rather than erasing the only recovery identity.
		if r.Envelope.MessageRef == "" {
			r = lastKnown
		}
		return ReceiptFor(r), fmt.Errorf("agent message status: persist delivery projection: %w", err)
	}
	if ctx.Err() != nil {
		return ReceiptFor(r), ctx.Err()
	}
	return ReceiptFor(r), nil
}

func PublicRoute(route metadata.AgentRouteRef) message.Route {
	provider := ""
	if route.Authority() != nil {
		provider = route.Authority().Provider()
	}
	return message.Route{AgentUID: route.AgentUID, PaneUID: route.PaneUID,
		ActivationGeneration: route.Generation, Provider: provider, Incarnation: route.Incarnation()}
}

func (s Service) ProjectClaudeDelivery(record store.Record, private agentdelivery.Delivery, adapterErr error) (store.Record, error) {
	kind, reason, unknown := message.EventKind(""), private.Reason, private.Ambiguous
	if private.MessageRef != "" && private.MessageRef != record.Envelope.MessageRef {
		return record, nil
	}
	if adapterErr != nil {
		kind, reason, unknown = message.EventFail, "provider-handoff-outcome-unknown", true
	} else if private.MessageRef != record.Envelope.MessageRef {
		return record, nil
	} else {
		switch private.State {
		case agentdelivery.StateHeld:
			kind = message.EventHold
		case agentdelivery.StateHandoff:
			updated, _, err := s.Store.MarkHandoff(record.Envelope.MessageRef)
			if err != nil {
				return record, err
			}
			return updated, nil
		case agentdelivery.StateDelivered:
			kind = message.EventDeliver
		case agentdelivery.StateRefused:
			kind = message.EventRefuse
		case agentdelivery.StateExpired:
			if private.Ambiguous {
				kind, reason, unknown = message.EventFail, "provider-handoff-outcome-unknown", true
			} else {
				kind = message.EventExpire
			}
		case agentdelivery.StateStale:
			if private.Ambiguous {
				kind, reason, unknown = message.EventFail, "provider-handoff-outcome-unknown", true
			} else {
				kind = message.EventStale
			}
		case agentdelivery.StateFailed:
			kind, unknown = message.EventFail, private.Ambiguous
		}
	}
	if kind == "" {
		return record, nil
	}
	updated, _, err := s.Store.Apply(record.Envelope.MessageRef, s.event(record, kind, reason, unknown))
	if err != nil {
		return record, err
	}
	return updated, nil
}

func (s Service) registryError(err error) error {
	if s.MapRegistryError != nil {
		return s.MapRegistryError(err)
	}
	return err
}
func (s Service) accepted(reg metadata.Registry, a metadata.Agent, r metadata.AgentRouteRef) {
	if s.Accepted != nil {
		s.Accepted(reg, a, r)
	}
}
