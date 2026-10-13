package app

import (
	"context"
	"flag"
	"fmt"
	"io"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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
	// VerbReview supplies only the shared exact-one cardinality cell here.
	// The required positional reference above prevents selectorless inference;
	// scope resolution and process Stop authority remain independent of its name.
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
	stopper := mutationruntime.ProcessAgentStopper{RegistryPath: c.messagePaths.registryPath, Load: c.loadRegistry, ReadIdentity: mutationruntime.ProcessIdentityReader(c.readProcessIdentity())}

	return stopper.StopExact(ctx, reg, agent, pane)
}
