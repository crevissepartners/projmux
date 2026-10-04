package app

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The kernel's per-user socket credentials admit short-lived terminal clients.
// Requests carry exact target authority, never claimed caller credentials.
type processForegroundRequest struct {
	Authority                       processhost.Authority
	Action, Operation, Prompt, Turn string
	MessageRef                      string
	Token                           processForegroundToken
	Response                        processhost.Response
	Decision                        codexappserver.ApprovalDecision
	Selections                      map[int]agentquestion.Selection
}

// Preserve the exact provider input bytes across JSON transport. RawMessage
// encoding compacts whitespace, which would invalidate the host's exact token.
type processForegroundToken struct {
	processhost.Request
	Input []byte
}

func (t processForegroundToken) request() processhost.Request {
	r := t.Request
	r.Input = t.Input
	return r
}

type processForegroundResult struct {
	Accepted            bool
	Stale, Busy, Closed bool
	Observation         *processHostObservation `json:",omitempty"`
	Receipt             *codexProcessReceipt    `json:",omitempty"`
}

func controlProcessForeground(ctx context.Context, peer coremetadata.ProcessIdentity, request processForegroundRequest, current func(context.Context, processhost.Authority) error, apply func() error) processForegroundResult {
	result := processForegroundResult{Stale: true}
	if ctx.Err() != nil || !peer.Valid() || int64(peer.OwnerUID) != int64(os.Getuid()) {
		return result
	}
	identity, _, err := localipc.Process(peer.PID)
	if err != nil || identity != peer || current(ctx, request.Authority) != nil {
		return result
	}
	err = apply()
	return processForegroundResult{Accepted: err == nil, Stale: errors.Is(err, processhost.ErrStale), Busy: errors.Is(err, processhost.ErrBusy), Closed: errors.Is(err, processhost.ErrClosed)}
}

func applyClaudeForeground(ctx context.Context, handle *processhost.Handle, r processForegroundRequest) error {
	switch r.Action {
	case "turn":
		return handle.Turn(ctx, r.Authority, r.Operation, r.Prompt)
	case "interrupt":
		return handle.Interrupt(ctx, r.Authority, r.Turn)
	case "respond":
		return handle.Respond(ctx, r.Authority, r.Token.request(), r.Response)
	case "stop":
		return handle.Stop(r.Authority.Binding)
	default:
		return processhost.ErrStale
	}
}

func applyCodexForeground(ctx context.Context, handle *processhost.CodexHandle, r processForegroundRequest) error {
	switch r.Action {
	case "turn":
		return handle.Turn(ctx, r.Authority, r.Operation, r.Prompt)
	case "interrupt":
		return handle.Interrupt(ctx, r.Authority, r.Turn)
	case "question":
		return handle.RespondQuestion(ctx, r.Authority, r.Token.request(), r.Selections)
	case "approval":
		return handle.RespondApproval(ctx, r.Authority, r.Token.request(), r.Decision)
	case "stop":
		return handle.Stop(r.Authority.Binding)
	default:
		return processhost.ErrStale
	}
}

// Both sides verify exact kernel birth identity. Socket inode replacement and
// PID reuse are refusal, never a reason to rediscover another host or replay.
func callProcessForeground(ctx context.Context, socket string, socketIdentity localipc.SocketIdentity, host coremetadata.ProcessIdentity, request any) (processForegroundResult, error) {
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil || identity != socketIdentity {
		return processForegroundResult{}, processhost.ErrStale
	}
	deadline := time.Now().Add(localipc.Deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(bounded, "unix", socket)
	if err != nil {
		return processForegroundResult{}, err
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	peer, _, err := localipc.PeerProcess(conn)
	if err != nil || peer != host || ctx.Err() != nil {
		return processForegroundResult{}, processhost.ErrStale
	}
	if err = localipc.WriteJSON(conn, request); err != nil {
		return processForegroundResult{}, err
	}
	if err = conn.CloseWrite(); err != nil {
		return processForegroundResult{}, err
	}
	var result processForegroundResult
	err = localipc.ReadJSON(conn, &result)
	return result, err
}
