package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

// sessionIdentityResolver is intentionally narrow until the sessions package exists.
// TODO: replace this with the concrete internal/core/sessions dependency.
type sessionIdentityResolver interface {
	SessionIdentityForPath(path string) (string, error)
}

type currentPathResolver interface {
	CurrentPanePath(ctx context.Context) (string, error)
}

type currentCommand struct {
	currentPath currentPathResolver
	identity    sessionIdentityResolver
	validate    func(path string) error
}

type currentPlan struct {
	CurrentPath string
	SessionName string
}

func newCurrentCommand() *currentCommand {
	return &currentCommand{
		currentPath: inttmux.NewClient(inttmux.ExecRunner{}),
		validate:    validateDirectory,
	}
}

// Run resolves the current tmux pane path and prints the next migration step.
func (c *currentCommand) Run(args []string, stdout, stderr io.Writer) error {
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

	printCurrentPlan(stdout, plan)
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

func printCurrentPlan(w io.Writer, plan currentPlan) {
	fmt.Fprintf(w, "current pane path: %s\n", plan.CurrentPath)

	if plan.SessionName != "" {
		fmt.Fprintf(w, "target session: %s\n", plan.SessionName)
		return
	}

	fmt.Fprintln(w, "TODO: wire internal/core/sessions before switch/create is implemented")
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
