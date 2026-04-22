package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

type tmuxPopupDisplayer interface {
	DisplayPopup(ctx context.Context, command string) error
}

type tmuxCommand struct {
	popup      tmuxPopupDisplayer
	popupErr   error
	executable func() (string, error)
}

func newTmuxCommand() *tmuxCommand {
	return &tmuxCommand{
		popup:      inttmux.NewClient(inttmux.ExecRunner{}),
		executable: os.Executable,
	}
}

// Run manages tmux-specific helper subcommands.
func (c *tmuxCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printTmuxUsage(stderr)
		return errors.New("tmux requires a subcommand")
	}

	switch fs.Arg(0) {
	case "popup-preview":
		return c.runPopupPreview(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printTmuxUsage(stdout)
		return nil
	default:
		printTmuxUsage(stderr)
		return fmt.Errorf("unknown tmux subcommand: %s", fs.Arg(0))
	}
}

func (c *tmuxCommand) runPopupPreview(args []string, _ io.Writer, stderr io.Writer) error {
	sessionName, err := parseTmuxPopupPreviewArgs(args, stderr)
	if err != nil {
		return err
	}

	popup, err := c.requirePopup()
	if err != nil {
		return err
	}

	binaryPath, err := c.resolveExecutable()
	if err != nil {
		return err
	}

	command, err := inttmux.BuildPopupPreviewCommand(binaryPath, sessionName)
	if err != nil {
		return fmt.Errorf("build tmux popup preview command: %w", err)
	}

	if err := popup.DisplayPopup(context.Background(), command); err != nil {
		return fmt.Errorf("open tmux popup preview for %q: %w", sessionName, err)
	}

	return nil
}

func (c *tmuxCommand) requirePopup() (tmuxPopupDisplayer, error) {
	if c.popupErr != nil {
		return nil, fmt.Errorf("configure tmux popup entry: %w", c.popupErr)
	}
	if c.popup == nil {
		return nil, errors.New("configure tmux popup entry: tmux popup entry is not configured")
	}
	return c.popup, nil
}

func (c *tmuxCommand) resolveExecutable() (string, error) {
	if c.executable == nil {
		return "", errors.New("resolve tmux popup executable: executable resolver is not configured")
	}

	path, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve tmux popup executable: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("resolve tmux popup executable: executable path is empty")
	}

	return path, nil
}

func parseTmuxPopupPreviewArgs(args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printTmuxUsage(stderr)
		return "", fmt.Errorf("tmux popup-preview requires exactly 1 argument: <session>")
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printTmuxUsage(stderr)
		return "", fmt.Errorf("tmux popup-preview requires a non-empty <session> argument")
	}

	return sessionName, nil
}

func printTmuxUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux tmux popup-preview <session>")
}
