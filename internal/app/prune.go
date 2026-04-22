package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/es5h/projmux/internal/core/lifecycle"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

type pruneInventoryResolver interface {
	ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error)
}

type pruneSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type pruneCommand struct {
	inventory pruneInventoryResolver
	killer    pruneSessionKiller
}

func newPruneCommand() *pruneCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	return &pruneCommand{
		inventory: client,
		killer:    client,
	}
}

func (c *pruneCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printPruneUsage(stderr)
		return errors.New("prune requires a subcommand")
	}

	switch fs.Arg(0) {
	case "ephemeral":
		return c.runEphemeral(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printPruneUsage(stdout)
		return nil
	default:
		printPruneUsage(stderr)
		return fmt.Errorf("unknown prune subcommand: %s", fs.Arg(0))
	}
}

func (c *pruneCommand) runEphemeral(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("prune ephemeral", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keepCount := fs.Int("keep", 3, "number of unattached ephemeral sessions to retain")

	if err := fs.Parse(args); err != nil {
		printPruneUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printPruneUsage(stderr)
		return fmt.Errorf("prune ephemeral does not accept positional arguments")
	}
	if c.inventory == nil {
		return fmt.Errorf("resolve ephemeral sessions to prune: inventory resolver is not configured")
	}

	sessions, err := c.inventory.ListEphemeralSessions(context.Background())
	if err != nil {
		return fmt.Errorf("resolve ephemeral sessions to prune: %w", err)
	}

	targets, err := lifecycle.PruneEphemeralTargets(sessions, *keepCount)
	if err != nil {
		return fmt.Errorf("plan ephemeral prune: %w", err)
	}
	if len(targets) == 0 {
		return nil
	}
	if c.killer == nil {
		return fmt.Errorf("kill ephemeral sessions to prune: killer is not configured")
	}

	for _, target := range targets {
		if err := c.killer.KillSession(context.Background(), target); err != nil {
			return fmt.Errorf("kill ephemeral session %q: %w", target, err)
		}
	}

	return nil
}

func printPruneUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux prune ephemeral [--keep=N]")
}
