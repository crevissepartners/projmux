// Package agentmessage owns the provider-neutral coordination envelope and
// public delivery reducer. Provider adapters may project their private state
// into this package, but provider locators, credentials, thread IDs, and
// session secrets are deliberately not representable here.
package agentmessage

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Version         = 1
	MaxRefBytes     = 160
	MaxPayloadBytes = 4 << 10
	MaxTTL          = 24 * time.Hour
)

var (
	ErrInvalidEnvelope = errors.New("invalid Agent message envelope")
	ErrRetryMismatch   = errors.New("agent message retry does not match the immutable envelope")
)

type Authority struct {
	Kind       string `json:"kind"`
	Trust      string `json:"trust"`
	Permission string `json:"permission"`
}

func PeerAuthority() Authority {
	return Authority{Kind: "peer", Trust: "untrusted", Permission: "coordination-only"}
}

// Route is the only durable address in a message. Provider authority and its
// volatile locator are re-resolved from the Registry for every operation.
type Route struct {
	AgentUID             string `json:"agentUID"`
	PaneUID              string `json:"paneUID"`
	ActivationGeneration string `json:"activationGeneration"`
	Provider             string `json:"provider"`
}

func (r Route) Valid() bool {
	return validRef(r.AgentUID) && validRef(r.PaneUID) && validRef(r.ActivationGeneration) && validProvider(r.Provider)
}

func (r Route) Same(other Route) bool {
	return r.Valid() && r == other
}

type Envelope struct {
	Version         int       `json:"version"`
	MessageRef      string    `json:"messageRef"`
	ConversationRef string    `json:"conversationRef"`
	ReplyTo         string    `json:"replyTo,omitempty"`
	Source          Route     `json:"source"`
	Target          Route     `json:"target"`
	Authority       Authority `json:"authority"`
	Payload         string    `json:"payload"`
	AcceptedAt      time.Time `json:"acceptedAt"`
	Deadline        time.Time `json:"deadline"`
}

func (e Envelope) Validate() error {
	if e.Version != Version || !validRef(e.MessageRef) || !validRef(e.ConversationRef) ||
		(e.ReplyTo != "" && !validRef(e.ReplyTo)) || !e.Source.Valid() || !e.Target.Valid() ||
		e.Authority != PeerAuthority() || !validPayload(e.Payload) || e.AcceptedAt.IsZero() || e.Deadline.IsZero() ||
		!e.Deadline.After(e.AcceptedAt) || e.Deadline.Sub(e.AcceptedAt) > MaxTTL {
		return ErrInvalidEnvelope
	}
	return nil
}

// SameRetry compares every caller-controlled immutable field plus the original
// TTL. acceptedAt is broker-owned, so a retry made later need not reproduce its
// timestamp; requiring the same duration still prevents a retry from extending
// the deadline.
func (e Envelope) SameRetry(candidate Envelope) bool {
	return e.Version == candidate.Version && e.MessageRef == candidate.MessageRef &&
		e.ConversationRef == candidate.ConversationRef && e.ReplyTo == candidate.ReplyTo &&
		e.Source.Same(candidate.Source) && e.Target.Same(candidate.Target) &&
		e.Authority == candidate.Authority && e.Payload == candidate.Payload &&
		e.Deadline.Sub(e.AcceptedAt) == candidate.Deadline.Sub(candidate.AcceptedAt)
}

// ValidateReply proves broker correlation rather than trusting a native reply
// address or caller-authored source. The new message must reverse the original
// route exactly and remain in its conversation.
func ValidateReply(original, reply Envelope) error {
	if err := original.Validate(); err != nil {
		return err
	}
	if err := reply.Validate(); err != nil {
		return err
	}
	if reply.ReplyTo != original.MessageRef || reply.ConversationRef != original.ConversationRef ||
		reply.MessageRef == original.MessageRef || !reply.Source.Same(original.Target) || !reply.Target.Same(original.Source) {
		return fmt.Errorf("%w: reply route or conversation mismatch", ErrInvalidEnvelope)
	}
	return nil
}

func validRef(value string) bool {
	if value != strings.TrimSpace(value) || value == "" || len(value) > MaxRefBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validProvider(value string) bool {
	switch value {
	case "codex", "claude", "antigravity":
		return true
	default:
		return false
	}
}

func validPayload(value string) bool {
	return value != "" && len(value) <= MaxPayloadBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

type State string

const (
	StateAccepted  State = "accepted"
	StateHeld      State = "held"
	StateDelivered State = "delivered"
	StateRefused   State = "refused"
	StateExpired   State = "expired"
	StateStale     State = "stale"
	StateFailed    State = "failed"
)

func (s State) Terminal() bool {
	switch s {
	case StateDelivered, StateRefused, StateExpired, StateStale, StateFailed:
		return true
	default:
		return false
	}
}

type Delivery struct {
	MessageRef      string    `json:"messageRef"`
	ConversationRef string    `json:"conversationRef"`
	State           State     `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	OutcomeUnknown  bool      `json:"outcomeUnknown,omitempty"`
	AcceptedAt      time.Time `json:"acceptedAt"`
	TerminalAt      time.Time `json:"terminalAt"`
}

type EventKind string

const (
	EventAccept  EventKind = "accept"
	EventHold    EventKind = "hold"
	EventDeliver EventKind = "deliver"
	EventRefuse  EventKind = "refuse"
	EventExpire  EventKind = "expire"
	EventStale   EventKind = "stale"
	EventFail    EventKind = "fail"
)

type Event struct {
	Kind            EventKind
	MessageRef      string
	ConversationRef string
	Target          Route
	Reason          string
	ObservedAt      time.Time
	OutcomeUnknown  bool
}

// Reduce owns the public terminal-once lifecycle. Foreign message,
// conversation, or target events and all duplicate/out-of-order events are
// no-ops. There is no transition back to accepted, so automatic resend is not
// representable.
func Reduce(current Delivery, envelope Envelope, event Event) (Delivery, bool) {
	if envelope.Validate() != nil || event.MessageRef != envelope.MessageRef || event.ConversationRef != envelope.ConversationRef ||
		!event.Target.Same(envelope.Target) || event.ObservedAt.Before(envelope.AcceptedAt) ||
		(current.State != "" && (current.MessageRef != envelope.MessageRef || current.ConversationRef != envelope.ConversationRef)) || current.State.Terminal() {
		return current, false
	}
	if current.State == "" {
		if event.Kind != EventAccept {
			return current, false
		}
		return Delivery{MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef,
			State: StateAccepted, AcceptedAt: envelope.AcceptedAt}, true
	}
	next := current
	switch event.Kind {
	case EventHold:
		if current.State != StateAccepted {
			return current, false
		}
		next.State = StateHeld
		next.Reason = boundedReason(event.Reason)
	case EventDeliver, EventRefuse, EventExpire, EventStale, EventFail:
		if current.State != StateAccepted && current.State != StateHeld {
			return current, false
		}
		switch event.Kind {
		case EventDeliver:
			next.State = StateDelivered
		case EventRefuse:
			next.State = StateRefused
		case EventExpire:
			next.State = StateExpired
		case EventStale:
			next.State = StateStale
		case EventFail:
			next.State = StateFailed
		}
		next.Reason = boundedReason(event.Reason)
		next.OutcomeUnknown = event.Kind == EventFail && event.OutcomeUnknown
		next.TerminalAt = event.ObservedAt.UTC()
	default:
		return current, false
	}
	return next, true
}

func boundedReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unspecified"
	}
	if len(value) > MaxRefBytes {
		value = value[:MaxRefBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
