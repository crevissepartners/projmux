package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

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
