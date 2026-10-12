package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func derivedAgentPaneName(agentName string) string {
	return mutationruntime.DerivedAgentPaneName(agentName)
}

func activationStateForPayload(payload []string) coremetadata.AgentActivationState {
	return mutationruntime.ActivationStateForPayload(payload)
}

func nativeCodexFreshCreateRequired(provider string, flags resourceCreateFlags) bool {
	return provider == aiModeCodex && !flags.interactiveOnly && (len(flags.payload) > 0 || flags.host == "process") &&
		strings.TrimSpace(flags.resumeConversation) == ""
}

func (c *createCommand) confirmAgentActivations(targets []agentActivationTarget) error {
	service := mutationruntime.AgentActivationService{Store: c.createTransaction().Store, Runner: c.runtime.Runner, Await: c.agents.AwaitAgentActivation}
	return service.Confirm(sharedActivationTargets(targets))
}

func (c *createCommand) planAgentPaneLaunch(provider string, workspace coremetadata.AgentWorkspace, flags resourceCreateFlags) (string, []string, error) {
	title, argv, _, err := c.planAgentPaneLaunchWithResume(provider, workspace, flags)
	return title, argv, err
}

func (c *createCommand) planAgentPaneLaunchWithResume(provider string, workspace coremetadata.AgentWorkspace, flags resourceCreateFlags) (string, []string, agentResumeLaunch, error) {
	conversation := strings.TrimSpace(flags.resumeConversation)
	if flags.dialogueReplyOnly {
		launcher, ok := c.agents.(claudeDialogueLauncher)
		if !ok {
			return "", nil, agentResumeLaunch{}, errors.New("claude reply-only launcher is unavailable")
		}
		title, argv, err := launcher.PlanClaudeDialogueLaunch(workspace, conversation)
		return title, argv, agentResumeLaunch{}, err
	}
	if conversation == "" {
		var title string
		var argv []string
		var err error
		// Only Claude is given its persona on the command line. A Codex
		// persona rides thread/start instead, so it must not reach the Claude
		// options launcher -- which refuses every other provider outright --
		// and the plain launch below is the one the native create replaces.
		personaFile := flags.personaLaunch.snapshot.Path
		if provider != aiModeClaude {
			personaFile = ""
		}
		// Claude keeps only the last --append-system-prompt-file, so the
		// Project's label link rules ride the one file: the rules snapshot,
		// or the composite of the persona and the rules.
		if provider == aiModeClaude && flags.projectLinks.systemPromptFile != "" {
			personaFile = flags.projectLinks.systemPromptFile
		}
		// The agent guidance goes in front of both, in the same one file.
		if provider == aiModeClaude && flags.agentGuidance.systemPromptFile != "" {
			personaFile = flags.agentGuidance.systemPromptFile
		}
		fresh := flags.sharedAgentSettingsFlags()
		if provider != aiModeClaude {
			fresh.ProfileLaunch.Settings = ""
		}
		title, argv, err = mutationruntime.PlanFreshAgent(c.agents, provider, workspace, fresh, personaFile)
		return title, argv, agentResumeLaunch{}, err
	}
	if c.resumes == nil {
		return "", nil, agentResumeLaunch{}, errors.New("create agent: the provider resume launcher is not configured")
	}
	// A resume-picker create hands the seam the launch values it inherited
	// from the Agents that already recorded this conversation, so the new
	// Agent starts with the persona snapshot, snapshot mode and effort they
	// would resume with. With nothing inherited the seam gets no annotations
	// and the argv is what it always was.
	launch, err := c.resumes.PlanAgentResume(provider, workspace, conversation, flags.resumeLaunchValues)
	return launch.title, launch.argv, launch, err
}

func (c *createCommand) bindAgentPane(ctx context.Context, paneID, provider, contextDir, title, declared string, flags resourceCreateFlags) error {
	if conversation := strings.TrimSpace(flags.resumeConversation); conversation != "" && c.resumes != nil {
		if source := strings.TrimSpace(flags.resumeSource); source != "" {
			return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
				PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
				ConversationID: conversation, ResumeSource: source, CodexNativeDeclared: declared,
			})
		}
		return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
			PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
			ConversationID: conversation, CodexNativeDeclared: declared,
		})
	}
	return c.agents.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
		PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title, CodexNativeDeclared: declared,
	})
}

func declaredPlainCodexLane(provider string, flags resourceCreateFlags, prompt string) string {
	if provider != aiModeCodex {
		return ""
	}
	if flags.interactiveOnly {
		return codexNativeDeclaredInteractiveOnly
	}
	if len(flags.payload) == 0 && strings.TrimSpace(flags.resumeConversation) == "" {
		return codexNativeDeclaredPayloadFreeFallback
	}
	if strings.TrimSpace(flags.resumeConversation) != "" && strings.TrimSpace(flags.resumeSource) == aisessions.SourceCodexRollout {
		return codexNativeDeclaredRolloutCatalogResume
	}
	return ""
}

func (c *createCommand) createAgent(spelling, provider string, flags resourceCreateFlags, shape resourceCreateShape, stdout, stderr io.Writer) error {
	if c.agents == nil {
		return errors.New("create agent: the provider launcher is not configured")
	}
	// The Settings gate applies to the canonical route too: spelling the command
	// differently does not re-enable a provider the operator switched off. It
	// runs before the store is opened, so a disabled provider costs zero
	// mutations and zero bytes of stdout.
	if err := c.agents.RequireAgentEnabled(provider); err != nil {
		return err
	}
	mode, err := resolveLifecycleProjection(spelling, flags.output)
	if err != nil {
		return err
	}
	labels, err := labelMap(flags.labels)
	if err != nil {
		return MapMetadataError(err)
	}
	// Scope resolution runs last of the preflight, so every argv-only refusal is
	// reported before an environment-dependent one. See runResourceWindow.
	scope, err := c.resolveCreateScope(spelling, flags, shape)
	if err != nil {
		return err
	}
	if flags.persona != "" {
		if flags.personaLaunch, err = c.preparePersonaLaunch(spelling, flags.persona, flags.personaOption); err != nil {
			return err
		}
	}
	if err := c.prepareProfileSettings(spelling, provider, &flags); err != nil {
		return err
	}
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	var out mutationruntime.AgentCreateResult
	var creator creatorRecord

	nativeLauncher, nativeLaunchCapable := c.resumes.(codexNativeAgentLauncher)
	prompt, nativePromptExact := nativePrompt(flags.payload)
	nativeCreate := nativeCodexFreshCreateRequired(provider, flags)
	var nativeRoute codexNativeEndpointRoute
	if nativeCreate {
		if !nativePromptExact {
			return nativeCreatePreparationRefusal(spelling, &codexNativeRouteError{Reason: "unsupported-create-shape"})
		}
		if c.codexNative == nil || !nativeLaunchCapable {
			return nativeCreatePreparationRefusal(spelling, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable})
		}
		nativeCtx, cancel := prepareNativeContext(context.Background())
		nativeRoute, err = c.codexNative.Current(nativeCtx)
		cancel()
		if err != nil || !nativeRoute.valid() || nativeRoute.State != coremetadata.CodexGenerationCurrent {
			if err == nil {
				err = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
			}
			return nativeCreatePreparationRefusal(spelling, err)
		}
	}
	execution := c.createExecution()
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspace
	}
	shared := &mutationruntime.AgentCreation{Execution: execution, ResolveWorkspace: resolver,
		Creator: func(ctx context.Context, working *coremetadata.Registry) (mutationruntime.AgentCreatorRecord, error) {
			var err error
			creator, err = c.decideCreator(ctx, spelling, working, flags.creator)
			return mutationruntime.AgentCreatorRecord{Annotations: creator.annotations()}, err
		},
		Prepare: func(_ coremetadata.Registry, project coremetadata.Project, workspace coremetadata.AgentWorkspace) (mutationruntime.AgentPreparation, error) {
			c.prepareProjectLinks(provider, project, &flags)
			c.prepareAgentGuidance(provider, &flags)
			return mutationruntime.AgentPreparation{
				Plan: func(workspace coremetadata.AgentWorkspace) (string, []string, error) {
					return c.planAgentPaneLaunch(provider, workspace, flags)
				},
				Bind: func(ctx context.Context, paneID string, workspace coremetadata.AgentWorkspace, title string) error {
					return c.bindAgentPane(ctx, paneID, provider, workspace.CWD, title, declaredPlainCodexLane(provider, flags, prompt), flags)
				},
				Annotations: func(base map[string]string) map[string]string {
					return flags.agentGuidance.withCreateAnnotation(flags.projectLinks.withCreateAnnotation(withCreateSettingSources(flags, flags.profileLaunch.withAnnotations(withModelAnnotation(flags.model, withEffortAnnotation(flags.effort, flags.personaLaunch.withAnnotations(withDialogueReplyOnlyAnnotation(flags.dialogueReplyOnly, base))))))))
				},
				DeveloperInstructions: func(uid string) string {
					return flags.agentGuidance.codexDeveloperInstructions(uid, flags.projectLinks.developerInstructions(flags.personaLaunch.content))
				},
				Policy: flags.profileLaunch.codexPolicy, Model: flags.model, Effort: flags.effort,
				Notices: func(name string) []string {
					var notices []string
					for _, notice := range []string{flags.projectLinks.notice(name), flags.agentGuidance.notice(name)} {
						if notice != "" {
							notices = append(notices, notice)
						}
					}
					return notices
				},
			}, nil
		},
	}
	if nativeCreate {
		shared.Native = &nativeCreationEngine{Controller: c.codexNative, Spelling: spelling, Launcher: nativeLauncher, Route: nativeRoute, Prompt: prompt, Runner: func() tmuxCommandRunner { return c.runtime.Runner },
			History: func(working *coremetadata.Registry, uid string) []sessionhistory.Record {
				if bound, ok := working.Agent(uid); ok {
					if row, valid := sessionhistory.ObservedRecordFor(working, bound.Metadata.UID, bound.Status.SessionRef); valid {
						return []sessionhistory.Record{row}
					}
				}
				return nil
			}}
	}
	request := mutationruntime.AgentCreateRequest{CreateSelection: flags.sharedSelection(), Provider: provider, Cwd: flags.cwd, AddDirs: flags.addDirs, Payload: flags.payload, Placement: flags.placement, CwdFrom: flags.cwdFrom, CwdSet: flags.cwdSet, DialogueReplyOnly: flags.dialogueReplyOnly, Labels: labels}
	if err := c.createTransaction().Transact(mutationruntime.CreateKindAgent, shared.Operation(scope.shared(), request, spelling, &out), c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	for _, row := range out.NativeHistory {
		c.recordCreateAgentSessionHistory(row, true, stderr)
	}
	if err := writeSplitCWDNotices(stderr, out.Notices); err != nil {
		return err
	}
	nativeLifecycle, nativeLifecycleCapable := c.resumes.(codexNativeLifecycleStarter)
	if nativeLifecycleCapable {
		for _, target := range out.NativeBindings {
			nativeLifecycle.startNativeCodexLifecycleObserver(codexLifecycleObserverTarget{Identity: codexLifecycleIdentity{AgentUID: target.Work.Agent.Metadata.UID, PaneUID: target.Work.Pane.Metadata.UID, RuntimeID: target.PaneID, Generation: target.Work.Activation.Generation, ThreadID: target.Binding.ThreadID}, Route: c.runtime.Target, NativeRoute: shared.Native.(*nativeCreationEngine).Route})
		}
	}
	var activationTargets []agentActivationTarget
	for _, target := range out.ActivationTargets {
		activationTargets = append(activationTargets, agentActivationTarget{agentUID: target.AgentUID, agentName: target.AgentName, paneUID: target.PaneUID, paneID: target.PaneID, generation: target.Generation})
	}
	if err := c.confirmAgentActivations(activationTargets); err != nil {
		return err
	}
	if err := c.warnUnregisteredClaudeActivations(activationTargets, stderr); err != nil {
		return err
	}
	creator.report(stderr)
	results := localShellResults(out.Results)
	receipt := createPlannedReceipt(coremetadata.KindAgent, results, out.SelectedWindowUIDs)
	receipt.Profile = flags.profileLaunch.receipt()
	if mode != cli.OutputModeDefault && mode != cli.OutputModeReceipt {
		if err := writeProfileDisclosure(stderr, receipt.Profile); err != nil {
			return err
		}
	}
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindAgent, results, receipt)
}

func (t agentActivationTarget) shared() mutationruntime.AgentActivationTarget {
	return mutationruntime.AgentActivationTarget{AgentUID: t.agentUID, AgentName: t.agentName, PaneUID: t.paneUID, PaneID: t.paneID, Generation: t.generation}
}
func sharedActivationTargets(ts []agentActivationTarget) []mutationruntime.AgentActivationTarget {
	var out []mutationruntime.AgentActivationTarget
	for _, t := range ts {
		out = append(out, t.shared())
	}
	return out
}
