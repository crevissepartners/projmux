package projmux

import (
	"context"
	"errors"
	"fmt"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"path/filepath"
	"strings"
)

const (
	CodeTurnInvalidInput        Code = "turn-invalid-input"
	CodeTurnProviderUnsupported Code = "turn-provider-unsupported"
	CodeTurnControlUnavailable  Code = "turn-control-unavailable"
	CodeTurnDeferredUnsupported Code = "turn-deferred-unsupported"
	CodeTurnBusy                Code = "turn-in-progress"
	CodeTurnStale               Code = "stale-binding"
	CodeTurnTransportUncertain  Code = "turn-transport-uncertain"
)

// StartTurn submits one explicit input, refusing a busy native Codex turn.
func (c *Client) StartTurn(ctx context.Context, req core.TurnRequest) (core.TurnResult, error) {
	return c.turn(ctx, req, "start")
}

// SteerTurn joins an exact active Codex turn; delivery remains unconfirmed.
func (c *Client) SteerTurn(ctx context.Context, req core.TurnRequest) (core.TurnResult, error) {
	return c.turn(ctx, req, "steer")
}

// DeliverTurn lets the existing epoch or owner choose start or steer once.
func (c *Client) DeliverTurn(ctx context.Context, req core.TurnRequest) (core.TurnResult, error) {
	return c.turn(ctx, req, "deliver")
}

// InterruptTurn requests cancellation; Claude requires caller-reported Via.
func (c *Client) InterruptTurn(ctx context.Context, req core.TurnRequest) (core.TurnResult, error) {
	return c.turn(ctx, req, "interrupt")
}
func (c *Client) turn(ctx context.Context, req core.TurnRequest, action string) (core.TurnResult, error) {
	var out core.TurnResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "TurnRequest.Ref must name one Agent"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "TurnRequest")
	if err != nil {
		return out, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err == nil {
		err = resources.Enforce(selector.Target{Verb: selector.VerbReview, Kind: metadata.KindAgent}, query, resolution)
	}
	if err != nil {
		return out, resourceRefusal(err)
	}
	a, found := reg.Agent(resolution.Matches[0].UID)
	if !found {
		return out, turnRefusal(processhost.ErrStale, out)
	}
	out.AgentUID, out.PaneUID = a.Metadata.UID, a.Status.PaneRef
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	// Deferred claims start a provider lifetime. SDK input never enters that lane.
	claim, err := runtime.ReadDeferredClaim(runtime.DeferredClaimPath(store.PathFor(c.paths.StateDir), a.Metadata.UID))
	if err != nil {
		return out, turnRefusal(err, out)
	}
	born := false
	if pane, ok := reg.Pane(a.Status.PaneRef); ok && pane.Status.ProcessSession != nil {
		_, provider, current := reg.CurrentProcessActivation(pane.Status.ProcessSession.Binding)
		born = current && provider == a.Spec.Provider
	}
	if claim.Agent != "" && !born {
		return out, &Refusal{Code: CodeTurnDeferredUnsupported, AgentUID: a.Metadata.UID, message: "SDK turn control does not start a child-free deferred claim"}
	}
	if pane, ok := reg.Pane(a.Status.PaneRef); ok {
		out.Generation = pane.Status.Activation.Generation
		if req.Generation != "" && req.Generation != out.Generation {
			return out, turnRefusal(processhost.ErrStale, out)
		}
		if req.Binding != (core.ProcessBinding{}) {
			var binding core.ProcessBinding
			if pane.Status.ProcessSession != nil {
				err = unregisterCopy(pane.Status.ProcessSession.Binding, &binding)
			}
			if err != nil {
				return out, err
			}
			if binding != req.Binding {
				return out, turnRefusal(processhost.ErrStale, out)
			}
		}
	} else if req.Generation != "" || req.Binding != (core.ProcessBinding{}) {
		return out, turnRefusal(processhost.ErrStale, out)
	}
	native := agentcontrol.Client{Runner: frozenTurnRunner{c}, StateDir: c.paths.StateDir, Transport: c.transport, LoadRegistry: storage.LoadReadOnly}
	service := runtime.TurnService{Control: native,
		RecordStart: func(ctx context.Context, b agentcontrol.Binding, r agentcontrol.Response) error {
			return runtime.RecordStartedTurn(b, r, func(apply func(*metadata.Registry, metadata.Mutator) error) error {
				_, err := storage.Update(func(reg *metadata.Registry) error { return apply(reg, metadata.Mutator{}) })
				return err
			})
		},
		Process: func(ctx context.Context, reg metadata.Registry, a metadata.Agent, r runtime.TurnRequest) (runtime.TurnResult, error) {
			return runtime.CallProcessTurn(ctx, store.PathFor(c.paths.StateDir), storage.LoadReadOnly, reg, a, r)
		},
		ClaudeInterrupt: func(ctx context.Context, reg metadata.Registry, a metadata.Agent, via string) (runtime.TurnResult, error) {
			return (runtime.ClaudeTurnInterrupter{StateDir: c.paths.StateDir, Load: storage.LoadReadOnly, Runner: turnRunner{c}}).Interrupt(ctx, reg, a, via)
		},
	}
	result, err := service.Execute(ctx, reg, *a, runtime.TurnRequest{Action: action, Text: req.Text, Via: req.Via})
	out = core.TurnResult{AgentUID: result.AgentUID, PaneUID: result.PaneUID, Generation: result.Generation, ThreadID: result.ThreadID, TurnID: result.TurnID, Operation: result.Operation, Mode: result.Mode, Acceptance: result.Acceptance, Delivery: result.Delivery, RunningTurn: result.RunningTurn, Origin: result.Origin, Process: result.Process, Accepted: result.Accepted, OutcomeUnknown: result.OutcomeUnknown}
	return out, turnRefusal(err, out)
}

type turnRunner struct{ c *Client }

func (r turnRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return frozenTurnRunner(r).Run(ctx, name, append(r.c.transport.Args(), args...)...)
}

type frozenTurnRunner struct{ c *Client }

func (r frozenTurnRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, fmt.Errorf("unsupported turn transport %q", name)
	}
	binary := ""
	for _, dir := range filepath.SplitList(r.c.agentEnvironment["PATH"]) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, "tmux")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			binary = path
			break
		}
	}
	if binary == "" {
		return nil, errors.New("tmux unavailable on captured PATH")
	}
	cmd := tmuxexec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"HOME=" + r.c.discoveryHome, "PATH=" + r.c.agentEnvironment["PATH"], "CODEX_HOME=" + r.c.agentEnvironment["CODEX_HOME"]}
	return cmd.CombinedOutput()
}
func turnRefusal(err error, out core.TurnResult) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := CodeTurnControlUnavailable
	var failure *runtime.TurnFailure
	var refusal *agentcontrol.Refusal
	switch {
	case errors.As(err, &failure):
		code = Code(failure.Code)
	case errors.As(err, &refusal):
		code = Code(refusal.Code)
	case errors.Is(err, processhost.ErrStale):
		code = CodeTurnStale
	case errors.Is(err, processhost.ErrBusy):
		code = CodeTurnBusy
	case out.OutcomeUnknown:
		code = CodeTurnTransportUncertain
	}
	return &Refusal{Code: code, AgentUID: out.AgentUID, message: err.Error(), cause: err}
}
