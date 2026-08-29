package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
	// binary -- costs zero mutations and zero tmux objects.
	PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (title string, argv []string, err error)
	// BindResumedAgentPane applies the managed-agent pane options to a pane the
	// caller already created and seeds the live routing index with the resumed
	// conversation id.
	BindResumedAgentPane(paneID, provider, contextDir, title, conversationID string)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

// The aiCommand is the production implementation of both launch seams. The two
// methods below live here rather than beside PlanAgentLaunch so this Phase adds
// the resume seam without editing the file that owns the create seam.
var _ agentResumeLauncher = (*aiCommand)(nil)

// PlanAgentResume builds the provider resume launch for one stored conversation.
//
// It is the create seam's PlanAgentLaunch with exactly one substitution: the
// exec argv comes from the provider's own resume builder instead of the plain
// binary invocation. There is deliberately no fallback: if the provider cannot
// render a resume argv for this conversation id, the error is returned and the
// caller stops. Degrading to a fresh session here is what the interactive resume
// picker does (ai.go's runSelectedResumeSession), and it is precisely what this
// route must not do, because a resume that silently starts a new conversation
// loses the operator's context without telling them.
func (c *aiCommand) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	mode := normalizeAIMode(provider)
	resumeArgv, err := resumeArgsForAgent(mode, conversationID)
	if err != nil {
		return "", nil, err
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		// No displayMessage here: this route is detached and non-interactive, so
		// the diagnostic belongs on the error the caller propagates rather than
		// in a tmux status line the operator may not be looking at.
		return "", nil, errors.New(c.missingAgentRunnerMessage(mode))
	}
	resumeArgv[0] = agentBin
	// The workspace half of the argv comes from the same provider grammar the
	// create seam uses, so a provider whose option arity is written down once
	// cannot be spelled two ways. There is no payload on this route -- the
	// conversation id is the provider's own resume option, not an operand -- so
	// no option terminator participates.
	workspaceArgs, err := providerLaunchArgs(mode, coremetadata.AgentWorkspace{
		CWD:                     strings.TrimSpace(workspace.CWD),
		AdditionalWritableRoots: workspace.AdditionalWritableRoots,
	}, nil)
	if err != nil {
		return "", nil, err
	}
	resumeArgv = append(resumeArgv[:1], append(workspaceArgs, resumeArgv[1:]...)...)
	plan, err := c.planAgentLaunch(mode, workspace.CWD, nil, resumeArgv, filepath.Dir(agentBin))
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

// BindResumedAgentPane applies the managed-agent pane options to a resumed
// Agent's new pane.
//
// It differs from BindManagedAgentPane in one respect: the resume metadata is
// populated, so `@projmux_ai_session_id` -- the *live routing index* hook ingest
// scans to decide which live pane an incoming event belongs to -- carries the
// resumed conversation id from the moment the pane exists instead of only after
// the provider's first hook fires. The durable pointer on the Agent is a
// separate value and is not written here.
func (c *aiCommand) BindResumedAgentPane(paneID, provider, contextDir, title, conversationID string) {
	c.BindResumedAgentPaneWithSource(paneID, provider, contextDir, title, conversationID, "")
}

func (c *aiCommand) BindResumedAgentPaneWithSource(paneID, provider, contextDir, title, conversationID, source string) {
	c.configureAIPane(paneID, provider, contextDir, title, aiPaneResumeMetadata{
		sessionID: conversationID,
		resumeID:  conversationID,
		source:    strings.TrimSpace(source),
		updatedAt: c.nowTime().UTC(),
	})
}

func (c *aiCommand) BindResumedAgentPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title, conversationID string,
) error {
	return c.BindResumedAgentPaneWithSourceOnRoute(ctx, runner, paneID, provider, contextDir, title, conversationID, "")
}

func (c *aiCommand) BindResumedAgentPaneWithSourceOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title, conversationID, source string,
) error {
	return c.BindAgentPaneOnRoute(ctx, runner, agentPaneBinding{
		PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
		ConversationID: conversationID, ResumeSource: source,
	})
}

// agentResumePlan is one preflighted rebind: everything `agent resume` fixed
// from the read-only registry before it opened the store.
type agentResumePlan struct {
	agentUID  string
	agentName string
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
	workspace   coremetadata.AgentWorkspace
	topic       string
	windowUID   string
	anchorUID   string
	// shared names the other Agents that record the same conversation, in uid
	// order. It is disclosed, never decisive: see planAgentResume.
	shared []string
}

// planAgentResume fixes one rebind from the read-only registry.
//
// Nothing here mutates, opens the store, or calls tmux, so every refusal it can
// produce leaves zero transactions, zero tmux objects, and zero bytes on stdout.
//
// # What resume does when several Agents point at one conversation
//
// Phase 0 deliberately did not enforce "one conversation <-> at most one live
// Agent": the ref is a best-effort observation rather than a declaration, and
// enforcing uniqueness at write time deadlocks, because an Offline Agent holds
// its conversation forever and a later Agent observing the same conversation
// could then never record it. The consequence is that several Agents may point
// at one conversation, and this Phase owes that state a rule.
//
// The rule is: **the conversation is never a selector.** `agent resume <ref>`
// rebinds exactly the Agent the reference resolves to, and nothing in this route
// ever searches the registry by conversation id to decide *which* Agent to
// rebind. Duplicates therefore neither redirect the rebind nor block it. That is
// total (defined for every duplicate count), deterministic (it depends on the
// operator's reference and the selector's exact-one cardinality, not on registry
// order, map iteration, or observedAt), and it is the only rule that does not
// re-create Phase 0's deadlock somewhere else: refusing a duplicate at resume
// time would make a state Phase 0 declared legal permanently unusable.
//
// Duplicates are not silent, though. They are disclosed on stderr in uid order,
// so an operator who is about to run two live panes on one provider conversation
// finds that out from projmux rather than from the provider.
//
// The ambiguity this resolves is "which conversation-sharing Agent gets the new
// Pane". The ambiguity it does *not* resolve is "which Agent does a bare
// reference mean" -- that one is already answered upstream by the selector
// engine's <resume, Agent> exact-one cell, which refuses rather than guesses.
//
// # What resume does with observedAt
//
// Nothing. It is deliberately not consulted by any gate on this path.
// `observedAt` records when projmux last *saw* the conversation; it is not a
// timestamp the provider supplied and it says nothing about whether the provider
// still holds the conversation. A wall-clock staleness heuristic built on it
// would be wrong in both directions: a conversation untouched for a month is
// perfectly resumable, and a conversation observed a minute ago may already have
// been deleted. The only authority on whether a conversation can be revived is
// the provider, and reading the provider's own store to ask is permanently out
// of scope. So projmux checks everything it can see -- a ref exists, it names a
// known provider, that provider is enabled, its conversation id is well formed,
// its binary is installed -- and hands the rest to the provider's resume argv.
func planAgentResume(spelling string, registry coremetadata.Registry, agent *coremetadata.Agent) (agentResumePlan, error) {
	name := agent.Metadata.Name

	// (d) An Agent whose provider hook never ran has nothing to revive. This is
	// the most important refusal in the route: the tempting behavior is to start
	// a fresh conversation "since there is nothing to resume", and that is
	// exactly the silent context loss `create` and `resume` are separate verbs
	// to prevent. The message names the other verb rather than performing it.
	ref := agent.Status.SessionRef
	if ref.Empty() {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has no provider session ref, so there is no conversation to resume; "+
				"projmux records one the first time that Agent's provider hook fires. "+
				"To start a new conversation instead, run `projmux create agent --provider <provider>`, which mints a new Agent",
			spelling, name)
	}
	conversationID := strings.TrimSpace(ref.ConversationID())
	if conversationID == "" {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a %s session ref that carries no conversation id; it cannot be resumed",
			spelling, name, ref.Provider)
	}
	provider := strings.TrimSpace(ref.Provider)
	if provider == "" {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a session ref with no provider discriminator; it cannot be resumed", spelling, name)
	}
	// spec.provider is cross-checked only when the Agent declares one. Phase 0
	// deliberately kept an Agent whose provider never normalized recordable, and
	// punishing it here would undo that leniency.
	if declared := strings.TrimSpace(agent.Spec.Provider); declared != "" && declared != provider {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s is a %s Agent but its session ref is a %s conversation; refusing to resume a mismatched conversation",
			spelling, name, declared, provider)
	}

	// A resumable Agent has already given its Pane up. A surviving paneRef means
	// the registry disagrees with itself, and binding a second Pane would orphan
	// the first, so this refuses rather than guessing which one is real.
	if paneUID := strings.TrimSpace(agent.Status.PaneRef); paneUID != "" {
		if pane, ok := registry.Pane(paneUID); ok {
			return agentResumePlan{}, fmt.Errorf(
				"%s: agent/%s is %s but still owns managed pane/%s; refusing to bind a second managed Pane",
				spelling, name, agent.Status.Phase, pane.Metadata.Name)
		}
	}

	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return agentResumePlan{}, fmt.Errorf("%s: agent/%s has no owning Window in the registry", spelling, name)
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return agentResumePlan{}, fmt.Errorf("%s: window/%s has no owning Project in the registry", spelling, window.Metadata.Name)
	}
	// The same rule `create` applies: a Project whose root has disappeared is
	// preserved and still resolves, but nothing may be materialized under a
	// directory tmux cannot enter. The check is restated with this route's own
	// spelling rather than borrowed, so the message names the verb the operator
	// actually ran.
	if condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot); ok && condition.Status == coremetadata.ConditionTrue {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: project/%s carries a MissingRoot condition for %q; rebind it before resuming an Agent under it",
			spelling, project.Metadata.Name, project.Spec.Root))
	}
	// Resume uses the stable role-agnostic Window anchor. There is no fallback to
	// the active, last-used, or an alternate live Pane.
	anchorUID := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if anchorUID == "" {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef, so there is no anchor to split",
			spelling, window.Metadata.Name, project.Metadata.Name))
	}
	if anchor, ok := registry.WindowAnchor(window.Metadata.UID); !ok || anchor.Metadata.UID != anchorUID {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) anchorPaneRef %q is dangling or cross-Window",
			spelling, window.Metadata.Name, project.Metadata.Name, anchorUID))
	}

	return agentResumePlan{
		agentUID:       agent.Metadata.UID,
		agentName:      name,
		provider:       provider,
		conversationID: conversationID,
		ref:            ref.Clone(),
		projectUID:     project.Metadata.UID,
		projectRoot:    project.Spec.Root,
		workspace:      agent.Spec.Workspace,
		topic:          agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic],
		windowUID:      window.Metadata.UID,
		anchorUID:      anchorUID,
		shared:         sharedConversationAgents(registry, agent.Metadata.UID, ref),
	}, nil
}

// sharedConversationAgents lists the other Agents recording the same
// conversation, in uid order.
//
// The order is the point: it makes the disclosure byte-identical regardless of
// the order the Agents happen to sit in the registry file, which is what a
// determinism assertion can pin.
func sharedConversationAgents(registry coremetadata.Registry, selfUID string, ref *coremetadata.AgentSessionRef) []string {
	type row struct{ uid, name string }
	var rows []row
	for i := range registry.Agents {
		other := registry.Agents[i]
		if other.Metadata.UID == selfUID || !other.Status.SessionRef.SameConversation(ref) {
			continue
		}
		rows = append(rows, row{uid: other.Metadata.UID, name: other.Metadata.Name})
	}
	slices.SortStableFunc(rows, func(a, b row) int { return cmp.Compare(a.uid, b.uid) })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("agent/%s (uid:%s)", r.name, r.uid))
	}
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
	title, launchArgv, err := r.launcher.PlanAgentResume(plan.provider, workspace, plan.conversationID)
	if err != nil {
		return fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w",
			spelling, plan.agentName, plan.provider, plan.conversationID, err)
	}
	nativeLauncher, nativeLaunchCapable := r.launcher.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := r.launcher.(codexNativeLifecycleStarter)
	var nativeLifecycleTargetAfterCommit codexLifecycleObserverTarget

	for _, other := range plan.shared {
		if _, err := fmt.Fprintf(stderr,
			"projmux: conversation %s:%s is also recorded on %s; %s rebinds only agent/%s\n",
			plan.provider, plan.conversationID, other, spelling, plan.agentName); err != nil {
			return err
		}
	}

	if err := r.create.transact(func(
		ctx context.Context,
		working *coremetadata.Registry,
		mutator coremetadata.Mutator,
		operationID string,
		ledger *runtimeLedger,
	) error {
		agent, ok := working.Agent(plan.agentUID)
		if !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.agentUID)
		}
		// The preflight ran against a read-only snapshot and the reconciler has
		// since run inside this transaction. Re-checking the two facts the plan
		// rests on -- the Agent is still resumable, and it is still the same
		// conversation -- is what stops a concurrent hook or transition from
		// turning this into a rebind of something else.
		if err := requireResumablePhase(spelling, agent); err != nil {
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

		// Metadata phase. AttachAgentPane creates the managed Pane owned by this
		// existing Agent and moves it Offline/Failed -> Running through the
		// closed transition table. No Agent is created and no name is allocated
		// for one, so the uid and metadata.name are structurally untouchable
		// here.
		pane, err := mutator.AttachAgentPane(working, plan.agentUID, coremetadata.BootstrapPane{
			CWD: contextDir,
		}, operationID)
		if err != nil {
			return MapMetadataError(err)
		}

		// Runtime phase, on the create routes' own materializer.
		sessionName, err := r.create.ensureProjectRuntime(ctx, working, mutator, *project, operationID, ledger)
		if err != nil {
			return err
		}
		anchorPaneID, err := r.create.ensureAnchorPane(ctx, working, mutator, ledger, *project, sessionName, operationID, paneTarget{
			windowUID:    plan.windowUID,
			anchorUID:    plan.anchorUID,
			storedAnchor: true,
		})
		if err != nil {
			return err
		}
		// A resume is a new materialization of the same Agent, so it issues a
		// fresh generation. That is what makes a late receipt from the process
		// this resume replaced recognizable as stale instead of being applied
		// to the Pane the operator is now looking at.
		activation, err := r.create.issuePaneActivation(working, mutator, pane.Metadata.UID, plan.agentUID, operationID)
		if err != nil {
			return err
		}
		workTitle := title
		workLaunchArgv := launchArgv
		usedNative := false
		nativeThreadID := ""
		if plan.provider == aiModeCodex && r.create.codexNative != nil && nativeLaunchCapable {
			nativeCtx, cancel := prepareNativeContext(ctx)
			prepared, nativeErr := r.create.codexNative.Resume(nativeCtx, workspace, plan.conversationID)
			cancel()
			switch {
			case nativeErr == nil:
				workTitle, workLaunchArgv, err = nativeLauncher.PlanNativeCodexResume(workspace, prepared.ThreadID)
				if err != nil {
					return nativeLaunchError(spelling, err)
				}
				if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
					AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, Generation: activation.Generation,
					ThreadID: prepared.ThreadID, TurnID: prepared.TurnID,
				}); err != nil {
					return MapMetadataError(err)
				}
				nativeThreadID = prepared.ThreadID
				usedNative = true
			case nativeFallbackAllowed(r.create.codexNative, nativeErr):
				// Preserve the current provider resume argv and hook refinement.
			default:
				return nativeLaunchError(spelling, nativeErr)
			}
		}
		paneID, err := r.create.runtime.splitPane(ctx, anchorPaneID, defaultPlacement, contextDir,
			r.create.runtime.supervisedLaunch(ctx, activation, workLaunchArgv))
		if paneID != "" {
			if claimErr := r.create.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := r.create.runtime.mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, r.create.runtime.warn)
		}
		if err != nil {
			return err
		}
		if usedNative {
			if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, r.create.runtime.runner, paneID, contextDir, workTitle, nativeThreadID); err != nil {
				return tmuxError("%s: bind native Codex Pane %s presentation metadata: %v", spelling, paneID, err)
			}
			if nativeLifecycleCapable {
				nativeLifecycleTargetAfterCommit = codexLifecycleObserverTarget{
					Identity: codexLifecycleIdentity{
						AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, RuntimeID: paneID,
						Generation: activation.Generation, ThreadID: nativeThreadID,
					},
					Route: r.create.runtime.target,
				}
			}
		} else if err := r.launcher.BindAgentPaneOnRoute(ctx, r.create.runtime.runner, agentPaneBinding{
			PaneID: paneID, Provider: plan.provider, ContextDir: contextDir, Title: workTitle,
			Topic: plan.topic, TopicManual: strings.TrimSpace(plan.topic) != "", ConversationID: plan.conversationID,
		}); err != nil {
			return tmuxError("%s: bind resumed Agent Pane %s presentation metadata: %v", spelling, paneID, err)
		}
		return nil
	}, r.create.exactProjectOwnershipGuard(plan.projectUID)); err != nil {
		return err
	}
	if nativeLifecycleTargetAfterCommit.valid() {
		nativeLifecycle.startNativeCodexLifecycleObserver(nativeLifecycleTargetAfterCommit)
	}

	_, err = fmt.Fprintf(stdout, "agent/%s resumed\n", plan.agentName)
	return err
}
