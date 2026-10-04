package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func (c *agentCommand) runProcessResumeCLI(agent coremetadata.Agent, flags resourceQueryFlags, model, effort string, prompt []string, stdout, stderr io.Writer) error {
	mode, err := resolveLifecycleProjection("agent resume", flags.output)
	if err != nil {
		return usageError(err.Error())
	}
	if mode == cli.OutputModePaneID {
		return usageError("agent resume: process agents do not have a pane-id output")
	}
	paneCandidate, err := c.processResumeCandidate(processAgentResumeRequest{options: processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID}}})
	if err != nil {
		return err
	}
	if paneCandidate.Record.Provider == aiModeClaude && strings.TrimSpace(strings.Join(prompt, " ")) == "" {
		return usageError("agent resume: process Claude requires -- <prompt>; stream-json emits init only after the first user frame")
	}
	options := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID}, Model: model, Effort: effort}
	if len(prompt) > 0 {
		options.Prompt = processResumeFirstFrame{Kind: "user", Text: strings.Join(prompt, " ")}
	}
	request, err := newProcessAgentResumeRequest(options)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	result, err := c.resumeProcessAgent(ctx, request)
	if err != nil {
		return result.fail(err)
	}
	creator := c.rebind.create
	syncChanged, syncControls, syncAttention, err := result.resumeSynchronization(creator)
	if err != nil {
		return result.fail(err)
	}
	if err = result.writeResumeResult(creator, stdout, stderr, mode); err != nil {
		return result.fail(err)
	}
	if mode != cli.OutputModeNone && (result.Previous.InterruptedTurn != "" || len(result.Previous.Expired) > 0) {
		if _, err = fmt.Fprintf(stderr, "previous generation: interruptedTurn=%s expiredControls=%d\n", result.Previous.InterruptedTurn, len(result.Previous.Expired)); err != nil {
			return result.fail(err)
		}
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
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
	waitErr = errors.Join(waitErr, controlErr, syncAttention())
	if waitErr != nil {
		if ended, ok := processOwnerEnded(result.owner.registryPath, result.Binding, result.owner.waitRecorded, snapshot, waitErr, stderr); ok {
			return ended
		}
		return processResumeFailure(result.Binding, waitErr)
	}
	return processWaitExit(snapshot)
}

// Failed resume preserves the Agent and its recorded conversation. Cleanup
// retires only the new owned generation; it never applies create's deletion.
func (r *processAgentResumeResult) fail(cause error) error {
	if r.hasNoChild() {
		cause = errors.Join(cause, r.restoreReservation())
	} else if r.Handle != nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.owner.waitProcessAgent(ctx, nil)
		cause = errors.Join(cause, err)
	}
	if r.Binding.Agent == "" {
		return cause
	}
	return processResumeFailure(r.Binding, cause)
}

func (r *processAgentResumeResult) resumeSynchronization(creator *createCommand) (func(processhost.Snapshot) error, func(context.Context) error, func() error, error) {
	attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(r.owner.registryPath)))
	records, err := attention.read()
	if err != nil {
		return nil, nil, nil, err
	}
	previous := ""
	if old, ok := records[r.Binding.Pane]; ok {
		if old.Binding != r.previousBinding {
			return nil, nil, nil, processhost.ErrStale
		}
		previous = old.Binding.Generation
	}
	if err = attention.activate(r.Binding, r.owner.Provider, previous); err != nil {
		return nil, nil, nil, err
	}

	control, err := creator.newProcessCreateControl(r.owner)
	if err != nil {
		return nil, nil, nil, err
	}
	return func(snapshot processhost.Snapshot) error {
		if err := r.owner.recordProcessSnapshot(snapshot); err != nil {
			return err
		}
		return control.sync(context.Background())
	}, control.syncControls, func() error { return control.attention.sync(r.Handle, r.Binding) }, nil
}

func (r *processAgentResumeResult) writeResumeResult(creator *createCommand, stdout, stderr io.Writer, mode cli.OutputMode) error {
	if mode != cli.OutputModeNone {
		ownership := stderr
		if mode == cli.OutputModeDefault {
			ownership = stdout
		}
		if _, err := fmt.Fprintf(ownership, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", r.Binding.Agent, r.Binding.Pane); err != nil {
			return err
		}
	}
	result := r.owner.Created
	result.action = cli.ActionReused
	receipt := processResumeReceipt(r.Binding, result.name)
	return creator.writeResultsWithReceipt(stdout, "agent resume", mode, coremetadata.KindAgent, []createResult{result}, receipt)
}

func processResumeReceipt(binding processhost.Binding, name string) cli.OperationReceipt {
	receipt := cli.NewReceipt(cli.OperationResumeAgent, cli.ReceiptTarget{Kind: string(coremetadata.KindAgent), UID: binding.Agent, Name: name}, cli.ReceiptEffects{
		Identity: cli.IdentityReused, Address: cli.AddressUnchanged, Topology: cli.TopologyUnchanged, DesiredState: cli.DesiredStateUnchanged, Runtime: cli.RuntimeMaterialized, Focus: cli.FocusUnchanged,
	})
	receipt.Add(string(coremetadata.KindAgent), binding.Agent, name, cli.ActionReused)
	receipt.SelectWindows(binding.Window)
	return receipt
}

// processResumeHasReference distinguishes a new turn after -- from the existing
// tmux spelling where -- introduces the Agent reference itself.
func processResumeHasReference(fs *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return true
		}
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		registered := fs.Lookup(name)
		if registered == nil || inline {
			continue
		}
		boolean, ok := registered.Value.(interface{ IsBoolFlag() bool })
		if !ok || !boolean.IsBoolFlag() {
			i++
		}
	}
	return false
}

func processResumeFailure(binding processhost.Binding, cause error) error {
	return fmt.Errorf("agent resume: recorded conversation preserved for agent uid:%s pane uid:%s; after this owned generation is retired, retry: projmux agent resume uid:%s -- <prompt>: %w", binding.Agent, binding.Pane, binding.Agent, cause)
}
