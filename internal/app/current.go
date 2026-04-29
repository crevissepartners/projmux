package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type sessionIdentityResolver interface {
	SessionIdentityForPath(path string) (string, error)
}

type currentPathResolver interface {
	CurrentPanePath(ctx context.Context) (string, error)
}

type currentSessionExecutor interface {
	EnsureSession(ctx context.Context, sessionName, cwd string) error
	OpenSession(ctx context.Context, sessionName string) error
}

type currentCommand struct {
	currentPath currentPathResolver
	sessions    currentSessionExecutor
	identity    sessionIdentityResolver
	identityErr error
	validate    func(path string) error
}

type currentPlan struct {
	CurrentPath string
	SessionName string
}

func newCurrentCommand() *currentCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	identity, err := newDefaultCurrentIdentityResolver()

	return &currentCommand{
		currentPath: client,
		sessions:    client,
		identity:    identity,
		identityErr: err,
		validate:    validateDirectory,
	}
}

// Run resolves the current tmux pane path and activates the derived session.
func (c *currentCommand) Run(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("current does not accept positional arguments")
	}

	plan, err := c.plan(context.Background())
	if err != nil {
		return err
	}

	if err := c.execute(context.Background(), plan); err != nil {
		return err
	}

	return nil
}

func (c *currentCommand) plan(ctx context.Context) (currentPlan, error) {
	path, err := c.currentPath.CurrentPanePath(ctx)
	if err != nil {
		return currentPlan{}, err
	}

	if err := c.validate(path); err != nil {
		return currentPlan{}, err
	}

	plan := currentPlan{
		CurrentPath: path,
	}

	if c.identityErr != nil {
		return currentPlan{}, fmt.Errorf("configure session identity resolver: %w", c.identityErr)
	}

	if c.identity == nil {
		return plan, nil
	}

	sessionName, err := c.identity.SessionIdentityForPath(path)
	if err != nil {
		return currentPlan{}, fmt.Errorf("resolve session identity: %w", err)
	}

	plan.SessionName = sessionName
	return plan, nil
}

func (c *currentCommand) execute(ctx context.Context, plan currentPlan) error {
	if plan.SessionName == "" {
		return fmt.Errorf("current command requires a target session")
	}
	if c.sessions == nil {
		return fmt.Errorf("current session executor is not configured")
	}

	if err := c.sessions.EnsureSession(ctx, plan.SessionName, plan.CurrentPath); err != nil {
		return fmt.Errorf("ensure tmux session %q: %w", plan.SessionName, err)
	}
	if err := c.sessions.OpenSession(ctx, plan.SessionName); err != nil {
		return fmt.Errorf("open tmux session %q: %w", plan.SessionName, err)
	}

	return nil
}

type currentIdentityResolver struct {
	namer coresessions.Namer
}

func newDefaultCurrentIdentityResolver() (sessionIdentityResolver, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return currentIdentityResolver{
		namer: coresessions.NewNamer(homeDir),
	}, nil
}

func (r currentIdentityResolver) SessionIdentityForPath(path string) (string, error) {
	return r.namer.SessionName(filepath.Clean(path)), nil
}

func validateDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat current pane path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("current pane path is not a directory: %s", path)
	}
	return nil
}
