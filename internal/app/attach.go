package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/es5h/projmux/internal/core/lifecycle"
	coresessions "github.com/es5h/projmux/internal/core/sessions"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

type attachInventoryResolver interface {
	ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error)
}

type attachSessionManager interface {
	EnsureSession(ctx context.Context, sessionName, cwd string) error
	OpenSession(ctx context.Context, sessionName string) error
}

type attachSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type attachCommand struct {
	inventory attachInventoryResolver
	sessions  attachSessionManager
	killer    attachSessionKiller
	homeDir   func() (string, error)
}

func newAttachCommand() *attachCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	return &attachCommand{
		inventory: client,
		sessions:  client,
		killer:    client,
		homeDir:   os.UserHomeDir,
	}
}

func (c *attachCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printAttachUsage(stderr)
		return errors.New("attach requires a subcommand")
	}

	switch fs.Arg(0) {
	case "auto":
		return c.runAuto(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAttachUsage(stdout)
		return nil
	default:
		printAttachUsage(stderr)
		return fmt.Errorf("unknown attach subcommand: %s", fs.Arg(0))
	}
}

func (c *attachCommand) runAuto(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("attach auto", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keepCount := fs.Int("keep", 3, "number of unattached ephemeral sessions to retain")

	if err := fs.Parse(args); err != nil {
		printAttachUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printAttachUsage(stderr)
		return fmt.Errorf("attach auto does not accept positional arguments")
	}

	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return err
	}

	inventory, err := c.resolveInventory(context.Background())
	if err != nil {
		return err
	}

	homeSession := coresessions.NewNamer(homeDir).SessionName(homeDir)
	plan, err := lifecycle.PlanAutoAttach(lifecycle.AutoAttachInputs{
		Sessions:    inventory,
		HomeSession: homeSession,
		KeepCount:   *keepCount,
	})
	if err != nil {
		return fmt.Errorf("plan auto attach: %w", err)
	}

	for _, target := range plan.PruneTargets {
		if c.killer == nil {
			return fmt.Errorf("prune auto-attach ephemeral sessions: killer is not configured")
		}
		if err := c.killer.KillSession(context.Background(), target); err != nil {
			return fmt.Errorf("prune auto-attach ephemeral session %q: %w", target, err)
		}
	}

	if plan.EnsureHomeSession {
		if c.sessions == nil {
			return fmt.Errorf("ensure auto-attach home session: session manager is not configured")
		}
		if err := c.sessions.EnsureSession(context.Background(), plan.AttachTarget, homeDir); err != nil {
			return fmt.Errorf("ensure auto-attach home session %q: %w", plan.AttachTarget, err)
		}
	}

	if c.sessions == nil {
		return fmt.Errorf("open auto-attach target: session manager is not configured")
	}
	if err := c.sessions.OpenSession(context.Background(), plan.AttachTarget); err != nil {
		return fmt.Errorf("open auto-attach target %q: %w", plan.AttachTarget, err)
	}

	return nil
}

func (c *attachCommand) resolveHomeDir() (string, error) {
	if c.homeDir == nil {
		return "", fmt.Errorf("resolve auto-attach home directory: home directory resolver is not configured")
	}

	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve auto-attach home directory: %w", err)
	}

	return filepath.Clean(homeDir), nil
}

func (c *attachCommand) resolveInventory(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	if c.inventory == nil {
		return nil, fmt.Errorf("resolve auto-attach inventory: inventory resolver is not configured")
	}

	sessions, err := c.inventory.ListEphemeralSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve auto-attach inventory: %w", err)
	}

	return sessions, nil
}

func printAttachUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux attach auto [--keep=N]")
}
