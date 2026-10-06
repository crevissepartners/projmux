package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"sync"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentHostTransferResult carries the existing projection and, only for a
// verified process target, the actual owner and its synchronization/lifetime.
// The receiver must arrange Wait before returning success to its own caller;
// if registration fails it must Cancel and Wait before discarding the result.
type agentHostTransferResult struct {
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

func (c *agentCommand) finishOwnedHostTransfer(ctx context.Context, cancel context.CancelFunc, owned *processAgentResumeResult, synchronization processRelaunchSynchronization, result agentRelaunchResult, request agentRelaunchRequest, stdout, stderr io.Writer, fail func(error) error) error {
	if c.hostTransferResult != nil {
		// Remove the admission cancellation bridge before publishing ownership.
		// A cancellation already admitted still takes the existing exact fail path.
		stopped := c.hostTransferResult.stopAdmission()
		if !stopped || ctx.Err() != nil || c.hostTransferContext.Err() != nil {
			cancel()
			return fail(context.Canceled)
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
