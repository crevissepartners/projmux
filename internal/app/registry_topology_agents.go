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
	// BindManagedAgentPane applies the managed-agent pane options.
	BindManagedAgentPane(paneID, provider, contextDir, title string)
	// BindResumedAgentPane applies them and seeds the live routing index with
	// the resumed conversation id.
	BindResumedAgentPane(paneID, provider, contextDir, title, conversationID string)
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
// Every branch that cannot produce a conversation id answers with a reason
// rather than a refusal. Reopening a Project must not be all-or-nothing: an
// Agent projmux never observed a conversation for is still an Agent the
// operator declared, so it comes back on a new conversation and is told so.
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
	// right: the declared provider launches, on a new conversation.
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
		for _, pane := range registry.PanesOf(agent.Metadata.UID) {
			if liveUIDs[pane.Metadata.UID] {
				materialized = true
				break
			}
			release = append(release, pane.Metadata.UID)
		}
		if materialized {
			continue
		}
		if !coremetadata.CanTransitionAgent(agent.Status.Phase, coremetadata.PhaseRunning) {
			plan.noteAgent(label, "phase "+string(agent.Status.Phase)+" cannot move to Running")
			continue
		}
		work, ok := planTopologyAgentReplay(plan, project, agent, label, launcher)
		if !ok {
			continue
		}
		work.releaseUIDs = release
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
) (registryTopologyAgentPlan, bool) {
	if launcher == nil {
		plan.noteAgent(label, "the Agent provider launcher is not configured on this route")
		return registryTopologyAgentPlan{}, false
	}
	decision := decideTopologyAgentResume(agent)
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
		// A provider that refuses to resume the conversation it recorded is the
		// second half of contract: the Agent still comes back, on a new
		// conversation, and the operator is told which one and why. This is the
		// one place topology replay deliberately differs from `agent resume`,
		// whose whole job is the single Agent the operator named and which
		// therefore refuses rather than degrading.
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
) error {
	for ai := range work.agents {
		replay := &work.agents[ai]
		if launcher == nil {
			return errors.New("topology Agent replay launcher is not configured")
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
				return MapMetadataError(err)
			}
		}
		pane, err := mutator.AttachAgentPane(registry, replay.agent.Metadata.UID, coremetadata.BootstrapPane{
			CWD: replay.cwd,
		}, operationID)
		if err != nil {
			return MapMetadataError(err)
		}
		activation, err := issuePaneActivation(newGeneration, registry, mutator, pane.Metadata.UID, replay.agent.Metadata.UID, operationID)
		if err != nil {
			return err
		}
		paneID, splitErr := runtime.splitPane(ctx, anchorID, defaultPlacement, replay.cwd,
			runtime.supervisedLaunch(ctx, activation, replay.argv))
		if paneID != "" {
			if adoptErr := adoptCreatedPane(ctx, runtime, paneID, sessionID, windowID, pane, ledger); adoptErr != nil {
				return errors.Join(splitErr, adoptErr)
			}
			observeActivationRuntime(registry, mutator, activation, paneID, runtime.warn)
		}
		if splitErr != nil {
			return splitErr
		}
		runtime.equalizeSplitLayout(ctx, anchorID, defaultPlacement)
		if replay.conversationID != "" {
			launcher.BindResumedAgentPane(paneID, replay.provider, replay.cwd, replay.title, replay.conversationID)
		} else {
			launcher.BindManagedAgentPane(paneID, replay.provider, replay.cwd, replay.title)
		}
		if err := mirrorTopologyAgentTopic(ctx, runtime, replay, paneID); err != nil {
			return err
		}
	}
	return nil
}

// mirrorTopologyAgentTopic projects the stored Agent topic onto the replayed
// Pane, the same way `agent resume` does. A replayed Agent is the Agent the
// operator already had, so its topic comes back with it; an Agent with no
// stored topic clears the shared legacy binder's compatibility seed instead.
func mirrorTopologyAgentTopic(ctx context.Context, runtime *materializer, replay *registryTopologyAgentPlan, paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return nil
	}
	topic := strings.TrimSpace(replay.agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic])
	if topic == "" {
		for _, option := range []string{aiPaneTopicOption, aiPaneTopicManualOption} {
			if _, err := runtime.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", paneID, option); err != nil {
				return tmuxError("materialize topology: clear empty Agent topic projection %s on Pane %s: %v", option, paneID, err)
			}
		}
		return nil
	}
	if _, err := runtime.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic); err != nil {
		return tmuxError("materialize topology: mirror stored Agent topic to Pane %s: %v", paneID, err)
	}
	if _, err := runtime.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, aiPaneTopicManualOption, "on"); err != nil {
		return tmuxError("materialize topology: mark stored Agent topic manual on Pane %s: %v", paneID, err)
	}
	return nil
}
