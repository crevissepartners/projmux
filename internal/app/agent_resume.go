package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// agentResumeLauncher is the provider-launch seam of `agent resume`.
//
// It is deliberately a *different* interface from agentLauncher even though one
// object satisfies both, because the two verbs must never share a launch
// construction. agentLauncher.PlanAgentLaunch builds the argv that starts a
// fresh conversation; PlanAgentResume builds the provider's resume argv from a
// conversation id and has no way to produce a fresh-start argv at all. Keeping
// them apart is what makes "resume never falls through to create" a property of
// the type system rather than of a code review.
type agentResumeLauncher interface {
	// RequireAgentEnabled applies the Settings enabled-agents gate. It is the
	// same gate `create agent` runs: switching a provider off in Settings does
	// not become bypassable by resuming instead of creating.
	RequireAgentEnabled(provider string) error
	// PlanAgentResume builds the provider's *resume* launch for one stored
	// conversation id. It creates nothing, so every failure it can report --
	// a malformed conversation id, an unknown provider, a missing provider
	// binary -- costs zero mutations and zero tmux objects. annotations are the
	// resumed Agent's own metadata annotations, passed through unread: the
	// seam alone decides what they mean for the launch (the persona).
	PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

// agentResumeModelLauncher is the optional seam of an `agent resume --model`.
// PlanAgentResumeWithModel is PlanAgentResume with the model passed once in
// the launch argv; an empty model is exactly PlanAgentResume. It is separate
// so the other resume consumers -- Continue replay and the resume picker --
// have no way to pass a model at all.
type agentResumeModelLauncher interface {
	PlanAgentResumeWithModel(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string, model string) (agentResumeLaunch, error)
}

var _ agentResumeModelLauncher = (*aiCommand)(nil)

// The aiCommand is the production implementation of both launch seams. The two
// methods below live here rather than beside PlanAgentLaunch so this Phase adds
// the resume seam without editing the file that owns the create seam.
var _ agentResumeLauncher = (*aiCommand)(nil)





















// agentResumePlan is one preflighted rebind: everything `agent resume` fixed
// from the read-only registry before it opened the store.
type agentResumePlan struct {
	codexTransfer     *codexNativeRebindTransfer
	dialogueReplyOnly bool
	agentUID          string
	agentName         string
	// provider is the union discriminator of the stored ref, which is the
	// provider whose resume argv will be built.
	provider string
	// conversationID is the identifier the provider's resume argv addresses.
	conversationID string
	// ref is the stored pointer, kept whole so the transaction can prove the
	// conversation did not change between the preflight read and the lock.
	ref *coremetadata.AgentSessionRef
	// The owner chain the new managed Pane is materialized into.
	projectUID  string
	projectRoot string
	// project is the owner Project as the plan read it, whose name and labels
	// are the variables of its label link rules.
	project   coremetadata.Project
	workspace coremetadata.AgentWorkspace
	topic     string
	windowUID string
	anchorUID string
	// shared names the other Agents that record the same conversation, in uid
	// order. It is disclosed, never decisive: see planAgentResume.
	shared []string
	// annotations are the Agent's own, handed to the resume seam unread.
	annotations map[string]string
	// modelOverride and effortOverride are `agent resume --model/--effort`,
	// both empty on every other rebind. Both are recorded on the Agent by the
	// rebind transaction and launched with. Later plain resumes re-pass the
	// effort but not the model, which the conversation owns.
	modelOverride  string
	effortOverride string
	// overrideSource is where the overrides came from, recorded beside them:
	// coremetadata.SettingSourceRelaunch for `agent relaunch`, and
	// coremetadata.SettingSourceResume (the empty value) for `agent resume`.
	overrideSource string
	// layerChanges are the changes `agent relaunch` makes to the layers
	// themselves: --profile, --instructions and --reset. Empty on every other
	// rebind.
	layerChanges agentSettingsRequest
	// stoppedPane is the managed Pane a restart closed right before this
	// rebind. Empty on every other rebind.
	stoppedPane stoppedAgentPane
	// retiredProcess is supplied only by host relaunch after exact supervisor Wait.
	retiredProcess *processResumeCandidate
	virtualWindow  bool
}

// settingsRequest is what this rebind asks of the Agent's layers.
func (p agentResumePlan) settingsRequest() agentSettingsRequest {
	request := p.layerChanges
	request.projectUID = p.projectUID
	request.model, request.effort, request.source = p.modelOverride, p.effortOverride, p.settingSource()
	return request
}

// settingSource is the source the rebind transaction records beside the
// model and effort overrides.
func (p agentResumePlan) settingSource() string {
	if p.overrideSource == "" {
		return coremetadata.SettingSourceResume
	}
	return p.overrideSource
}

// launchAnnotations are the annotations the resume seam reads: the Agent's
// own, with the effort override in place of the recorded effort. That is the
// value the rebind transaction records, so the launch reads what the Agent
// will record. Without an override they are the Agent's own map itself.
func (p agentResumePlan) launchAnnotations() map[string]string {
	if p.effortOverride == "" {
		return p.annotations
	}
	out := maps.Clone(p.annotations)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentEffort] = p.effortOverride
	return out
}





// agentRebinder materializes the new managed Pane of a resumed Agent.
//
// It holds the create command rather than re-deriving its plumbing: the
// transaction order, the runtime ledger, the rollback, the Project runtime
// ensure and the anchor resolution are the ones `create` ships, and a second
// implementation of any of them would be a second set of bugs. What it does not
// share is the metadata half. `create agent` mints an Agent; this route never
// calls CreateAgent at all, so the uid and metadata.name of the resumed Agent
// cannot change no matter what happens on the runtime side.
type agentRebinder struct {
	create           *createCommand
	launcher         agentResumeLauncher
	resolveWorkspace func(string, coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
}

func newAgentRebinder(create *createCommand, launcher agentResumeLauncher) *agentRebinder {
	return &agentRebinder{create: create, launcher: launcher, resolveWorkspace: resolveAgentWorkspaceFor}
}

// rebind attaches a new managed Pane, launched with the provider's resume argv,
// to the Agent the plan named.
//
// The launch is constructed before the store is opened, which is what makes the
// route's central guarantee measurable: on every failure path the number of
// split-window calls issued is zero, so no conversation -- neither the stored one
// nor a fresh one -- is ever started by a failed resume.
func (r *agentRebinder) rebind(spelling string, plan agentResumePlan, stdout, stderr io.Writer) error {
	if r == nil || r.create == nil || r.launcher == nil {
		return errors.New(spelling + ": the resume materialization seam is not configured")
	}

	// The Settings gate runs before anything else the store would see, exactly
	// as on `create agent`.
	if err := r.launcher.RequireAgentEnabled(plan.provider); err != nil {
		return err
	}
	// Resume is selector-authoritative, so it binds the app's logical route
	// without an anchor Pane. `agent resume <ref>` has already resolved one
	// exact Agent through the selector engine's exact-one cell, and planAgentResume
	// has proven that Agent's owning Window and that Window's stored
	// anchorPaneRef; the split target is that stored anchor, not whatever Pane
	// the operator happens to be standing in. Requiring an inherited or typed
	// `%N` on top of it blocked the one invocation resume exists for -- reviving
	// an Agent whose own Pane is, by definition, already gone -- and it blocked
	// it with a containment refusal about a socket and a server that were both
	// correct. Physical socket, ownership marker, server generation and the
	// typed object guards below are all unchanged: what is dropped is only the
	// requirement that the caller supply an anchor Pane by typing or inheriting
	// one. A standalone server still gets none of this: it keeps requiring the
	// inherited $/@/% receipt.
	r.create.selectRuntimeAuthority(true)
	// The provider resume argv is the only argv this route can produce. If the
	// stored conversation id cannot be rendered into one -- malformed, wrong
	// shape for the provider, provider binary absent -- the route stops here,
	// with the store still unopened.
	contextDir := plan.workspace.CWD
	if contextDir == "" {
		contextDir = plan.projectRoot
	}
	workspace := plan.workspace
	workspace.CWD = contextDir
	nativeLauncher, nativeLaunchCapable := r.launcher.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := r.launcher.(codexNativeLifecycleStarter)
	var nativeRoute codexNativeEndpointRoute
	var title string
	var launchArgv []string
	var personaNotice, effortNotice, linksNotice, guidanceNotice, settingsNotice string
	var resumed agentResumeLaunch
	// settings are the layered settings this launch runs with: the
	// overrides, then the profile as it is now (agentsettings.Resolve). The
	// rebind transaction records them.
	var settings agentSettingsLaunch
	var links projectLinksLaunch
	var guidance agentGuidanceLaunch
	var nativePolicy codexappserver.ThreadPolicy
	var err error
	if plan.provider == aiModeCodex {
		// The profile the Agent records is re-read by name before anything
		// else, so a profile that is gone or invalid refuses with zero writes
		// and zero provider calls. Its current sandbox and approval ride the
		// thread/resume below, and resumed carries the digest the transaction
		// records -- the same record a Claude resume makes.
		settings, err = r.resolveSettings(plan.provider, plan.annotations, plan.settingsRequest())
		if err == nil {
			// A relaunch that switches the profile resumes with the new one.
			resumed, nativePolicy, err = r.create.codexResumeProfile(settings.launchAnnotations(plan.annotations), plan.projectUID)
		}
		if err == nil {
			nativeCtx, cancel := prepareNativeContext(context.Background())
			nativeRoute, err = resolveCodexNativeResumeRoute(nativeCtx, r.create.codexNative, plan.ref, "uid:"+plan.agentUID)
			cancel()
			if err != nil {
				return nativeResumePreparationRefusal(spelling, err)
			}
			if !nativeLaunchCapable {
				return nativeResumePreparationRefusal(spelling, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable})
			}
			effort, invalid, skipped := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
			resumed.effortInvalid, resumed.effortSkipped = invalid, skipped
			title, launchArgv, err = planNativeCodexResumeOptions(nativeLauncher, nativeRoute, workspace, plan.conversationID, settings.model(plan.modelOverride), effort)
			effortNotice = resumed.effortNotice(plan.agentName)
			settingsNotice = settings.notice(plan.agentName)
		}
	} else {
		if plan.dialogueReplyOnly && plan.annotations[coremetadata.AnnotationAgentProfile] != "" {
			// The reply-only launch is fixed and cannot carry a profile's
			// permissions, so it is refused rather than resumed without them.
			err = &profileResumeError{name: plan.annotations[coremetadata.AnnotationAgentProfile], reason: profileReasonLaneUnsupported,
				detail: "the reply-only activation cannot carry a profile"}
		} else if plan.dialogueReplyOnly {
			launcher, ok := r.launcher.(claudeDialogueLauncher)
			if !ok {
				return errors.New("claude reply-only resume launcher is unavailable")
			}
			title, launchArgv, err = launcher.PlanClaudeDialogueLaunch(workspace, plan.conversationID)
		} else {
			// The Project's current label link rules are read from the Project
			// that owns the Agent's Window. Rules that differ from the recorded
			// digest launch with the snapshot off, and the transaction below
			// records exactly the digest and mode this launch reads.
			links = planProjectLinksWith(r.launcher, plan.provider, plan.project, plan.annotations)
			// The agent guidance is compared with its recorded digest the same
			// way, and launched and recorded the same way.
			guidance = planAgentGuidanceWith(r.launcher, plan.provider, plan.annotations)
			// The settings are resolved from the layers. A profile that is
			// gone or invalid leaves them unlayered, so the seam below
			// refuses the resume exactly as it did before layers existed.
			if resolved, resolveErr := r.resolveSettings(plan.provider, plan.annotations, plan.settingsRequest().withPromptParts(guidance, links)); resolveErr == nil {
				settings = resolved.writeSnapshot()
			} else if plan.layerChanges.changesLayers() || profile.ReasonOf(resolveErr) == profile.ReasonOutOfScope {
				// A change to the layers is never launched without them.
				err = resolveErr
			}
			launchAnnotations := guidance.resumeLaunchAnnotations(links.resumeLaunchAnnotations(settings.launchAnnotations(plan.launchAnnotations())))
			switch model := settings.model(plan.modelOverride); {
			case err != nil:
			case settings.snapshotErr != nil:
				err = settings.snapshotErr
			case model != "":
				launcher, ok := r.launcher.(agentResumeModelLauncher)
				if !ok {
					return errors.New(spelling + ": the resume launcher cannot pass --model")
				}
				resumed, err = launcher.PlanAgentResumeWithModel(plan.provider, workspace, plan.conversationID, launchAnnotations, model)
			default:
				resumed, err = r.launcher.PlanAgentResume(plan.provider, workspace, plan.conversationID, launchAnnotations)
			}
			title, launchArgv = resumed.title, resumed.argv
			personaNotice, effortNotice = resumed.personaNotice(plan.agentName), resumed.effortNotice(plan.agentName)
			linksNotice = cmp.Or(links.notice(plan.agentName), resumed.projectLinksNotice(plan.agentName))
			guidanceNotice = cmp.Or(guidance.notice(plan.agentName), resumed.agentGuidanceNotice(plan.agentName))
			settingsNotice = settings.notice(plan.agentName)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w",
			spelling, plan.agentName, plan.provider, plan.conversationID, err)
	}
	var reservedNative *codexNativeReservedInit
	if plan.codexTransfer != nil {
		initCtx, cancel := prepareNativeContext(context.Background())
		effort, _, _ := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
		reservedNative, err = plan.codexTransfer.initialize(initCtx, r, plan, workspace, settings, links, guidance, resumed, codexappserver.ThreadSettings{Model: settings.model(plan.modelOverride), Effort: effort, Policy: nativePolicy})
		cancel()
		if err != nil {
			return nativeLaunchError(spelling, err)
		}
	}
	var nativeLifecycleTargetAfterCommit codexLifecycleObserverTarget

	for _, other := range plan.shared {
		if _, err := fmt.Fprintf(stderr,
			"projmux: conversation %s:%s is also recorded on %s; %s rebinds only agent/%s\n",
			plan.provider, plan.conversationID, other, spelling, plan.agentName); err != nil {
			return err
		}
	}

	nameReason := ""
	if err := r.create.transact(diagnostics.CreateKindResume, func(
		ctx context.Context,
		working *coremetadata.Registry,
		mutator coremetadata.Mutator,
		operationID string,
		ledger *RuntimeLedger,
	) error {
		nameReason = ""
		agent, ok := working.Agent(plan.agentUID)
		if !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.agentUID)
		}
		if plan.retiredProcess != nil {
			if plan.virtualWindow {
				if err := validateRetiredProcessForTmux(working, *plan.retiredProcess); err != nil {
					return err
				}
			} else if err := retireProcessPaneForTmux(working, mutator, *plan.retiredProcess); err != nil {
				return err
			}
			agent, _ = working.Agent(plan.agentUID)
		}
		// The preflight ran against a read-only snapshot and the reconciler has
		// since run inside this transaction. Re-checking the two facts the plan
		// rests on -- the Agent is still resumable, and it is still the same
		// conversation -- is what stops a concurrent hook or transition from
		// turning this into a rebind of something else.
		if reservedNative != nil {
			reserved, present := working.Pane(reservedNative.Pane.Metadata.UID)
			if !present || agent.Status.Phase != coremetadata.PhasePending || agent.Status.PaneRef != reservedNative.Pane.Metadata.UID || !sameHostTransferSpec(agent.Spec, reservedNative.Expected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, reservedNative.Expected.Metadata.Annotations) || !reflect.DeepEqual(agent.Status.SessionRef, reservedNative.Expected.Status.SessionRef) || !reflect.DeepEqual(reserved.Status.Activation, reservedNative.Pane.Status.Activation) {
				return errors.New(spelling + ": native transfer reservation changed before commit")
			}
		} else if err := requireResumablePhase(spelling, agent); err != nil {
			return err
		}
		if !agent.Status.SessionRef.SameConversation(plan.ref) {
			return fmt.Errorf(
				"%s: agent/%s now points at a different conversation than the one this resume planned; re-run it",
				spelling, plan.agentName)
		}
		project, ok := working.Project(plan.projectUID)
		if !ok {
			return fmt.Errorf("%s: project %q disappeared before the rebind ran", spelling, plan.projectUID)
		}
		resolver := r.resolveWorkspace
		if resolver == nil {
			resolver = resolveAgentWorkspaceFor
		}
		workspace, err := resolver(spelling, *working, *project, plan.provider, agent.Spec.Workspace.CWD, agent.Spec.Workspace.AdditionalWritableRoots)
		if err != nil {
			return err
		}
		if workspace.CWD != plan.workspace.CWD || !slices.Equal(workspace.AdditionalWritableRoots, plan.workspace.AdditionalWritableRoots) {
			return fmt.Errorf("%s: agent/%s workspace changed after preflight; re-run it", spelling, plan.agentName)
		}
		// Persist the normalized effective workspace for legacy Agents whose
		// pre-Phase6 spec was empty, before any runtime object is created.
		agent.Spec.Workspace = workspace
		// The layered settings are recorded with their sources in the same
		// transaction, before any runtime object exists: the profile a
		// relaunch switched to, the effort, the model when it was passed, and
		// the instructions with the snapshot mode off when their content
		// changed. A failure anywhere later rolls the whole transaction back,
		// so a resume that does not launch records neither value nor source.
		if err := settings.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// The profile this launch re-applied is recorded with the digest of
		// the content it applied, in the same transaction.
		if err := recordResumedProfileDigest(working, mutator, plan.agentUID, resumed); err != nil {
			return err
		}
		// So are the Project label link rules this launch reads, when they
		// changed, with the sticky snapshot mode off.
		if err := links.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// And the agent guidance, the same way.
		if err := guidance.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// The reply-only activation is recorded, so every later resume of
		// the Agent launches it again. An Agent that records it already is
		// left as it was.
		if plan.dialogueReplyOnly {
			if _, err := mutator.SetAgentDialogueReplyOnly(working, plan.agentUID); err != nil {
				return MapMetadataError(err)
			}
		}
		// An unlayered launch records only its overrides, as before.
		if plan.effortOverride != "" && !settings.layered {
			if _, err := mutator.SetAgentEffort(working, plan.agentUID, plan.effortOverride, plan.settingSource()); err != nil {
				return MapMetadataError(err)
			}
		}
		if plan.modelOverride != "" && !settings.layered {
			if _, err := mutator.SetAgentModel(working, plan.agentUID, plan.modelOverride, plan.settingSource()); err != nil {
				return MapMetadataError(err)
			}
		}

		// Name handoff. The new managed Pane carries the non-automatic name of
		// the Agent's old Pane row that selectAgentPaneNameHandoff picks, and
		// that row is released first so the name has exactly one holder; with no
		// such row and no restart, it gets the `<agent>-pane` name `create agent`
		// gives. A name that cannot be carried is disclosed and never refuses
		// the resume.
		paneName, reason := "", ""
		if reservedNative == nil {
			paneName, reason = r.handOffResumedPaneName(ctx, working, mutator, *agent, plan)
		}
		nameReason = reason
		if agent, ok = working.Agent(plan.agentUID); !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.agentUID)
		}

		// Metadata phase. AttachAgentPane creates the managed Pane owned by this
		// existing Agent and moves it Offline/Failed -> Running through the
		// closed transition table. No Agent is created and no name is allocated
		// for one, so the uid and metadata.name are structurally untouchable
		// here.
		var pane coremetadata.Pane
		var attachReason string
		if reservedNative != nil {
			observed, present := working.Pane(reservedNative.Pane.Metadata.UID)
			if !present {
				return errors.New(spelling + ": native reserved Pane disappeared")
			}
			pane = observed.Clone()
			if _, err = mutator.TransitionAgent(working, plan.agentUID, coremetadata.PhaseRunning, ""); err != nil {
				return err
			}
		} else {
			pane, attachReason, err = attachAgentPaneWithName(working, mutator, plan.agentUID, contextDir, paneName, operationID)
		}
		if err != nil {
			return MapMetadataError(err)
		}
		if attachReason != "" {
			nameReason = attachReason
		}

		virtual := plan.retiredProcess != nil && plan.virtualWindow
		if plan.retiredProcess != nil && plan.virtualWindow {
			if err := mutator.DeletePane(working, plan.retiredProcess.Pane.Metadata.UID); err != nil {
				return err
			}
		}
		// Runtime phase, on the create routes' own materializer.
		var sessionName, anchorPaneID string
		if !virtual {
			sessionName, err = r.create.ensureProjectRuntime(ctx, working, mutator, *project, operationID, ledger)
			if err != nil {
				return err
			}
			anchorPaneID, err = r.create.ensureAnchorPane(ctx, working, mutator, ledger, *project, sessionName, operationID, paneTarget{
				windowUID:    plan.windowUID,
				anchorUID:    plan.anchorUID,
				storedAnchor: true,
			})
			if err != nil {
				return err
			}
		}
		// A resume is a new materialization of the same Agent, so it issues a
		// fresh generation. That is what makes a late receipt from the process
		// this resume replaced recognizable as stale instead of being applied
		// to the Pane the operator is now looking at.
		var activation superviseSpec
		if reservedNative != nil {
			activation = reservedNative.Activation
		} else {
			activation, err = r.create.issuePaneActivation(working, mutator, pane.Metadata.UID, plan.agentUID, operationID)
		}
		if err != nil {
			return err
		}
		activation.DialogueReplyOnly = plan.dialogueReplyOnly

		workTitle := title
		workLaunchArgv := launchArgv
		usedNative := false
		nativeThreadID := ""
		if plan.provider == aiModeCodex {
			if routeErr := validateCodexNativeResumeRoute(agent.Status.SessionRef, nativeRoute); routeErr != nil {
				return nativeResumePreparationRefusal(spelling, bindCodexResumeAgentRef(routeErr, "uid:"+plan.agentUID))
			}
			nativeCtx, cancel := prepareNativeContext(ctx)
			// The thread runs its later turns with what the launch argv
			// carries, the model and the effort, and with the profile's
			// policy: a thread another client still holds keeps its own
			// settings on resume, so they are applied to it as well. A thread
			// that still answers with others is a *PolicyMismatchError or a
			// *SettingsMismatchError, never a safe fallback: it lands in the
			// default arm and the whole transaction rolls back.
			model := settings.model(plan.modelOverride)
			effort, _, _ := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
			var prepared codexappserver.ThreadBinding
			var nativeErr error
			nativeSettings := codexappserver.ThreadSettings{Model: model, Effort: effort, Policy: nativePolicy}
			if plan.codexTransfer != nil {
				prepared = reservedNative.Binding
				nativeErr = plan.codexTransfer.check(nativeCtx)
			} else {
				prepared, nativeErr = r.create.codexNative.Resume(nativeCtx, nativeRoute, workspace, plan.conversationID, nativeSettings)
			}
			cancel()
			switch {
			case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) == strings.TrimSpace(plan.conversationID):
				workTitle, workLaunchArgv, err = planNativeCodexResumeOptions(nativeLauncher, nativeRoute, workspace, prepared.ThreadID, model, effort)
				if err != nil {
					return nativeLaunchError(spelling, err)
				}
				// A stored ref from a retired generation (or a draining /
				// handover-pending marker) is moved onto the endpoint that just
				// resumed the thread, inside this transaction, so the committed
				// Agent names the default endpoint with a current lifecycle.
				if _, err := mutator.AdoptCodexResumeEndpoint(working, plan.agentUID, nativeRoute.Endpoint); err != nil {
					return MapMetadataError(err)
				}
				if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
					AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, Generation: activation.Generation,
					ThreadID: prepared.ThreadID, TurnID: prepared.TurnID, Endpoint: nativeRoute.Endpoint,
				}); err != nil {
					return MapMetadataError(err)
				}
				nativeThreadID = prepared.ThreadID
				usedNative = true
			case nativeErr == nil:
				return nativeLaunchError(spelling, fmt.Errorf("%w: native resume returned a different thread", codexappserver.ErrProtocol))
			case nativeFallbackAllowed(r.create.codexNative, nativeErr):
				return nativeResumePreparationRefusal(spelling, nativeErr)
			default:
				return nativeLaunchError(spelling, nativeErr)
			}
		}
		if virtual {
			pane.Spec.CWD = contextDir
		}
		var paneID string
		if virtual {
			paneID, err = r.create.materializeVirtualPane(ctx, working, mutator, ledger, *project, plan.windowUID, pane, activation, workLaunchArgv)
		} else {
			paneID, err = r.create.runtime.SplitPane(ctx, anchorPaneID, defaultPlacement, contextDir, r.create.runtime.SupervisedLaunch(ctx, activation, workLaunchArgv), nil)
		}
		if paneID != "" && !virtual {
			// The supervised child now runs and will want the Registry lock
			// this transaction holds; create.outcome measures the rest of the hold.
			markSupervisedSpawn(ctx)
			if claimErr := r.create.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := r.create.runtime.Mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, r.create.runtime.Warn)
		}
		if err != nil {
			return err
		}
		if usedNative {
			var transferGrant *codexbroker.NativeTransferGrant
			if plan.codexTransfer != nil {
				transferGrant, err = plan.codexTransfer.grant(ctx, *working, pane, activation, paneID)
				if err != nil {
					return err
				}
			}
			if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, r.create.runtime.Runner, paneID, contextDir, workTitle, plan.topic, nativeThreadID); err != nil {
				return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", spelling, paneID, err)
			}
			if nativeLifecycleCapable {
				nativeLifecycleTargetAfterCommit = codexLifecycleObserverTarget{
					TransferGrant: transferGrant,
					Identity: codexLifecycleIdentity{
						AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, RuntimeID: paneID,
						Generation: activation.Generation, ThreadID: nativeThreadID,
					},
					Route: r.create.runtime.Target, NativeRoute: nativeRoute,
				}
			}
		} else if err := r.launcher.BindAgentPaneOnRoute(ctx, r.create.runtime.Runner, agentPaneBinding{
			PaneID: paneID, Provider: plan.provider, ContextDir: contextDir, Title: workTitle,
			Topic: plan.topic, TopicManual: strings.TrimSpace(plan.topic) != "", ConversationID: plan.conversationID,
		}); err != nil {
			return fmt.Errorf("%s: bind resumed Agent Pane %s presentation metadata: %w", spelling, paneID, err)
		}
		return nil
	}, r.create.exactProjectOwnershipGuard(plan.projectUID)); err != nil {
		return err
	}
	if nativeLifecycleTargetAfterCommit.valid() {
		startup := nativeLifecycle.startNativeCodexLifecycleObserver(nativeLifecycleTargetAfterCommit)
		if plan.codexTransfer != nil {
			if startup.Status != codexObserverStartupReady {
				return errors.New(spelling + ": native transfer observer is not ready; recovery remains fenced")
			}
			if err := plan.codexTransfer.complete(nativeLifecycleTargetAfterCommit); err != nil {
				return err
			}
		}
	}
	if nameReason != "" {
		// A lost disclosure must not turn a committed resume into a failure.
		fmt.Fprintln(stderr, agentPaneNameNotice(plan.agentName, nameReason))
	}
	if personaNotice != "" {
		// The Agent is back without its persona; like the name notice, a lost
		// disclosure must not turn a committed resume into a failure.
		fmt.Fprintln(stderr, personaNotice)
	}
	if effortNotice != "" {
		// The Agent is back without the effort it recorded, for the same reason
		// and with the same best-effort disclosure.
		fmt.Fprintln(stderr, effortNotice)
	}
	if linksNotice != "" {
		// The Agent is back without its Project's label link rules, disclosed
		// the same way.
		fmt.Fprintln(stderr, linksNotice)
	}
	if guidanceNotice != "" {
		// The Agent is back without the agent guidance, disclosed the same
		// way.
		fmt.Fprintln(stderr, guidanceNotice)
	}
	if settingsNotice != "" {
		// The Agent is back with its recorded instructions instead of the
		// ones its layers ask for, disclosed the same way.
		fmt.Fprintln(stderr, settingsNotice)
	}

	_, err = fmt.Fprintf(stdout, "agent/%s resumed\n", plan.agentName)
	return err
}

// stoppedAgentPane is the managed Pane a restart closed: its uid and its
// non-automatic name, "" when it was named by its own UID.
type stoppedAgentPane struct {
	uid  string
	name string
}

// handOffResumedPaneName chooses the name the resumed Agent's new Pane carries
// and releases the old Pane row that held it.
//
// A restart's stopped Pane comes first: its name is carried once the stop has
// dropped its row.
//
// Resume keeps an Offline Agent's old Pane rows as evidence, and each row still
// holds its name reservation. The candidates are the rows proven live nowhere
// by the same server-wide owner inventory Continue replay uses; only the
// selected source row is released, and only when releasing it leaves the
// Window's stored anchor where it is. Every outcome that
// carries no non-automatic name comes back as a reason instead of an error.
//
// When there is neither a stopped Pane nor any candidate row -- a resume after
// `delete pane` dropped the old row -- the name is derivedAgentPaneName's
// `<agent>-pane`, the one `create agent` gives. A candidate row that cannot be
// carried keeps its reason and the automatic name.
func (r *agentRebinder) handOffResumedPaneName(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	agent coremetadata.Agent,
	plan agentResumePlan,
) (string, string) {
	if stopped := plan.stoppedPane; stopped.name != "" {
		if _, ok := working.Pane(stopped.uid); !ok {
			// The restart's stop dropped the row and released its name, so
			// no old row holds it. A holder that took it since is disclosed
			// by attachAgentPaneWithName.
			return stopped.name, ""
		}
	}
	var owned []coremetadata.Pane
	named := false
	for _, pane := range working.PanesOf(agent.Metadata.UID) {
		if pane.Metadata.UID == agent.Status.PaneRef {
			continue
		}
		owned = append(owned, pane)
		named = named || agentPaneNameCandidate(pane, agent.Metadata.UID)
	}
	if !named {
		// Nothing could be carried, so no runtime inventory is taken. With no
		// restart behind it either, the new Pane gets the name `create agent`
		// gives an Agent's Pane; attachAgentPaneWithName discloses a holder
		// or an invalid name and falls back to the automatic one.
		if plan.stoppedPane.uid == "" {
			return derivedAgentPaneName(agent.Metadata.Name), ""
		}
		return "", ""
	}
	guard, err := newTopologyOwnerGuard(ctx, r.create.runtime)
	if err != nil {
		return "", fmt.Sprintf("old Pane rows could not be proven live nowhere on this socket: %v", err)
	}
	var nonLive []string
	var liveCandidate error
	for _, pane := range owned {
		if claimErr := guard.requireSolePaneUID(pane.Metadata.UID, "", plan.agentName); claimErr != nil {
			if liveCandidate == nil && agentPaneNameCandidate(pane, agent.Metadata.UID) {
				liveCandidate = claimErr
			}
			continue
		}
		nonLive = append(nonLive, pane.Metadata.UID)
	}
	handoff := selectAgentPaneNameHandoff(*working, agent, nonLive)
	if handoff.name == "" {
		if handoff.reason == "" && liveCandidate != nil {
			return "", liveCandidate.Error()
		}
		return "", handoff.reason
	}
	window, ok := working.Window(plan.windowUID)
	if !ok {
		return "", fmt.Sprintf("window %q is not in the Registry", plan.windowUID)
	}
	anchor := window.Spec.AnchorPaneRef
	before := working.Clone()
	if err := mutator.DeletePane(working, handoff.sourceUID); err != nil {
		*working = before
		return "", fmt.Sprintf("old pane/%s (uid:%s) could not be released: %v", handoff.name, handoff.sourceUID, err)
	}
	// The rebind splits the Window's stored anchor, so a release that re-anchored
	// the Window would turn a name into a refused resume. Keep the row instead.
	if window, ok := working.Window(plan.windowUID); !ok || window.Spec.AnchorPaneRef != anchor {
		*working = before
		return "", fmt.Sprintf("releasing old pane/%s (uid:%s) would move window %s's anchor", handoff.name, handoff.sourceUID, plan.windowUID)
	}
	return handoff.name, ""
}
