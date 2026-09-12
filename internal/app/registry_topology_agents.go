package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// topologyAgentLauncher is the provider-launch seam the closed-Project topology
// replay consumes.
//
// It is the union of the two launch seams `create agent` already owns -- the
// fresh-conversation one on agentLauncher and the resume one on
// agentResumeLauncher -- and nothing else. Replay is not a third way to start a
// provider: it picks one of the two existing launches per Agent and hands the
// argv to the topology materializer, so an option grammar or a Settings gate
// written down once cannot be spelled a second way here.
type topologyAgentLauncher interface {
	// RequireAgentEnabled applies the Settings enabled-agents gate. Reopening a
	// Project does not re-enable a provider the operator switched off.
	RequireAgentEnabled(provider string) error
	// PlanAgentLaunch builds the fresh-conversation argv. Replay passes no
	// payload: reopening a Project is not the moment to re-send an initial task.
	PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (title string, argv []string, err error)
	// PlanAgentResume builds the provider resume argv for one stored
	// conversation id.
	PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (title string, argv []string, err error)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

// The aiCommand is the production implementation of both halves already.
var _ topologyAgentLauncher = (*aiCommand)(nil)

// registryTopologyAgentPlan is one Agent this pass will bring back.
//
// The argv is fixed at plan time, before the Registry lock is taken and before
// the first tmux mutation, exactly like the shell Pane half of the plan. An
// Agent whose launch cannot be constructed therefore never reaches the plan at
// all: it is reported as a notice and the rest of the topology still converges.
type registryTopologyAgentPlan struct {
	agent coremetadata.Agent
	// provider is the launch discriminator: the session ref's provider when the
	// Agent is resumed, otherwise the Agent's declared spec.provider.
	provider string
	// conversationID is the Registry `status.sessionRef` conversation this Pane
	// rejoins. Empty means this launch starts a new conversation, and the reason
	// it does was disclosed as a notice at plan time.
	conversationID string
	title          string
	argv           []string
	cwd            string
	// releaseUIDs are managed Pane uids the Agent still records although none of
	// them is live in the selected Project. They are proven unclaimed
	// server-wide by the owner guard and released before the new Pane is
	// attached, so a stale registry row cannot orphan a Pane.
	releaseUIDs []string
	// reusePaneUID is the retained managed Pane identity this replay binds again.
	// It is used for an Agent Window anchor so materialization never deletes the
	// required anchor and mints a replacement UID.
	reusePaneUID string
}

// topologyAgentReplayAuthority names why a stored Agent is being considered
// for automatic materialization. Ordinary Project Continue and explicit
// reconcile resume retained conversations after unplanned stops. Snapshot restore
// is an explicit, separate replay authority and keeps the pre-existing snapshot
// recipe behavior.
type topologyAgentReplayAuthority uint8

const (
	topologyAgentReplayInterrupted topologyAgentReplayAuthority = iota
	topologyAgentReplaySnapshot
)

// decideTopologyAgentContinueEligibility admits only a current managed activation
// after an unplanned stop. Runtime liveness and foreign UID claims are checked
// separately by the topology observer and owner guard, including under the lock.
// A missing receipt is distinct from a malformed one; it still needs an exact
// retained activation, never a phase or recycled runtime handle alone.
func decideTopologyAgentContinueEligibility(registry coremetadata.Registry, agent coremetadata.Agent) (bool, string) {
	switch agent.Status.Phase {
	case coremetadata.PhaseRunning:
		if strings.TrimSpace(agent.Status.PaneRef) == "" {
			return false, "the pre-projection Running Agent has no exact paneRef"
		}
	case coremetadata.PhaseOffline, coremetadata.PhaseFailed:
		if agent.Status.PaneRef != "" {
			return false, "the projected " + string(agent.Status.Phase) + " Agent still records a current paneRef"
		}
	default:
		return false, "phase " + string(agent.Status.Phase) + " is not a retained Running, Offline, or Failed activation"
	}

	receipt := agent.Status.LastTermination
	paneUID := agent.Status.PaneRef
	if receipt != nil {
		if reason := topologyContinueTerminationReason(*receipt); reason != "" {
			return false, reason
		}
		if receipt.AgentUID != agent.Metadata.UID || strings.TrimSpace(receipt.PaneUID) == "" ||
			strings.TrimSpace(receipt.Generation) == "" || receipt.ObservedAt.IsZero() {
			return false, "termination evidence lacks the exact Agent, Pane, generation, or observation"
		}
		if agent.Status.Phase == coremetadata.PhaseRunning && paneUID != receipt.PaneUID {
			return false, "the pre-projection Running Agent no longer binds the termination evidence Pane"
		}
		paneUID = receipt.PaneUID
	} else if paneUID == "" {
		// Released Agents have no paneRef. Require one retained owned Pane, not
		// the newest timestamp or the first pane in Registry/runtime order.
		panes := registry.PanesOf(agent.Metadata.UID)
		if len(panes) != 1 {
			return false, "no termination evidence and no unique retained managed Pane activation"
		}
		paneUID = panes[0].Metadata.UID
	}
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return false, "retained Pane " + paneUID + " is not in the Registry"
	}
	if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != agent.Metadata.UID || pane.Spec.Role != coremetadata.PaneRoleAgent {
		return false, "retained Pane is not the Agent's managed Pane"
	}
	activation := pane.Status.Activation
	if strings.TrimSpace(activation.Generation) == "" || activation.AgentUID != agent.Metadata.UID ||
		strings.TrimSpace(activation.OperationID) == "" || activation.StartedAt.IsZero() ||
		(receipt != nil && activation.Generation != receipt.Generation) {
		return false, "termination evidence is not for the retained Pane's current Agent activation generation"
	}
	if !sameTopologyTerminationEvidence(pane.Status.LastTermination, receipt) {
		return false, "Agent and retained Pane do not carry the same exact termination evidence"
	}
	return true, ""
}

// topologyContinueTerminationReason validates the consumer's evidence boundary
// without changing receipt production or sticky intent. Reconcile has no wait
// status or operation receipt; supervisors carry either an exit or a signal.
func topologyContinueTerminationReason(receipt coremetadata.TerminationEvidence) string {
	pairing := false
	switch receipt.Source {
	case coremetadata.TerminationSourceControlAction:
		pairing = receipt.Classification == coremetadata.TerminationInterrupted || receipt.Classification == coremetadata.TerminationIntentional
	case coremetadata.TerminationSourceSupervisor:
		pairing = receipt.Classification == coremetadata.TerminationNormal || receipt.Classification == coremetadata.TerminationKilled || receipt.Classification == coremetadata.TerminationAbnormal
	case coremetadata.TerminationSourceReconcile:
		pairing = receipt.Classification == coremetadata.TerminationUnknown
	}
	if !pairing {
		return fmt.Sprintf("termination evidence has unsupported source/classification %q/%q", receipt.Source, receipt.Classification)
	}
	if receipt.Classification == coremetadata.TerminationIntentional || receipt.Classification == coremetadata.TerminationNormal {
		return fmt.Sprintf("termination evidence %s/%s excludes automatic Continue replay", receipt.Source, receipt.Classification)
	}
	validShape := receipt.ExitCode == nil && receipt.Signal == ""
	switch receipt.Source {
	case coremetadata.TerminationSourceControlAction:
		validShape = validShape && strings.TrimSpace(receipt.OperationID) != ""
	case coremetadata.TerminationSourceSupervisor:
		signal := strings.TrimSpace(receipt.Signal)
		validShape = (receipt.ExitCode != nil && signal == "") || (receipt.ExitCode == nil && signal != "")
		code := 0
		if receipt.ExitCode != nil {
			code = *receipt.ExitCode
			validShape = validShape && code >= 0
		}
		validShape = validShape && coremetadata.ClassifyProcessExit(code, signal) == receipt.Classification
	}
	if !validShape {
		return fmt.Sprintf("termination evidence %s/%s has an invalid intent or wait-status shape", receipt.Source, receipt.Classification)
	}
	return ""
}

func sameTopologyTerminationEvidence(left, right *coremetadata.TerminationEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Source != right.Source || left.Classification != right.Classification ||
		!left.ObservedAt.Equal(right.ObservedAt) || left.PaneUID != right.PaneUID ||
		left.AgentUID != right.AgentUID || left.Generation != right.Generation ||
		left.Signal != right.Signal || left.OperationID != right.OperationID {
		return false
	}
	if left.ExitCode == nil || right.ExitCode == nil {
		return left.ExitCode == nil && right.ExitCode == nil
	}
	return *left.ExitCode == *right.ExitCode
}

// topologyAgentResumeDecision is the pure verdict about one stored Agent: which
// provider its Pane launches, and which conversation -- if any -- that launch
// rejoins.
//
// The only replay identifier is Registry `status.sessionRef`. No provider
// conversation store is read, and `ClaudeSessionRef.TranscriptPath` in
// particular is never consulted.
type topologyAgentResumeDecision struct {
	provider       string
	conversationID string
	// reason is why this Agent is not resumed. It is empty exactly when
	// conversationID is non-empty.
	reason string
}

// decideTopologyAgentResume folds one Agent's stored session ref into a launch
// decision.
//
// Every branch that cannot produce a conversation id answers with a reason so
// the caller's replay authority can apply its own fail-closed rule. An eligible
// Continue Agent must resume a recorded conversation exactly. Explicit snapshot
// restore retains its older recipe fallback.
func decideTopologyAgentResume(agent coremetadata.Agent) topologyAgentResumeDecision {
	declared := strings.TrimSpace(agent.Spec.Provider)
	ref := agent.Status.SessionRef
	if ref.Empty() {
		return topologyAgentResumeDecision{
			provider: declared,
			reason:   "no provider session ref is recorded; projmux records one the first time that Agent's provider hook fires",
		}
	}
	provider := strings.TrimSpace(ref.Provider)
	if provider == "" {
		return topologyAgentResumeDecision{
			provider: declared,
			reason:   "the recorded session ref carries no provider discriminator",
		}
	}
	conversation := strings.TrimSpace(ref.ConversationID())
	if conversation == "" {
		return topologyAgentResumeDecision{
			provider: declared,
			reason:   "the recorded " + provider + " session ref carries no conversation id",
		}
	}
	// spec.provider is cross-checked only when the Agent declares one, matching
	// `agent resume`. A mismatch is never resolved by guessing which side is
	// right: ordinary Continue refuses it, while explicit snapshot restore may
	// retain the recipe's declared-provider fallback.
	if declared != "" && declared != provider {
		return topologyAgentResumeDecision{
			provider: declared,
			reason: fmt.Sprintf("it is a %s Agent but its session ref is a %s conversation",
				declared, provider),
		}
	}
	return topologyAgentResumeDecision{provider: provider, conversationID: conversation}
}

// planTopologyWindowAgents fixes the Agent half of one Window's plan.
//
// An Agent whose managed Pane is already live is not this pass's work and
// produces neither an item nor a notice, which is what keeps a repeat run a
// Registry-write-free no-op.
func planTopologyWindowAgents(
	plan *registryTopologyPlan,
	registry coremetadata.Registry,
	project coremetadata.Project,
	window coremetadata.Window,
	windowOrder int,
	live []observedTopologyPane,
	launcher topologyAgentLauncher,
	anchorPaneUID string,
	authority topologyAgentReplayAuthority,
) []registryTopologyAgentPlan {
	liveUIDs := map[string]bool{}
	for _, pane := range live {
		if pane.uid != "" {
			liveUIDs[pane.uid] = true
		}
	}
	var out []registryTopologyAgentPlan
	for order, agent := range registry.AgentsOf(window.Metadata.UID) {
		label := window.Metadata.Name + "/" + agent.Metadata.Name
		materialized := false
		var release []string
		reusePaneUID := ""
		for _, pane := range registry.PanesOf(agent.Metadata.UID) {
			if liveUIDs[pane.Metadata.UID] {
				materialized = true
				break
			}
			if pane.Metadata.UID == anchorPaneUID && agent.Status.PaneRef == pane.Metadata.UID {
				reusePaneUID = pane.Metadata.UID
				continue
			}
			release = append(release, pane.Metadata.UID)
		}
		if materialized {
			continue
		}
		if authority != topologyAgentReplaySnapshot {
			if eligible, reason := decideTopologyAgentContinueEligibility(registry, agent); !eligible {
				plan.noteAgent(label, reason)
				continue
			}
		}
		if !coremetadata.CanTransitionAgent(agent.Status.Phase, coremetadata.PhaseRunning) {
			plan.noteAgent(label, "phase "+string(agent.Status.Phase)+" cannot move to Running")
			continue
		}
		work, ok := planTopologyAgentReplay(plan, project, agent, label, launcher, authority)
		if !ok {
			continue
		}
		work.releaseUIDs = release
		work.reusePaneUID = reusePaneUID
		plan.addItem(windowOrder*1000+500+order, coremetadata.KindAgent, label, agent.Metadata.UID, "materialize")
		out = append(out, work)
	}
	return out
}

// planTopologyAgentReplay builds one Agent's launch, or explains why it has
// none.
//
// The order is the point: every refusal that costs nothing -- an unconfigured
// seam, an Agent with no provider at all, a provider the operator switched off,
// a workspace directory that is gone -- is answered before any provider argv is
// built, and the argv itself is built before the caller has created a single
// resource.
func planTopologyAgentReplay(
	plan *registryTopologyPlan,
	project coremetadata.Project,
	agent coremetadata.Agent,
	label string,
	launcher topologyAgentLauncher,
	authority topologyAgentReplayAuthority,
) (registryTopologyAgentPlan, bool) {
	if launcher == nil {
		plan.noteAgent(label, "the Agent provider launcher is not configured on this route")
		return registryTopologyAgentPlan{}, false
	}
	decision := decideTopologyAgentResume(agent)
	if authority != topologyAgentReplaySnapshot {
		if decision.conversationID == "" {
			plan.noteAgent(label, "no exact conversation can be resumed: "+decision.reason)
			return registryTopologyAgentPlan{}, false
		}
		// ConversationID selects the populated union member, so check that the
		// member really belongs to the discriminator before passing it onward.
		ref := agent.Status.SessionRef
		validRef := false
		switch decision.provider {
		case "claude":
			validRef = ref.Claude != nil && ref.Codex == nil && ref.Antigravity == nil
		case "codex":
			validRef = ref.Codex != nil && ref.Claude == nil && ref.Antigravity == nil
		case "antigravity":
			validRef = ref.Antigravity != nil && ref.Claude == nil && ref.Codex == nil
		}
		if !validRef {
			plan.noteAgent(label, "the recorded session ref has an unsupported provider or mismatched provider member")
			return registryTopologyAgentPlan{}, false
		}
	}
	if decision.provider == "" {
		plan.noteAgent(label, "neither the Agent nor its session ref names a provider")
		return registryTopologyAgentPlan{}, false
	}
	if err := launcher.RequireAgentEnabled(decision.provider); err != nil {
		plan.noteAgent(label, err.Error())
		return registryTopologyAgentPlan{}, false
	}
	cwd := strings.TrimSpace(agent.Spec.Workspace.CWD)
	if cwd == "" {
		cwd = project.Spec.Root
	}
	if reason := validateMaterializeDirectory(cwd, "Agent cwd"); reason != "" {
		plan.noteAgent(label, reason)
		return registryTopologyAgentPlan{}, false
	}
	workspace := agent.Spec.Workspace
	workspace.CWD = cwd

	work := registryTopologyAgentPlan{agent: agent, provider: decision.provider, cwd: cwd}
	if decision.conversationID != "" {
		title, argv, err := launcher.PlanAgentResume(decision.provider, workspace, decision.conversationID)
		if err == nil {
			work.conversationID, work.title, work.argv = decision.conversationID, title, argv
			return work, true
		}
		if authority != topologyAgentReplaySnapshot {
			plan.noteAgent(label, fmt.Sprintf("the %s provider could not build the required exact resume launch for conversation %s: %v",
				decision.provider, decision.conversationID, err))
			return registryTopologyAgentPlan{}, false
		}
		// Explicit snapshot restore retains the prior recipe fallback: the Agent
		// still comes back on a new conversation and the operator is told why.
		// Ordinary Continue returned above instead of degrading the recorded
		// conversation.
		decision.reason = fmt.Sprintf("the %s provider could not build a resume launch for conversation %s: %v",
			decision.provider, decision.conversationID, err)
	}
	title, argv, err := launcher.PlanAgentLaunch(decision.provider, workspace, nil)
	if err != nil {
		plan.noteAgent(label, fmt.Sprintf("%s, and no fresh %s launch could be built either: %v",
			decision.reason, decision.provider, err))
		return registryTopologyAgentPlan{}, false
	}
	work.title, work.argv = title, argv
	plan.noteNewConversation(label, decision.reason)
	return work, true
}

// replayTopologyWindowAgents materializes the Agent half of one Window.
//
// It runs on the same anchor, the same activation ledger, the same
// ownership-checked adoption, and the same rollback as the Window's shell Panes,
// because a replayed Agent Pane is an ordinary managed Pane that happens to
// carry a provider argv.
func replayTopologyWindowAgents(
	ctx context.Context,
	runtime *materializer,
	registry *coremetadata.Registry,
	mutator coremetadata.Mutator,
	launcher topologyAgentLauncher,
	work *registryTopologyWindowPlan,
	anchorID, sessionID, windowID string,
	ledger *runtimeLedger,
	newGeneration func() (string, error),
	operationID string,
) (map[string]string, error) {
	bindings := map[string]string{}
	for ai := range work.agents {
		replay := &work.agents[ai]
		if launcher == nil {
			return nil, errors.New("topology Agent replay launcher is not configured")
		}
		// A stale managed Pane row is released before the new one is attached.
		// The owner guard has already proven none of these uids is live anywhere
		// on this socket, so this removes a Registry row and never a live pane.
		// The canonical Pane delete is what does it, so the Agent lands Offline
		// through the same transition an operator's `delete pane` produces
		// rather than through a second, replay-only path.
		for _, paneUID := range replay.releaseUIDs {
			if _, ok := registry.Pane(paneUID); !ok {
				continue
			}
			if err := mutator.DeletePane(registry, paneUID); err != nil {
				return nil, MapMetadataError(err)
			}
		}
		var pane coremetadata.Pane
		var err error
		if replay.reusePaneUID != "" {
			pane, err = mutator.RebindAgentPane(registry, replay.agent.Metadata.UID, replay.reusePaneUID)
		} else {
			pane, err = mutator.AttachAgentPane(registry, replay.agent.Metadata.UID, coremetadata.BootstrapPane{
				CWD: replay.cwd,
			}, operationID)
		}
		if err != nil {
			return nil, MapMetadataError(err)
		}
		activation, err := issuePaneActivation(newGeneration, registry, mutator, pane.Metadata.UID, replay.agent.Metadata.UID, operationID)
		if err != nil {
			return nil, err
		}
		paneID, splitErr := runtime.splitPane(ctx, anchorID, defaultPlacement, replay.cwd,
			runtime.supervisedLaunch(ctx, activation, replay.argv))
		if paneID != "" {
			if adoptErr := adoptCreatedPane(ctx, runtime, paneID, sessionID, windowID, pane, ledger); adoptErr != nil {
				return nil, errors.Join(splitErr, adoptErr)
			}
			observeActivationRuntime(registry, mutator, activation, paneID, runtime.warn)
		}
		if splitErr != nil {
			return nil, splitErr
		}
		bindings[pane.Metadata.UID] = paneID
		runtime.equalizeSplitLayout(ctx, anchorID, defaultPlacement)
		if err := launcher.BindAgentPaneOnRoute(ctx, runtime.runner, agentPaneBinding{
			PaneID: paneID, Provider: replay.provider, ContextDir: replay.cwd, Title: replay.title,
			Topic:          replay.agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic],
			TopicManual:    strings.TrimSpace(replay.agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic]) != "",
			ConversationID: replay.conversationID,
		}); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}
