package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// canonicalCreateAgent is the spelling the provider shortcuts normalize onto.
const canonicalCreateAgent = "create agent"

// Provider startup and initial-task acknowledgement are separate bounded
// stages. Exact SessionStart evidence opens the acknowledgement window; it does
// not itself acknowledge the task. Keeping each bound at five seconds lets a
// provider spend startup time before a delayed hook without turning one larger
// timeout into the contract.
const (
	agentActivationStartupDeadline         = 5 * time.Second
	agentActivationAcknowledgementDeadline = 5 * time.Second
)

// agentWork is one allocated Agent plus its managed Pane, waiting for the
// runtime phase to give the Pane a live tmux binding.
type agentWork struct {
	target     paneTarget
	windowName string
	agent      coremetadata.Agent
	pane       coremetadata.Pane
	// activation is the generation this Agent launch was issued.
	activation superviseSpec
}

// runResourceAgent answers the canonical resource-backed `create agent` and the
// three provider shortcuts.
//
// It is `create pane` with two substitutions, which is the point: the scope
// resolution, the Window fan-out, the split anchor, the Window ensure, the
// operation ledger and the rollback are the shared ones, not a second
// implementation.
// What differs is the metadata it allocates -- a Window-owned Agent plus the
// Agent-owned managed Pane, rather than a Window-owned shell Pane -- and the
// command the detached split runs, which is the provider launch instead of the
// raw payload.
//
// Three properties are load bearing and are asserted rather than assumed:
//
//   - The Agent is always new. There is no lookup of an existing Agent of the
//     same provider anywhere on this path; rebinding an existing conversation is
//     `agent resume`, which is a different verb with a different cardinality.
//   - The name never depends on the work. The provider id is the only name seed,
//     so the topic, the prompt, and the payload after `--` cannot reach it.
//   - Nothing moves the client. The split goes through the materializer, which
//     owns `-d`; the focus-following legacy split is not on this path at all.
//
// shortcutProvider is empty for the canonical spelling and carries the provider
// for `create codex|claude|antigravity`.
func (c *createCommand) runResourceAgent(shortcutProvider string, args []string, stdout, stderr io.Writer) error {
	spelling := canonicalCreateAgent
	if shortcutProvider != "" {
		spelling = "create " + shortcutProvider
	}

	shape := resourceCreateShape{split: true, provider: true}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	provider, err := c.resolveCreateProvider(spelling, shortcutProvider, flags)
	if err != nil {
		return err
	}
	// An argv-only refusal, so it lands before the Settings gate, the scope
	// derivation, and the transaction: `--interactive-only` names a Codex-only
	// lane, and silently ignoring it on another provider would let an operator
	// believe they had opted out of something that was never there.
	if err := requireInteractiveOnlyProvider(spelling, provider, flags); err != nil {
		return err
	}
	return c.createAgent(spelling, provider, flags, shape, stdout, stderr)
}

// createAgent is the shared body of every canonical Agent create.
//
// It is separate from the argv half because two producers reach it: the public
// `create agent` spellings above, and the Projmux split UI, whose resume
// selection is the same allocation and the same materialization with one
// substitution -- the provider's resume argv instead of its fresh-start argv.
// Sharing the body is what keeps the split UI from becoming a second definition
// of what creating an Agent means.
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
	mode, err := c.resolveProjection(spelling, flags.output)
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
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	var results []createResult
	var activationTargets []agentActivationTarget
	var nativeLifecycleTargets []codexLifecycleObserverTarget
	nativeLauncher, nativeLaunchCapable := c.resumes.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := c.resumes.(codexNativeLifecycleStarter)
	prompt, nativePromptExact := nativePrompt(flags.payload)
	nativeCandidate := provider == aiModeCodex && c.codexNative != nil && nativeLaunchCapable &&
		flags.codexCapability == nil && nativePromptExact && !flags.interactiveOnly
	// A default native create is the prompted shape: exactly one non-empty
	// operand on the Codex provider with no explicit interactive-only opt-out
	// and no capability selection. It is the only shape whose native authority
	// must be proven rather than attempted, because it is the only shape whose
	// silent degradation would hand the operator a plain CLI Agent they cannot
	// control natively without being told.
	defaultNativeCreate := nativeCandidate && prompt != ""
	if err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		project, err := c.resolveProject(*working, scope)
		if err != nil {
			return err
		}
		if err := c.refuseMissingRoot(project); err != nil {
			return err
		}

		resolver := c.resolveWorkspace
		if resolver == nil {
			resolver = resolveAgentWorkspace
		}
		workspace, err := resolver(*working, project, provider, flags.cwd, flags.addDirs)
		if err != nil {
			return err
		}

		// The launch is constructed before anything is allocated. A missing
		// provider binary is the most likely failure on this route, and it has
		// to land while the operation still owns nothing.
		title, launchArgv, err := c.planAgentPaneLaunch(provider, workspace, flags)
		if err != nil {
			return err
		}

		// The declared <create, Agent> cell is this route's fan-out cardinality:
		// one Agent per resolved target Window, at least one overall. It is the
		// Agent row rather than the Window row because this route never resolves
		// an existing Agent -- rebinding a conversation is `agent resume` -- so
		// the only Agent count it can fix is the one it produces.
		plan, windows, err := c.resolveSplitTargets(working, mutator, project, scope, flags,
			selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindAgent}, spelling, operationID)
		if err != nil {
			return err
		}
		// A Registry transaction can roll back several target Panes, but it
		// cannot delete app-server threads. Native identity therefore stays
		// atomic by being used only for the exact-one create shape.
		//
		// A default native create whose selector resolved several Windows is
		// refused here rather than fanned out onto the plain CLI lane: dropping
		// every target to a lane with no native turn control is exactly the
		// silent degradation this route no longer performs. The refusal lands
		// before the first Agent or Pane is allocated, so it costs zero threads,
		// zero Panes, and zero Registry mutations. `--interactive-only` remains
		// the way to ask for the plain-CLI fan-out on purpose.
		if defaultNativeCreate && len(plan.targets) > 1 {
			return nativeFanOutRefusal(spelling, len(plan.targets))
		}
		nativeEligible := nativeCandidate && len(plan.targets) == 1

		// Metadata phase. Every Agent and every managed Pane is allocated before
		// the first tmux call, so an explicit --name that collides inside a
		// target Window refuses with zero runtime objects created.
		agents := make([]agentWork, 0, len(plan.targets))
		for _, target := range plan.targets {
			window, ok := working.Window(target.windowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, target.windowUID)
			}
			agent, err := mutator.CreateAgent(working, target.windowUID, coremetadata.CreateAgentOptions{
				// The explicit --name names the Agent. The managed Pane derives
				// its own name from the Agent's, so one flag cannot name two
				// resources.
				Name:        flags.name,
				Provider:    provider,
				Labels:      labels,
				Workspace:   workspace,
				Activation:  activationStateForPayload(flags.payload),
				OperationID: operationID,
			})
			if err != nil {
				return MapMetadataError(err)
			}
			pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
				CWD:    workspace.CWD,
				Labels: labels,
			}, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
			if err != nil {
				return err
			}
			agents = append(agents, agentWork{
				target:     target,
				windowName: window.Metadata.Name,
				agent:      agent,
				pane:       pane,
				activation: activation,
			})
		}

		// Runtime phase.
		sessionName, err := c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
		if err != nil {
			return err
		}
		for i := range windows {
			if err := c.materializeWindow(ctx, working, mutator, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range agents {
			anchorPaneID, err := c.ensureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, work.target)
			if err != nil {
				return err
			}
			workTitle := title
			workLaunchArgv := launchArgv
			var nativeBinding coremetadata.CodexActivationBinding
			usedNative := false
			if nativeEligible {
				nativeCtx, cancel := prepareNativeContext(ctx)
				prepared, nativeErr := c.codexNative.Create(nativeCtx, workspace, prompt, work.activation.Generation)
				cancel()
				switch {
				case nativeErr == nil:
					workTitle, workLaunchArgv, err = nativeLauncher.PlanNativeCodexResume(workspace, prepared.ThreadID)
					if err != nil {
						return nativeLaunchError(spelling, err)
					}
					if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
						AgentUID: work.agent.Metadata.UID, PaneUID: work.pane.Metadata.UID,
						Generation: work.activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID,
					}); err != nil {
						return MapMetadataError(err)
					}
					nativeBinding = coremetadata.CodexActivationBinding{ThreadID: prepared.ThreadID, TurnID: prepared.TurnID}
					usedNative = true
				case nativeFallbackAllowed(c.codexNative, nativeErr):
					if defaultNativeCreate {
						// Native authority could not be proven, and no provider
						// conversation was mutated. A managed Agent is not created on
						// the plain CLI lane behind the operator's back.
						return nativeCreatePreparationRefusal(spelling, nativeErr)
					}
					// Empty prompt. Preserve the current CLI argv, hook
					// acknowledgement, output, and late-refinement contract
					// byte-for-byte.
				case nativeRootsUnsupported(nativeErr):
					// Fail closed, but before any conversation existed: the
					// explicit opt-out is still an honest answer here.
					return nativeCreatePreparationRefusal(spelling, nativeErr)
				default:
					return nativeLaunchError(spelling, nativeErr)
				}
			}
			paneID, err := c.runtime.splitPane(ctx, anchorPaneID, flags.placement, workspace.CWD,
				c.runtime.supervisedLaunch(ctx, work.activation, workLaunchArgv))
			if paneID != "" {
				if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, work.pane.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(err, claimErr)
				}
				if mirrorErr := c.runtime.mirror.MirrorPane(ctx, paneID, work.pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
				observeActivationRuntime(working, mutator, work.activation, paneID, c.runtime.warn)
			}
			if err != nil {
				return err
			}
			c.runtime.equalizeSplitLayout(ctx, anchorPaneID, flags.placement)
			// The managed-pane options are what make this pane an agent pane to
			// the statusbar, the attention tracker, and the notification
			// pipeline. They are applied after the pane exists and before the
			// result is reported.
			if usedNative {
				if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, c.runtime.runner, paneID, workspace.CWD, workTitle, nativeBinding.ThreadID); err != nil {
					return tmuxError("%s: bind native Codex Pane %s presentation metadata: %v", spelling, paneID, err)
				}
				if nativeLifecycleCapable {
					nativeLifecycleTargets = append(nativeLifecycleTargets, codexLifecycleObserverTarget{
						Identity: codexLifecycleIdentity{
							AgentUID: work.agent.Metadata.UID, PaneUID: work.pane.Metadata.UID, RuntimeID: paneID,
							Generation: work.activation.Generation, ThreadID: nativeBinding.ThreadID,
						},
						Route: c.runtime.target,
					})
				}
			} else if err := c.bindAgentPane(ctx, paneID, provider, workspace.CWD, workTitle,
				declaredPlainCodexLane(provider, flags, prompt), flags); err != nil {
				return tmuxError("%s: bind Agent Pane %s presentation metadata: %v", spelling, paneID, err)
			}
			if len(flags.payload) > 0 && !usedNative {
				activationTargets = append(activationTargets, agentActivationTarget{
					agentUID:   work.agent.Metadata.UID,
					agentName:  work.agent.Metadata.Name,
					paneUID:    work.pane.Metadata.UID,
					paneID:     paneID,
					generation: work.activation.Generation,
				})
			}
			results = append(results, createResult{
				kind: coremetadata.KindAgent,
				uid:  work.agent.Metadata.UID,
				name: work.agent.Metadata.Name,
				// `-o pane-id` is the managed Pane's raw transport handle, which
				// is what the existing pane launchers and skill bridges consume.
				paneID:      paneID,
				projectName: project.Metadata.Name,
				windowName:  work.windowName,
				windowUID:   work.target.windowUID,
			})
		}
		return nil
	}, c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	// The exact Registry binding becomes observable only after the transaction
	// commits. Starting inside the callback would correctly fail the startup
	// guard against the pre-transaction snapshot and strand no observer.
	for _, target := range nativeLifecycleTargets {
		nativeLifecycle.startNativeCodexLifecycleObserver(target)
	}
	if err := c.confirmAgentActivations(activationTargets); err != nil {
		return err
	}
	return c.writeResults(stdout, spelling, mode, coremetadata.KindAgent, results)
}

type agentActivationTarget struct {
	agentUID   string
	agentName  string
	paneUID    string
	paneID     string
	generation string
}

func activationStateForPayload(payload []string) coremetadata.AgentActivationState {
	if len(payload) == 0 {
		return coremetadata.ActivationNotRequested
	}
	return coremetadata.ActivationPending
}

func (c *createCommand) confirmAgentActivations(targets []agentActivationTarget) error {
	var diagnostics []error
	for _, target := range targets {
		acknowledged, _, err := c.agents.AwaitAgentActivation(context.Background(), c.runtime.runner, target.paneID,
			agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
		source := string(coremetadata.InteractionSourceProviderHook)
		state := coremetadata.ActivationAcknowledged
		reason := ""
		if err != nil || !acknowledged {
			state = coremetadata.ActivationUnconfirmed
			if err != nil {
				reason = coremetadata.ActivationReasonFailed
			} else {
				reason = coremetadata.ActivationReasonTimedOut
			}
		}
		var committed coremetadata.Agent
		if _, updateErr := c.store.update(func(registry *coremetadata.Registry) error {
			agentUID, generation, bound := exactAgentActivationBinding(*registry, target.paneUID, target.paneID)
			if !bound || agentUID != target.agentUID || generation != target.generation {
				return fmt.Errorf("create agent: activation binding changed for uid:%s Pane %s", target.agentUID, target.paneID)
			}
			updated, setErr := c.store.mutator().SetAgentActivation(registry, target.agentUID, state, source, reason)
			committed = updated.Clone()
			return setErr
		}); updateErr != nil {
			diagnostics = append(diagnostics, MapMetadataError(updateErr))
			continue
		}
		// A provider hook may commit after Await's final read but before the
		// timeout writer takes the Registry lock. SetAgentActivation is monotonic,
		// so inspect the committed authority instead of the stale local decision.
		if committed.Status.Activation.State == coremetadata.ActivationUnconfirmed {
			diagnostics = append(diagnostics, fmt.Errorf("create agent: agent/%s uid:%s has live managed Pane %s but initial task activation was not confirmed: %s; retry the task through the provider or clean up with `projmux delete agent uid:%s --yes`", target.agentName, target.agentUID, target.paneID, reason, target.agentUID))
		}
	}
	return errors.Join(diagnostics...)
}

type agentLaunchOutcomeRow struct {
	Outcome    string
	RC         string
	Stdout     string
	Resources  string
	Activation string
	Diagnostic string
}

// agentLaunchOutcomeTable is the closed command-result contract. String values
// make empty output and absent diagnostics printable rather than ambiguous
// blank cells in docs, tests, or support output.
var agentLaunchOutcomeTable = []agentLaunchOutcomeRow{
	{Outcome: "pre-runtime failure", RC: "nonzero", Stdout: "empty", Resources: "none", Activation: "not created", Diagnostic: "bounded refusal/failure"},
	{Outcome: "created+acknowledged", RC: "0", Stdout: "exact %N one line", Resources: "preserved", Activation: string(coremetadata.ActivationAcknowledged), Diagnostic: "none"},
	{Outcome: "created+unconfirmed", RC: "nonzero", Stdout: "empty", Resources: "preserved", Activation: string(coremetadata.ActivationUnconfirmed), Diagnostic: "exact Agent UID, live Pane, retry/delete remediation"},
	{Outcome: "delayed acknowledgement", RC: "0", Stdout: "exact %N one line", Resources: "preserved", Activation: string(coremetadata.ActivationAcknowledged), Diagnostic: "none"},
}

// resolveCreateProvider fixes the provider of one canonical Agent create.
//
// The canonical spelling requires an explicit `--provider`; the saved split mode
// is deliberately not consulted, because a canonical route whose result depends
// on hidden state is not canonical. A shortcut already names its provider, so
// respelling it is a usage error rather than a silent winner.
func (c *createCommand) resolveCreateProvider(spelling, shortcutProvider string, flags resourceCreateFlags) (string, error) {
	if shortcutProvider == "" {
		return requireCanonicalProvider(spelling, flags.provider)
	}
	if flags.providerSet {
		return "", usageError(fmt.Sprintf(
			"%s already names the provider; drop --provider or use `projmux create agent --provider %s`",
			spelling, strings.TrimSpace(flags.provider)))
	}
	return shortcutProvider, nil
}

// planAgentPaneLaunch builds the provider launch of one canonical Agent create.
//
// The two branches are two different launches, not two spellings of one. A fresh
// create appends the operator's payload as the provider's initial task; a resume
// joins a conversation the provider already has and takes no payload at all --
// the conversation id is the provider's own resume option, not an operand. There
// is deliberately no fallback from the second to the first: a resume that
// silently started a new conversation would lose the context the operator picked
// the row for.
func (c *createCommand) planAgentPaneLaunch(provider string, workspace coremetadata.AgentWorkspace, flags resourceCreateFlags) (string, []string, error) {
	conversation := strings.TrimSpace(flags.resumeConversation)
	if conversation == "" {
		if flags.codexCapability != nil {
			launcher, ok := c.agents.(codexCapabilityAgentLauncher)
			if !ok {
				return "", nil, errors.New("create agent: Codex capability launch is not configured")
			}
			return launcher.PlanAgentLaunchWithCapability(provider, workspace, flags.payload, *flags.codexCapability)
		}
		return c.agents.PlanAgentLaunch(provider, workspace, flags.payload)
	}
	if c.resumes == nil {
		return "", nil, errors.New("create agent: the provider resume launcher is not configured")
	}
	return c.resumes.PlanAgentResume(provider, workspace, conversation)
}

// bindAgentPane applies the managed-agent pane options.
//
// A resumed Pane additionally carries the conversation id in the live routing
// index from the moment the pane exists, which is what lets the provider's first
// hook event be attributed to this pane instead of having to wait for the
// provider to report the conversation itself.
func (c *createCommand) bindAgentPane(ctx context.Context, paneID, provider, contextDir, title, declared string, flags resourceCreateFlags) error {
	if conversation := strings.TrimSpace(flags.resumeConversation); conversation != "" && c.resumes != nil {
		if source := strings.TrimSpace(flags.resumeSource); source != "" {
			return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.runner, agentPaneBinding{
				PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
				ConversationID: conversation, ResumeSource: source, CodexNativeDeclared: declared,
			})
		}
		return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.runner, agentPaneBinding{
			PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
			ConversationID: conversation, CodexNativeDeclared: declared,
		})
	}
	return c.agents.BindAgentPaneOnRoute(ctx, c.runtime.runner, agentPaneBinding{
		PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title, CodexNativeDeclared: declared,
	})
}

// declaredPlainCodexLane names why one managed Codex Agent is being created on
// the plain CLI lane, from the closed declared vocabulary.
//
// Only the two by-design lanes are declared. Every other route to the plain
// lane is either a refusal that creates nothing, or a genuine loss of native
// authority that must stay visible as an unexplained native fallback.
func declaredPlainCodexLane(provider string, flags resourceCreateFlags, prompt string) string {
	if provider != aiModeCodex || strings.TrimSpace(flags.resumeConversation) != "" {
		return ""
	}
	if flags.interactiveOnly {
		return codexNativeDeclaredInteractiveOnly
	}
	if prompt == "" && len(flags.payload) == 0 {
		return codexNativeDeclaredEmptyPrompt
	}
	return ""
}
