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

type tmuxPopupClient interface {
	DisplayPopupWithOptions(ctx context.Context, command string, options inttmux.PopupOptions) error
}

type tmuxCommand struct {
	popup        tmuxPopupClient
	executable   func() (string, error)
	popupOptions func(sessionName string) inttmux.PopupOptions
}

func newTmuxCommand() *tmuxCommand {
	return &tmuxCommand{
		popup:        inttmux.NewClient(inttmux.ExecRunner{}),
		executable:   os.Executable,
		popupOptions: defaultPopupPreviewOptions,
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
		return c.runPopupPreview(fs.Args()[1:], stderr)
	case "help", "--help", "-h":
		printTmuxUsage(stdout)
		return nil
	default:
		printTmuxUsage(stderr)
		return fmt.Errorf("unknown tmux subcommand: %s", fs.Arg(0))
	}
}

func (c *tmuxCommand) runPopupPreview(args []string, stderr io.Writer) error {
	sessionName, err := parseTmuxPopupPreviewArgs(args, stderr)
	if err != nil {
		return err
	}
	if c.popup == nil {
		return errors.New("configure tmux popup client: tmux popup client is not configured")
	}
	if c.executable == nil {
		return errors.New("configure tmux popup executable: tmux popup executable resolver is not configured")
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve tmux popup executable: %w", err)
	}

	command, err := inttmux.BuildPopupPreviewCommand(binaryPath, sessionName)
	if err != nil {
		return fmt.Errorf("build tmux popup preview command for %q: %w", sessionName, err)
	}

	options := defaultPopupPreviewOptions(sessionName)
	if c.popupOptions != nil {
		options = c.popupOptions(sessionName)
	}

	if err := c.popup.DisplayPopupWithOptions(context.Background(), command, options); err != nil {
		return fmt.Errorf("display tmux popup preview for %q: %w", sessionName, err)
	}

	return nil
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

func defaultPopupPreviewOptions(sessionName string) inttmux.PopupOptions {
	return inttmux.PopupOptions{
		Width:         "80%",
		Height:        "80%",
		Title:         "projmux: " + strings.TrimSpace(sessionName),
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
}

func printTmuxUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux tmux popup-preview <session>")
}
