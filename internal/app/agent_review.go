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

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
)

type agentReviewStarter interface {
	Start(context.Context, string, corecap.ReviewTarget) (corecap.ReviewResult, error)
}

type defaultCodexReviewStarter struct{}

func (defaultCodexReviewStarter) Start(ctx context.Context, threadID string, target corecap.ReviewTarget) (corecap.ReviewResult, error) {
	return codexappserver.StartDefaultReview(ctx, version.String(), threadID, target)
}

type agentReviewBindingLookup interface {
	LiveThreadID(context.Context, string) (string, bool, error)
}

type tmuxAgentReviewBindingLookup struct {
	lookup intmetadata.Mirror
	runner tmuxCommandRunner
}

func defaultAgentReviewBindingLookup() agentReviewBindingLookup {
	return inheritedAgentReviewBindingLookup(os.Getenv, inttmux.ExecRunner{})
}

func inheritedAgentReviewBindingLookup(lookupEnv func(string) string, runner tmuxCommandRunner) agentReviewBindingLookup {
	if lookupEnv == nil || runner == nil {
		return nil
	}
	socket, _, _ := strings.Cut(strings.TrimSpace(lookupEnv("TMUX")), ",")
	target, err := tmuxSocketPathTarget(socket)
	if err != nil {
		return nil
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	return &tmuxAgentReviewBindingLookup{lookup: intmetadata.NewMirror(routed), runner: routed}
}

func (l *tmuxAgentReviewBindingLookup) LiveThreadID(ctx context.Context, paneUID string) (string, bool, error) {
	target, found, err := l.lookup.FindPaneTargetForUID(ctx, paneUID)
	if err != nil || !found {
		return "", found, err
	}
	out, err := l.runner.Run(ctx, "tmux", "show-options", "-pv", "-t", target, aiPaneThreadIDOption)
	if err != nil {
		return "", false, err
	}
	threadID := strings.TrimSpace(string(out))
	return threadID, threadID != "", nil
}

type exactReviewBinding struct {
	AgentUID   string
	PaneUID    string
	Generation string
	ThreadID   string
}

func resolveExactReviewBinding(registry coremetadata.Registry, agent coremetadata.Agent, liveThreadID string, live bool) (exactReviewBinding, error) {
	if agent.Spec.Provider != aiModeCodex {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s uses provider %s, not Codex", agent.Metadata.Name, agent.Spec.Provider)
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || strings.TrimSpace(agent.Status.PaneRef) == "" {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s has no current Running Pane binding", agent.Metadata.Name)
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != aiModeCodex || agent.Status.SessionRef.Codex == nil {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s has no exact Codex thread binding", agent.Metadata.Name)
	}
	threadID := strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID)
	if threadID == "" {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s has no exact Codex thread binding", agent.Metadata.Name)
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s current Pane ownership is not exact", agent.Metadata.Name)
	}
	generation := strings.TrimSpace(pane.Status.Activation.Generation)
	if generation == "" {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s current Pane has no activation generation", agent.Metadata.Name)
	}
	if !live || strings.TrimSpace(liveThreadID) != threadID {
		return exactReviewBinding{}, fmt.Errorf("agent review unavailable: agent/%s live Pane thread does not match its current binding", agent.Metadata.Name)
	}
	return exactReviewBinding{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: generation, ThreadID: threadID}, nil
}

func (c *agentCommand) runReview(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent review"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var agentRef, base, commit, instructions string
	fs.StringVar(&agentRef, "agent", "", "exact Agent reference: <name> or uid:<uid>")
	fs.StringVar(&base, "base", "", "review changes against a base branch")
	fs.StringVar(&commit, "commit", "", "review one commit")
	fs.StringVar(&instructions, "instructions", "", "review with custom instructions")
	positionals, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if len(positionals) > 1 || (len(positionals) == 1 && agentRef != "") {
		return usageError(spelling + " accepts one Agent reference, either positional or with --agent")
	}
	if len(positionals) == 1 {
		agentRef = positionals[0]
	}
	target, err := parseReviewTarget(base, commit, instructions)
	if err != nil {
		return usageError(err.Error())
	}
	registry, agent, err := c.resolveOneAgent(spelling, agentRef, selector.VerbReview)
	if err != nil {
		return err
	}
	if c.reviewBinding == nil || c.reviews == nil {
		return errors.New("agent review unavailable: native Codex review is not configured")
	}
	timeout := c.reviewTimeout
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	actionCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	liveThreadID, live, err := c.reviewBinding.LiveThreadID(actionCtx, agent.Status.PaneRef)
	if err != nil {
		return fmt.Errorf("agent review unavailable: read current live Pane binding: %w", err)
	}
	binding, err := resolveExactReviewBinding(registry, agent, liveThreadID, live)
	if err != nil {
		return err
	}
	result, err := c.reviews.Start(actionCtx, binding.ThreadID, target)
	if err != nil {
		if errors.Is(err, corecap.ErrUnavailable) {
			return fmt.Errorf("agent review unavailable: %w", err)
		}
		return fmt.Errorf("agent review: %w", err)
	}
	if strings.TrimSpace(result.ThreadID) == "" || strings.TrimSpace(result.TurnID) == "" || result.Status == corecap.ReviewUnknown {
		return errors.New("agent review: app-server returned an incomplete or unknown initial review turn")
	}
	if result.ThreadID != binding.ThreadID {
		return fmt.Errorf("agent review: app-server returned review thread %q for exact inline thread %q", result.ThreadID, binding.ThreadID)
	}
	kind := reviewInteraction(result.Status)
	var committed coremetadata.Agent
	if err := c.mutateAgent(binding.AgentUID, func(working *coremetadata.Registry, mut coremetadata.Mutator) error {
		current, ok := working.Agent(binding.AgentUID)
		if !ok {
			return errors.New("agent review: exact Agent disappeared before lifecycle commit")
		}
		if _, err := resolveRegistryReviewBinding(*working, *current, binding); err != nil {
			return err
		}
		updated, err := mut.SetAgentInteraction(working, binding.AgentUID, kind, string(coremetadata.InteractionSourceProviderControl))
		committed = updated.Clone()
		return err
	}); err != nil {
		return err
	}
	if err := c.mirrorAgentInteraction(committed, kind); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "review started thread=%s turn=%s status=%s\n", result.ThreadID, result.TurnID, result.Status)
	return err
}

func parseReviewTarget(base, commit, instructions string) (corecap.ReviewTarget, error) {
	values := []struct {
		kind  corecap.ReviewTargetKind
		value string
	}{{corecap.ReviewBaseBranch, base}, {corecap.ReviewCommit, commit}, {corecap.ReviewCustom, instructions}}
	target := corecap.ReviewTarget{Kind: corecap.ReviewUncommitted}
	set := 0
	for _, value := range values {
		if strings.TrimSpace(value.value) != "" {
			target = corecap.ReviewTarget{Kind: value.kind, Value: strings.TrimSpace(value.value)}
			set++
		}
	}
	if set > 1 {
		return corecap.ReviewTarget{}, errors.New("agent review accepts only one of --base, --commit, or --instructions")
	}
	return target, nil
}

func resolveRegistryReviewBinding(registry coremetadata.Registry, agent coremetadata.Agent, want exactReviewBinding) (exactReviewBinding, error) {
	threadID := ""
	if agent.Status.SessionRef != nil && agent.Status.SessionRef.Codex != nil {
		threadID = agent.Status.SessionRef.Codex.ThreadID
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	exactOwner := ok && pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent && pane.Metadata.OwnerRef.UID == want.AgentUID
	if agent.Metadata.UID != want.AgentUID || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != want.PaneUID || strings.TrimSpace(threadID) != want.ThreadID || !exactOwner || pane.Status.Activation.Generation != want.Generation {
		return exactReviewBinding{}, errors.New("agent review: exact Agent binding changed before lifecycle commit; stale lifecycle write refused")
	}
	return want, nil
}

func reviewInteraction(status corecap.ReviewStatus) coremetadata.AgentInteractionKind {
	switch status {
	case corecap.ReviewCompleted:
		return coremetadata.InteractionResponseComplete
	case corecap.ReviewFailed, corecap.ReviewInterrupted:
		return coremetadata.InteractionIdle
	default:
		return coremetadata.InteractionInProgress
	}
}
