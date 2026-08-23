package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type codexLifecycleIdentity struct {
	AgentUID   string
	PaneUID    string
	RuntimeID  string
	Generation string
	ThreadID   string
}

func (i codexLifecycleIdentity) valid() bool {
	return strings.TrimSpace(i.AgentUID) != "" && strings.TrimSpace(i.PaneUID) != "" &&
		strings.TrimSpace(i.RuntimeID) != "" && strings.TrimSpace(i.Generation) != "" &&
		strings.TrimSpace(i.ThreadID) != ""
}

type codexPendingApproval struct {
	TurnID    string
	ItemID    string
	RequestID string
	Kind      codexappserver.ApprovalKind
	Notified  bool
}

type codexLifecycleNotice struct {
	Category  string
	ID        string
	Severity  string
	ThreadID  string
	TurnID    string
	ItemID    string
	RequestID string
	Kind      codexappserver.ApprovalKind
}

type codexLifecycleProjection struct {
	Accepted       bool
	Invalidated    bool
	Interaction    coremetadata.AgentInteractionKind
	Notices        []codexLifecycleNotice
	ClearNoticeIDs []string
}

// codexLifecycleReducer owns one exact source epoch. An event can mutate state
// only when both its connection epoch and its thread identity are current.
// Approval request content is intentionally absent from this type.
type codexLifecycleReducer struct {
	epoch            uint64
	active           bool
	identity         codexLifecycleIdentity
	threadState      codexappserver.ThreadState
	currentTurnID    string
	currentTurnState codexappserver.TurnState
	interaction      coremetadata.AgentInteractionKind
	pending          map[string]codexPendingApproval
	terminalTurns    map[string]codexappserver.TurnState
}

func (r *codexLifecycleReducer) begin(epoch uint64, identity codexLifecycleIdentity, snapshot codexappserver.LifecycleSnapshot) codexLifecycleProjection {
	if epoch == 0 || !identity.valid() || strings.TrimSpace(snapshot.ThreadID) != strings.TrimSpace(identity.ThreadID) {
		return codexLifecycleProjection{}
	}
	r.epoch = epoch
	r.active = true
	r.identity = identity
	r.threadState = snapshot.ThreadState
	r.currentTurnID = strings.TrimSpace(snapshot.TurnID)
	r.currentTurnState = snapshot.TurnState
	r.pending = map[string]codexPendingApproval{}
	r.terminalTurns = map[string]codexappserver.TurnState{}
	if snapshot.ThreadState == codexappserver.ThreadStateNotLoaded {
		// A not-loaded snapshot is an invalidation boundary, never evidence
		// for a healthy provider-control-plane epoch. Return the first accepted
		// clear projection so the observer can order clear before fallback.
		return r.invalidate(epoch)
	}
	if r.currentTurnID != "" && (snapshot.TurnState == codexappserver.TurnStateCompleted || snapshot.TurnState == codexappserver.TurnStateFailed || snapshot.TurnState == codexappserver.TurnStateInterrupted) {
		r.terminalTurns[r.currentTurnID] = snapshot.TurnState
	}
	r.interaction = r.snapshotInteraction()
	return codexLifecycleProjection{Accepted: true, Interaction: r.interaction}
}

func (r *codexLifecycleReducer) invalidate(epoch uint64) codexLifecycleProjection {
	if !r.active || epoch != r.epoch {
		return codexLifecycleProjection{}
	}
	projection := codexLifecycleProjection{Accepted: true, Invalidated: true, Interaction: coremetadata.InteractionUnknown}
	projection.ClearNoticeIDs = r.pendingNoticeIDs()
	r.active = false
	r.pending = nil
	r.currentTurnID = ""
	r.currentTurnState = codexappserver.TurnStateUnknown
	r.interaction = coremetadata.InteractionUnknown
	return projection
}

func (r *codexLifecycleReducer) apply(epoch uint64, event codexappserver.LifecycleEvent) codexLifecycleProjection {
	if !r.active || epoch != r.epoch || strings.TrimSpace(event.ThreadID) != strings.TrimSpace(r.identity.ThreadID) {
		return codexLifecycleProjection{}
	}
	projection := codexLifecycleProjection{Accepted: true, Interaction: r.interaction}
	switch event.Kind {
	case codexappserver.LifecycleTurnStarted:
		if event.TurnID == "" {
			return codexLifecycleProjection{}
		}
		projection.ClearNoticeIDs = r.pendingNoticeIDs()
		r.pending = map[string]codexPendingApproval{}
		r.terminalTurns = map[string]codexappserver.TurnState{}
		r.currentTurnID = event.TurnID
		r.currentTurnState = codexappserver.TurnStateInProgress
		r.threadState = codexappserver.ThreadStateActive
		r.interaction = coremetadata.InteractionInProgress
	case codexappserver.LifecycleThreadStatus:
		if event.ThreadState == codexappserver.ThreadStateNotLoaded {
			return r.invalidate(epoch)
		}
		if r.threadState == codexappserver.ThreadStateWaitingOnApproval && event.ThreadState != codexappserver.ThreadStateWaitingOnApproval {
			projection.ClearNoticeIDs = r.notifiedPendingNoticeIDs()
			for requestID, pending := range r.pending {
				pending.Notified = false
				r.pending[requestID] = pending
			}
		}
		r.threadState = event.ThreadState
		r.interaction = r.liveInteraction()
		if r.interaction == coremetadata.InteractionApprovalRequired {
			projection.Notices = r.actionableApprovalNotices()
		}
	case codexappserver.LifecycleApprovalPending:
		if event.TurnID == "" || event.ItemID == "" || event.RequestID == "" || event.TurnID != r.currentTurnID {
			return codexLifecycleProjection{}
		}
		if r.pending == nil {
			r.pending = map[string]codexPendingApproval{}
		}
		if existing, exists := r.pending[event.RequestID]; exists {
			// A resolved notification only identifies its original request. Keep
			// that request bound to the exact turn/item tuple observed when it
			// became pending; a colliding request ID must not replace it.
			if existing.TurnID != event.TurnID || existing.ItemID != event.ItemID {
				return codexLifecycleProjection{}
			}
		} else {
			r.pending[event.RequestID] = codexPendingApproval{TurnID: event.TurnID, ItemID: event.ItemID, RequestID: event.RequestID, Kind: event.ApprovalKind}
		}
		r.interaction = r.liveInteraction()
		if r.interaction == coremetadata.InteractionApprovalRequired {
			projection.Notices = r.actionableApprovalNotices()
		}
	case codexappserver.LifecycleRequestResolved:
		pending, exists := r.pending[event.RequestID]
		if !exists || pending.TurnID != r.currentTurnID {
			return codexLifecycleProjection{}
		}
		delete(r.pending, event.RequestID)
		projection.ClearNoticeIDs = []string{codexApprovalNoticeID(r.identity, pending)}
		r.interaction = r.liveInteraction()
	case codexappserver.LifecycleTurnCompleted:
		if event.TurnID == "" || event.TurnID != r.currentTurnID {
			return codexLifecycleProjection{}
		}
		if _, duplicate := r.terminalTurns[event.TurnID]; duplicate {
			return codexLifecycleProjection{}
		}
		if event.TurnState != codexappserver.TurnStateCompleted && event.TurnState != codexappserver.TurnStateFailed && event.TurnState != codexappserver.TurnStateInterrupted {
			return codexLifecycleProjection{}
		}
		projection.ClearNoticeIDs = r.pendingNoticeIDs()
		r.pending = map[string]codexPendingApproval{}
		r.currentTurnState = event.TurnState
		r.terminalTurns[event.TurnID] = event.TurnState
		if event.TurnState == codexappserver.TurnStateCompleted {
			r.interaction = coremetadata.InteractionResponseComplete
			projection.Notices = []codexLifecycleNotice{{
				Category: "response_complete", ID: codexCompletionNoticeID(r.identity, event.TurnID),
				Severity: notify.SeverityInfo, ThreadID: r.identity.ThreadID, TurnID: event.TurnID,
			}}
		} else {
			r.interaction = coremetadata.InteractionIdle
		}
	default:
		return codexLifecycleProjection{}
	}
	projection.Interaction = r.interaction
	return projection
}

func (r *codexLifecycleReducer) snapshotInteraction() coremetadata.AgentInteractionKind {
	if r.currentTurnState == codexappserver.TurnStateCompleted {
		return coremetadata.InteractionResponseComplete
	}
	if r.currentTurnState == codexappserver.TurnStateFailed || r.currentTurnState == codexappserver.TurnStateInterrupted {
		return coremetadata.InteractionIdle
	}
	return r.liveInteraction()
}

func (r *codexLifecycleReducer) liveInteraction() coremetadata.AgentInteractionKind {
	if r.currentTurnState == codexappserver.TurnStateCompleted {
		return coremetadata.InteractionResponseComplete
	}
	switch r.threadState {
	case codexappserver.ThreadStateIdle:
		return coremetadata.InteractionIdle
	case codexappserver.ThreadStateWaitingOnUserInput:
		return coremetadata.InteractionInputRequired
	case codexappserver.ThreadStateWaitingOnApproval:
		for _, pending := range r.pending {
			if pending.TurnID == r.currentTurnID && pending.ItemID != "" && pending.RequestID != "" {
				return coremetadata.InteractionApprovalRequired
			}
		}
		return coremetadata.InteractionInProgress
	case codexappserver.ThreadStateActive:
		return coremetadata.InteractionInProgress
	case codexappserver.ThreadStateSystemError:
		return coremetadata.InteractionIdle
	default:
		return coremetadata.InteractionUnknown
	}
}

func (r *codexLifecycleReducer) actionableApprovalNotices() []codexLifecycleNotice {
	keys := make([]string, 0, len(r.pending))
	for requestID, pending := range r.pending {
		if pending.TurnID == r.currentTurnID && !pending.Notified {
			keys = append(keys, requestID)
		}
	}
	sort.Strings(keys)
	notices := make([]codexLifecycleNotice, 0, len(keys))
	for _, requestID := range keys {
		pending := r.pending[requestID]
		pending.Notified = true
		r.pending[requestID] = pending
		notices = append(notices, codexLifecycleNotice{
			Category: "approval_required", ID: codexApprovalNoticeID(r.identity, pending), Severity: notify.SeverityCritical,
			ThreadID: r.identity.ThreadID, TurnID: pending.TurnID, ItemID: pending.ItemID, RequestID: pending.RequestID, Kind: pending.Kind,
		})
	}
	return notices
}

func (r *codexLifecycleReducer) pendingNoticeIDs() []string {
	ids := make([]string, 0, len(r.pending))
	for _, pending := range r.pending {
		ids = append(ids, codexApprovalNoticeID(r.identity, pending))
	}
	sort.Strings(ids)
	return ids
}

func (r *codexLifecycleReducer) notifiedPendingNoticeIDs() []string {
	ids := make([]string, 0, len(r.pending))
	for _, pending := range r.pending {
		if pending.Notified {
			ids = append(ids, codexApprovalNoticeID(r.identity, pending))
		}
	}
	sort.Strings(ids)
	return ids
}

func codexApprovalNoticeID(identity codexLifecycleIdentity, pending codexPendingApproval) string {
	return "ai:codex:native:approval:" + identity.ThreadID + ":" + pending.TurnID + ":" + pending.ItemID + ":" + pending.RequestID
}

func codexCompletionNoticeID(identity codexLifecycleIdentity, turnID string) string {
	return "ai:codex:native:completed:" + identity.ThreadID + ":" + strings.TrimSpace(turnID)
}

type agentMutationMirror interface {
	FindPaneTargetForUID(context.Context, string) (string, bool, error)
	WriteTopic(context.Context, string, string) error
	WriteInteraction(context.Context, string, coremetadata.AgentInteractionKind) error
}

type tmuxAgentMutationMirror struct {
	lookup intmetadata.Mirror
	runner tmuxCommandRunner
}

func defaultAgentMutationMirror() agentMutationMirror {
	return inheritedAgentMutationMirror(os.Getenv, inttmux.ExecRunner{})
}

func inheritedAgentMutationMirror(lookupEnv func(string) string, runner tmuxCommandRunner) agentMutationMirror {
	if lookupEnv == nil || runner == nil {
		return nil
	}
	socket, _, _ := strings.Cut(strings.TrimSpace(lookupEnv("TMUX")), ",")
	target, err := tmuxSocketPathTarget(socket)
	if err != nil {
		return nil
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	return &tmuxAgentMutationMirror{lookup: intmetadata.NewMirror(routed), runner: routed}
}

func (m *tmuxAgentMutationMirror) FindPaneTargetForUID(ctx context.Context, uid string) (string, bool, error) {
	return m.lookup.FindPaneTargetForUID(ctx, uid)
}

func (m *tmuxAgentMutationMirror) WriteTopic(ctx context.Context, target, topic string) error {
	if topic == "" {
		if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", target, aiPaneTopicOption); err != nil {
			return err
		}
		_, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", target, aiPaneTopicManualOption)
		return err
	}
	if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-t", target, aiPaneTopicOption, topic); err != nil {
		return err
	}
	_, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-t", target, aiPaneTopicManualOption, "on")
	return err
}

func (m *tmuxAgentMutationMirror) WriteInteraction(ctx context.Context, target string, kind coremetadata.AgentInteractionKind) error {
	state, badge, attention := agentTmuxProjection(kind)
	if state == "" {
		if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", target, aiPaneStateOption); err != nil {
			return err
		}
	} else if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-t", target, aiPaneStateOption, state); err != nil {
		return err
	}
	if badge == "" {
		if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", target, aiPaneBadgeKindOption); err != nil {
			return err
		}
	} else if _, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-t", target, aiPaneBadgeKindOption, badge); err != nil {
		return err
	}
	if attention == "" {
		_, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", target, attentionStateOption)
		return err
	}
	_, err := m.runner.Run(ctx, "tmux", "set-option", "-p", "-t", target, attentionStateOption, attention)
	return err
}

func agentTmuxProjection(kind coremetadata.AgentInteractionKind) (state, badge, attention string) {
	switch kind {
	case coremetadata.InteractionInProgress:
		return "thinking", aiBadgeKindInProgress, attentionStateBusy
	case coremetadata.InteractionApprovalRequired:
		return "waiting", aiBadgeKindApprovalRequired, attentionStateReply
	case coremetadata.InteractionInputRequired:
		return "waiting", aiBadgeKindInputRequired, attentionStateReply
	case coremetadata.InteractionResponseComplete:
		return "waiting", aiBadgeKindResponseComplete, attentionStateReply
	case coremetadata.InteractionIdle:
		return "idle", "", ""
	default:
		return "", "", ""
	}
}

func (c *agentCommand) resolveOneAgent(spelling, ref string, verb selector.Verb) (coremetadata.Registry, coremetadata.Agent, error) {
	registry, err := c.loadRegistry()
	if err != nil {
		return coremetadata.Registry{}, coremetadata.Agent{}, MapMetadataError(err)
	}
	flags := resourceQueryFlags{kind: coremetadata.KindAgent, active: c.activeTarget}
	if strings.TrimSpace(ref) != "" {
		flags.addPositionalRef(ref)
	}
	resolution, err := flags.resolve(verb, false, registry)
	if err != nil {
		return coremetadata.Registry{}, coremetadata.Agent{}, MapMetadataError(err)
	}
	agent, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Registry{}, coremetadata.Agent{}, fmt.Errorf("%s: resolved Agent disappeared", spelling)
	}
	return registry, agent.Clone(), nil
}

func (c *agentCommand) runTopic(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent topic requires get, set, or clear")
	}
	action := args[0]
	fs := flag.NewFlagSet("agent topic "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var agentRef string
	fs.StringVar(&agentRef, "agent", "", "exact Agent reference: <name> or uid:<uid>")
	positionals, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	var topic string
	switch action {
	case "get", "clear":
		if len(positionals) > 1 {
			return usageError("agent topic " + action + " accepts at most one Agent reference")
		}
		if len(positionals) == 1 {
			if agentRef != "" {
				return usageError("agent topic: specify the Agent once")
			}
			agentRef = positionals[0]
		}
	case "set":
		if len(positionals) < 1 || len(positionals) > 2 {
			return usageError("agent topic set requires <text> and accepts an optional Agent reference")
		}
		topic = strings.TrimSpace(positionals[0])
		if topic == "" {
			return usageError("agent topic set requires non-empty <text>")
		}
		if len(positionals) == 2 {
			if agentRef != "" {
				return usageError("agent topic: specify the Agent once")
			}
			agentRef = positionals[1]
		}
	default:
		return usageError("agent topic requires get, set, or clear")
	}
	_, agent, err := c.resolveOneAgent("agent topic "+action, agentRef, selector.VerbTopic)
	if err != nil {
		return err
	}
	if action == "get" {
		fmt.Fprintln(stdout, strings.TrimSpace(agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic]))
		return nil
	}
	if action == "clear" {
		topic = ""
	}
	var committed coremetadata.Agent
	if err := c.mutateAgent(agent.Metadata.UID, func(reg *coremetadata.Registry, mut coremetadata.Mutator) error {
		updated, err := mut.SetAgentTopic(reg, agent.Metadata.UID, topic)
		committed = updated.Clone()
		return err
	}); err != nil {
		return err
	}
	return c.mirrorAgentTopic(committed, topic)
}

func (c *agentCommand) runStatus(args []string, stdout, stderr io.Writer) error {
	action := "get"
	if len(args) > 0 && (args[0] == "get" || args[0] == "set") {
		action, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("agent status "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var agentRef string
	fs.StringVar(&agentRef, "agent", "", "exact Agent reference: <name> or uid:<uid>")
	positionals, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	var kind coremetadata.AgentInteractionKind
	if action == "set" {
		if len(positionals) < 1 || len(positionals) > 2 {
			return usageError("agent status set requires <unknown|idle|in_progress|approval_required|input_required|response_complete> and an optional Agent reference")
		}
		kind = coremetadata.AgentInteractionKind(positionals[0])
		if !coremetadata.ValidAgentInteractionKind(kind) {
			return usageError(fmt.Sprintf("agent status set: unsupported interaction kind %q", kind))
		}
		if len(positionals) == 2 {
			if agentRef != "" {
				return usageError("agent status: specify the Agent once")
			}
			agentRef = positionals[1]
		}
	} else {
		if len(positionals) > 1 {
			return usageError("agent status get accepts at most one Agent reference")
		}
		if len(positionals) == 1 {
			if agentRef != "" {
				return usageError("agent status: specify the Agent once")
			}
			agentRef = positionals[0]
		}
	}
	_, agent, err := c.resolveOneAgent("agent status "+action, agentRef, selector.VerbStatus)
	if err != nil {
		return err
	}
	if action == "get" {
		interaction := agent.EffectiveInteraction(c.clock())
		fmt.Fprintf(stdout, "%s lifecycle=%s observedAt=%s source=%s\n", interaction.Kind, agent.Status.Phase, formatOptionalTime(interaction.ObservedAt), interaction.Source)
		return nil
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || strings.TrimSpace(agent.Status.PaneRef) == "" {
		return usageError(fmt.Sprintf("agent status set: agent/%s is %s without a current managed Pane; semantic status can only be set on a Running Agent", agent.Metadata.Name, agent.Status.Phase))
	}
	var committed coremetadata.Agent
	if err := c.mutateAgent(agent.Metadata.UID, func(reg *coremetadata.Registry, mut coremetadata.Mutator) error {
		current, ok := reg.Agent(agent.Metadata.UID)
		if !ok {
			return fmt.Errorf("agent status set: agent %q disappeared", agent.Metadata.UID)
		}
		if current.Status.Phase != coremetadata.PhaseRunning || strings.TrimSpace(current.Status.PaneRef) == "" {
			return usageError(fmt.Sprintf("agent status set: agent/%s is %s without a current managed Pane; semantic status can only be set on a Running Agent", current.Metadata.Name, current.Status.Phase))
		}
		updated, err := mut.SetAgentInteraction(reg, agent.Metadata.UID, kind, string(coremetadata.InteractionSourceManual))
		committed = updated.Clone()
		return err
	}); err != nil {
		return err
	}
	return c.mirrorAgentInteraction(committed, kind)
}

func formatOptionalTime(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	return at.UTC().Format(time.RFC3339)
}

func (c *agentCommand) mutateAgent(uid string, apply func(*coremetadata.Registry, coremetadata.Mutator) error) error {
	if c.store == nil {
		return errors.New("agent mutation store is not configured")
	}
	return c.store.mutate(coremetadata.KindAgent, []string{uid}, apply)
}

func (c *agentCommand) mirrorAgentTopic(agent coremetadata.Agent, topic string) error {
	return c.mirrorAgent(agent, "agent topic", func(ctx context.Context, target string) error { return c.mirror.WriteTopic(ctx, target, topic) })
}

func (c *agentCommand) mirrorAgentInteraction(agent coremetadata.Agent, kind coremetadata.AgentInteractionKind) error {
	return c.mirrorAgent(agent, "agent status", func(ctx context.Context, target string) error { return c.mirror.WriteInteraction(ctx, target, kind) })
}

func (c *agentCommand) mirrorAgent(agent coremetadata.Agent, verb string, write func(context.Context, string) error) error {
	if c.mirror == nil || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return nil
	}
	ctx := context.Background()
	target, found, err := c.mirror.FindPaneTargetForUID(ctx, agent.Status.PaneRef)
	if err != nil {
		return committedMirrorError(verb, coremetadata.KindAgent, agent.Metadata.UID, err)
	}
	if !found {
		return nil
	}
	if err := write(ctx, target); err != nil {
		return committedMirrorError(verb, coremetadata.KindAgent, agent.Metadata.UID, err)
	}
	return nil
}
