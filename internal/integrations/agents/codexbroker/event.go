package codexbroker

import (
	"encoding/json"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// EventOrigin separates the two legs the snapshot barrier merges. A consumer
// that needs to know whether a delivery restated existing state or reported a
// new one reads this instead of guessing from Sequence.
type EventOrigin string

const (
	// EventOriginSnapshot is the single pre-turn snapshot that opens every
	// connection's stream for a binding.
	EventOriginSnapshot EventOrigin = "snapshot"
	// EventOriginLive is an endpoint notification.
	EventOriginLive EventOrigin = "live"
)

// ApprovalLease is the fenced authority to answer exactly one inbound server
// request. It binds the raw JSON-RPC request id, the thread, and both epochs
// together, because each of those alone is forgeable by a stale caller: a raw
// id repeats across connections, a thread outlives a connection, and an epoch
// says nothing about which request is being answered.
//
// A lease is valid only on the connection that minted it. Disconnect or
// connection replacement revokes every outstanding lease.
type ApprovalLease struct {
	Fence        Fence
	ThreadID     string
	RawRequestID json.RawMessage
}

// held reports whether this lease was minted for an inbound server request.
func (l ApprovalLease) held() bool { return len(l.RawRequestID) > 0 }

// Event is one endpoint message delivered to exactly one binding.
//
// Params is opaque pass-through: it is the provider's payload, it is handed to
// the consumer verbatim, and the broker never copies it into telemetry, into
// the write ledger, or into any other state it keeps.
type Event struct {
	Fence    Fence
	Origin   EventOrigin
	Sequence uint64
	Method   string
	Params   json.RawMessage
	Snapshot codexappserver.ThreadSnapshot
	Lease    ApprovalLease
}

// AttributeNotification returns the exact thread one notification may be
// attributed to.
//
// Attribution is the only routing input, and it is read from the payload's
// declared thread id and nothing else. A notification that declares no thread
// is attributed to no binding and to no Agent: fanning it out to every binding
// would forge provenance, and picking the newest or nearest binding would be
// exactly the "first match" derivation the authority model forbids.
func AttributeNotification(notification codexappserver.Notification) (string, bool) {
	if len(notification.Params) == 0 {
		return "", false
	}
	var params struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(notification.Params, &params) != nil {
		return "", false
	}
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return "", false
	}
	return threadID, true
}
