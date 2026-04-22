package app

import (
	"fmt"
	"io"

	"github.com/es5h/projmux/internal/version"
)

// Run is the current CLI bootstrap. Feature commands will grow from here.
func Run(args []string, stdout, stderr io.Writer) error {
	return New().Run(args, stdout, stderr)
}

// App wires the CLI entrypoints to concrete command handlers.
type App struct {
	current      *currentCommand
	kill         *killCommand
	pin          *pinCommand
	preview      *previewCommand
	sessionPopup *sessionPopupCommand
	switcher     *switchCommand
	tag          *tagCommand
}

// New builds the default application graph.
func New() *App {
	return &App{
		current:      newCurrentCommand(),
		kill:         newKillCommand(),
		pin:          newPinCommand(),
		preview:      newPreviewCommand(),
		sessionPopup: newSessionPopupCommand(),
		switcher:     newSwitchCommand(),
		tag:          newTagCommand(),
	}
}

// Run dispatches the configured application commands.
func (a *App) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "current":
		return a.current.Run(args[1:], stdout, stderr)
	case "kill":
		return a.kill.Run(args[1:], stdout, stderr)
	case "pin":
		return a.pin.Run(args[1:], stdout, stderr)
	case "preview":
		return a.preview.Run(args[1:], stdout, stderr)
	case "session-popup":
		return a.sessionPopup.Run(args[1:], stdout, stderr)
	case "switch":
		return a.switcher.Run(args[1:], stdout, stderr)
	case "tag":
		return a.tag.Run(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		_, err := fmt.Fprintf(stdout, "projmux %s\n", version.String())
		return err
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "projmux")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  current   Resolve the active tmux pane path")
	fmt.Fprintln(w, "  kill      Terminate tagged tmux sessions")
	fmt.Fprintln(w, "  pin       Manage pinned project directories")
	fmt.Fprintln(w, "  preview   Manage persisted tmux preview selection")
	fmt.Fprintln(w, "  session-popup  Read tmux popup preview state")
	fmt.Fprintln(w, "  switch    Pick and open a project tmux session")
	fmt.Fprintln(w, "  tag       Manage tagged tmux sessions")
	fmt.Fprintln(w, "  help      Show bootstrap help")
	fmt.Fprintln(w, "  version   Print the current version")
}
