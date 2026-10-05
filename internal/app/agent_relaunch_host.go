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
	"strings"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func relaunchCurrentHost(reg coremetadata.Registry, agent coremetadata.Agent) string {
	if pane, ambiguous := processResumePane(reg, agent.Metadata.UID); pane != nil && !ambiguous {
		return "process"
	}
	return "tmux"
}

func (c *agentCommand) runHostRelaunch(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("agent relaunch: agent/%s %s (%s); nothing was changed", target.Metadata.Name, detail, reason))
	}
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if provider != aiModeClaude {
		return refuse(relaunchReasonProviderUnsupported, "execution host changes currently require Claude")
	}
	if coremetadata.RecordsDialogueReplyOnly(target.Metadata.Annotations) {
		return refuse(replyOnlyReasonLaunchFixed, "has a fixed reply-only launch; execution host changes are unsupported")
	}
	if request.host == "process" && strings.TrimSpace(strings.Join(request.prompt, " ")) == "" {
		return usageError("agent relaunch: moving Claude to process requires -- <prompt>; nothing was changed")
	}
	if request.host == "tmux" && len(request.prompt) != 0 {
		return usageError("agent relaunch: a first prompt applies only to the process target; nothing was changed")
	}
	if err := requireLaunchOptions(agentRelaunchSpelling, provider, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if c.rebind == nil || c.rebind.create == nil {
		return refuse(relaunchReasonNoConversation, "has no configured relaunch owner")
	}
	if request.host == "tmux" {
		return c.moveProcessClaudeToTmux(reg, target, request, refuse, stdout, stderr)
	}
	return c.moveTmuxClaudeToProcess(reg, target, request, refuse, stdout, stderr)
}

func hostRelaunchResult(recipe processRelaunchRecipe, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest, current string) agentRelaunchResult {
	result := recipe.result(target, pane, request)
	result.CurrentHost, result.TargetHost = current, request.host
	result.RelaunchReasons = append(result.RelaunchReasons, "host-changed")
	result.Outcome = personaOutcomeWouldResume
	if recipe.restart.running {
		result.Outcome = personaOutcomeWouldRestart
	}
	return result
}

// Process evidence is retained until the successful tmux rebind transaction.
// Its exact Wait and old recipe are checked again inside that transaction.
func retireProcessPaneForTmux(reg *coremetadata.Registry, mut coremetadata.Mutator, old processResumeCandidate) error {
	pane, present := reg.Pane(old.Pane.Metadata.UID)
	agent, found := reg.Agent(old.Agent.Metadata.UID)
	if !present || !found || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != old.Pane.Metadata.UID ||
		!reflect.DeepEqual(agent.Spec, old.Agent.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, old.Agent.Metadata.Annotations) ||
		!agent.Status.SessionRef.SameConversation(old.Agent.Status.SessionRef) || !processResumeRecordEqual(pane.Status.ProcessSession, &old.Record) ||
		!coremetadata.MatchesProcessWait(old.Record.Binding, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return errors.New("agent relaunch: retired process source changed before tmux rebind")
	}
	return mut.DeletePane(reg, pane.Metadata.UID)
}

func (c *agentCommand) moveProcessClaudeToTmux(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, refuse func(string, string) error, stdout, stderr io.Writer) error {
	pane, ambiguous := processResumePane(reg, target.Metadata.UID)
	if pane == nil || ambiguous {
		return refuse(relaunchReasonNoConversation, "has no exact process source")
	}
	validation := request
	validation.socket = deleteSocketFlags{}
	if err := c.validateProcessRelaunch(reg, target, *pane, validation, refuse); err != nil {
		return err
	}
	recipe, err := c.planProcessRelaunchRecipe(reg, target, *pane, request, refuse)
	if err != nil {
		return err
	}
	// Predict only on a clone, retaining the real source and its history.
	predicted := reg.Clone()
	predictedAgent, _ := predicted.Agent(target.Metadata.UID)
	predictedAgent.Status.Phase = coremetadata.PhaseOffline
	if err := c.store.mutator().DeletePane(&predicted, pane.Metadata.UID); err != nil {
		return err
	}
	predictedAgent, _ = predicted.Agent(target.Metadata.UID)
	plan, err := c.prepareResume(agentRelaunchSpelling, predicted, predictedAgent)
	if err != nil {
		return refuse(relaunchReasonNoConversation, err.Error())
	}
	plan.modelOverride, plan.effortOverride, plan.overrideSource = request.model, request.effort, coremetadata.SettingSourceRelaunch
	plan.layerChanges = request.settings()
	result := hostRelaunchResult(recipe, target, *pane, request, "process")
	if request.dryRun {
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if recipe.restart.confirmationRequired() && !request.yes {
		return refuse(relaunchReasonAgentBusy, "would interrupt work; re-run with --yes")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	old, err := c.stopProcessRelaunch(ctx, reg, target, *pane, request, recipe.restart)
	if err != nil {
		return err
	}
	plan.retiredProcess = &old
	forward := stdout
	if request.json {
		forward = io.Discard
	}
	if err := c.rebind.rebind(agentRelaunchSpelling, plan, forward, stderr); err != nil {
		return fmt.Errorf("agent relaunch: tmux launch failed; previous process recipe and conversation retained; recover with: %s: %w", processRelaunchRecovery(reg, target, request), err)
	}
	after, err := c.loadRegistry()
	if err != nil {
		return err
	}
	newAgent, found := after.Agent(target.Metadata.UID)
	if !found {
		return errors.New("agent relaunch: resumed Agent is unavailable")
	}
	result.NewPaneUID, result.Outcome = newAgent.Status.PaneRef, personaOutcomeResumed
	if recipe.restart.running {
		result.Outcome = personaOutcomeRestarted
	}
	return writeAgentRelaunchResult(stdout, request, result)
}

type tmuxRelaunchSource struct {
	conversation processhost.TmuxConversationSource
	process      coremetadata.ProcessIdentity
	retired      coremetadata.Agent
	request      agentRelaunchRequest
}

func (c *agentCommand) moveTmuxClaudeToProcess(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, refuse func(string, string) error, stdout, stderr io.Writer) error {
	restart := c.newAgentRestart(agentRelaunchSpelling, reg, target, aiModeClaude, relaunchTokens, refuse)
	if err := restart.checkTarget(); err != nil {
		return err
	}
	if err := c.plan(restart, request.settings(), request.socket); err != nil {
		return err
	}
	pane, found := reg.Pane(restart.paneUID)
	if !found || pane.Status.Activation.Claude == nil || !pane.Status.Activation.Claude.Process.Valid() {
		return refuse(relaunchReasonNoConversation, "has no exact recorded tmux Claude process identity")
	}
	recipe, err := c.planProcessRelaunchRecipe(reg, target, *pane, request, refuse)
	if err != nil {
		return err
	}
	source := tmuxRelaunchSource{process: pane.Status.Activation.Claude.Process, request: request}
	source.conversation = processhost.TmuxConversationSource{Project: recipe.restart.registryWindowProject(), Window: target.Metadata.OwnerUID(), Agent: target.Metadata.UID, Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation, Operation: pane.Status.Activation.OperationID, RuntimeID: pane.Status.Activation.RuntimeID, Session: target.Status.SessionRef.ConversationID()}
	result := hostRelaunchResult(recipe, target, *pane, request, "tmux")
	if request.dryRun {
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if recipe.restart.confirmationRequired() && !request.yes {
		return refuse(relaunchReasonAgentBusy, "would interrupt work; re-run with --yes")
	}
	command, settings, err := c.planHostProcessClaudeLaunch(recipe)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = c.stopTmuxTransfer(ctx, target, *pane, request); err != nil {
		return err
	}
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	retired, found := latest.Agent(target.Metadata.UID)
	if !found {
		return errors.New("agent relaunch: retired source is unavailable")
	}
	if !reflect.DeepEqual(retired.Spec, target.Spec) || !reflect.DeepEqual(retired.Metadata.Annotations, target.Metadata.Annotations) || !retired.Status.SessionRef.SameConversation(target.Status.SessionRef) { return errors.New("agent relaunch: source recipe changed during Stop; no child started") }
	source.retired = retired.Clone()
	return c.startTmuxTransfer(ctx, cancel, reg, source, recipe, command, settings, result, stdout, stderr)
}

func (r *agentRestart) registryWindowProject() string {
	window, ok := r.registry.Window(r.target.Metadata.OwnerUID())
	if !ok {
		return ""
	}
	return window.Metadata.OwnerUID()
}

func (c *agentCommand) planHostProcessClaudeLaunch(recipe processRelaunchRecipe) (processhost.Command, agentSettingsLaunch, error) {
	settings := recipe.restart.settings.writeSnapshot()
	if settings.snapshotErr != nil {
		return processhost.Command{}, settings, settings.snapshotErr
	}
	ai := c.ai.(*aiCommand)
	annotations := settings.launchAnnotations(recipe.annotations)
	_, _, profile, _, err := ai.resumeProfileSettings(aiModeClaude, annotations)
	if err != nil {
		return processhost.Command{}, settings, err
	}
	persona, err := ai.resumePersonaSnapshot(aiModeClaude, annotations)
	if err != nil {
		return processhost.Command{}, settings, err
	}
	instructions, err := ai.resumeSystemPromptFile(aiModeClaude, annotations, persona)
	if err != nil {
		return processhost.Command{}, settings, err
	}
	instructions, err = ai.resumeGuidanceSystemPromptFile(aiModeClaude, annotations, instructions)
	if err != nil {
		return processhost.Command{}, settings, err
	}
	command, err := ai.PlanProcessClaudeCommand(recipe.workspace, processClaudeLaunchOptions{Model: settings.resolution.New.Model.Value, Effort: annotations[coremetadata.AnnotationAgentEffort], SettingsFile: profile, InstructionsFile: instructions})
	command.Args = append(claudeResumeSnapshotArgs(aiModeClaude, annotations), command.Args...)
	return command, settings, err
}

func (c *agentCommand) stopTmuxTransfer(ctx context.Context, target coremetadata.Agent, pane coremetadata.Pane, request agentRelaunchRequest) error {
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, found := latest.Agent(target.Metadata.UID)
	currentPane, present := latest.Pane(pane.Metadata.UID)
	if !found || !present || !reflect.DeepEqual(agent.Spec, target.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, target.Metadata.Annotations) ||
		!agent.Status.SessionRef.SameConversation(target.Status.SessionRef) || !reflect.DeepEqual(currentPane.Status.Activation, pane.Status.Activation) {
		return errors.New("agent relaunch: source changed before Stop; nothing was changed")
	}
	if !request.yes && agent.EffectiveInteraction(c.clock()).Kind != coremetadata.InteractionIdle && agent.EffectiveInteraction(c.clock()).Kind != coremetadata.InteractionResponseComplete {
		return usageError("agent relaunch: source started work; re-run with --yes (relaunch-agent-busy); nothing was changed")
	}
	stopErr := c.stopAgentPane(request.socket, pane.Metadata.UID, io.Discard, io.Discard)
	bounded, cancel := context.WithTimeout(ctx, processRelaunchRetirementTimeout)
	defer cancel()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	for {
		if err = c.verifyTmuxTransferRetirement(pane.Metadata.UID, pane.Status.Activation.Claude.Process, request); err == nil {
			return nil
		}
		select {
		case <-bounded.Done():
			return fmt.Errorf("agent relaunch: source retirement unconfirmed; no new child; recover with: %s: %w", relaunchRerunCommand(latest, target, request), errors.Join(stopErr, err, bounded.Err()))
		case <-ticker.C:
		}
	}
}

func (c *agentCommand) verifyTmuxTransferRetirement(paneUID string, process coremetadata.ProcessIdentity, request agentRelaunchRequest) error {
	route, err := resolveDeleteTarget(agentRelaunchSpelling, request.socket, c.lookupEnv)
	if err != nil {
		return err
	}
	live := c.managedPaneLive
	if live == nil {
		live = observeManagedPaneLive
	}
	alive, err := live(route, paneUID)
	if err != nil || alive {
		return errors.Join(err, errors.New("old tmux Pane retirement is unconfirmed"))
	}
	current, _, processErr := localipc.Process(process.PID)
	if processErr == nil {
		if current == process {
			return errors.New("old Claude writer remains alive")
		}
		return nil // PID was reused with a different kernel birth identity.
	}
	if err := syscall.Kill(process.PID, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return errors.New("old Claude writer liveness is unknown")
}

func (c *agentCommand) startTmuxTransfer(ctx context.Context, cancel context.CancelFunc, reg coremetadata.Registry, source tmuxRelaunchSource, recipe processRelaunchRecipe, command processhost.Command, settings agentSettingsLaunch, result agentRelaunchResult, stdout, stderr io.Writer) error {
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
		if err := recipe.links.record(working, mut, binding.Agent); err != nil { return err }
		agent, _ = working.Agent(binding.Agent)
		expected = agent.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	owned := processAgentResumeResult{Binding: binding, owner: processAgentCreateResult{Binding: binding, Provider: aiModeClaude, registryPath: intmetadata.PathFor(state), Created: createResult{kind: coremetadata.KindAgent, uid: binding.Agent, name: source.retired.Metadata.Name, windowUID: binding.Window}}}
	fail := func(cause error) error {
		if !owned.hasNoChild() {
			stopped, stopCancel := context.WithCancel(context.Background())
			stopCancel()
			snapshot, waitErr := owned.owner.waitProcessAgent(stopped, nil)
			cause = errors.Join(cause, waitErr)
			if snapshot.Exit == nil {
				return fmt.Errorf("agent relaunch: target retirement unknown; inspect agent uid:%s; no replacement writer permitted: %w", binding.Agent, cause)
			}
		}
		_, restoreErr := creator.store.update(func(working *coremetadata.Registry) error {
			pane, ok := working.Pane(binding.Pane)
			agent, found := working.Agent(binding.Agent)
			if !ok || !found || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(binding) || (agent.Status.Phase != coremetadata.PhasePending && agent.Status.Phase != coremetadata.PhaseOffline) {
				return processhost.ErrStale
			}
			if err := creator.store.mutator().DeletePane(working, binding.Pane); err != nil {
				return err
			}
			agent, _ = working.Agent(binding.Agent)
			agent.Metadata.Annotations = maps.Clone(source.retired.Metadata.Annotations)
			agent.Spec = source.retired.Clone().Spec
			agent.Status = source.retired.Clone().Status
			return nil
		})
		return fmt.Errorf("agent relaunch: transfer failed; previous recipe/conversation retained; recover with: %s: %w", processRelaunchRecovery(reg, source.retired, source.request), errors.Join(cause, restoreErr))
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	host, err := processhost.NewHost(binding.Host, processhost.Command{Path: executable, Args: []string{"internal", "process-host-supervisor"}, Env: command.Env}, creator.processCreateTransactions(owned.owner.registryPath), processhost.DefaultLimits())
	if err != nil {
		return fail(err)
	}
	transfer := processhost.ClaudeTransfer{Source: source.conversation, Verify: func(ctx context.Context, from processhost.TmuxConversationSource, to processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if from != source.conversation || to != binding {
			return processhost.ErrStale
		}
		if err := c.verifyTmuxTransferRetirement(from.Pane, source.process, source.request); err != nil {
			return err
		}
		latest, err := c.loadRegistry()
		if err != nil {
			return err
		}
		agent, ok := latest.Agent(binding.Agent)
		if !ok || !reflect.DeepEqual(agent.Spec, expected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, expected.Metadata.Annotations) || !agent.Status.SessionRef.SameConversation(source.retired.Status.SessionRef) {
			return processhost.ErrStale
		}
		return nil
	}}
	owned.Handle, err = startProcessClaudeTransfer(ctx, host, processhost.Launch{Binding: binding, Command: command}, owned.owner.registryPath, transfer, operation+"-transfer", strings.Join(source.request.prompt, " "))
	owned.owner.Handle = owned.Handle
	if err != nil {
		return fail(err)
	}
	var sync processRelaunchSynchronization
	sync.changed, sync.controls, sync.attention, err = owned.resumeSynchronization(creator)
	if err != nil {
		return fail(err)
	}
	result.NewPaneUID, result.Outcome = binding.Pane, personaOutcomeRestarted
	if err := writeAgentRelaunchResult(stdout, source.request, result); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "agent uid:%s pane uid:%s runtime=process foreground=owned\n", binding.Agent, binding.Pane)
	return runProcessRelaunchOwner(ctx, cancel, &owned, sync)
}
