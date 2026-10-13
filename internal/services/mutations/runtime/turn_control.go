package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
)

type TurnRequest struct{ Action, Text, Via string }
type TurnResult struct {
	AgentUID, PaneUID, Generation, ThreadID, TurnID, Operation string
	Mode, Acceptance, Delivery, RunningTurn, Origin            string
	Process                                                    bool
	Accepted, OutcomeUnknown                                   bool
	Response                                                   agentcontrol.Response
}
type TurnFailure struct {
	Code  string
	Cause error
}

func (e *TurnFailure) Error() string           { return e.Cause.Error() }
func (e *TurnFailure) Unwrap() error           { return e.Cause }
func turnFailure(code string, err error) error { return &TurnFailure{Code: code, Cause: err} }

type TurnControl interface {
	Bind(context.Context, metadata.Registry, metadata.Agent) (agentcontrol.Binding, error)
	Call(context.Context, agentcontrol.Binding, agentcontrol.Request) (agentcontrol.Response, error)
}

// TurnService shares execution, receipt validation and the legacy fallback.
// The native epoch or process owner selects deliver's mode, exactly once.
type TurnService struct {
	Control         TurnControl
	RecordStart     func(context.Context, agentcontrol.Binding, agentcontrol.Response) error
	Process         func(context.Context, metadata.Registry, metadata.Agent, TurnRequest) (TurnResult, error)
	ClaudeInterrupt func(context.Context, metadata.Registry, metadata.Agent, string) (TurnResult, error)
}

func (s TurnService) Execute(ctx context.Context, reg metadata.Registry, a metadata.Agent, req TurnRequest) (TurnResult, error) {
	out := TurnResult{AgentUID: a.Metadata.UID, PaneUID: a.Status.PaneRef, Mode: req.Action}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if req.Action != "start" && req.Action != "steer" && req.Action != "deliver" && req.Action != "interrupt" {
		return out, turnFailure("turn-invalid-input", errors.New("unknown turn action"))
	}
	if req.Action != "interrupt" && strings.TrimSpace(req.Text) == "" {
		return out, turnFailure("turn-invalid-input", errors.New("agent turn requires text"))
	}
	if req.Action == "interrupt" && a.Spec.Provider == "claude" {
		if err := operatorclient.Validate(req.Via); err != nil {
			return out, turnFailure("operator-client-invalid", fmt.Errorf("Claude turn interrupt requires explicit caller-reported Via: %w", err))
		}
	} else if req.Via != "" {
		return out, turnFailure("turn-invalid-input", errors.New("Via is available only for Claude turn interrupt"))
	}
	pane, found := reg.Pane(a.Status.PaneRef)
	if found && pane.Metadata.OwnerUID() == a.Metadata.UID && pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
		if (a.Spec.Provider != "codex" && a.Spec.Provider != "claude") || (a.Spec.Provider == "claude" && req.Action != "start" && req.Action != "interrupt") {
			return out, turnFailure("turn-provider-unsupported", errors.New("Agent provider does not support this process turn action"))
		}
		out.Process = true
		if s.Process == nil {
			return out, turnFailure("turn-control-unavailable", errors.New("process turn transport unavailable"))
		}
		return s.Process(ctx, reg, a, req)
	}
	if a.Spec.Provider == "claude" && req.Action == "interrupt" {
		if s.ClaudeInterrupt == nil {
			return out, turnFailure("turn-control-unavailable", errors.New("Claude interrupt transport unavailable"))
		}
		return s.ClaudeInterrupt(ctx, reg, a, req.Via)
	}
	if err := agentcontrol.RequireCapability("agent turn "+req.Action, a); err != nil {
		return out, turnFailure("turn-provider-unsupported", err)
	}
	b, err := s.Control.Bind(ctx, reg, a)
	if err != nil {
		return out, err
	}
	out.Generation, out.ThreadID = b.Identity.Generation, b.Identity.ThreadID
	response, err := s.Control.Call(ctx, b, agentcontrol.Request{Operation: "turn-" + req.Action, Text: req.Text})
	// Only an explicit refusal proves the old observer did not write input.
	if err == nil && req.Action == "deliver" && !response.OK && response.Code == "invalid-operation" {
		out.Mode = "start"
		response, err = s.Control.Call(ctx, b, agentcontrol.Request{Operation: "turn-start", Text: req.Text})
		if err == nil && !response.OK && response.Code == "turn-in-progress" {
			out.Mode = "steer"
			response, err = s.Control.Call(ctx, b, agentcontrol.Request{Operation: "turn-steer", Text: req.Text})
		}
	} else if req.Action == "deliver" {
		out.Mode = "start"
		if response.Acceptance == "provider" {
			out.Mode = "steer"
		}
	}
	out.Response = response
	out.TurnID, out.Acceptance, out.Delivery = response.TurnID, response.Acceptance, response.Delivery
	if err != nil {
		out.OutcomeUnknown = !errors.Is(err, agentcontrol.ErrNotSent)
		return out, err
	}
	if err = response.Error(); err != nil {
		out.OutcomeUnknown = response.Code == "timeout" || response.Code == "protocol-error" || strings.HasSuffix(response.Code, "-failed")
		return out, agentcontrol.Recovery(err, b)
	}
	out.Accepted = true
	if response.ThreadID != b.Identity.ThreadID || strings.TrimSpace(response.TurnID) == "" {
		out.OutcomeUnknown = true
		return out, agentcontrol.Recovery(&agentcontrol.BindingError{Reason: "turn response did not preserve the exact thread identity"}, b)
	}
	if out.Mode == "steer" && (response.Acceptance != "provider" || response.Delivery != "unconfirmed") {
		out.OutcomeUnknown = true
		return out, agentcontrol.Recovery(&agentcontrol.BindingError{Reason: "turn/" + req.Action + " response did not carry the exact provider-acceptance receipt"}, b)
	}
	if out.Mode == "start" && s.RecordStart != nil {
		if err = s.RecordStart(ctx, b, response); err != nil {
			return out, err
		}
	}
	return out, nil
}
