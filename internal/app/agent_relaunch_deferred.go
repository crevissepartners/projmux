package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// Only same-location Claude relaunch calls this consumer. Host transfer keeps
// its own prompt requirement and never enters the deferred claim path.
func (c *agentCommand) startDeferredRelaunch(ctx context.Context, cancel context.CancelFunc, candidate processResumeCandidate, request agentRelaunchRequest, recipe processRelaunchRecipe, launch processRelaunchLaunch, result agentRelaunchResult, stdout, stderr io.Writer) error {
	files, err := deferredLaunchFiles(launch.command.Args)
	if err != nil {
		return err
	}
	planned := coremetadata.Registry{Agents: []coremetadata.Agent{candidate.Agent.Clone()}}
	mutator := c.rebind.create.store.mutator()
	if err = launch.settings.record(&planned, mutator, candidate.Agent.Metadata.UID); err != nil {
		return err
	}
	if err = recipe.guidance.record(&planned, mutator, candidate.Agent.Metadata.UID); err != nil {
		return err
	}
	if err = recipe.links.record(&planned, mutator, candidate.Agent.Metadata.UID); err != nil {
		return err
	}
	updated, _ := planned.Agent(candidate.Agent.Metadata.UID)
	updated.Spec.Workspace = recipe.workspace
	command := launch.command
	command.Env = nil
	record := &deferredLaunchRecord{Version: 1, Agent: candidate.Agent.Metadata.UID, Retired: *candidate.Record.Clone(), OldSpec: candidate.Agent.Spec, NewSpec: updated.Spec, OldAnnotations: candidate.Agent.Metadata.Annotations, NewAnnotations: updated.Metadata.Annotations, Command: command, Files: files, Model: recipe.restart.settings.resolution.New.Model.Value, Effort: recipe.restart.settings.resolution.New.Effort.Value}
	// Acquire uses the same guard and identity checks as ordinary deferred resume.
	claim, err := c.claimDeferredProcessAgent(ctx, processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: record.Agent}})
	if err != nil {
		return err
	}
	defer func() { _ = claim.Close() }()
	if err = c.commitDeferredRelaunch(ctx, claim, candidate, record); err != nil {
		return err
	}
	output := stdout
	if request.json {
		output = stderr
	}
	if _, err = fmt.Fprintf(output, "agent uid:%s pane uid:%s runtime=process foreground=claimed\n", record.Agent, record.Retired.Binding.PaneUID); err != nil {
		return err
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	owned, err := claim.WaitPeer(ctx)
	if err != nil {
		if owned.Handle == nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	changed, controls, attention, err := owned.resumeSynchronization(c.rebind.create)
	if err != nil {
		return owned.fail(err)
	}
	result.Outcome = personaOutcomeResumed
	if recipe.restart.running {
		result.Outcome = personaOutcomeRestarted
	}
	result.NewPaneUID = owned.Binding.Pane
	if err = writeAgentRelaunchResult(stdout, request, result); err != nil {
		return owned.fail(err)
	}
	if _, err = fmt.Fprintf(stderr, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", owned.Binding.Agent, owned.Binding.Pane); err != nil {
		return owned.fail(err)
	}
	// stdin is already watched above; the owner loop remains the B1 Wait path.
	return runProcessRelaunchOwner(ctx, cancel, &owned, processRelaunchSynchronization{changed, controls, attention}, true)
}

func (c *agentCommand) commitDeferredRelaunch(ctx context.Context, claim *deferredProcessClaim, candidate processResumeCandidate, record *deferredLaunchRecord) error {
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	defer unlock()
	if err = c.checkDeferredClaim(record.Agent, claim); err != nil {
		return err
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := reg.Agent(record.Agent)
	pane, ambiguous := processResumePane(reg, record.Agent)
	if !found || ambiguous || pane == nil || !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) || !reflect.DeepEqual(agent.Spec, candidate.Agent.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, candidate.Agent.Metadata.Annotations) {
		return deferredRefused("relaunch source changed before configuration commit")
	}
	if err = writeDeferredState(c.deferredStatePath("deferred-launches", record.Agent), record); err != nil {
		return err
	}
	// A crash between these writes is repaired only by the exact old/new proof.
	return c.reconcileDeferredLaunch(ctx, record)
}
