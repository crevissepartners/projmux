package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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

func (p agentResumePlan) launchAnnotations() map[string]string {
	return p.sharedPlan().LaunchAnnotations()
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
	settingsApplication func(*agentSettingsApplication) // optional invocation-local value receiver
	create              *createCommand
	launcher            agentResumeLauncher
	resolveWorkspace    func(string, coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
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
	var managedInstructions *codexappserver.ManagedInstructions
	var err error
	if plan.provider == aiModeCodex {
		prepared, route, nativeSettings, prepareErr := mutationruntime.PrepareNativeCodexResume(context.Background(), spelling, plan.sharedPlan(), r.create.processCreatePaths, func(ctx context.Context, ref *coremetadata.AgentSessionRef, agentRef string) (mutationruntime.NativeCodexRoute, error) {
			ctx, cancel := prepareNativeContext(ctx)
			defer cancel()
			route, err := resolveCodexNativeResumeRoute(ctx, r.create.codexNative, ref, agentRef)
			if err == nil && !nativeLaunchCapable {
				err = &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
			}
			return route.shared(), err
		}, func(route mutationruntime.NativeCodexRoute, w coremetadata.AgentWorkspace, id, model, effort string) (string, []string, error) {
			return planNativeCodexResumeOptions(nativeLauncher, localNativeCodexRoute(route), w, id, model, effort)
		})
		if prepareErr != nil {
			var profileErr *mutationruntime.ProfileResumeError
			if errors.As(prepareErr, &profileErr) {
				return fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w", spelling, plan.agentName, plan.provider, plan.conversationID, localResumeError(profileErr))
			}
			return prepareErr
		}
		nativeRoute = localNativeCodexRoute(route)
		title, launchArgv = prepared.Title, prepared.Argv
		resumed = localAgentResumeLaunch(prepared.Resumed)
		settings = localAgentSettingsLaunch(prepared.Settings)
		nativePolicy = nativeSettings.Policy
		managedInstructions = nativeSettings.ManagedInstructions
		if settings.managedBase != nil {
			guidanceNotice = settings.managedBase.Notice()
		}
		effortNotice = resumed.effortNotice(plan.agentName)
		settingsNotice = settings.notice(plan.agentName)
	} else {
		prepared, prepareErr := r.sharedResumePreparation().Prepare(spelling, plan.sharedPlan())
		if prepareErr != nil {
			return prepareErr
		}
		title, launchArgv = prepared.Title, prepared.Argv
		resumed = localAgentResumeLaunch(prepared.Resumed)
		settings = localAgentSettingsLaunch(prepared.Settings)
		links = localProjectLinksLaunch(prepared.Links)
		guidance = localAgentGuidanceLaunch(prepared.Guidance)
		personaNotice, effortNotice = resumed.personaNotice(plan.agentName), resumed.effortNotice(plan.agentName)
		linksNotice = cmp.Or(links.notice(plan.agentName), resumed.projectLinksNotice(plan.agentName))
		guidanceNotice = cmp.Or(guidance.notice(plan.agentName), resumed.agentGuidanceNotice(plan.agentName))
		settingsNotice = settings.notice(plan.agentName)
	}
	if err != nil {
		return fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w",
			spelling, plan.agentName, plan.provider, plan.conversationID, err)
	}
	var reservedNative *codexNativeReservedInit
	if plan.codexTransfer != nil {
		initCtx, cancel := prepareNativeContext(context.Background())
		effort, _, _ := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
		reservedNative, err = plan.codexTransfer.initialize(initCtx, r, plan, workspace, settings, links, guidance, resumed, codexappserver.ThreadSettings{ManagedInstructions: managedInstructions, Model: settings.model(plan.modelOverride), Effort: effort, Policy: nativePolicy})
		cancel()
		if err != nil {
			return nativeLaunchError(spelling, err)
		}
	}
	var nativeLifecycleTargetAfterCommit codexLifecycleObserverTarget

	for _, notice := range plan.sharedPlan().SharedConversationNotices(spelling) {
		if _, err := fmt.Fprintln(stderr, notice); err != nil {
			return err
		}
	}

	var out mutationruntime.AgentResumeResult
	machine := &mutationruntime.AgentResumeExecution{Execution: r.create.createExecution(), ResolveWorkspace: r.resolveWorkspace, Layered: settings.layered, Reserved: reservedNative != nil,
		Record: func(working *coremetadata.Registry, mutator coremetadata.Mutator, uid string) error {
			prepared := mutationruntime.PreparedAgentResume{Settings: settings.shared(), Resumed: resumed.shared(), Links: links.shared(), Guidance: guidance.shared()}
			return prepared.Record(working, mutator, plan.agentUID)
		},
		Handoff: func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, agent coremetadata.Agent, _ mutationruntime.AgentResumePlan) (string, string) {
			return r.handOffResumedPaneName(ctx, working, mutator, agent, plan)
		},
		MaterializeVirtual: r.create.materializeVirtualPane,
	}
	machine.Retire = func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if plan.retiredProcess != nil {
			if plan.virtualWindow {
				if err := validateRetiredProcessForTmux(working, *plan.retiredProcess); err != nil {
					return err
				}
			} else if err := retireProcessPaneForTmux(working, mutator, *plan.retiredProcess); err != nil {
				return err
			}
		}
		return nil
	}
	if reservedNative != nil {
		machine.ValidateReservation = func(working *coremetadata.Registry, agent *coremetadata.Agent) error {
			if reservedNative != nil {
				reserved, present := working.Pane(reservedNative.Pane.Metadata.UID)
				if !present || agent.Status.Phase != coremetadata.PhasePending || agent.Status.PaneRef != reservedNative.Pane.Metadata.UID || !sameHostTransferSpec(agent.Spec, reservedNative.Expected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, reservedNative.Expected.Metadata.Annotations) || !reflect.DeepEqual(agent.Status.SessionRef, reservedNative.Expected.Status.SessionRef) || !reflect.DeepEqual(reserved.Status.Activation, reservedNative.Pane.Status.Activation) {
					return errors.New(spelling + ": native transfer reservation changed before commit")
				}
			}
			return nil
		}
		machine.Attach = func(working *coremetadata.Registry, mutator coremetadata.Mutator) (coremetadata.Pane, error) {
			var pane coremetadata.Pane
			if reservedNative != nil {
				observed, present := working.Pane(reservedNative.Pane.Metadata.UID)
				if !present {
					return coremetadata.Pane{}, errors.New(spelling + ": native reserved Pane disappeared")
				}
				pane = observed.Clone()
				if _, err = mutator.TransitionAgent(working, plan.agentUID, coremetadata.PhaseRunning, ""); err != nil {
					return coremetadata.Pane{}, err
				}
			}
			return pane, err
		}
		machine.Activation = func() superviseSpec { return reservedNative.Activation }
	}
	machine.BeforeRuntime = func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if plan.retiredProcess != nil && plan.virtualWindow {
			if err := mutator.DeletePane(working, plan.retiredProcess.Pane.Metadata.UID); err != nil {
				return err
			}
		}
		return nil
	}
	usedNative := false
	nativeThreadID := ""
	if plan.provider == aiModeCodex {
		effort, _, _ := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
		native := &mutationruntime.NativeCodexResumeExecution{ManagedBase: settings.managedBase, Spelling: spelling, AgentUID: plan.agentUID, ConversationID: plan.conversationID, Route: nativeRoute.shared(), SafeFallback: func(err error) bool { return nativeFallbackAllowed(r.create.codexNative, err) }, Settings: codexappserver.ThreadSettings{ManagedInstructions: managedInstructions, Model: settings.model(plan.modelOverride), Effort: effort, Policy: nativePolicy},
			Resume: func(ctx context.Context, route mutationruntime.NativeCodexRoute, w coremetadata.AgentWorkspace, id string, settings codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
				if plan.codexTransfer != nil {
					return reservedNative.Binding, plan.codexTransfer.check(ctx)
				}
				return r.create.codexNative.Resume(ctx, localNativeCodexRoute(route), w, id, settings)
			}, Plan: func(route mutationruntime.NativeCodexRoute, w coremetadata.AgentWorkspace, id, model, effort string) (string, []string, error) {
				return planNativeCodexResumeOptions(nativeLauncher, localNativeCodexRoute(route), w, id, model, effort)
			}}
		machine.PrepareLaunch = func(ctx context.Context, working *coremetadata.Registry, mut coremetadata.Mutator, pane coremetadata.Pane, activation superviseSpec, w coremetadata.AgentWorkspace, title string, argv []string) (string, []string, error) {
			title, argv, err := native.PrepareLaunch(ctx, working, mut, pane, activation, w, title, argv)
			nativeThreadID = native.ThreadID
			usedNative = nativeThreadID != ""
			return title, argv, err
		}
	}
	machine.Bind = func(ctx context.Context, working *coremetadata.Registry, pane coremetadata.Pane, activation superviseSpec, paneID, contextDir, workTitle string) error {
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
	}
	if err := r.create.transact(diagnostics.CreateKindResume, machine.Operation(spelling, plan.sharedPlan(), title, launchArgv, &out), r.create.exactProjectOwnershipGuard(plan.projectUID)); err != nil {
		return err
	}
	nameReason := out.NameReason

	if nativeLifecycleTargetAfterCommit.valid() {
		if plan.codexTransfer != nil {
			if err := plan.codexTransfer.confirmManaged(nativeLifecycleTargetAfterCommit); err != nil {
				return err
			}
		}
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
	if settings.managedBase != nil {
		guidanceNotice = settings.managedBase.Notice()
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

	application := preparedAgentSettingsApplication(agentApplicationSource{modelArg: settings.model(plan.modelOverride), nativeReady: usedNative, replyOnly: plan.dialogueReplyOnly, provider: plan.provider, host: "tmux", settings: settings, resumed: resumed, guidance: guidance, links: links, workspace: workspace, annotations: plan.launchAnnotations()})
	if r.settingsApplication != nil {
		r.settingsApplication(application)
	} else {
		writeAgentSettingsNotice(stderr, application)
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

func (r *agentRebinder) handOffResumedPaneName(ctx context.Context, w *coremetadata.Registry, m coremetadata.Mutator, a coremetadata.Agent, p agentResumePlan) (string, string) {
	machine := mutationruntime.AgentResumeExecution{Execution: r.create.createExecution()}
	return machine.HandOffResumedPaneName(ctx, w, m, a, p.sharedPlan())
}
