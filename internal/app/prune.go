package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type pruneInventoryResolver interface {
	ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error)
}

type pruneSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type pruneCommand struct {
	diagnostics          *diagnostics.LifecycleRecorder
	inventory            pruneInventoryResolver
	killer               pruneSessionKiller
	reconcileNotify      func()
	cleanupKilledSession func(string)
	// project owns the canonical `prune project` route. It is a separate
	// handler because it prunes resource metadata rather than tmux runtime
	// state, which is the split the runtime/resource namespace boundary makes.
	project rawArgvCommand
	// agent owns the canonical `prune agent` route for the same reason: it
	// deletes stale Agent resources through the Registry alone and has no live
	// tmux half.
	agent rawArgvCommand
}

type previewSelectionDeleter interface {
	Delete(sessionName string) error
}

type killedSessionPreviewCleaner struct {
	store previewSelectionDeleter
	err   error
}

func newKilledSessionPreviewCleaner() *killedSessionPreviewCleaner {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return &killedSessionPreviewCleaner{err: err}
	}
	store := corepreview.NewDefaultStore(paths)
	return &killedSessionPreviewCleaner{store: store}
}

func (c *killedSessionPreviewCleaner) cleanup(sessionName string) {
	if c == nil || c.err != nil || c.store == nil {
		return
	}
	_ = c.store.Delete(sessionName)
}

func newPruneCommand(recorders ...*diagnostics.LifecycleRecorder) *pruneCommand {
	opts := []inttmux.ClientOption{}
	if len(recorders) > 0 && recorders[0] != nil {
		opts = append(opts, inttmux.WithLifecycleDiagnostics(recorders[0]))
	}
	client := inttmux.NewClient(inttmux.ExecRunner{}, opts...)
	return &pruneCommand{
		diagnostics: recorderFrom(recorders),
		inventory:   client,
		killer:      client,
		project:     newPruneProjectCommand(),
		agent:       newPruneAgentCommand(),
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
	// `prune project` is the bounded missing-root prune. `prune agent` is its
	// stale-Agent sibling.
	case "project":
		if c.project == nil {
			return errors.New("prune project: the resource registry handler is not configured")
		}
		return c.project.Run(fs.Args()[1:], stdout, stderr)
	case "agent":
		if c.agent == nil {
			return errors.New("prune agent: the resource registry handler is not configured")
		}
		return c.agent.Run(fs.Args()[1:], stdout, stderr)
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

	killedAny := false
	defer func() {
		if killedAny && c.reconcileNotify != nil {
			c.reconcileNotify()
		}
	}()
	for _, target := range targets {
		if c.diagnostics != nil {
			c.diagnostics.Mark(diagnostics.OperationSessionKill)
		}
		if err := c.killer.KillSession(context.Background(), target); err != nil {
			return fmt.Errorf("kill ephemeral session %q: %w", target, err)
		}
		killedAny = true
		if c.cleanupKilledSession != nil {
			c.cleanupKilledSession(target)
		}
	}

	return nil
}

func printPruneUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux runtime prune [--keep=N]")
	fmt.Fprintln(w, "  projmux prune project")
	fmt.Fprintln(w, "  projmux prune agent")
}
