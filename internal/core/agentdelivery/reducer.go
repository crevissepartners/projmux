// Package agentdelivery owns the private provider-final-hop lifecycle used by
// Phase 2. Its queued state is deliberately below the future public broker's
// accepted state: Phase 3 may project broker acceptance into this adapter, but
// this package neither defines nor exposes that public broker contract.
package agentdelivery

import "strings"

type State string

const (
	StateQueued    State = "queued"
	StateHeld      State = "held"
	StateHandoff   State = "handoff"
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

type EventKind string

const (
	EventQueue        EventKind = "queue"
	EventHold         EventKind = "hold"
	EventBeginHandoff EventKind = "begin-handoff"
	EventDeliver      EventKind = "deliver"
	EventRefuse       EventKind = "refuse"
	EventExpire       EventKind = "expire"
	EventStale        EventKind = "stale"
	EventFail         EventKind = "fail"
)

// Delivery contains no message payload or provider locator. Ambiguous is set
// whenever bytes may have crossed the provider pipe but no exact full-write
// receipt committed; callers must never automatically resend that outcome.
type Delivery struct {
	MessageRef string `json:"messageRef"`
	State      State  `json:"state"`
	WaiterRef  string `json:"waiterRef,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Ambiguous  bool   `json:"ambiguous,omitempty"`
}

type Event struct {
	Kind             EventKind
	MessageRef       string
	WaiterRef        string
	Reason           string
	FullFrameWritten bool
	HelperReceipt    bool
}

// Reduce is terminal-once and fail-closed. Duplicate, foreign, stale, and
// out-of-order events are no-ops. The bool reports whether state changed.
func Reduce(current Delivery, event Event) (Delivery, bool) {
	if !validRef(event.MessageRef) || (current.MessageRef != "" && event.MessageRef != current.MessageRef) || current.State.Terminal() {
		return current, false
	}
	if current.MessageRef == "" {
		if event.Kind != EventQueue {
			return current, false
		}
		return Delivery{MessageRef: event.MessageRef, State: StateQueued}, true
	}

	next := current
	switch event.Kind {
	case EventHold:
		if current.State != StateQueued {
			return current, false
		}
		next.State = StateHeld
		next.Reason = boundedReason(event.Reason)
	case EventBeginHandoff:
		if (current.State != StateQueued && current.State != StateHeld) || !validRef(event.WaiterRef) {
			return current, false
		}
		next.State = StateHandoff
		next.WaiterRef = event.WaiterRef
		next.Reason = ""
	case EventDeliver:
		if current.State != StateHandoff || event.WaiterRef != current.WaiterRef || !event.FullFrameWritten || !event.HelperReceipt {
			return current, false
		}
		next.State = StateDelivered
		next.Reason = "provider-pipe-full-frame"
	case EventRefuse:
		if current.State != StateQueued && current.State != StateHeld {
			return current, false
		}
		next.State = StateRefused
		next.Reason = boundedReason(event.Reason)
	case EventExpire:
		if current.State == StateHandoff {
			next.State = StateFailed
			next.Reason = "observation-timeout"
			next.Ambiguous = true
		} else if current.State == StateQueued || current.State == StateHeld {
			next.State = StateExpired
			next.Reason = boundedReason(event.Reason)
		} else {
			return current, false
		}
	case EventStale:
		if current.State == StateHandoff {
			next.State = StateFailed
			next.Reason = "delivery-outcome-unknown"
			next.Ambiguous = true
		} else if current.State == StateQueued || current.State == StateHeld {
			next.State = StateStale
			next.Reason = boundedReason(event.Reason)
		} else {
			return current, false
		}
	case EventFail:
		if current.State != StateQueued && current.State != StateHeld && current.State != StateHandoff {
			return current, false
		}
		next.State = StateFailed
		next.Reason = boundedReason(event.Reason)
		next.Ambiguous = current.State == StateHandoff
	default:
		return current, false
	}
	return next, true
}

func validRef(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 160 && !strings.ContainsAny(value, "\r\n\x00")
}

func boundedReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unspecified"
	}
	if len(value) > 160 {
		return value[:160]
	}
	return value
}
