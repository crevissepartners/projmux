package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type attachInventoryResolver interface {
	ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error)
}

type attachSessionManager interface {
	EnsureSession(ctx context.Context, sessionName, cwd string) error
	CreateEphemeralSession(ctx context.Context, sessionName, cwd string) error
	OpenSession(ctx context.Context, sessionName string) error
}

type attachSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type attachCommand struct {
	diagnostics          *diagnostics.LifecycleRecorder
	inventory            attachInventoryResolver
	sessions             attachSessionManager
	killer               attachSessionKiller
	homeDir              func() (string, error)
	workingDir           func() (string, error)
	now                  func() time.Time
	cleanupKilledSession func(string)
	ensureHomeSession    func(context.Context, string, string) error
	// lookupEnv answers the inside-tmux question for `attach project`.
	lookupEnv func(string) string
	// switcher owns the Project open path that `attach project` forwards to.
	switcher rawArgvCommand
}

func newAttachCommand(recorders ...*diagnostics.LifecycleRecorder) *attachCommand {
	client := defaultTmuxClient(recorders...)
	control := newShellCommand(nil, recorders...)
	return &attachCommand{
		diagnostics: recorderFrom(recorders),
		inventory:   client,
		sessions:    client,
		killer:      client,
		homeDir:     os.UserHomeDir,
		workingDir:  os.Getwd,
		now:         time.Now,
		lookupEnv:   os.Getenv,
		ensureHomeSession: func(ctx context.Context, sessionName, cwd string) error {
			return control.prepareControlSession(ctx, defaultAppSocket, control.defaultConfigPath(), shellTarget{SessionName: sessionName, CWD: cwd})
		},
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
	case "project":
		return c.runProject(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAttachUsage(stdout)
		return nil
	default:
		printAttachUsage(stderr)
		return fmt.Errorf("unknown attach subcommand: %s", fs.Arg(0))
	}
}

// runProject is the canonical `attach project <ref>` entry point.
//
// This is the one navigation route that is allowed to materialize a Project: a
// caller outside tmux has no client to redirect, so entering a Project runtime
// means creating the tmux session when it is missing and then attaching to it.
// Called from inside a tmux client it refuses with exit 2 and points at `focus
// project`, which moves the existing client and never materializes anything.
func (c *attachCommand) runProject(args []string, stdout, stderr io.Writer) error {
	const spelling = "attach project"

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 1 {
		return usageError(spelling + " requires exactly one Project reference")
	}
	if c.insideTmuxClient() {
		return usageError(fmt.Sprintf(
			"%s is the outside-tmux entry point; from inside a tmux client run `projmux focus project %s` instead",
			spelling, fs.Arg(0)))
	}
	return forwardRawArgv(c.switcher, spelling, "switch", []string{"open"}, fs.Args(), stdout, stderr)
}

// insideTmuxClient reports whether this invocation already runs inside a tmux
// client. $TMUX is the tmux-set marker for exactly that.
func (c *attachCommand) insideTmuxClient() bool {
	if c.lookupEnv == nil {
		return false
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *attachCommand) runAuto(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("attach auto", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keepCount := fs.Int("keep", 3, "number of unattached ephemeral sessions to retain")
	fallback := fs.String("fallback", "home", "fallback session policy: home or ephemeral")

	if err := fs.Parse(args); err != nil {
		printAttachUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printAttachUsage(stderr)
		return fmt.Errorf("attach auto does not accept positional arguments")
	}
	if *fallback != "home" && *fallback != "ephemeral" {
		printAttachUsage(stderr)
		return fmt.Errorf("attach auto fallback must be one of: home, ephemeral")
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
	return c.executeAutoAttachPlan(context.Background(), plan, *fallback, homeDir)
}

// executeAutoAttachPlan owns the ordered mutation boundary after planning.
// Keeping it separate makes the real kill-then-open composite flow directly
// testable without changing the planner's session-retention policy.
func (c *attachCommand) executeAutoAttachPlan(ctx context.Context, plan lifecycle.AutoAttachPlan, fallback, homeDir string) error {

	for _, target := range plan.PruneTargets {
		if c.killer == nil {
			return fmt.Errorf("prune auto-attach ephemeral sessions: killer is not configured")
		}
		if err := c.killer.KillSession(ctx, target); err != nil {
			return fmt.Errorf("prune auto-attach ephemeral session %q: %w", target, err)
		}
		if c.cleanupKilledSession != nil {
			c.cleanupKilledSession(target)
		}
	}

	if plan.EnsureHomeSession {
		if c.sessions == nil {
			return fmt.Errorf("ensure auto-attach home session: session manager is not configured")
		}

		if fallback == "ephemeral" {
			cwd, err := c.resolveWorkingDir()
			if err != nil {
				return err
			}
			if c.now == nil {
				return fmt.Errorf("resolve auto-attach ephemeral clock: clock is not configured")
			}

			sessionName := lifecycle.EphemeralSessionName(cwd, c.now())
			if err := c.sessions.CreateEphemeralSession(ctx, sessionName, cwd); err != nil {
				return fmt.Errorf("create auto-attach ephemeral session %q: %w", sessionName, err)
			}
			plan.AttachTarget = sessionName
		} else if c.ensureHomeSession == nil {
			return fmt.Errorf("ensure auto-attach home session: canonical ControlSession bootstrap is not configured")
		} else if err := c.ensureHomeSession(ctx, plan.AttachTarget, homeDir); err != nil {
			return fmt.Errorf("ensure auto-attach home session %q: %w", plan.AttachTarget, err)
		}
	}

	if c.sessions == nil {
		return fmt.Errorf("open auto-attach target: session manager is not configured")
	}
	if err := c.sessions.OpenSession(ctx, plan.AttachTarget); err != nil {
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

func (c *attachCommand) resolveWorkingDir() (string, error) {
	if c.workingDir == nil {
		return "", fmt.Errorf("resolve auto-attach working directory: working directory resolver is not configured")
	}

	cwd, err := c.workingDir()
	if err != nil {
		return "", fmt.Errorf("resolve auto-attach working directory: %w", err)
	}

	return filepath.Clean(cwd), nil
}

func printAttachUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux attach project <ref>")
	fmt.Fprintln(w, "  projmux runtime attach [--keep=N] [--fallback=home|ephemeral]")
}
