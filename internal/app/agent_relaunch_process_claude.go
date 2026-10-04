package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Same-location Claude relaunch uses the recorded process conversation and the
// resume owner's lifetime. It never opens a tmux transport or creates a session.
func (c *agentCommand) runProcessClaudeRelaunch(reg coremetadata.Registry, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("agent relaunch: agent/%s %s (%s); nothing was changed", target.Metadata.Name, detail, reason))
	}
	if c.rebind == nil || c.rebind.create == nil {
		return refuse(relaunchReasonNoConversation, "has no configured process resume owner")
	}
	if request.socket.socket != "" || request.socket.socketPath != "" {
		return usageError("agent relaunch: process agents do not use a tmux socket; nothing was changed")
	}
	if err := requireLaunchOptions(agentRelaunchSpelling, aiModeClaude, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if coremetadata.RecordsDialogueReplyOnly(target.Metadata.Annotations) {
		return refuse("reply-only-launch-fixed", "has a fixed reply-only launch; process relaunch is unsupported")
	}
	if pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Provider != aiModeClaude || pane.Status.ProcessSession.SessionID == "" {
		return refuse(relaunchReasonNoConversation, "has no recorded process conversation")
	}
	running := target.Status.Phase == coremetadata.PhaseRunning
	if !running && target.Status.Phase != coremetadata.PhaseOffline {
		return refuse(relaunchReasonNoConversation, "is not Running or Offline with a retired process conversation")
	}
	if running {
		activation, provider, current := reg.CurrentProcessActivation(pane.Status.ProcessSession.Binding)
		if !current || provider != aiModeClaude {
			return refuse(relaunchReasonNoConversation, "has no current process ownership")
		}
		ancestors := c.processAncestors
		if ancestors == nil {
			ancestors = processAncestry
		}
		chain, err := ancestors()
		if err != nil {
			return refuse(relaunchReasonSelfTarget, "caller ancestry could not be verified; run from an external terminal")
		}
		if slices.Contains(chain, activation.Child.PID) || slices.Contains(chain, activation.HostProcess.PID) {
			return refuse(relaunchReasonSelfTarget, "owns this caller's lifetime; run from an external terminal")
		}
	} else if _, err := c.processResumeCandidate(processAgentResumeRequest{options: processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: target.Metadata.UID}}}); err != nil {
		return refuse(relaunchReasonNoConversation, err.Error())
	}
	window, ok := reg.Window(target.Metadata.OwnerUID())
	if !ok {
		return refuse(relaunchReasonNoConversation, "has no owning Window")
	}
	project, ok := reg.Project(window.Metadata.OwnerUID())
	if !ok {
		return refuse(relaunchReasonNoConversation, "has no owning Project")
	}
	restart := c.newAgentRestart(agentRelaunchSpelling, reg, target, aiModeClaude, relaunchTokens, refuse)
	restart.running, restart.paneUID = running, pane.Metadata.UID
	ai, ok := c.ai.(*aiCommand)
	if !ok {
		return refuse(relaunchReasonNoConversation, "process launcher is unavailable")
	}
	guidance := ai.PlanProcessAgentGuidance()
	guidance.recorded = target.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]
	links := planProjectLinksWith(c.rebind.launcher, aiModeClaude, *project, target.Metadata.Annotations)
	settingsRequest := request.settings().withPromptParts(guidance, links)
	var err error
	restart.settings, err = c.rebind.resolveSettings(aiModeClaude, target.Metadata.Annotations, settingsRequest)
	if err != nil {
		var profileErr *relaunchProfileError
		if errors.As(err, &profileErr) {
			return refuse(profileErr.reason, profileErr.Error())
		}
		return refuse(relaunchReasonNoConversation, err.Error())
	}
	if err = restart.refuseLayerChange(settingsRequest); err != nil {
		return err
	}
	if restart.settings.instructionsErr != nil {
		return refuse(relaunchReasonNoConversation, restart.settings.instructionsErr.Error())
	}
	if err = ai.RequireAgentEnabled(aiModeClaude); err != nil {
		return err
	}
	// Validate the profile and workspace before Stop, without writing a snapshot.
	resolvedRegistry := reg.Clone()
	if err = restart.settings.record(&resolvedRegistry, c.rebind.create.store.mutator(), target.Metadata.UID); err != nil {
		return err
	}
	resolvedTarget, _ := resolvedRegistry.Agent(target.Metadata.UID)
	annotations := guidance.resumeLaunchAnnotations(links.resumeLaunchAnnotations(restart.settings.launchAnnotations(resolvedTarget.Metadata.Annotations)))
	if _, _, _, _, err = ai.resumeProfileSettings(aiModeClaude, annotations); err != nil {
		return refuse(relaunchReasonNoConversation, err.Error())
	}
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspaceFor
	}
	workspace, err := resolver(agentRelaunchSpelling, reg, *project, aiModeClaude, target.Spec.Workspace.CWD, target.Spec.Workspace.AdditionalWritableRoots)
	if err != nil {
		return err
	}
	if ai.findAgentBinary(aiModeClaude) == "" {
		return refuse(relaunchReasonNoConversation, ai.missingAgentRunnerMessage(aiModeClaude))
	}
	settings := restart.settings.resolution
	result := agentRelaunchResult{Action: "relaunch", DryRun: request.dryRun, AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: aiModeClaude, Phase: target.Status.Phase, Interaction: restart.interaction, PaneUID: pane.Metadata.UID, CurrentEffort: target.Metadata.Annotations[coremetadata.AnnotationAgentEffort], CurrentModel: target.Metadata.Annotations[coremetadata.AnnotationAgentModel], NewEffort: request.effort, NewModel: request.model, Restart: running, ConfirmationRequired: restart.confirmationRequired(), CurrentSettings: settings.Current, NewSettings: settings.New, RelaunchReasons: append([]string{}, settings.Reasons...)}
	changesLayers := settings.ProfileSwitched || request.instructions != nil || len(request.reset) > 0
	if running && request.model == "" && len(settings.Reasons) == 0 && !(changesLayers && settings.LayersChanged()) && len(request.prompt) == 0 {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = pane.Metadata.UID
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if request.dryRun {
		result.Outcome = personaOutcomeWouldResume
		if running {
			result.Outcome = personaOutcomeWouldRestart
		}
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if restart.confirmationRequired() && !request.yes {
		return refuse(relaunchReasonAgentBusy, fmt.Sprintf("is %s with interaction %s; restarting would cut that turn; re-run with --yes", target.Status.Phase, restart.interaction))
	}
	prompt := strings.Join(request.prompt, " ")
	if strings.TrimSpace(prompt) == "" {
		return usageError("agent relaunch: process Claude requires -- <prompt>; nothing was changed")
	}
	// Resolve all provider arguments before retiring the writer. Resume's planner
	// reads the resolved annotation recipe; relaunch retains its own layer sources.
	launchSettings := restart.settings.writeSnapshot()
	if launchSettings.snapshotErr != nil {
		return launchSettings.snapshotErr
	}
	planned := processResumeCandidate{Agent: target.Clone(), Pane: pane.Clone(), Record: *pane.Status.ProcessSession.Clone()}
	planned.Agent.Metadata.Annotations = annotations
	planned.Agent.Spec.Workspace = workspace
	plan, config, _, err := c.planProcessResume(planned, processAgentResumeOptions{})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if running {
		// Re-read recipe, ownership and interaction immediately before admission.
		latest, e := c.loadRegistry()
		if e != nil {
			return e
		}
		current, found := latest.Agent(target.Metadata.UID)
		currentPane, present := latest.Pane(pane.Metadata.UID)
		if !found || !present || !reflect.DeepEqual(current.Spec, target.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, target.Metadata.Annotations) || !reflect.DeepEqual(currentPane.Status.Activation, pane.Status.Activation) {
			return refuse(relaunchReasonNoConversation, "changed since planning; retry the command")
		}
		if !request.yes {
			restart.interaction = current.EffectiveInteraction(c.clock()).Kind
			if restart.confirmationRequired() {
				return refuse(relaunchReasonAgentBusy, "started work since planning; re-run with --yes")
			}
		}
		_, err = c.callProcessClaudeTurn(latest, *current, "stop", "")
		if err != nil {
			return fmt.Errorf("agent relaunch: old host Stop failed; previous settings preserved; recover with: %s: %w", processRelaunchRecovery(reg, target, request), err)
		}
	}
	candidate, err := c.awaitProcessRelaunchRetirement(ctx, target, pane, running)
	if err != nil {
		return fmt.Errorf("agent relaunch: old host retirement is unconfirmed; no new child started; recover with: %s: %w", processRelaunchRecovery(reg, target, request), err)
	}
	if !reflect.DeepEqual(candidate.Agent.Spec, target.Spec) || !reflect.DeepEqual(candidate.Agent.Metadata.Annotations, target.Metadata.Annotations) {
		return fmt.Errorf("agent relaunch: launch recipe changed during Stop; no new child started; recover with: %s", processRelaunchRecovery(reg, target, request))
	}
	creator := c.rebind.create
	operation, err := newCreateOperationID()
	if err != nil {
		return err
	}
	generation, err := creator.mintGeneration()
	if err != nil {
		return err
	}
	binding := processSchemaBinding(candidate.Record.Binding)
	binding.Host, binding.Generation, binding.Operation = operation, generation, operation
	state, err := creator.store.stateDir()
	if err != nil {
		return err
	}
	if err = c.reserveProcessClaudeRelaunch(ctx, candidate, launchSettings, guidance, links, binding); err != nil {
		return err
	}
	owned := c.processResumeResult(candidate, binding, intmetadata.PathFor(state))
	fail := func(cause error) error {
		cause = errors.Join(cause, c.retireFailedProcessRelaunch(&owned))
		cause = errors.Join(cause, c.restoreProcessRelaunchSettings(candidate, binding, launchSettings, guidance, links))
		return fmt.Errorf("agent relaunch: failed to apply the launch configuration; recorded conversation retained; recover with: %s (foreground owner remains required): %w", processRelaunchRecovery(reg, target, request), cause)
	}
	if err = owned.startProcessResume(ctx, creator, plan, config, processResumeFirstFrame{Kind: "user", Text: prompt}); err != nil {
		return fail(err)
	}
	syncChanged, syncControls, syncAttention, err := owned.resumeSynchronization(creator)
	if err != nil {
		return fail(err)
	}
	result.Outcome = personaOutcomeResumed
	if running {
		result.Outcome = personaOutcomeRestarted
	}
	result.NewPaneUID = binding.Pane
	if err = writeAgentRelaunchResult(stdout, request, result); err != nil {
		return fail(err)
	}
	if _, err = fmt.Fprintf(stderr, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", binding.Agent, binding.Pane); err != nil {
		return fail(err)
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	snapshot, waitErr := owned.owner.waitProcessAgent(ctx, processSnapshotSynchronizer(syncChanged, func(snapshot processhost.Snapshot) error {
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
		return err
	}
	return processWaitExit(snapshot)
}

func processRelaunchRecovery(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest) string {
	command := relaunchRerunCommand(reg, target, request)
	if len(request.prompt) > 0 {
		command += " -- " + personaCommandWord(strings.Join(request.prompt, " "))
	} else {
		command += " -- <prompt>"
	}
	return command
}

// Only the owner's exact durable supervisor Wait and process resume candidate
// can authorize the next writer. Host disappearance alone never does.
func (c *agentCommand) awaitProcessRelaunchRetirement(ctx context.Context, target coremetadata.Agent, pane coremetadata.Pane, running bool) (processResumeCandidate, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	request := processAgentResumeRequest{options: processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: target.Metadata.UID}}}
	for {
		candidate, err := c.processResumeCandidate(request)
		if err == nil {
			if candidate.Record.Binding != pane.Status.ProcessSession.Binding || candidate.Record.SessionID != pane.Status.ProcessSession.SessionID {
				return processResumeCandidate{}, processhost.ErrStale
			}
			if !coremetadata.MatchesProcessWait(candidate.Record.Binding, candidate.Pane.Status.LastTermination) {
				return processResumeCandidate{}, errors.New("exact old child Wait is unavailable")
			}
			return candidate, nil
		}
		if !running {
			return processResumeCandidate{}, err
		}
		select {
		case <-bounded.Done():
			return processResumeCandidate{}, errors.Join(err, bounded.Err())
		case <-ticker.C:
		}
	}
}

func (c *agentCommand) restoreProcessRelaunchSettings(previous processResumeCandidate, binding processhost.Binding, settings agentSettingsLaunch, guidance agentGuidanceLaunch, links projectLinksLaunch) error {
	_, err := c.rebind.create.store.update(func(reg *coremetadata.Registry) error {
		pane, ok := reg.Pane(binding.Pane)
		agent, found := reg.Agent(binding.Agent)
		if !ok || !found || pane.Status.ProcessSession == nil || agent.Status.Phase != coremetadata.PhaseOffline {
			return errors.New("cannot restore previous launch recipe before exact retirement")
		}
		if pane.Status.ProcessSession.Binding != metadataProcessBinding(binding) && pane.Status.ProcessSession.Binding != previous.Record.Binding {
			return processhost.ErrStale
		}
		expectedRegistry := reg.Clone()
		expectedAgent, _ := expectedRegistry.Agent(binding.Agent)
		expectedAgent.Metadata.Annotations = maps.Clone(previous.Agent.Metadata.Annotations)
		if err := settings.record(&expectedRegistry, c.rebind.create.store.mutator(), binding.Agent); err != nil {
			return err
		}
		if err := guidance.record(&expectedRegistry, c.rebind.create.store.mutator(), binding.Agent); err != nil {
			return err
		}
		if err := links.record(&expectedRegistry, c.rebind.create.store.mutator(), binding.Agent); err != nil {
			return err
		}
		expectedAgent, _ = expectedRegistry.Agent(binding.Agent)
		expected := expectedAgent.Metadata.Annotations
		if !reflect.DeepEqual(agent.Metadata.Annotations, expected) {
			return errors.New("launch annotations changed; previous recipe restoration refused")
		}
		agent.Metadata.Annotations = maps.Clone(previous.Agent.Metadata.Annotations)
		return nil
	})
	return err
}

// Reservation commits the recipe and prompt digests together with the fresh
// generation, after revalidating the retired record and unchanged old recipe.
func (c *agentCommand) reserveProcessClaudeRelaunch(ctx context.Context, candidate processResumeCandidate, settings agentSettingsLaunch, guidance agentGuidanceLaunch, links projectLinksLaunch, binding processhost.Binding) error {
	_, err := c.rebind.create.store.update(func(reg *coremetadata.Registry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := processResumeRefusal(*reg, binding.Agent, false); err != nil {
			return err
		}
		pane, _ := processResumePane(*reg, binding.Agent)
		agent, _ := reg.Agent(binding.Agent)
		if !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) || !reflect.DeepEqual(agent.Spec, candidate.Agent.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, candidate.Agent.Metadata.Annotations) {
			return processhost.ErrStale
		}
		mutator := c.rebind.create.store.mutator()
		if err := mutator.ReserveProcessResume(reg, candidate.Record.Binding, metadataProcessBinding(binding)); err != nil {
			return err
		}
		if err := settings.record(reg, mutator, binding.Agent); err != nil {
			return err
		}
		if err := guidance.record(reg, mutator, binding.Agent); err != nil {
			return err
		}
		return links.record(reg, mutator, binding.Agent)
	})
	return err
}

// A provider can exit before its birth identity is published. Once exact Wait
// proves that owned child is gone, release the still-unpublished reservation;
// never infer that from a dead PID or from a startup error alone.
func (c *agentCommand) retireFailedProcessRelaunch(result *processAgentResumeResult) error {
	if result.hasNoChild() {
		return result.restoreReservation()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := result.owner.waitProcessAgent(ctx, nil)
	if snapshot.Binding != result.Binding || snapshot.Exit == nil {
		return err
	}
	reg, readErr := c.loadRegistry()
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	pane, ok := reg.Pane(result.Binding.Pane)
	agent, found := reg.Agent(result.Binding.Agent)
	if ok && found && agent.Status.Phase == coremetadata.PhasePending && pane.Status.Activation.IsZero() {
		// No activation was committed and the actual child has been reaped. The
		// existing reservation writer still fences the complete durable recipe.
		return result.restoreReservation()
	}
	return err
}
