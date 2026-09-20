package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// canonicalCreateAgent is the spelling the provider shortcuts normalize onto.
const canonicalCreateAgent = "create agent"

// Provider startup and initial-task acknowledgement are separate bounded
// stages. Exact SessionStart evidence opens the acknowledgement window; it does
// not itself acknowledge the task. Keeping them separate lets a provider spend
// startup time before a delayed hook without turning one larger timeout into
// the contract.
//
// Both bounds were five seconds, which is inside the cost of starting a real
// provider on a busy machine, so a create that had worked would still exit
// nonzero and offer to delete the live Agent. Measured on a 14-core machine
// under `while :; do :; done` at 1x and 3x nproc (observed loadavg 37-72, real
// `claude`, n=20, the product's own stage timestamps):
//
//	stage           p50     p90     observed max
//	startup         9.5s    13.9s   24.4s  (loadavg 69.8)
//	acknowledgement 4.2s     5.2s    8.6s  (loadavg 63.3)
//
// Each bound covers its own peak plus one more step of the observed tail slope,
// max + (max - p90). Twenty samples make a max a weak tail estimate, and this
// tail is heavy: startup jumps 1.75x from p90 to max. Sizing to the sample max
// alone would leave the bound one unlucky run from the behavior this change
// exists to remove.
//
// The asymmetry is the reason to spend the headroom rather than save it. A
// bound that is too low deletes live work; a bound that is too high only delays
// reporting a provider that really is dead, once, on the failure path -- worst
// case 47s for one target and 47s x targets for a fan-out create, which waits
// per target in confirmAgentActivations. The same load that produced these
// peaks also put 19-61s of Registry and tmux work *before* this wait: those
// creates took 26-90s end to end.
//
// The headroom is also what makes the exit code honest. Exceeding a bound is
// reported as a failure only because it is meant to mean "the task never
// arrived" rather than "this run was slower than the sample" -- so the claim
// depends on the margin, not just on the timeout existing.
//
// These are not a sufficiency proof. They cover the measured envelope up to
// loadavg ~72 on this machine class; heavier load is explicitly outside it.
const (
	agentActivationStartupDeadline         = 35 * time.Second
	agentActivationAcknowledgementDeadline = 12 * time.Second
)

// agentPaneNameSuffix is the launcher convention for the name of the Pane an
// Agent owns. It used to live only in the launcher documentation as a
// mandatory follow-up `rename pane`, which meant a caller that had not read
// that page left the managed Pane addressed by a raw UID.
const agentPaneNameSuffix = "-pane"

// derivedAgentPaneName is the explicit Pane name `create agent` supplies for
// the Pane an Agent owns, or "" for automatic naming.
//
// This is deliberately an *explicit* name handed to the existing explicit-name
// machinery (`BootstrapPane.Name` -> `addPaneTx` -> `mintAndReserveName`), not
// a new automatic-name rule. The automatic allocator is untouched, so a Pane
// with no Agent -- a shell Pane, `create pane`, bootstrap topology -- still
// gets its exact full minted UID as its name.
//
// The derivation runs once, at create. It is not an invariant: `rename agent`
// does not follow the Pane, and `rename pane` stays free to move the Pane name
// anywhere. Because `reserveExplicitName` skips a slot the same uid already
// holds, a launcher that still issues the documented `rename pane
// <agent-name>-pane` afterwards gets a successful no-op rather than a
// conflict.
//
// Length is the one fallback. An Agent name may be the full 128 bytes
// `ValidateName` allows, so the derived name can be 133 and invalid. Refusing
// there would break `create agent --name <long>` calls that succeed today, so
// the derivation yields "" and the Pane falls back to automatic naming.
// Nothing else falls back: a *collision* on the derived name is a typed
// zero-write refusal from the metadata phase, which runs entirely before the
// first tmux or provider call.
func derivedAgentPaneName(agentName string) string {
	derived := agentName + agentPaneNameSuffix
	if coremetadata.ValidateName(derived) != nil {
		return ""
	}
	return derived
}

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
//   - The automatic name is the exact minted Agent UID. Only an explicit
//     `--name` can replace it; provider, topic, prompt, and payload cannot.
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
	if err := requireClaudeLaunchOptions(spelling, provider, flags); err != nil {
		return err
	}
	if err := requireClaudePersona(spelling, provider, flags); err != nil {
		return err
	}
	if err := requireClaudeDialogueMode(provider, flags.dialogueReplyOnly, flags.payload); err != nil {
		return err
	}
	if flags.dialogueReplyOnly {
		if _, ok := c.agents.(claudeDialogueLauncher); !ok {
			return errors.New("claude reply-only launcher is unavailable")
		}
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
		if flags.personaLaunch, err = c.preparePersonaLaunch(spelling, flags.persona); err != nil {
			return err
		}
	}
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	var results []createResult
	var selectedWindowUIDs []string
	var notices []string
	var activationTargets []agentActivationTarget
	var nativeLifecycleTargets []codexLifecycleObserverTarget
	nativeLauncher, nativeLaunchCapable := c.resumes.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := c.resumes.(codexNativeLifecycleStarter)
	prompt, nativePromptExact := nativePrompt(flags.payload)
	nativeCreate := nativeCodexFreshCreateRequired(provider, flags)
	var nativeRoute codexNativeEndpointRoute
	var creator creatorProvenance
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
	if err := c.transact(diagnostics.CreateKindAgent, func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
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
		// An explicit --cwd names the working directory outright, so it wins
		// over --cwd-from. Otherwise this CLI route follows its own argv:
		// only --cwd-from decides, and no config file is opened.
		source := splitCWDFromProject
		if !flags.cwdSet {
			source = cliSplitCWDSource(flags.cwdFrom)
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
		// The receipt quotes the planner, not the Agents this route produced,
		// so `create agent` and the three provider shortcuts report the same
		// selected set as `create pane` for the same argv -- including on the
		// native refusal below, which is decided on this very set.
		selectedWindowUIDs = plan.selectedWindowUIDs()
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
		if nativeCreate && len(plan.targets) > 1 {
			return nativeFanOutRefusal(spelling, len(plan.targets))
		}
		nativeEligible := nativeCreate && len(plan.targets) == 1
		if nativeEligible {
			// Admission is revalidated while the Registry mutation lock is held
			// and before any metadata, provider, or tmux effect. The rolling
			// coordinator commits its current pointer behind the same lock, so a
			// create is wholly before or wholly after the switch.
			admissionCtx, admissionCancel := prepareNativeContext(ctx)
			admitted, routeErr := c.codexNative.Resolve(admissionCtx, nativeRoute.Endpoint)
			admissionCancel()
			if routeErr != nil || !admitted.valid() || !admitted.Endpoint.Same(nativeRoute.Endpoint) || admitted.State != coremetadata.CodexGenerationCurrent {
				if routeErr == nil {
					routeErr = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
				}
				return nativeCreatePreparationRefusal(spelling, routeErr)
			}
			nativeRoute = admitted
		}

		// Metadata phase. Every Agent and every managed Pane is allocated before
		// the first tmux call, so an explicit --name that collides in the target
		// root refuses with zero runtime objects created. The creator is
		// observed once, before the first allocation, for the whole fan-out.
		creator = c.observeCreator(ctx, working)
		agents := make([]agentWork, 0, len(plan.targets))
		for _, target := range plan.targets {
			window, ok := working.Window(target.windowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, target.windowUID)
			}
			agent, err := mutator.CreateAgent(working, target.windowUID, coremetadata.CreateAgentOptions{
				// The explicit --name names only the Agent. The managed Pane
				// takes its name from the Agent's, one derivation later.
				Name:        flags.name,
				Provider:    provider,
				Labels:      labels,
				Annotations: withEffortAnnotation(flags.effort, flags.personaLaunch.withAnnotations(creator.annotations())),
				Workspace:   workspace,
				Activation:  activationStateForPayload(flags.payload),
				OperationID: operationID,
			})
			if err != nil {
				return MapMetadataError(err)
			}
			pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
				Name:   derivedAgentPaneName(agent.Metadata.Name),
				CWD:    workspace.CWD,
				Labels: labels,
			}, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			pane = creator.annotatePane(working, pane)
			activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
			if err != nil {
				return err
			}
			activation.DialogueReplyOnly = flags.dialogueReplyOnly
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
			workWorkspace := workspace
			workTitle := title
			workLaunchArgv := launchArgv
			if source == splitCWDFromPane {
				dir, notice := c.splitPaneLaunchDir(ctx, anchorPaneID, project.Spec.Root)
				if notice != "" {
					notices = append(notices, splitCWDNoticeLine(spelling, work.windowName, notice))
				}
				if dir != project.Spec.Root {
					// The Pane directory becomes this Agent's working directory
					// through the unchanged workspace validation, and its launch is
					// planned again for that workspace.
					workWorkspace, err = resolver(*working, project, provider, dir, flags.addDirs)
					if err != nil {
						return err
					}
					workTitle, workLaunchArgv, err = c.planAgentPaneLaunch(provider, workWorkspace, flags)
					if err != nil {
						return err
					}
					if stored, ok := working.Agent(work.agent.Metadata.UID); ok {
						stored.Spec.Workspace = workWorkspace
					}
					if stored, ok := working.Pane(work.pane.Metadata.UID); ok {
						stored.Spec.CWD = workWorkspace.CWD
					}
					work.pane.Spec.CWD = workWorkspace.CWD
				}
			}
			var nativeBinding coremetadata.CodexActivationBinding
			usedNative := false
			if nativeEligible {
				if err := mutator.StageCodexEndpoint(working, work.agent.Metadata.UID, nativeRoute.Endpoint); err != nil {
					return MapMetadataError(err)
				}
				nativeCtx, cancel := prepareNativeContext(ctx)
				prepared, nativeErr := c.codexNative.Create(nativeCtx, nativeRoute, workWorkspace, prompt, work.activation.Generation)
				cancel()
				switch {
				case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) != "":
					workTitle, workLaunchArgv, err = nativeLauncher.PlanNativeCodexResume(nativeRoute, workWorkspace, prepared.ThreadID)
					if err != nil {
						return nativeLaunchError(spelling, err)
					}
					if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
						AgentUID: work.agent.Metadata.UID, PaneUID: work.pane.Metadata.UID,
						Generation: work.activation.Generation, ThreadID: prepared.ThreadID, TurnID: prepared.TurnID,
						Endpoint: nativeRoute.Endpoint,
					}); err != nil {
						return MapMetadataError(err)
					}
					nativeBinding = coremetadata.CodexActivationBinding{ThreadID: prepared.ThreadID, TurnID: prepared.TurnID}
					usedNative = true
				case nativeErr == nil:
					return nativeLaunchError(spelling, fmt.Errorf("%w: native create returned an empty thread", codexappserver.ErrProtocol))
				case nativeFallbackAllowed(c.codexNative, nativeErr):
					return nativeCreatePreparationRefusal(spelling, nativeErr)
				case nativeRootsUnsupported(nativeErr):
					// Fail closed, but before any conversation existed: the
					// explicit opt-out is still an honest answer here.
					return nativeCreatePreparationRefusal(spelling, nativeErr)
				default:
					return nativeLaunchError(spelling, nativeErr)
				}
			}
			paneID, err := c.runtime.splitPane(ctx, anchorPaneID, flags.placement, workWorkspace.CWD,
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
				if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, c.runtime.runner, paneID, workWorkspace.CWD, workTitle, "", nativeBinding.ThreadID); err != nil {
					return tmuxError("%s: bind native Codex Pane %s presentation metadata: %v", spelling, paneID, err)
				}
				if nativeLifecycleCapable {
					nativeLifecycleTargets = append(nativeLifecycleTargets, codexLifecycleObserverTarget{
						Identity: codexLifecycleIdentity{
							AgentUID: work.agent.Metadata.UID, PaneUID: work.pane.Metadata.UID, RuntimeID: paneID,
							Generation: work.activation.Generation, ThreadID: nativeBinding.ThreadID,
						},
						Route: c.runtime.target, NativeRoute: nativeRoute,
					})
				}
			} else if err := c.bindAgentPane(ctx, paneID, provider, workWorkspace.CWD, workTitle,
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
	if err := writeSplitCWDNotices(stderr, notices); err != nil {
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
	if err := c.warnUnregisteredClaudeActivations(activationTargets, stderr); err != nil {
		return err
	}
	creator.reportSkip(stderr)
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindAgent, results,
		createPlannedReceipt(coremetadata.KindAgent, results, selectedWindowUIDs))
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

// nativeCodexFreshCreateRequired is the pre-provider lane decision shared by
// public create and every canonical UI intent. A fresh payload-free Codex
// create is deliberately plain: the current installed tuple cannot hand a
// zero-turn thread to an independent TUI durably, so consulting Current,
// Resolve, or Create would already be too late to fall back safely.
//
// Prompted creates retain the native-required contract. Multi-operand payloads
// also stay on that contract and are rejected by nativePrompt instead of being
// silently reinterpreted by this decision.
func nativeCodexFreshCreateRequired(provider string, flags resourceCreateFlags) bool {
	return provider == aiModeCodex && !flags.interactiveOnly && len(flags.payload) > 0 &&
		strings.TrimSpace(flags.resumeConversation) == ""
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
			diagnostics = append(diagnostics, errors.New(activationUnconfirmedDiagnostic(target, reason)))
		}
	}
	return errors.Join(diagnostics...)
}

// activationUnconfirmedDiagnosticSteps is the ordered remediation an
// unconfirmed activation prints. The order is the contract, not the prose: a
// nonzero exit used to lead with `delete agent`, and operators -- including
// this repository's own launcher automation, which propagates the exit code --
// deleted Agents that were alive and already working, because a provider hook
// delayed past the bound still refines the activation to acknowledged
// afterwards. Reading committed authority is therefore always cheaper and safer
// than deleting, so deletion is last and conditional on the two reads above it
// finding no evidence.
func activationUnconfirmedDiagnosticSteps(target agentActivationTarget) []string {
	return []string{
		fmt.Sprintf("the Agent and its managed Pane %s were created and are still live; nothing was rolled back", target.paneID),
		fmt.Sprintf("re-read the committed activation with `projmux get agent uid:%s` first: a provider hook that arrived after the bound refines it to acknowledged", target.agentUID),
		fmt.Sprintf("if it is still unconfirmed, look at Pane %s (`tmux capture-pane -p -t %s`) or the provider transcript for the initial task, and retry it through the provider when it never arrived", target.paneID, target.paneID),
		fmt.Sprintf("only when neither read shows activation evidence, clean up with `projmux delete agent uid:%s --yes`", target.agentUID),
	}
}

// activationUnconfirmedDiagnostic names the exact live resources first and then
// prints activationUnconfirmedDiagnosticSteps in order.
func activationUnconfirmedDiagnostic(target agentActivationTarget, reason string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "create agent: agent/%s uid:%s has live managed Pane %s but initial task activation was not confirmed: %s",
		target.agentName, target.agentUID, target.paneID, reason)
	for _, step := range activationUnconfirmedDiagnosticSteps(target) {
		builder.WriteString("; ")
		builder.WriteString(step)
	}
	return builder.String()
}

// Registration arrives on the provider's own SessionStart hook, which is not
// ordered against the activation acknowledgement this create already waited
// for. A short grace therefore separates "the hook is a moment behind" from
// "the hook never ran", and only the second is worth a warning. The create
// itself never fails on this: registration is the messaging lane, and a Pane
// that works for its operator is not a failed create.
// They are variables, not constants, so a test can run the whole loop without
// spending its wall clock; nothing outside tests reassigns them.
var (
	claudeRegistrationCreateGrace        = 2 * time.Second
	claudeRegistrationCreatePollInterval = 100 * time.Millisecond
	claudeRegistrationCreateSleep        = time.Sleep
)

// warnUnregisteredClaudeActivations reports every acknowledged Claude
// activation that finished with no registration lease.
//
// Before this existed the state was silent: the Agent came up, looked healthy
// in every projection, and only refused when someone finally sent it a message
// -- which for an unattended worker could be hours later, or never. Saying it
// at create time is the cheapest place to say it.
func (c *createCommand) warnUnregisteredClaudeActivations(targets []agentActivationTarget, stderr io.Writer) error {
	if len(targets) == 0 || c.store == nil || c.store.load == nil {
		return nil
	}
	remaining := targets
	var warnings []string
	for deadline := time.Now().Add(claudeRegistrationCreateGrace); ; {
		registry, err := c.store.load()
		if err != nil {
			// The resources exist and the create succeeded; a Registry read
			// that fails here is not a reason to fail it retroactively.
			return nil
		}
		var pending []agentActivationTarget
		warnings = warnings[:0]
		for _, target := range remaining {
			agent, ok := registry.Agent(target.agentUID)
			if !ok || agent.Spec.Provider != string(aiprovider.Claude) ||
				agent.Status.PaneRef != target.paneUID {
				continue
			}
			shape := classifyAgentClaudeRegistration(registry, *agent)
			if shape == coremetadata.ClaudeRegistrationReady {
				continue
			}
			pending = append(pending, target)
			warnings = append(warnings, unregisteredClaudeActivationWarning(target, *agent, shape))
		}
		remaining = pending
		if len(remaining) == 0 || !time.Now().Before(deadline) {
			break
		}
		claudeRegistrationCreateSleep(claudeRegistrationCreatePollInterval)
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintln(stderr, warning); err != nil {
			return err
		}
	}
	return nil
}

// unregisteredClaudeActivationWarning states what is live, what is missing,
// what it costs, and what fixes it -- in that order, so an operator who reads
// only the first clause still learns that nothing was rolled back.
func unregisteredClaudeActivationWarning(target agentActivationTarget, agent coremetadata.Agent, shape coremetadata.ClaudeRegistrationShape) string {
	return fmt.Sprintf(
		"create agent: warning: agent/%s uid:%s is live in Pane %s but activation finished with no Claude registration lease, so `projmux agent message send` to it will be refused; %s",
		target.agentName, target.agentUID, target.paneID,
		shape.Diagnosis()+"; "+claudeRegistrationNextAction(shape, agent.Metadata.UID))
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
	{Outcome: "created+unconfirmed", RC: "nonzero", Stdout: "empty", Resources: "preserved", Activation: string(coremetadata.ActivationUnconfirmed), Diagnostic: "exact Agent UID, live Pane, recheck-first remediation ending in conditional delete"},
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
	title, argv, _, err := c.planAgentPaneLaunchWithResume(provider, workspace, flags)
	return title, argv, err
}

// planAgentPaneLaunchWithResume is planAgentPaneLaunch that also returns the
// resume launch, whose persona and effort notices a resume-picker create
// discloses once its Agent has a name. Every other launch returns the zero
// resume launch, which discloses nothing.
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
		personaFile := flags.personaLaunch.snapshot.Path
		switch {
		case flags.model != "" || flags.effort != "" || personaFile != "":
			launcher, ok := c.agents.(claudeOptionsAgentLauncher)
			if !ok {
				return "", nil, agentResumeLaunch{}, errors.New("create agent: the Claude model launcher is not configured")
			}
			title, argv, err = launcher.PlanAgentLaunchWithOptions(provider, workspace, flags.payload, flags.model, flags.effort, personaFile)
		case provider == aiModeCodex && len(flags.payload) == 0:
			// A payload-free Codex create always takes the plain lane.
			title, argv, err = c.agents.PlanAgentLaunch(aiModeCodex, workspace, nil)
		default:
			title, argv, err = c.agents.PlanAgentLaunch(provider, workspace, flags.payload)
		}
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
