package app

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"io"
)

type turnControlAdapter struct{ c *agentCommand }

func (a turnControlAdapter) Bind(_ context.Context, reg coremetadata.Registry, agent coremetadata.Agent) (agentcontrol.Binding, error) {
	b, err := a.c.bindAgentControl("agent turn start", reg, agent)
	return serviceApprovalBinding(b), err
}
func (a turnControlAdapter) Call(_ context.Context, b agentcontrol.Binding, r agentcontrol.Request) (agentcontrol.Response, error) {
	old := exactAgentControlBinding{Identity: codexLifecycleIdentity(b.Identity), Endpoint: b.Endpoint, Epoch: b.Epoch, StateDir: b.StateDir, ProjectUID: b.ProjectUID, WindowUID: b.WindowUID, Fence: b.Fence}
	response, err := a.c.callControl(old, agentControlRequest{Operation: r.Operation, Text: r.Text})
	return agentcontrol.Response(response), err
}
func (c *agentCommand) turnService() runtime.TurnService {
	return runtime.TurnService{
		Control: turnControlAdapter{c},
		RecordStart: func(_ context.Context, b agentcontrol.Binding, r agentcontrol.Response) error {
			return runtime.RecordStartedTurn(b, r, func(apply func(*coremetadata.Registry, coremetadata.Mutator) error) error {
				return c.mutateAgent(b.Identity.AgentUID, apply)
			})
		},
		Process: func(ctx context.Context, reg coremetadata.Registry, a coremetadata.Agent, r runtime.TurnRequest) (runtime.TurnResult, error) {
			out := runtime.TurnResult{AgentUID: a.Metadata.UID, PaneUID: a.Status.PaneRef, Process: true, Mode: r.Action}
			if pane, ok := reg.Pane(a.Status.PaneRef); ok && pane.Status.ProcessSession != nil {
				out.Generation, out.ThreadID = pane.Status.ProcessSession.Binding.Generation, pane.Status.ProcessSession.ThreadID
			}
			if a.Spec.Provider == aiModeClaude && r.Action == "interrupt" && c.processRuntime != nil && c.processRuntime.controlOverride != nil {
				out.TurnID = a.Status.Progress.TurnRef
				err := c.processRuntime.controlOverride(ctx, reg, a.Status.PaneRef, resourcegraph.ProcessInterrupt, out.TurnID, "")
				out.Accepted = err == nil
				return out, err
			}

			paths, err := c.controlPaths()
			if err != nil {
				return out, err
			}
			result, err := runtime.CallProcessTurn(ctx, intmetadata.PathFor(paths.StateDir), c.loadRegistry, reg, a, r)
			if r.Action == "steer" && errors.Is(err, processhost.ErrBusy) {
				err = fmt.Errorf("%w; next: `projmux agent turn start uid:%s -- <text>`", err, a.Metadata.UID)
			}
			return result, err
		},
		ClaudeInterrupt: func(ctx context.Context, reg coremetadata.Registry, a coremetadata.Agent, via string) (runtime.TurnResult, error) {
			return c.claudeTurnInterrupter(ctx, reg, a, via)
		},
	}
}
func (c *agentCommand) claudeTurnInterrupter(ctx context.Context, reg coremetadata.Registry, a coremetadata.Agent, via string) (runtime.TurnResult, error) {
	if c.controlRoute == nil || c.controlRunner == nil || c.controlPaths == nil {
		return runtime.TurnResult{}, fmt.Errorf("claude turn interrupt unavailable: exact tmux route or private state path is not configured")
	}
	route, err := c.controlRoute(ctx)
	if err != nil {
		return runtime.TurnResult{}, fmt.Errorf("claude turn interrupt unavailable: resolve exact tmux socket: %w", err)
	}
	paths, err := c.controlPaths()
	if err != nil {
		return runtime.TurnResult{}, fmt.Errorf("claude turn interrupt unavailable: resolve audit path: %w", err)
	}
	var guards []func() error
	if target := c.declaredProcessTarget(reg, a); target != nil {
		guards = append(guards, func() error { return target.admit(resourcegraph.ProcessKeys) })
	}
	return (runtime.ClaudeTurnInterrupter{StateDir: paths.StateDir, Runner: explicitTmuxRunner{Runner: c.controlRunner, Target: route.Target}, Load: c.loadRegistry, Now: c.now, Guards: guards}).Interrupt(ctx, reg, a, via)
}
func (c *agentCommand) writeTurnResult(out io.Writer, r runtime.TurnResult, action string) error {
	label := agentActionSendTurn
	if action == "interrupt" {
		label = agentActionInterruptTurn
	} else if r.Mode == "steer" {
		label = agentActionSteerTurn
	}
	if r.Process && action != "deliver" && action != "steer" {
		a := coremetadata.Agent{}
		a.Metadata.UID = r.AgentUID
		if r.Delivery == "joined" {
			return c.writeJoinedProcessTurn(out, label, a, r.TurnID, processTurnJoin{Turn: r.RunningTurn, Origin: r.Origin})
		}
		return c.writeProcessTurn(out, label, a, r.TurnID)
	}
	if action == "interrupt" && r.Delivery == "sent" {
		_, err := fmt.Fprintf(out, "%s agent=uid:%s pane=uid:%s delivery=sent (cancellation unconfirmed)\n", label, r.AgentUID, r.PaneUID)
		return err
	}
	mode := ""
	if action == "deliver" {
		mode = " mode=" + r.Mode
	}
	receipt := ""
	if r.Mode == "steer" {
		receipt = " acceptance=" + r.Acceptance + " delivery=" + r.Delivery
	}
	_, err := fmt.Fprintf(out, "%s thread=%s turn=%s%s%s\n", label, safeApprovalDetail(r.ThreadID), safeApprovalDetail(r.TurnID), mode, receipt)
	return err
}
