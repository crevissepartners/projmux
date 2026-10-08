package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"reflect"
	"sync"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type agentProcessRelaunchState string

const (
	agentProcessPreview   agentProcessRelaunchState = "Preview"
	agentProcessUnchanged agentProcessRelaunchState = "Unchanged"
	agentProcessOwned     agentProcessRelaunchState = "Owned"
	agentProcessPrepared  agentProcessRelaunchState = "Prepared"
)

// Prepared is a durable recipe projection, never a live owner or writer grant.
// First input must still acquire the existing claim and validate the frozen plan.
type agentProcessPreparedResult struct {
	AgentUID, PaneUID, Conversation, LaunchDigest string
}

// The receiver registers Wait before ACK; registration failure cancels and
// waits for this exact target. Prepared has no lifetime to register.
type agentHostTransferResult struct {
	State         agentProcessRelaunchState
	Prepared      *agentProcessPreparedResult
	Result        agentRelaunchResult
	Owned         *agentHostTransferLifetime
	stopAdmission func() bool
}

type agentHostTransferLifetime struct {
	Context         context.Context
	Cancel          context.CancelFunc
	Target          *processAgentResumeResult
	synchronization processRelaunchSynchronization
	once            sync.Once
	err             error
}

// Wait consumes this exact target once, including actual Wait durability,
// controls and attention. Consumer cancellation/shutdown stops the target;
// producer request cancellation after handoff does not.
func (life *agentHostTransferLifetime) Wait(ctx context.Context) error {
	stop := context.AfterFunc(ctx, life.Cancel)
	defer stop()
	life.once.Do(func() {
		defer life.Cancel()
		life.err = waitOwnedProcessRelaunch(life.Context, life.Target, life.synchronization)
	})
	return life.err
}

// runOwnedHostRelaunch uses the production admission and transfer path without
// parsing stdout or retaining the producer's request as the child lifetime.
// The command copy confines injection to this call; the CLI remains foreground.
func (c *agentCommand) runOwnedHostRelaunch(ctx context.Context, reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, foreground ...io.Writer) (agentHostTransferResult, error) {
	if err := ctx.Err(); err != nil {
		return agentHostTransferResult{}, err
	}
	if request.host != "tmux" && request.host != "process" {
		return agentHostTransferResult{}, errors.New("agent relaunch: explicit execution host required")
	}
	var result agentHostTransferResult
	call := *c
	call.hostTransferContext, call.hostTransferResult = ctx, &result
	// The existing CLI adapter supplies its output streams and retains default
	// signal/EOF ownership. Typed callers omit them and receive the lifetime.
	stdout, stderr := io.Writer(io.Discard), io.Writer(io.Discard)
	if len(foreground) == 2 {
		stdout, stderr = foreground[0], foreground[1]
		call.hostTransferContext, call.hostTransferResult = nil, nil
	}
	err := call.dispatchHostTransfer(reg, target, request, stdout, stderr)
	return result, err
}

func (c *agentCommand) hostTransferLifetime() (context.Context, context.CancelFunc, func()) {
	if c.hostTransferResult == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		return ctx, cancel, cancel
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.hostTransferContext))
	stop := context.AfterFunc(c.hostTransferContext, cancel)
	c.hostTransferResult.stopAdmission = stop
	return ctx, cancel, func() {
		stop()
		if c.hostTransferResult.Owned == nil {
			cancel()
		}
	}
}

func (c *agentCommand) publishHostTransferResult(stdout io.Writer, request agentRelaunchRequest, result agentRelaunchResult) error {
	if c.hostTransferResult != nil {
		c.hostTransferResult.Result = result
		return nil
	}
	return writeAgentRelaunchResult(stdout, request, result)
}

func (c *agentCommand) finishOwnedHostTransfer(ctx context.Context, cancel context.CancelFunc, owned *processAgentResumeResult, synchronization processRelaunchSynchronization, result agentRelaunchResult, request agentRelaunchRequest, stdout, stderr io.Writer, fail func(error) error, beforeOwned ...func() error) error {
	if c.hostTransferResult != nil {
		// Remove the admission cancellation bridge before publishing ownership.
		// A cancellation already admitted still takes the existing exact fail path.
		stopped := c.hostTransferResult.stopAdmission()
		if !stopped || ctx.Err() != nil || c.hostTransferContext.Err() != nil {
			cancel()
			return fail(context.Canceled)
		}
		for _, finish := range beforeOwned {
			if err := finish(); err != nil {
				cancel()
				return fail(err)
			}
		}
		c.hostTransferResult.Result = result
		c.hostTransferResult.Owned = &agentHostTransferLifetime{Context: ctx, Cancel: cancel, Target: owned, synchronization: synchronization}
		return nil
	}
	if err := writeAgentRelaunchResult(stdout, request, result); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", owned.Binding.Agent, owned.Binding.Pane)
	return runProcessRelaunchOwner(ctx, cancel, owned, synchronization)
}

// runOwnedProcessRelaunch is the private samehost settings producer. A command
// copy reuses H1's admission bridge and exact owner lifetime; stdout is not an API.
func (c *agentCommand) runOwnedProcessRelaunch(ctx context.Context, reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest) (agentHostTransferResult, error) {
	if err := ctx.Err(); err != nil {
		return agentHostTransferResult{}, err
	}
	if request.host != "" && request.host != "process" {
		return agentHostTransferResult{}, errors.New("agent relaunch: samehost process target required")
	}
	current, found := reg.Agent(target.Metadata.UID)
	if !found || !reflect.DeepEqual(*current, target) {
		return agentHostTransferResult{}, errors.New("agent relaunch: supplied process source changed")
	}
	pane, ambiguous := processResumePane(reg, target.Metadata.UID)
	if ambiguous || pane == nil {
		return agentHostTransferResult{}, errors.New("agent relaunch: exact process Pane required")
	}
	var result agentHostTransferResult
	call := *c
	call.hostTransferContext, call.hostTransferResult = ctx, &result
	if err := call.runProcessRelaunch(reg, target, *pane, request, io.Discard, io.Discard); err != nil {
		return agentHostTransferResult{}, err
	}
	switch {
	case result.Owned != nil:
		result.State = agentProcessOwned
	case result.Prepared != nil:
		result.State = agentProcessPrepared
	case result.Result.Unchanged:
		result.State = agentProcessUnchanged
	case result.Result.DryRun:
		result.State = agentProcessPreview
	default:
		return agentHostTransferResult{}, errors.New("agent relaunch: missing typed process result")
	}
	return result, nil
}

// prepareOwnedClaudeResume exposes no-firstinput explicit Resume to a private
// consumer. An existing plan is validated by the ordinary resume engine and
// returned unchanged; it is never treated as explicit replacement recovery.
func (c *agentCommand) prepareOwnedClaudeResume(ctx context.Context, opts processAgentResumeOptions) (agentHostTransferResult, error) {
	if c.rebind == nil || c.rebind.create == nil {
		return agentHostTransferResult{}, errors.New("agent resume: process resume creator is not configured")
	}
	if opts.Prompt != (processResumeFirstFrame{}) || opts.claim != nil {
		return agentHostTransferResult{}, errors.New("agent resume: preparation requires no first input or claim")
	}
	if err := ctx.Err(); err != nil {
		return agentHostTransferResult{}, err
	}
	request, err := newProcessAgentResumeRequest(opts)
	if err != nil {
		return agentHostTransferResult{}, err
	}
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return agentHostTransferResult{}, err
	}
	if candidate.Record.Provider != aiModeClaude {
		return agentHostTransferResult{}, errors.New("agent resume: no-firstinput preparation requires Claude")
	}
	candidate, prepared, err := c.prepareDeferredLaunch(ctx, candidate, opts)
	if err != nil {
		return agentHostTransferResult{}, err
	}
	if prepared != nil {
		if _, err = c.frozenDeferredCommand(candidate, prepared); err != nil {
			return agentHostTransferResult{}, err
		}
		if err = ctx.Err(); err != nil {
			return agentHostTransferResult{}, err
		}
		return agentHostTransferResult{State: agentProcessPrepared, Result: agentRelaunchResult{AgentUID: candidate.Agent.Metadata.UID, PaneUID: candidate.Pane.Metadata.UID, NewPaneUID: candidate.Pane.Metadata.UID, Provider: aiModeClaude, CurrentHost: "process", TargetHost: "process", Action: "resume", Phase: coremetadata.PhaseOffline}, Prepared: processPreparedProjection(prepared)}, nil
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return agentHostTransferResult{}, err
	}
	// Empty overrides retain the recorded recipe through the samehost planner.
	call := *c
	call.processResumePreparation = opts.Model == "" && opts.Effort == ""
	result, err := call.runOwnedProcessRelaunch(ctx, reg, candidate.Agent, agentRelaunchRequest{agentRef: "uid:" + candidate.Agent.Metadata.UID, model: opts.Model, effort: opts.Effort})
	if err == nil {
		result.Result.Action = "resume"
	}
	return result, err
}

func processPreparedProjection(record *deferredLaunchRecord) *agentProcessPreparedResult {
	return &agentProcessPreparedResult{AgentUID: record.Agent, PaneUID: record.Retired.Binding.PaneUID, Conversation: record.Retired.SessionID, LaunchDigest: deferredLaunchDigest(record)}
}
