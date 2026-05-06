package app

import (
	"errors"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/version"
)

// Run is the current CLI bootstrap. Feature commands will grow from here.
func Run(args []string, stdout, stderr io.Writer) error {
	return New().Run(args, stdout, stderr)
}

// UsageError marks an error caused by invalid CLI input (parse failure,
// invalid enum, missing required flag). The main entrypoint treats these as
// exit code 2.
type UsageError struct {
	Message string
}

// Error implements the error interface for UsageError.
func (e *UsageError) Error() string {
	return e.Message
}

// usageError builds a UsageError with the supplied message.
func usageError(message string) error {
	return &UsageError{Message: message}
}

// IsUsageError reports whether err (or any wrapped error) is a UsageError.
func IsUsageError(err error) bool {
	var ue *UsageError
	return errors.As(err, &ue)
}

// App wires the CLI entrypoints to concrete command handlers.
type App struct {
	ai           *aiCommand
	attention    *attentionCommand
	attach       *attachCommand
	current      *currentCommand
	doctor       *doctorCommand
	focus        *focusCommand
	initCmd      *initCommand
	kill         *killCommand
	notify       *notifyCommand
	pin          *pinCommand
	preview      *previewCommand
	prune        *pruneCommand
	sessions     *sessionsCommand
	sessionPopup *sessionPopupCommand
	settings     *settingsCommand
	setup        *setupCommand
	shell        *shellCommand
	status       *statusCommand
	statusbar    *statusbarCommand
	switcher     *switchCommand
	tag          *tagCommand
	tmux         *tmuxCommand
	update       *updateCommand
	upgrade      *upgradeCommand
	usage        *usageCommand
}

// New builds the default application graph.
func New() *App {
	ai := newAICommand()
	switcher := newSwitchCommand()
	return &App{
		ai:           ai,
		attention:    newAttentionCommand(),
		attach:       newAttachCommand(),
		current:      newCurrentCommand(),
		doctor:       newDoctorCommand(),
		focus:        newFocusCommand(),
		initCmd:      newInitCommand(),
		kill:         newKillCommand(),
		notify:       newNotifyCommand(),
		pin:          newPinCommand(),
		preview:      newPreviewCommand(),
		prune:        newPruneCommand(),
		sessions:     newSessionsCommand(),
		sessionPopup: newSessionPopupCommand(),
		settings:     newSettingsCommand(ai, switcher),
		setup:        newSetupCommand(),
		shell:        newShellCommand(),
		status:       newStatusCommand(),
		statusbar:    newStatusbarCommand(),
		switcher:     switcher,
		tag:          newTagCommand(),
		tmux:         newTmuxCommand(),
		update:       newUpdateCommand(),
		upgrade:      newUpgradeCommand(),
		usage:        newUsageCommand(),
	}
}

// Run dispatches the configured application commands.
func (a *App) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "ai":
		return a.ai.Run(args[1:], stdout, stderr)
	case "attention":
		return a.attention.Run(args[1:], stdout, stderr)
	case "attach":
		return a.attach.Run(args[1:], stdout, stderr)
	case "current":
		return a.current.Run(args[1:], stdout, stderr)
	case "doctor":
		return a.doctor.Run(args[1:], stdout, stderr)
	case "focus":
		return a.focus.Run(args[1:], stdout, stderr)
	case "init":
		return a.initCmd.Run(args[1:], stdout, stderr)
	case "kill":
		return a.kill.Run(args[1:], stdout, stderr)
	case "notify":
		return a.notify.Run(args[1:], stdout, stderr)
	case "pin":
		return a.pin.Run(args[1:], stdout, stderr)
	case "preview":
		return a.preview.Run(args[1:], stdout, stderr)
	case "prune":
		return a.prune.Run(args[1:], stdout, stderr)
	case "sessions":
		return a.sessions.Run(args[1:], stdout, stderr)
	case "session-popup":
		return a.sessionPopup.Run(args[1:], stdout, stderr)
	case "settings":
		return a.settings.Run(args[1:], stdout, stderr)
	case "setup":
		return a.setup.Run(args[1:], stdout, stderr)
	case "shell":
		return a.shell.Run(args[1:], stdout, stderr)
	case "status":
		return a.status.Run(args[1:], stdout, stderr)
	case "statusbar":
		return a.statusbar.Run(args[1:], stdout, stderr)
	case "switch":
		return a.switcher.Run(args[1:], stdout, stderr)
	case "tag":
		return a.tag.Run(args[1:], stdout, stderr)
	case "tmux":
		return a.tmux.Run(args[1:], stdout, stderr)
	case "update":
		return a.update.Run(args[1:], stdout, stderr)
	case "upgrade":
		return a.upgrade.Run(args[1:], stdout, stderr)
	case "usage":
		return a.usage.Run(args[1:], stdout, stderr)
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
	fmt.Fprintln(w, "  ai        Manage tmux AI split launch and settings")
	fmt.Fprintln(w, "  attention Manage tmux pane attention badges")
	fmt.Fprintln(w, "  attach    Open tmux lifecycle entry helpers")
	fmt.Fprintln(w, "  current   Resolve the active tmux pane path")
	fmt.Fprintln(w, "  doctor    Diagnose runtime dependencies and suggest installs")
	fmt.Fprintln(w, "  focus     Switch the active client to a session/window/pane target")
	fmt.Fprintln(w, "  init      Merge projmux keybindings into a terminal config")
	fmt.Fprintln(w, "  kill      Terminate tagged tmux sessions")
	fmt.Fprintln(w, "  notify    Persist status-bar notifications (push/list/ack)")
	fmt.Fprintln(w, "  pin       Manage pinned project directories")
	fmt.Fprintln(w, "  preview   Manage persisted tmux preview selection")
	fmt.Fprintln(w, "  prune     Trim stale tmux lifecycle state")
	fmt.Fprintln(w, "  sessions  Pick and open an existing tmux session")
	fmt.Fprintln(w, "  session-popup  Read tmux popup preview state")
	fmt.Fprintln(w, "  settings  Configure projmux")
	fmt.Fprintln(w, "  setup     Probe terminal key delivery for projmux bindings")
	fmt.Fprintln(w, "  shell     Open the isolated projmux tmux app")
	fmt.Fprintln(w, "  status    Render tmux status bar segments")
	fmt.Fprintln(w, "  statusbar Dispatch projmux status bar clicks and shortcuts")
	fmt.Fprintln(w, "  switch    Pick and open a project tmux session")
	fmt.Fprintln(w, "  tag       Manage tagged tmux sessions")
	fmt.Fprintln(w, "  tmux      Open tmux popup entry helpers")
	fmt.Fprintln(w, "  update    Check installer-aware release update status")
	fmt.Fprintln(w, "  upgrade   Self-update projmux via go install")
	fmt.Fprintln(w, "  usage     Report AI token usage across 5h and weekly windows")
	fmt.Fprintln(w, "  help      Show bootstrap help")
	fmt.Fprintln(w, "  version   Print the current version")
}
