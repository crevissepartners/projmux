package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func (c *agentCommand) runDeferredResumeCLI(agent coremetadata.Agent, flags resourceQueryFlags, model, effort string, stdout, stderr io.Writer) error {
	mode, err := resolveLifecycleProjection("agent resume", flags.output)
	if err != nil {
		return usageError(err.Error())
	}
	if mode == cli.OutputModePaneID {
		return usageError("agent resume: process agents do not have a pane-id output")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	options := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID}, Model: model, Effort: effort}
	claim, err := c.claimDeferredProcessAgent(ctx, options)
	if err != nil {
		return err
	}
	defer func() { _ = claim.Close() }()
	if mode != cli.OutputModeNone {
		output := stderr
		if mode == cli.OutputModeDefault {
			output = stdout
		}
		if _, err = fmt.Fprintf(output, "agent uid:%s pane uid:%s runtime=process foreground=claimed\n", agent.Metadata.UID, claim.record.Binding.PaneUID); err != nil {
			return err
		}
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	result, err := claim.WaitPeer(ctx)
	if err != nil {
		if result.Handle == nil && err == context.Canceled {
			return nil
		}
		return err
	}
	creator := c.rebind.create
	syncChanged, syncControls, syncAttention, err := result.resumeSynchronization(creator)
	if err != nil {
		return result.fail(err)
	}
	if err = result.writeResumeResult(creator, stdout, stderr, mode); err != nil {
		return result.fail(err)
	}
	snapshot, waitErr := result.owner.waitProcessAgent(ctx, processSnapshotSynchronizer(syncChanged, func(snapshot processhost.Snapshot) error {
		if len(snapshot.Pending) > 0 {
			return syncControls(context.WithoutCancel(ctx))
		}
		return nil
	}))
	controlErr := syncControls(context.Background())
	if errors.Is(controlErr, processhost.ErrClosed) || errors.Is(controlErr, processhost.ErrStale) {
		controlErr = nil
	}
	if err = errors.Join(waitErr, controlErr, syncAttention()); err != nil {
		return processResumeFailure(result.Binding, err)
	}
	return processWaitExit(snapshot)
}
