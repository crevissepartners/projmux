package codexappserver

import (
	"context"
	"encoding/json"
)

// Requester is the exact slice of one initialized app-server connection the
// typed control and lifecycle helpers below need.
//
// It exists so those helpers stay the single owner of the wire shapes even
// when the connection is not this process's own *Client. The endpoint broker
// owns one shared connection and lends it out behind a fenced request path, so
// a broker client that reimplemented turn/start params or lifecycle snapshot
// decoding would create a second, silently divergent definition of the same
// protocol.
//
// *Client satisfies it as written.
type Requester interface {
	Request(ctx context.Context, method string, params, result any) error
}

// StartExactTurnOn starts one turn on an exact thread over any Requester.
func StartExactTurnOn(ctx context.Context, requester Requester, threadID, text string) (ControlResult, error) {
	return startExactTurn(ctx, requester, threadID, text)
}

// SteerExactTurnOn steers one exact in-progress turn over any Requester.
func SteerExactTurnOn(ctx context.Context, requester Requester, threadID, expectedTurnID, text string) (ControlResult, error) {
	return steerExactTurn(ctx, requester, threadID, expectedTurnID, text)
}

// InterruptExactTurnOn interrupts one exact in-progress turn over any Requester.
func InterruptExactTurnOn(ctx context.Context, requester Requester, threadID, turnID string) (ControlResult, error) {
	return interruptExactTurn(ctx, requester, threadID, turnID)
}

// ReadLifecycleSnapshotOn converges one exact thread's lifecycle snapshot over
// any Requester.
func ReadLifecycleSnapshotOn(ctx context.Context, requester Requester, threadID string) (LifecycleSnapshot, error) {
	return readLifecycleSnapshot(ctx, requester, threadID)
}

// NormalizeServerRequestID projects one raw inbound server-request id onto the
// bounded text form the approval envelope compares against.
//
// The raw bytes stay the response identity; this is only the rendering used to
// match a pending request. A consumer that rebuilds a Notification from a
// transport that carried the raw id needs the same projection, and deriving it
// a second time is how a string id and a numeric id that render alike stop
// being two distinct requests.
func NormalizeServerRequestID(raw json.RawMessage) (string, error) {
	return normalizeServerRequestID(raw)
}
