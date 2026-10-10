package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

type tmuxCodexRelaunchSource struct {
	conversation processhost.TmuxCodexSource
	retired      coremetadata.Agent
	request      agentRelaunchRequest
	reservation  *codexbroker.RemoteTransfer
	journal      *codexHostTransferRecord
	journalPath  string
}

func (c *agentCommand) runCodexHostRelaunch(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("agent relaunch: agent/%s %s (%s); nothing was changed", target.Metadata.Name, detail, reason))
	}
	if coremetadata.RecordsDialogueReplyOnly(target.Metadata.Annotations) {
		return refuse(replyOnlyReasonLaunchFixed, "has a fixed reply-only launch")
	}
	if request.host == "tmux" && len(request.prompt) != 0 {
		return usageError("agent relaunch: a first prompt applies only to the process target; nothing was changed")
	}
	if err := requireLaunchOptions(agentRelaunchSpelling, aiModeCodex, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if c.rebind == nil || c.rebind.create == nil {
		return refuse(relaunchReasonNoConversation, "has no configured relaunch owner")
	}
	path, err := c.codexHostTransferPath(target.Metadata.UID)
	if err != nil {
		return err
	}
	journal, err := readCodexHostTransfer(path)
	if err != nil {
		return err
	}
	if journal != nil {
		if c.hostTransferResult != nil {
			return fmt.Errorf("agent relaunch: pending Codex transfer requires CLI recovery; recover with: %s; nothing was changed", codexTransferRecoveryCommand(target.Metadata.UID, request))
		}
		return c.recoverCodexHostTransfer(reg, target, request, path, journal, stdout, stderr)
	}
	if request.host == "tmux" {
		return c.moveProcessToTmux(reg, target, request, refuse, stdout, stderr)
	}
	return c.moveTmuxCodexToProcess(reg, target, request, refuse, stdout, stderr)
}

func (c *agentCommand) moveTmuxCodexToProcess(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, refuse func(string, string) error, stdout, stderr io.Writer) error {
	restart := c.newAgentRestart(agentRelaunchSpelling, reg, target, aiModeCodex, relaunchTokens, refuse)
	if err := restart.checkTarget(); err != nil {
		return err
	}
	pane, found := reg.Pane(restart.paneUID)
	if !found || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() || target.Status.SessionRef == nil || target.Status.SessionRef.Codex == nil || pane.Status.Activation.Codex.ThreadID != target.Status.SessionRef.Codex.ThreadID {
		return refuse(relaunchReasonNoConversation, "has no exact tmux Codex broker authority")
	}
	if _, err := resolveDeleteTarget(agentRelaunchSpelling, request.socket, c.lookupEnv); err != nil {
		return err
	}
	recipe, err := c.planProcessRelaunchRecipe(reg, target, *pane, request, refuse)
	if err != nil {
		return err
	}
	result := hostRelaunchResult(recipe, target, *pane, request, "tmux")
	if request.dryRun {
		return c.publishHostTransferResult(stdout, request, result)
	}
	if recipe.restart.confirmationRequired() && !request.yes {
		return refuse(relaunchReasonAgentBusy, "would interrupt work; re-run with --yes")
	}
	journalPath, err := c.codexHostTransferPath(target.Metadata.UID)
	if err != nil {
		return err
	}
	unlock, err := lockDeferredClaim(journalPath)
	if err != nil {
		return err
	}
	defer unlock()
	pending, err := readCodexHostTransfer(journalPath)
	if err != nil {
		return err
	}
	if pending != nil {
		return errors.New("agent relaunch: another Codex transfer requires recovery; no source stopped")
	}
	command, config, settings, err := c.planHostProcessCodexLaunch(recipe)
	if err != nil {
		return err
	}
	ctx, cancel, release := c.hostTransferLifetime()
	defer release()
	authority := *pane.Status.Activation.Codex.Authority
	domain, err := codexBrokerStateDomain(c.lookupEnv, os.UserHomeDir)
	if err != nil {
		return err
	}
	key, err := codexbroker.NewEndpointKey(authority.StateDomainID, authority.EndpointGenerationID)
	if err != nil {
		return err
	}
	discovery, err := codexBrokerRuntimeDiscovery(domain, key, authority.BrokerRuntimeID)
	if err != nil {
		return err
	}
	conn, err := codexbroker.DialTransfer(ctx, discovery, codexbroker.DialConfig{})
	if err != nil {
		return err
	}
	from := codexbroker.TransferSource{Project: restart.registryWindowProject(), Window: target.Metadata.OwnerUID(), Agent: target.Metadata.UID, Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation, Operation: pane.Status.Activation.OperationID, PaneRuntimeID: pane.Status.Activation.RuntimeID, RuntimeID: authority.BrokerRuntimeID, Thread: pane.Status.Activation.Codex.ThreadID, Endpoint: key, Fence: codexbroker.Fence{Connection: codexbroker.ConnectionEpoch(authority.ConnectionEpoch), Binding: codexbroker.BindingEpoch(authority.BindingEpoch)}}
	preparedReceipt, err := codexbroker.NewTransferReceipt(from)
	if err != nil {
		_ = conn.Close()
		return err
	}
	journal := &codexHostTransferRecord{Version: 1, Source: target.Clone(), Pane: pane.Clone(), Receipt: preparedReceipt, Phase: "preparing"}
	if err = writeCodexHostTransfer(journalPath, journal); err != nil {
		_ = conn.Close()
		return err
	}
	reservation, err := conn.PrepareTransfer(ctx, preparedReceipt)
	if err != nil {
		if cleanupErr := c.cleanupNoEffectCodexTransfer(context.WithoutCancel(ctx), journalPath, journal); cleanupErr == nil {
			_ = conn.Close()
			return fmt.Errorf("agent relaunch: prepare confirmed no-effect; source unchanged: %w", err)
		}
		_ = conn.Close()
		return fmt.Errorf("agent relaunch: prepare outcome retained; recover with: %s: %w", codexTransferRecoveryCommand(target.Metadata.UID, request), err)
	}
	defer reservation.Close()
	journal.Receipt = reservation.Receipt()
	journal.Phase = "prepared"
	if err = writeCodexHostTransfer(journalPath, journal); err != nil {
		_ = reservation.Abort(context.WithoutCancel(ctx))
		return err
	}
	// Until unsubscribe is attempted, an abort can return only the existing
	// source binding's authority. Later failures retain the reservation.
	if err = c.stopTmuxCodexTransfer(ctx, target, *pane, request); err != nil {
		if abortErr := reservation.Abort(context.WithoutCancel(ctx)); abortErr == nil {
			_ = removeCodexHostTransfer(journalPath)
		}
		return err
	}
	retireCtx, retireCancel := context.WithTimeout(ctx, 90*time.Second)
	journal.Phase = "retiring"
	if err = writeCodexHostTransfer(journalPath, journal); err != nil {
		retireCancel()
		return err
	}
	err = reservation.Retire(retireCtx)
	retireCancel()
	if err != nil {
		return fmt.Errorf("agent relaunch: exact Codex retirement unconfirmed; source remains fenced: %w", err)
	}
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	retired, found := latest.Agent(target.Metadata.UID)
	if !found || !reflect.DeepEqual(retired.Spec, target.Spec) || !reflect.DeepEqual(retired.Metadata.Annotations, target.Metadata.Annotations) || !retired.Status.SessionRef.SameConversation(target.Status.SessionRef) {
		return errors.New("agent relaunch: source changed after retirement; reservation retained")
	}
	journal.Receipt = reservation.Receipt()
	journal.Retired = retired
	journal.Phase = "retired"
	if err = writeCodexHostTransfer(journalPath, journal); err != nil {
		return err
	}
	source := tmuxCodexRelaunchSource{journal: journal, journalPath: journalPath, retired: retired.Clone(), request: request, reservation: reservation, conversation: processhost.TmuxCodexSource{Project: from.Project, Window: from.Window, Agent: from.Agent, Pane: from.Pane, Generation: from.Generation, Operation: from.Operation, RuntimeID: from.PaneRuntimeID, Thread: from.Thread, BrokerRuntime: from.RuntimeID, Endpoint: string(from.Endpoint), ConnectionEpoch: uint64(from.Fence.Connection), BindingEpoch: uint64(from.Fence.Binding)}}
	return c.startTmuxCodexTransfer(ctx, cancel, reg, source, recipe, command, config, settings, result, stdout, stderr)
}

func (c *agentCommand) planHostProcessCodexLaunch(recipe processRelaunchRecipe) (processhost.Command, processhost.CodexConfig, agentSettingsLaunch, error) {
	settings := recipe.restart.settings.writeSnapshot()
	if settings.snapshotErr != nil {
		return processhost.Command{}, processhost.CodexConfig{}, settings, settings.snapshotErr
	}
	annotations := settings.launchAnnotations(recipe.annotations)
	_, policy, err := c.rebind.create.codexResumeProfile(annotations)
	if err != nil {
		return processhost.Command{}, processhost.CodexConfig{}, settings, err
	}
	ai := c.ai.(*aiCommand)
	command, err := processhost.CodexCommand(ai.findAgentBinary(aiModeCodex), recipe.workspace.CWD, os.Environ(), nil)
	config := processhost.CodexConfig{Version: version.String(), Roots: slices.Clone(recipe.workspace.AdditionalWritableRoots), Settings: codexappserver.ThreadSettings{Model: settings.resolution.New.Model.Value, Effort: annotations[coremetadata.AnnotationAgentEffort], Policy: policy}}
	return command, config, settings, err
}

func (c *agentCommand) stopTmuxCodexTransfer(ctx context.Context, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest) error {
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	current, found := latest.Agent(target.Metadata.UID)
	currentPane, present := latest.Pane(pane.Metadata.UID)
	if !found || !present || !reflect.DeepEqual(current.Spec, target.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, target.Metadata.Annotations) || !current.Status.SessionRef.SameConversation(target.Status.SessionRef) || !reflect.DeepEqual(currentPane.Status.Activation, pane.Status.Activation) {
		return errors.New("agent relaunch: exact source changed before Stop")
	}
	if !request.yes && current.EffectiveInteraction(c.clock()).Kind != coremetadata.InteractionIdle && current.EffectiveInteraction(c.clock()).Kind != coremetadata.InteractionResponseComplete {
		return usageError("agent relaunch: source started work; re-run with --yes")
	}
	stopErr := c.stopAgentPane(request.socket, pane.Metadata.UID, io.Discard, io.Discard)
	bounded, cancel := context.WithTimeout(ctx, processRelaunchRetirementTimeout)
	defer cancel()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	for {
		if err = c.verifyTmuxCodexPaneRetired(pane.Metadata.UID, request); err == nil {
			return nil
		}
		select {
		case <-bounded.Done():
			return errors.Join(stopErr, err, bounded.Err())
		case <-ticker.C:
		}
	}
}

func (c *agentCommand) verifyTmuxCodexPaneRetired(pane string, request agentRelaunchRequest) error {
	route, err := resolveDeleteTarget(agentRelaunchSpelling, request.socket, c.lookupEnv)
	if err != nil {
		return err
	}
	live := c.managedPaneLive
	if live == nil {
		live = observeManagedPaneLive
	}
	alive, err := live(route, pane)
	if err != nil {
		return err
	}
	if alive {
		return errors.New("old tmux Codex Pane remains alive")
	}
	return nil
}

func (c *agentCommand) startTmuxCodexTransfer(ctx context.Context, cancel context.CancelFunc, reg coremetadata.Registry, source tmuxCodexRelaunchSource, recipe processRelaunchRecipe, command processhost.Command, config processhost.CodexConfig, settings agentSettingsLaunch, result agentRelaunchResult, stdout, stderr io.Writer) error {
	creator := c.rebind.create
	operation, err := newCreateOperationID()
	if err != nil {
		return err
	}
	generation, err := creator.mintGeneration()
	if err != nil {
		return err
	}
	state, err := creator.store.stateDir()
	if err != nil {
		return err
	}
	source.journal.Target = processhost.Binding{Host: operation, Project: source.conversation.Project, Window: source.conversation.Window, Agent: source.conversation.Agent, Generation: generation, Operation: operation}
	planned := reg.Clone()
	plannedAgent, _ := planned.Agent(source.retired.Metadata.UID)
	plannedAgent.Spec.Workspace = recipe.workspace
	if err = settings.record(&planned, creator.store.mutator(), source.retired.Metadata.UID); err != nil {
		return err
	}
	if err = recipe.guidance.record(&planned, creator.store.mutator(), source.retired.Metadata.UID); err != nil {
		return err
	}
	if err = recipe.links.record(&planned, creator.store.mutator(), source.retired.Metadata.UID); err != nil {
		return err
	}
	plannedAgent, _ = planned.Agent(source.retired.Metadata.UID)
	plannedExpected := plannedAgent.Clone()
	source.journal.Expected = &plannedExpected
	source.journal.Phase = "reserving"
	if err = writeCodexHostTransfer(source.journalPath, source.journal); err != nil {
		return err
	}
	var binding processhost.Binding
	var expected coremetadata.Agent
	_, err = creator.store.update(func(working *coremetadata.Registry) error {
		agent, ok := working.Agent(source.retired.Metadata.UID)
		if !ok || !reflect.DeepEqual(agent.Spec, source.retired.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, source.retired.Metadata.Annotations) || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" || !agent.Status.SessionRef.SameConversation(source.retired.Status.SessionRef) {
			return errors.New("agent relaunch: source recipe changed before target reservation")
		}
		mut := creator.store.mutator()
		pane, err := mut.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{CWD: recipe.workspace.CWD}, operation)
		if err != nil {
			return err
		}
		binding = processhost.Binding{Host: operation, Project: source.conversation.Project, Window: source.conversation.Window, Agent: agent.Metadata.UID, Pane: pane.Metadata.UID, Generation: generation, Operation: operation}
		if err := mut.ReserveProcessBinding(working, metadataProcessBinding(binding)); err != nil {
			return err
		}
		agent, _ = working.Agent(binding.Agent)
		agent.Spec.Workspace = recipe.workspace
		if err := settings.record(working, mut, binding.Agent); err != nil {
			return err
		}
		if err := recipe.guidance.record(working, mut, binding.Agent); err != nil {
			return err
		}
		if err := recipe.links.record(working, mut, binding.Agent); err != nil {
			return err
		}
		agent, _ = working.Agent(binding.Agent)
		expected = agent.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	source.journal.Target = binding
	source.journal.Expected = &expected
	source.journal.Phase = "reserved"
	if err = writeCodexHostTransfer(source.journalPath, source.journal); err != nil {
		return err
	}
	owned := processAgentResumeResult{Binding: binding, owner: processAgentCreateResult{Binding: binding, Provider: aiModeCodex, registryPath: intmetadata.PathFor(state), Created: createResult{kind: coremetadata.KindAgent, uid: binding.Agent, name: source.retired.Metadata.Name, windowUID: binding.Window}}}
	handedOff := false
	fail := func(cause error) error {
		if owned.Handle != nil && !owned.hasNoChild() {
			stopped, stopCancel := context.WithCancel(context.Background())
			stopCancel()
			snapshot, waitErr := owned.owner.waitProcessAgent(stopped, nil)
			cause = errors.Join(cause, waitErr)
			if snapshot.Exit == nil || waitErr != nil {
				return fmt.Errorf("agent relaunch: target retirement unknown; inspect agent uid:%s; no replacement writer permitted: %w", binding.Agent, cause)
			}
		}
		observed, readErr := c.loadRegistry()
		if readErr != nil {
			return errors.Join(cause, readErr)
		}
		if pane, ok := observed.Pane(binding.Pane); ok {
			copy := pane.Clone()
			source.journal.TerminatedTarget = &copy
			if handedOff {
				source.journal.Phase = "handoff-retired"
				if err := writeCodexHostTransfer(source.journalPath, source.journal); err != nil {
					return errors.Join(cause, err)
				}
			}
			var publishErr error
			if handedOff {
				publishErr = publishCodexHostTransferArchive(source.journalPath, source.journal)
			} else {
				publishErr = writeCodexHostTransfer(source.journalPath, source.journal)
			}
			if err := publishErr; err != nil {
				return fmt.Errorf("agent relaunch: owned target actual Wait persisted; archive failure retained; recover with: %s: %w", codexTransferRecoveryCommand(binding.Agent, source.request), errors.Join(cause, err))
			}
		}
		_, restoreErr := creator.store.update(func(working *coremetadata.Registry) error {
			current, ok := working.Agent(binding.Agent)
			if !ok || !reflect.DeepEqual(current.Status.SessionRef, expected.Status.SessionRef) {
				return processhost.ErrStale
			}
			return restoreTmuxTransferReservation(working, creator.store.mutator(), binding, expected, source.retired)
		})
		if restoreErr != nil {
			return fmt.Errorf("agent relaunch: transfer failed; recovery fenced by changed target state; inspect agent uid:%s before retrying: %w", binding.Agent, errors.Join(cause, restoreErr))
		}
		if handedOff {
			return fmt.Errorf("agent relaunch: new writer retired after handoff; previous recipe retained; recover with: %s: %w", codexTransferRecoveryCommand(binding.Agent, source.request), cause)
		}
		source.journal.Phase = "restored"
		journalErr := writeCodexHostTransfer(source.journalPath, source.journal)
		return fmt.Errorf("agent relaunch: transfer failed; previous recipe/conversation retained; recover with: %s --host tmux: %w", tmuxTransferRecovery(reg, source.retired, source.request), errors.Join(cause, journalErr))
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	host, err := processhost.NewHost(binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: command.Env}, creator.processCreateTransactions(owned.owner.registryPath), processhost.DefaultLimits())
	if err != nil {
		return fail(err)
	}
	transfer := processhost.CodexTransfer{Source: source.conversation, Verify: func(ctx context.Context, from processhost.TmuxCodexSource, to processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if from != source.conversation || to != binding {
			return processhost.ErrStale
		}
		if err := c.verifyTmuxCodexPaneRetired(from.Pane, source.request); err != nil {
			return err
		}
		if err := source.reservation.Check(ctx); err != nil {
			return err
		}
		if !source.reservation.Receipt().Retired {
			return processhost.ErrStale
		}
		latest, err := c.loadRegistry()
		if err != nil {
			return err
		}
		agent, ok := latest.Agent(binding.Agent)
		if !ok || !sameHostTransferSpec(agent.Spec, expected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, expected.Metadata.Annotations) || !agent.Status.SessionRef.SameConversation(source.retired.Status.SessionRef) {
			return fmt.Errorf("%w: target recipe/session changed", processhost.ErrStale)
		}
		return nil
	}}
	source.journal.Phase = "launching"
	if err = writeCodexHostTransfer(source.journalPath, source.journal); err != nil {
		return fail(err)
	}
	launch := processhost.Launch{Binding: binding, Command: command, Spawned: processCodexCreateSpawn(owned.owner.registryPath, binding)}
	ctx, owned.owner.stopRecorder = newProcessOwnerStop(ctx, owned.owner.registryPath, binding)
	endpoint, err := startProcessCodexTransfer(ctx, host, launch, config, owned.owner.registryPath, transfer)
	owned.owner.codexEndpoint = endpoint
	if endpoint != nil && endpoint.handle != nil {
		owned.Handle, owned.owner.Handle = endpoint.handle, endpoint.handle
	}
	if err != nil {
		return fail(err)
	}
	source.journal.Phase = "ready"
	if err = writeCodexHostTransfer(source.journalPath, source.journal); err != nil {
		return fail(err)
	}
	if err := source.reservation.Finish(ctx); err != nil {
		return fail(err)
	}
	handedOff = true
	if err = completeCodexHostTransfer(source.journalPath); err != nil {
		return fail(err)
	}
	if prompt := strings.Join(source.request.prompt, " "); prompt != "" {
		snapshot, err := owned.Handle.Observe(binding)
		if err != nil {
			return fail(err)
		}
		if err = owned.Handle.Turn(ctx, processhost.Authority{Binding: binding, Connection: snapshot.Connection, Session: snapshot.Session}, binding.Operation+"-transfer", prompt); err != nil {
			return fail(err)
		}
	}
	var sync processRelaunchSynchronization
	sync.changed, sync.controls, sync.attention, err = owned.resumeSynchronization(creator)
	if err != nil {
		return fail(err)
	}
	result.NewPaneUID, result.Outcome = binding.Pane, personaOutcomeRestarted
	return c.finishOwnedHostTransfer(ctx, cancel, &owned, sync, result, source.request, stdout, stderr, fail)
}
