package app

import (
	"context"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

func (c *agentCommand) runDeferredResumeCLI(agent coremetadata.Agent, flags resourceQueryFlags, model, effort string, stdout, stderr io.Writer) error {
	mode, err := resolveLifecycleProjection("agent resume", flags.output)
	if err != nil {
		return usageError(err.Error())
	}
	if mode == cli.OutputModePaneID {
		return usageError("agent resume: process agents do not have a pane-id output")
	}
	ctx, cancel := processForegroundLifetime()
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
	// The claim waits for its first input under the same EOF trigger; the owned
	// Wait below does not start it again.
	processStartStdinEOF(ctx, cancel)
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
	return result.ownedWait(syncChanged, syncControls, syncAttention).run(ctx, cancel, nil, stderr)
}
