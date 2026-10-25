package app

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// Mark-offline retires one process generation whose host and provider the
// kernel proves gone. It starts nothing and is never reported as a stop.
func (c *agentCommand) runMarkOffline(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent mark-offline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	flags := resourceQueryFlags{kind: coremetadata.KindAgent}
	flags.register(fs)
	flags.registerOutput(fs)
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return flagParseError(err)
	}
	if len(refs) != 1 {
		return usageError("agent mark-offline requires exactly one Agent reference")
	}
	flags.addPositionalRef(refs[0])
	mode, field, err := resolveOutputMode("agent mark-offline", flags.output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not an agent mark-offline projection", field))
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	// As in agent stop, VerbReview supplies only the exact-one cardinality cell.
	resolution, err := flags.resolve(selector.VerbReview, false, reg)
	if err != nil {
		return MapMetadataError(err)
	}
	uid := resolution.Matches[0].UID
	if pane, ambiguous := processResumePane(reg, uid); pane == nil || ambiguous || pane.Status.ProcessSession == nil {
		return usageError(mutationruntime.MarkOfflineProcessUnsupported + ": agent mark-offline requires one exact process Agent; tmux Agents are unsupported")
	}
	store := intmetadata.NewStore(c.messagePaths.registryPath)
	marker := mutationruntime.ProcessAgentMarkOffline{Update: store.UpdateConvergent, Mutator: intmetadata.DefaultMutator(), ReadIdentity: mutationruntime.ProcessIdentityReader(c.readProcessIdentity())}
	if err = marker.MarkExact(context.Background(), reg, uid); err != nil {
		return err
	}
	receipt := cli.OperationReceipt(mutationruntime.MarkOfflineReceipt(marker.Latest, uid, marker.PaneUID, marker.Changed))
	if err = receipt.Validate(); err != nil {
		return err
	}
	switch mode {
	case cli.OutputModeReceipt:
		return receipt.WriteJSON(stdout)
	case cli.OutputModeNone:
		return nil
	}
	state := "marked offline"
	if !marker.Changed {
		state = "already offline"
	}
	_, err = fmt.Fprintf(stdout, "%s agent uid:%s pane uid:%s termination=reconcile/unknown resumeState=resumable\n", state, uid, marker.PaneUID)
	return err
}
