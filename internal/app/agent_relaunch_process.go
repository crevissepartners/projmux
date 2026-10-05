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
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	processRelaunchRetirementTimeout = 10 * time.Second
	processRelaunchRetirementPoll    = 20 * time.Millisecond
)

type processRelaunchRecipe struct {
	restart     *agentRestart
	guidance    agentGuidanceLaunch
	links       projectLinksLaunch
	annotations map[string]string
	workspace   coremetadata.AgentWorkspace
}

type processRelaunchLaunch struct {
	settings agentSettingsLaunch
	command  processhost.Command
	config   processhost.CodexConfig
	prompt   string
}

type processRelaunchSynchronization struct {
	changed   func(processhost.Snapshot) error
	controls  func(context.Context) error
	attention func() error
}

// Same-location process relaunch validates the recipe before retiring the old
// writer, then starts a new foreground generation through the resume engine.
func (c *agentCommand) runProcessRelaunch(reg coremetadata.Registry, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("agent relaunch: agent/%s %s (%s); nothing was changed", target.Metadata.Name, detail, reason))
	}
	if err := c.validateProcessRelaunch(reg, target, pane, request, refuse); err != nil {
		return err
	}
	recipe, err := c.planProcessRelaunchRecipe(reg, target, pane, request, refuse)
	if err != nil {
		return err
	}
	result := recipe.result(target, pane, request)
	deferred := recipe.restart.provider == aiModeClaude && strings.TrimSpace(strings.Join(request.prompt, " ")) == ""
	changesLayers := recipe.restart.settings.resolution.ProfileSwitched || request.instructions != nil || len(request.reset) > 0
	if !deferred && recipe.restart.running && request.model == "" && len(recipe.restart.settings.resolution.Reasons) == 0 && !(changesLayers && recipe.restart.settings.resolution.LayersChanged()) && len(request.prompt) == 0 {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = pane.Metadata.UID
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if request.dryRun {
		result.Outcome = personaOutcomeWouldResume
		if recipe.restart.running {
			result.Outcome = personaOutcomeWouldRestart
		}
		return writeAgentRelaunchResult(stdout, request, result)
	}
	launch, err := c.planProcessRelaunchLaunch(target, pane, request, recipe)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	candidate, err := c.stopProcessRelaunch(ctx, reg, target, pane, request, recipe.restart)
	if err != nil {
		return err
	}
	if deferred {
		return c.startDeferredRelaunch(ctx, cancel, candidate, request, recipe, launch, result, stdout, stderr)
	}
	return c.startProcessRelaunch(ctx, cancel, reg, candidate, request, recipe, launch, result, stdout, stderr)
}

func (c *agentCommand) validateProcessRelaunch(reg coremetadata.Registry, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, refuse func(string, string) error) error {
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if c.rebind == nil || c.rebind.create == nil {
		return refuse(relaunchReasonNoConversation, "has no configured process resume owner")
	}
	if request.socket.socket != "" || request.socket.socketPath != "" {
		return usageError("agent relaunch: process agents do not use a tmux socket; nothing was changed")
	}
	if err := requireLaunchOptions(agentRelaunchSpelling, provider, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if coremetadata.RecordsDialogueReplyOnly(target.Metadata.Annotations) {
		return refuse(replyOnlyReasonLaunchFixed, "has a fixed reply-only launch; process relaunch is unsupported")
	}
	if pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Provider != provider || processRelaunchConversation(pane.Status.ProcessSession) == "" {
		return refuse(relaunchReasonNoConversation, "has no recorded process conversation")
	}
	running := target.Status.Phase == coremetadata.PhaseRunning
	if !running && target.Status.Phase != coremetadata.PhaseOffline {
		return refuse(relaunchReasonNoConversation, "is not Running or Offline with a retired process conversation")
	}
	if running {
		activation, actualProvider, current := reg.CurrentProcessActivation(pane.Status.ProcessSession.Binding)
		if !current || actualProvider != provider {
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
	return nil
}

func (c *agentCommand) planProcessRelaunchRecipe(reg coremetadata.Registry, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, refuse func(string, string) error) (processRelaunchRecipe, error) {
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	window, ok := reg.Window(target.Metadata.OwnerUID())
	if !ok {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, "has no owning Window")
	}
	project, ok := reg.Project(window.Metadata.OwnerUID())
	if !ok {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, "has no owning Project")
	}
	restart := c.newAgentRestart(agentRelaunchSpelling, reg, target, provider, relaunchTokens, refuse)
	restart.running, restart.paneUID = target.Status.Phase == coremetadata.PhaseRunning, pane.Metadata.UID
	ai, ok := c.ai.(*aiCommand)
	if !ok {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, "process launcher is unavailable")
	}
	guidance := ai.PlanProcessAgentGuidance()
	if request.host == "tmux" {
		guidance = planAgentGuidanceWith(c.rebind.launcher, provider, target.Metadata.Annotations)
	}
	guidance.recorded = target.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]
	links := planProjectLinksWith(c.rebind.launcher, provider, *project, target.Metadata.Annotations)
	settingsRequest := request.settings().withPromptParts(guidance, links)
	var err error
	restart.settings, err = c.rebind.resolveSettings(provider, target.Metadata.Annotations, settingsRequest)
	if err != nil {
		var profileErr *relaunchProfileError
		if errors.As(err, &profileErr) {
			return processRelaunchRecipe{}, refuse(profileErr.reason, profileErr.Error())
		}
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, err.Error())
	}
	if err = restart.refuseLayerChange(settingsRequest); err != nil {
		return processRelaunchRecipe{}, err
	}
	if provider == aiModeCodex && restart.settings.resolution.ProfileSwitched {
		if err = c.refuseCodexPermissionsKept(restart); err != nil {
			return processRelaunchRecipe{}, err
		}
	}
	if restart.settings.instructionsErr != nil {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, restart.settings.instructionsErr.Error())
	}
	if err = ai.RequireAgentEnabled(provider); err != nil {
		return processRelaunchRecipe{}, err
	}
	// Validate the profile and workspace before Stop, without writing a snapshot.
	resolvedRegistry := reg.Clone()
	if err = restart.settings.record(&resolvedRegistry, c.rebind.create.store.mutator(), target.Metadata.UID); err != nil {
		return processRelaunchRecipe{}, err
	}
	resolvedTarget, _ := resolvedRegistry.Agent(target.Metadata.UID)
	annotations := guidance.resumeLaunchAnnotations(links.resumeLaunchAnnotations(restart.settings.launchAnnotations(resolvedTarget.Metadata.Annotations)))
	if provider == aiModeCodex {
		_, _, err = c.rebind.create.codexResumeProfile(annotations)
	} else {
		_, _, _, _, err = ai.resumeProfileSettings(provider, annotations)
	}
	if err != nil {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, err.Error())
	}
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspaceFor
	}
	workspace, err := resolver(agentRelaunchSpelling, reg, *project, provider, target.Spec.Workspace.CWD, target.Spec.Workspace.AdditionalWritableRoots)
	if err != nil {
		return processRelaunchRecipe{}, err
	}
	if ai.findAgentBinary(provider) == "" {
		return processRelaunchRecipe{}, refuse(relaunchReasonNoConversation, ai.missingAgentRunnerMessage(provider))
	}
	return processRelaunchRecipe{restart: restart, guidance: guidance, links: links, annotations: annotations, workspace: workspace}, nil
}

func (r processRelaunchRecipe) result(target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest) agentRelaunchResult {
	settings := r.restart.settings.resolution
	return agentRelaunchResult{CurrentHost: "process", TargetHost: "process", Action: "relaunch", DryRun: request.dryRun, AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: r.restart.provider, Phase: target.Status.Phase, Interaction: r.restart.interaction, PaneUID: pane.Metadata.UID, CurrentEffort: target.Metadata.Annotations[coremetadata.AnnotationAgentEffort], CurrentModel: target.Metadata.Annotations[coremetadata.AnnotationAgentModel], NewEffort: request.effort, NewModel: request.model, Restart: r.restart.running, ConfirmationRequired: r.restart.confirmationRequired(), CurrentSettings: settings.Current, NewSettings: settings.New, RelaunchReasons: append([]string{}, settings.Reasons...)}
}

func (c *agentCommand) planProcessRelaunchLaunch(target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, r processRelaunchRecipe) (processRelaunchLaunch, error) {
	if r.restart.confirmationRequired() && !request.yes {
		return processRelaunchLaunch{}, r.restart.refuse(relaunchReasonAgentBusy, fmt.Sprintf("is %s with interaction %s; restarting would cut that turn; re-run with --yes", target.Status.Phase, r.restart.interaction))
	}
	prompt := strings.Join(request.prompt, " ")
	// Resolve all provider arguments before retiring the writer. Resume's planner
	// reads the resolved annotation recipe; relaunch retains its own layer sources.
	launchSettings := r.restart.settings.writeSnapshot()
	if launchSettings.snapshotErr != nil {
		return processRelaunchLaunch{}, launchSettings.snapshotErr
	}
	planned := processResumeCandidate{Agent: target.Clone(), Pane: pane.Clone(), Record: *pane.Status.ProcessSession.Clone()}
	planned.Agent.Metadata.Annotations = r.annotations
	planned.Agent.Spec.Workspace = r.workspace
	plan, config, _, err := c.planProcessResume(planned, processAgentResumeOptions{})
	if err != nil {
		return processRelaunchLaunch{}, err
	}
	return processRelaunchLaunch{settings: launchSettings, command: plan, config: config, prompt: prompt}, nil
}

func (c *agentCommand) stopProcessRelaunch(ctx context.Context, reg coremetadata.Registry, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, restart *agentRestart) (processResumeCandidate, error) {
	provider := pane.Status.ProcessSession.Provider
	if restart.running {
		// Re-read recipe, ownership and interaction immediately before admission.
		latest, e := c.loadRegistry()
		if e != nil {
			return processResumeCandidate{}, e
		}
		current, found := latest.Agent(target.Metadata.UID)
		currentPane, present := latest.Pane(pane.Metadata.UID)
		if !found || !present || !reflect.DeepEqual(current.Spec, target.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, target.Metadata.Annotations) || !reflect.DeepEqual(currentPane.Status.Activation, pane.Status.Activation) {
			return processResumeCandidate{}, restart.refuse(relaunchReasonNoConversation, "changed since planning; retry the command")
		}
		if !request.yes {
			restart.interaction = current.EffectiveInteraction(c.clock()).Kind
			if restart.confirmationRequired() {
				return processResumeCandidate{}, restart.refuse(relaunchReasonAgentBusy, "started work since planning; re-run with --yes")
			}
		}
		_, err := c.callProcessTurn(latest, *current, provider, "stop", "")
		if err != nil {
			return processResumeCandidate{}, fmt.Errorf("agent relaunch: old host Stop failed; previous settings preserved; recover with: %s: %w", processRelaunchRecovery(reg, target, request), err)
		}
	}
	candidate, err := c.awaitProcessRelaunchRetirement(ctx, target, pane, restart.running)
	if err != nil {
		return processResumeCandidate{}, fmt.Errorf("agent relaunch: old host retirement is unconfirmed; no new child started; recover with: %s: %w", processRelaunchRecovery(reg, target, request), err)
	}
	if !reflect.DeepEqual(candidate.Agent.Spec, target.Spec) || !reflect.DeepEqual(candidate.Agent.Metadata.Annotations, target.Metadata.Annotations) {
		return processResumeCandidate{}, fmt.Errorf("agent relaunch: launch recipe changed during Stop; no new child started; recover with: %s", processRelaunchRecovery(reg, target, request))
	}
	return candidate, nil
}

func (c *agentCommand) startProcessRelaunch(ctx context.Context, cancel context.CancelFunc, reg coremetadata.Registry, candidate processResumeCandidate, request agentRelaunchRequest, recipe processRelaunchRecipe, launch processRelaunchLaunch, result agentRelaunchResult, stdout, stderr io.Writer) error {
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
	if err = c.reserveProcessRelaunch(ctx, candidate, launch.settings, recipe.guidance, recipe.links, binding); err != nil {
		return err
	}
	owned := c.processResumeResult(candidate, binding, intmetadata.PathFor(state))
	fail := func(cause error) error {
		diagnostic := codexappserver.Diagnostic(cause)
		if recipe.restart.provider == aiModeCodex && diagnostic.Method == "thread/resume" && diagnostic.RPCCode != nil && *diagnostic.RPCCode == -32600 {
			// The sanitized protocol diagnostic retains the code, not the
			// provider's arbitrary message. Describe the possible writer conflict
			// without claiming every -32600 is proof of an active writer.
			cause = fmt.Errorf("thread/resume refused (-32600); an active writer may still own the recorded thread: %w", cause)
		}
		cause = errors.Join(cause, c.retireFailedProcessRelaunch(&owned))
		cause = errors.Join(cause, c.restoreProcessRelaunchSettings(candidate, binding, launch.settings, recipe.guidance, recipe.links))
		return fmt.Errorf("agent relaunch: failed to apply the launch configuration; recorded conversation retained; %s (foreground owner remains required): %w", c.processRelaunchFailureRecovery(reg, candidate.Agent, request), cause)
	}
	if err = owned.startProcessResume(ctx, creator, launch.command, launch.config, processRelaunchFirstFrame(launch.prompt)); err != nil {
		return fail(err)
	}
	var sync processRelaunchSynchronization
	sync.changed, sync.controls, sync.attention, err = owned.resumeSynchronization(creator)
	if err != nil {
		return fail(err)
	}
	result.Outcome = personaOutcomeResumed
	if recipe.restart.running {
		result.Outcome = personaOutcomeRestarted
	}
	result.NewPaneUID = binding.Pane
	if err = writeAgentRelaunchResult(stdout, request, result); err != nil {
		return fail(err)
	}
	if _, err = fmt.Fprintf(stderr, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", binding.Agent, binding.Pane); err != nil {
		return fail(err)
	}
	return runProcessRelaunchOwner(ctx, cancel, &owned, sync)
}

func runProcessRelaunchOwner(ctx context.Context, cancel context.CancelFunc, owned *processAgentResumeResult, sync processRelaunchSynchronization, stdinWatched ...bool) error {
	if len(stdinWatched) == 0 || !stdinWatched[0] {
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	}
	snapshot, waitErr := owned.owner.waitProcessAgent(ctx, processSnapshotSynchronizer(sync.changed, func(snapshot processhost.Snapshot) error {
		if len(snapshot.Pending) > 0 {
			return sync.controls(context.WithoutCancel(ctx))
		}
		return nil
	}))
	controlErr := sync.controls(context.Background())
	if errors.Is(controlErr, processhost.ErrClosed) || errors.Is(controlErr, processhost.ErrStale) {
		controlErr = nil
	}
	if err := errors.Join(waitErr, controlErr, sync.attention()); err != nil {
		if owned.owner.waitRecorded && (errors.Is(err, processhost.ErrClosed) || errors.Is(err, processhost.ErrStale)) {
			return fmt.Errorf("agent relaunch: generation %s is retired; this caller no longer owns agent uid:%s; inspect with: projmux describe agent uid:%s: %w", owned.Binding.Generation, owned.Binding.Agent, owned.Binding.Agent, err)
		}
		return err
	}
	return processWaitExit(snapshot)
}
func processRelaunchRecovery(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest) string {
	command := relaunchRerunCommand(reg, target, request)
	if len(request.prompt) > 0 {
		command += " -- " + personaCommandWord(strings.Join(request.prompt, " "))
	} else if target.Spec.Provider == aiModeClaude && request.host != "tmux" {
		command += " -- <prompt>"
	}
	return command
}

// A failed retirement may leave Pending ownership. Do not present relaunch as
// immediately executable in that state, or loosen resume's ownership fence.
func (c *agentCommand) processRelaunchFailureRecovery(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest) string {
	command := processRelaunchRecovery(reg, target, request)
	latest, err := c.loadRegistry()
	if err == nil {
		current, found := latest.Agent(target.Metadata.UID)
		if found && current.Status.Phase == coremetadata.PhaseOffline {
			return "recover with: " + command
		}
	}
	return fmt.Sprintf("inspect with: projmux describe agent uid:%s; relaunch remains refused until the owned child has an exact Wait and the Agent is Offline; only then recover with: %s", target.Metadata.UID, command)
}

// Only the owner's exact durable supervisor Wait and process resume candidate
// can authorize the next writer. Host disappearance alone never does.
func (c *agentCommand) awaitProcessRelaunchRetirement(ctx context.Context, target coremetadata.Agent, pane coremetadata.Pane, running bool) (processResumeCandidate, error) {
	bounded, cancel := context.WithTimeout(ctx, processRelaunchRetirementTimeout)
	defer cancel()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	request := processAgentResumeRequest{options: processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: target.Metadata.UID}}}
	for {
		candidate, err := c.processResumeCandidate(request)
		if err == nil {
			if candidate.Record.Binding != pane.Status.ProcessSession.Binding || processRelaunchConversation(&candidate.Record) != processRelaunchConversation(pane.Status.ProcessSession) {
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
func (c *agentCommand) reserveProcessRelaunch(ctx context.Context, candidate processResumeCandidate, settings agentSettingsLaunch, guidance agentGuidanceLaunch, links projectLinksLaunch, binding processhost.Binding) error {
	unlock, lockErr := lockDeferredClaim(c.deferredClaimPath(binding.Agent))
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if err := c.checkDeferredClaim(binding.Agent, nil); err != nil {
		return err
	}
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

func processRelaunchConversation(record *coremetadata.ProcessSessionRecord) string {
	if record == nil {
		return ""
	}
	if record.Provider == aiModeCodex {
		return record.ThreadID
	}
	return record.SessionID
}

func processRelaunchFirstFrame(prompt string) processResumeFirstFrame {
	if strings.TrimSpace(prompt) == "" {
		return processResumeFirstFrame{}
	}
	return processResumeFirstFrame{Kind: "user", Text: prompt}
}
