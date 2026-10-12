package runtime

import (
"context"
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/aiprovider"
"github.com/crevissepartners/projmux/internal/cli"
"github.com/crevissepartners/projmux/internal/core/operatorclient"
"github.com/crevissepartners/projmux/internal/core/selector"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
"io"
"maps"
"os"
"slices"
"strconv"
"strings"
"time"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// forAgent is the record for one allocated Agent. The creator is never the
// created Agent itself: if the record would name it, the keys are removed from
// the stored Agent and nothing is recorded on its Pane. Every basis already
// resolves its Agent before allocation, so this is a guarantee, not a path.
func (r creatorRecord) forAgent(working *coremetadata.Registry, agentUID string) creatorRecord {
	if r.agentUID == "" || r.agentUID != agentUID {
		return r
	}
	if stored, ok := working.Agent(agentUID); ok {
		for key := range r.annotations() {
			delete(stored.Metadata.Annotations, key)
		}
	}
	return creatorRecord{observed: r.observed}
}

// annotatePane records the same keys on the managed Pane the create just
// attached, in the working Registry of the same transaction.
func (r creatorRecord) annotatePane(working *coremetadata.Registry, pane coremetadata.Pane) coremetadata.Pane {
	annotations := r.annotations()
	if annotations == nil || working == nil {
		return pane
	}
	stored, ok := working.Pane(pane.Metadata.UID)
	if !ok {
		return pane
	}
	if stored.Metadata.Annotations == nil {
		stored.Metadata.Annotations = map[string]string{}
	}
	maps.Copy(stored.Metadata.Annotations, annotations)
	return stored.Clone()
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

type agentActivationTarget struct {
	agentUID   string
	agentName  string
	paneUID    string
	paneID     string
	generation string
}

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

func activationStateForPayload(payload []string) coremetadata.AgentActivationState {
	if len(payload) == 0 {
		return coremetadata.ActivationNotRequested
	}
	return coremetadata.ActivationPending
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
		if flags.personaLaunch, err = c.preparePersonaLaunch(spelling, flags.persona, flags.personaOption); err != nil {
			return err
		}
	}
	if err := c.prepareProfileSettings(spelling, provider, &flags); err != nil {
		return err
	}
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	var results []createResult
	var selectedWindowUIDs []string
	var notices []string
	var activationTargets []agentActivationTarget
	var nativeLifecycleTargets []codexLifecycleObserverTarget
	var nativeSessionHistory []sessionhistory.Record
	nativeLauncher, nativeLaunchCapable := c.resumes.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := c.resumes.(codexNativeLifecycleStarter)
	prompt, nativePromptExact := nativePrompt(flags.payload)
	nativeCreate := nativeCodexFreshCreateRequired(provider, flags)
	var nativeRoute codexNativeEndpointRoute
	var creator creatorRecord
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
	if err := c.transact(diagnostics.CreateKindAgent, func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		nativeSessionHistory = nil
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

		// The Project's label link rules are read from the resolved Project,
		// never from the working directory.
		c.prepareProjectLinks(provider, project, &flags)
		// The agent guidance goes in front of whatever file that prepared.
		c.prepareAgentGuidance(provider, &flags)
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
		// decided once, before the first allocation, for the whole fan-out.
		if creator, err = c.decideCreator(ctx, spelling, working, flags.creator); err != nil {
			return err
		}
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
				Annotations: flags.agentGuidance.withCreateAnnotation(flags.projectLinks.withCreateAnnotation(withCreateSettingSources(flags, flags.profileLaunch.withAnnotations(withModelAnnotation(flags.model, withEffortAnnotation(flags.effort, flags.personaLaunch.withAnnotations(withDialogueReplyOnlyAnnotation(flags.dialogueReplyOnly, creator.annotations())))))))),
				Workspace:   workspace,
				Activation:  activationStateForPayload(flags.payload),
				OperationID: operationID,
			})
			if err != nil {
				return MapMetadataError(err)
			}
			agentCreator := creator.forAgent(working, agent.Metadata.UID)
			if notice := flags.projectLinks.notice(agent.Metadata.Name); notice != "" {
				notices = append(notices, notice)
			}
			if notice := flags.agentGuidance.notice(agent.Metadata.Name); notice != "" {
				notices = append(notices, notice)
			}
			pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
				Name:   derivedAgentPaneName(agent.Metadata.Name),
				CWD:    workspace.CWD,
				Labels: labels,
			}, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			pane = agentCreator.annotatePane(working, pane)
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
		var sessionName string
		if len(windows) > 0 {
			sessionName, err = c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
			if err != nil {
				return err
			}
		}
		for i := range windows {
			if err := c.materializeWindow(ctx, working, mutator, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range agents {
			var anchorPaneID string
			if !work.target.virtual {
				if sessionName == "" {
					sessionName, err = c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
					if err != nil {
						return err
					}
				}
				anchorPaneID, err = c.ensureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, work.target)
				if err != nil {
					return err
				}
			}
			workWorkspace := workspace
			workTitle := title
			workLaunchArgv := launchArgv
			if source == splitCWDFromPane && !work.target.virtual {
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
				prepared, nativeErr := c.codexNative.Create(nativeCtx, nativeRoute, codexNativeCreateInput{
					Workspace: workWorkspace, DeveloperInstructions: flags.agentGuidance.codexDeveloperInstructions(work.agent.Metadata.UID, flags.projectLinks.developerInstructions(flags.personaLaunch.content)),
					Policy: flags.profileLaunch.codexPolicy, Model: flags.model, Effort: flags.effort,
					Prompt: prompt, RequestKey: work.activation.Generation,
				})
				cancel()
				switch {
				case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) != "":
					workTitle, workLaunchArgv, err = planNativeCodexResumeOptions(nativeLauncher, nativeRoute, workWorkspace, prepared.ThreadID, flags.model, flags.effort)
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
					if bound, ok := working.Agent(work.agent.Metadata.UID); ok {
						if row, valid := sessionhistory.ObservedRecordFor(working, bound.Metadata.UID, bound.Status.SessionRef); valid {
							nativeSessionHistory = append(nativeSessionHistory, row)
						}
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
			var paneID string
			if work.target.virtual {
				paneID, err = c.materializeVirtualPane(ctx, working, mutator, ledger, project, work.target.windowUID, work.pane, work.activation, workLaunchArgv)
			} else {
				paneID, err = c.runtime.SplitPane(ctx, anchorPaneID, flags.placement, workWorkspace.CWD, c.runtime.SupervisedLaunch(ctx, work.activation, workLaunchArgv), nil)
			}
			if paneID != "" && !work.target.virtual {
				// The supervised child now runs and will want the Registry lock
				// this transaction holds; create.outcome measures the rest of the hold.
				markSupervisedSpawn(ctx)
				if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, paneID, work.pane.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(err, claimErr)
				}
				if mirrorErr := c.runtime.Mirror.MirrorPane(ctx, paneID, work.pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
				observeActivationRuntime(working, mutator, work.activation, paneID, c.runtime.Warn)
			}
			if err != nil {
				return err
			}
			if !work.target.virtual {
				c.runtime.EqualizeSplitLayout(ctx, anchorPaneID, flags.placement)
			}
			// The managed-pane options are what make this pane an agent pane to
			// the statusbar, the attention tracker, and the notification
			// pipeline. They are applied after the pane exists and before the
			// result is reported.
			if usedNative {
				if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, c.runtime.Runner, paneID, workWorkspace.CWD, workTitle, "", nativeBinding.ThreadID); err != nil {
					return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", spelling, paneID, err)
				}
				if nativeLifecycleCapable {
					nativeLifecycleTargets = append(nativeLifecycleTargets, codexLifecycleObserverTarget{
						Identity: codexLifecycleIdentity{
							AgentUID: work.agent.Metadata.UID, PaneUID: work.pane.Metadata.UID, RuntimeID: paneID,
							Generation: work.activation.Generation, ThreadID: nativeBinding.ThreadID,
						},
						Route: c.runtime.Target, NativeRoute: nativeRoute,
					})
				}
			} else if err := c.bindAgentPane(ctx, paneID, provider, workWorkspace.CWD, workTitle,
				declaredPlainCodexLane(provider, flags, prompt), flags); err != nil {
				return fmt.Errorf("%s: bind Agent Pane %s presentation metadata: %w", spelling, paneID, err)
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
	for _, row := range nativeSessionHistory {
		c.recordCreateAgentSessionHistory(row, true, stderr)
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
	creator.report(stderr)
	receipt := createPlannedReceipt(coremetadata.KindAgent, results, selectedWindowUIDs)
	receipt.Profile = flags.profileLaunch.receipt()
	if mode != cli.OutputModeDefault && mode != cli.OutputModeReceipt {
		// These projections have no room for the disclosure, so it goes to
		// stderr instead of being dropped.
		if err := writeProfileDisclosure(stderr, receipt.Profile); err != nil {
			return err
		}
	}
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindAgent, results, receipt)
}
