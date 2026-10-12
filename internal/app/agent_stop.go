package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Stop targets one existing process generation. Only its owner writes Wait;
// this command observes that evidence and never marks a failed stop Offline.
func (c *agentCommand) runStop(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	flags := resourceQueryFlags{kind: coremetadata.KindAgent}
	flags.register(fs)
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return flagParseError(err)
	}
	if len(refs) != 1 {
		return usageError("agent stop requires exactly one Agent reference")
	}
	flags.addPositionalRef(refs[0])
	reg, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbReview, false, reg)
	if err != nil {
		return MapMetadataError(err)
	}
	agent, found := reg.Agent(resolution.Matches[0].UID)
	if !found {
		return processhost.ErrStale
	}
	pane, ambiguous := processResumePane(reg, agent.Metadata.UID)
	if pane == nil || ambiguous || pane.Status.ProcessSession == nil {
		return usageError("agent stop requires one exact process Agent; tmux Agents are unsupported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), processRelaunchRetirementTimeout)
	defer cancel()
	if err = c.stopExactProcessAgent(ctx, reg, agent.Clone(), pane.Clone()); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "stopped agent uid:%s pane uid:%s\n", agent.Metadata.UID, pane.Metadata.UID)
	return err
}

func (c *agentCommand) stopExactProcessAgent(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent, pane coremetadata.Pane) error {
	if pane.Status.Activation.IsZero() && agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return nil
	}
	if pane.Status.Activation.Process == nil {
		return errors.New("agent-stop-wait-unconfirmed: exact process owner unavailable")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
		if _, err := c.callProcessTurnContext(ctx, reg, agent, pane.Status.ProcessSession.Provider, "stop", ""); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	for {
		latest, err := c.loadRegistry()
		if err != nil {
			return err
		}
		current, ok := latest.Pane(pane.Metadata.UID)
		target, found := latest.Agent(agent.Metadata.UID)
		if !ok || !found || current.Metadata.OwnerUID() != agent.Metadata.UID || current.Status.ProcessSession == nil || current.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding {
			return processhost.ErrStale
		}
		if target.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(current.Status.ProcessSession.Binding, current.Status.LastTermination) && coremetadata.SameProcessWait(current.Status.LastTermination, target.Status.LastTermination) && processActivationAbsent(*pane.Status.Activation.Process, c.readProcessIdentity()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent-stop-wait-unconfirmed: agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s: %w", agent.Metadata.UID, pane.Metadata.UID, agent.Metadata.UID, ctx.Err())
		case <-ticker.C:
		}
	}
}
