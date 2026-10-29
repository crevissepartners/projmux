package core

import (
	"encoding/json"
	"time"
)

// AgentProcessBinding names one provider process generation of a process
// Agent: the host instance, the Pane and the generation. A Binding built from
// a GraphProcessKey is only a request, never authority: ReadAgentProcess and
// ReadAgentProcessEvents compare it again with the live Host and Generation,
// and a read never follows a newer generation.
type AgentProcessBinding struct {
	Host       string `json:"host"`
	Pane       string `json:"pane"`
	Generation string `json:"generation"`
}

// IsZero reports an unset Binding, which asks for the current one.
func (b AgentProcessBinding) IsZero() bool { return b == AgentProcessBinding{} }

// AgentProcessRequest selects one exact process Agent ("uid:<agent>"). A zero
// Binding reads the current generation once and returns its Binding; pass that
// Binding to later reads.
type AgentProcessRequest struct {
	Agent   string
	Binding AgentProcessBinding
}

// AgentProcessEventsRequest reads the events after the sequence After. Wait
// is how long a long poll waits for a new event when none follows After: zero
// reads once, and any value is capped at the owner read deadline (5s), which
// also bounds the whole call.
type AgentProcessEventsRequest struct {
	Agent   string
	Binding AgentProcessBinding
	After   uint64
	Wait    time.Duration
}

// AgentProcessExit is the provider's actual Wait evidence.
type AgentProcessExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// AgentProcessSnapshot is one observation of a process Agent's generation.
// Running reports a provider turn in progress and Pending the number of open
// control requests; read their display IDs with ListQuestions and
// ListApprovals. Attention is the Registry interaction projection the CLI
// shows, empty when the Agent has none. Sequence is the last event sequence
// of the snapshot an event batch carries; ReadAgentProcess leaves it zero.
// Exit is set only from actual Wait evidence. Provider session and connection
// identifiers, request content and control credentials never appear here;
// read session IDs with ListAgentSessions.
type AgentProcessSnapshot struct {
	Binding   AgentProcessBinding `json:"binding"`
	Provider  string              `json:"provider"`
	State     string              `json:"state"`
	Running   bool                `json:"running,omitempty"`
	Pending   int                 `json:"pending,omitempty"`
	Attention string              `json:"attention,omitempty"`
	OwnerMode string              `json:"ownerMode,omitempty"`
	Host      ProcessIdentity     `json:"host"`
	Child     ProcessIdentity     `json:"child"`
	Sequence  uint64              `json:"sequence,omitempty"`
	Exit      *AgentProcessExit   `json:"exit,omitempty"`
}

// AgentProcessEvent is one provider event. Payload is JSON with the same value
// as the provider's own frame. The owner's answer frame re-encodes it, which
// compacts whitespace and may escape HTML characters such as < > &, so compare
// it as a JSON value, never byte for byte. An event without a provider frame
// (stream-gap, exit and the like) has no Payload. It never carries projmux
// control requests. Exit is set on a provider exit event.
type AgentProcessEvent struct {
	Sequence uint64            `json:"sequence"`
	Kind     string            `json:"kind"`
	Payload  json.RawMessage   `json:"payload,omitempty"`
	Exit     *AgentProcessExit `json:"exit,omitempty"`
}

// AgentProcessEventBatch is one bounded read. Events are ascending and follow
// the request's After; Next is the last returned sequence and the next read's
// After. Gap reports that the host dropped events after After (its
// "stream-gap" event is in Events); resynchronize from Snapshot. More reports
// that the answer frame left events for the next read; it is not a Gap.
// Omitted lists the returned events whose Payload did not fit one answer frame
// and was removed; their identity and Exit are kept. Terminal reports a
// generation that ended with its final Snapshot.
type AgentProcessEventBatch struct {
	Binding  AgentProcessBinding  `json:"binding"`
	Events   []AgentProcessEvent  `json:"events,omitempty"`
	Snapshot AgentProcessSnapshot `json:"snapshot"`
	Next     uint64               `json:"next"`
	Gap      bool                 `json:"gap,omitempty"`
	More     bool                 `json:"more,omitempty"`
	Terminal bool                 `json:"terminal,omitempty"`
	Omitted  []uint64             `json:"omitted,omitempty"`
}
