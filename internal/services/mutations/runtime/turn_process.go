package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"strings"
	"time"
)

type ProcessTurnJoin struct{ Turn, Origin string }
type ProcessTurnResponse struct {
	Accepted, Stale, Busy, Closed, Unsupported bool
	DeleteRefusal, BusyReason                  string
	UserDelivery                               *processhost.UserTurnDelivery
	Receipt                                    json.RawMessage
	Join                                       *ProcessTurnJoin
}
type processTurnRequest struct {
	Authority                       processhost.Authority
	Action, Operation, Prompt, Turn string
}
type processTurnEnvelope struct{ Foreground *processTurnRequest }

// CallProcessTurn writes at most one frame to the exact owned host. It neither
// starts a child-free claim nor retries uncertain transport outcomes.
func CallProcessTurn(ctx context.Context, registryPath string, load func() (metadata.Registry, error), reg metadata.Registry, a metadata.Agent, req TurnRequest) (TurnResult, error) {
	out := TurnResult{AgentUID: a.Metadata.UID, PaneUID: a.Status.PaneRef, Process: true, Mode: req.Action}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	pane, ok := reg.Pane(a.Status.PaneRef)
	if !ok || pane.Status.ProcessSession == nil {
		return out, processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	b := session.Binding
	out.Generation, out.ThreadID = b.Generation, session.ThreadID
	activation, provider, ok := reg.CurrentProcessActivation(b)
	if !ok || provider != a.Spec.Provider {
		return out, processhost.ErrStale
	}
	name := "/host.sock"
	if provider == "codex" {
		name = "/codex-host.sock"
	}
	socket := claude.ActivationLeaseDir(registryPath, pane.Metadata.UID, b.Generation) + name
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return out, fmt.Errorf("process-host-unavailable: %w", err)
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return out, err
	}
	out.Operation = "op-" + hex.EncodeToString(nonce[:])
	action := req.Action
	switch action {
	case "start":
		action = "turn"
	case "steer":
		action = "user-steer"
	case "deliver":
		action = "user-deliver"
	}
	sessionID := session.SessionID
	if provider == "codex" {
		sessionID = session.ThreadID
	}
	binding := processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
	request := processTurnEnvelope{Foreground: &processTurnRequest{Authority: processhost.Authority{Binding: binding, Connection: b.OperationID, Session: sessionID}, Action: action, Operation: out.Operation, Prompt: req.Text, Turn: session.TurnID}}
	latest, err := load()
	if err != nil {
		return out, err
	}
	current, currentProvider, ok := latest.CurrentProcessActivation(b)
	if !ok || currentProvider != provider || current != activation {
		return out, processhost.ErrStale
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var response ProcessTurnResponse
	err = localipc.ExchangeOwnedJSON(ctx, socket, identity, activation.HostProcess, request, &response)
	if err != nil {
		out.OutcomeUnknown = true
		return out, fmt.Errorf("process-host-unavailable: %w", err)
	}
	return ProjectProcessTurn(out, req, response)
}
func ProjectProcessTurn(out TurnResult, req TurnRequest, response ProcessTurnResponse) (TurnResult, error) {
	if err := ProcessTurnAcceptance(response); err != nil {
		return out, err
	}
	out.Accepted = true
	if req.Action == "deliver" || req.Action == "steer" {
		r := response.UserDelivery
		if r == nil || response.Stale || response.Busy || response.Closed || len(response.Receipt) > 0 && string(response.Receipt) != "null" || (r.Mode != processhost.UserTurnStart && r.Mode != processhost.UserTurnSteer) || out.Operation == "" || r.Operation != out.Operation || strings.TrimSpace(r.TurnID) == "" || strings.TrimSpace(r.TurnID) != r.TurnID || req.Action == "steer" && r.Mode != processhost.UserTurnSteer {
			out.OutcomeUnknown = true
			return out, processhost.ErrStale
		}
		out.Mode, out.TurnID = string(r.Mode), r.TurnID
		if r.Mode == processhost.UserTurnSteer {
			out.Acceptance, out.Delivery = "provider", "unconfirmed"
		}
	} else {
		out.TurnID = out.Operation
	}
	if response.Join != nil {
		out.RunningTurn, out.Origin = response.Join.Turn, response.Join.Origin
		out.Delivery = "joined"
	}
	out.Response = agentResponse(out)
	return out, nil
}
func ProcessTurnAcceptance(result ProcessTurnResponse) error {
	switch {
	case result.DeleteRefusal != "":
		return turnFailure("agent-protected", errors.New(result.DeleteRefusal))
	case result.Accepted:
		return nil
	case result.Busy:
		var advice string
		switch result.BusyReason {
		case "joined-input-limit":
			advice = fmt.Sprintf("the running turn already holds %d joined inputs or %d bytes; wait for its result", processhost.ClaudeJoinedInputs, processhost.ClaudeJoinedInputBytes)
		case "control-pending":
			advice = "answer the pending permission or question first"
		case "join-unsupported":
			advice = "a turn is running and this input path cannot join it; wait for its result"
		case "turn-not-open":
			advice = "the admitted turn has not started yet; retry shortly"
		case "message-handoff-pending":
			advice = "a peer message is being handed to the agent; retry shortly"
		case "message-handoff-expired":
			advice = "a peer message handoff never reported its outcome; input waits for the turn's result or the agent's exit"
		case "interrupt-pending":
			advice = "the running turn is being interrupted; wait for its result"
		case "event-limit":
			advice = "the running turn holds its event limit; wait for its result"
		default:
			return processhost.ErrBusy
		}
		return fmt.Errorf("%w: %s: %s", processhost.ErrBusy, result.BusyReason, advice)
	case result.Closed:
		return processhost.ErrClosed
	case result.Unsupported:
		return fmt.Errorf("%w: the agent's process owner does not implement this action; relaunch the agent to run a current owner", ErrProcessStopUnsupported)
	default:
		return processhost.ErrStale
	}
}

func agentResponse(out TurnResult) agentcontrol.Response {
	return agentcontrol.Response{OK: out.Accepted, ThreadID: out.ThreadID, TurnID: out.TurnID, Acceptance: out.Acceptance, Delivery: out.Delivery}
}
