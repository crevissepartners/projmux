package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/version"
)

// legacyHookMigrationOnce ensures the best-effort declarative migration only
// runs once per process invocation. Settings UI entry points still trigger
// migration directly so users who never set PROJMUX_CWD still see the
// converted entries.
var legacyHookMigrationOnce sync.Once

func runLegacyHookMigrations() {
	legacyHookMigrationOnce.Do(func() {
		// Project migration only fires when PROJMUX_CWD is set; otherwise
		// the caller invoked projmux outside any project context and we
		// have nothing to migrate.
		if cwd := os.Getenv("PROJMUX_CWD"); cwd != "" {
			_, _ = hooks.MigrateProjectLegacyScripts(cwd, "", os.Stderr)
		}
		_, _ = hooks.MigrateGlobalLegacyScripts(os.Getenv, os.UserHomeDir, "", os.Stderr)
	})
}

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
	hook         *hookCommand
	initCmd      *initCommand
	kill         *killCommand
	notify       *notifyCommand
	pin          *pinCommand
	popupWaitKey *popupWaitKeyCommand
	preview      *previewCommand
	prune        *pruneCommand
	quit         *quitCommand
	sessions     *sessionsCommand
	sessionState *sessionStateCommand
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
	welcome      *welcomeCommand
	window       *windowCommand
}

// New builds the default application graph.
func New() *App {
	ai := newAICommand()
	switcher := newSwitchCommand()
	update := newUpdateCommand()
	quit := newQuitCommand()
	return &App{
		ai:           ai,
		attention:    newAttentionCommand(),
		attach:       newAttachCommand(),
		current:      newCurrentCommand(),
		doctor:       newDoctorCommand(),
		focus:        newFocusCommand(),
		hook:         newHookCommand(),
		initCmd:      newInitCommand(),
		kill:         newKillCommand(),
		notify:       newNotifyCommand(),
		pin:          newPinCommand(),
		popupWaitKey: newPopupWaitKeyCommand(),
		preview:      newPreviewCommand(),
		prune:        newPruneCommand(),
		quit:         quit,
		sessions:     newSessionsCommand(),
		sessionState: newSessionStateCommand(),
		sessionPopup: newSessionPopupCommand(),
		settings:     newSettingsCommand(ai, switcher, update, quit),
		setup:        newSetupCommand(),
		shell:        newShellCommand(update),
		status:       newStatusCommand(),
		statusbar:    newStatusbarCommand(),
		switcher:     switcher,
		tag:          newTagCommand(),
		tmux:         newTmuxCommand(),
		update:       update,
		upgrade:      newUpgradeCommand(),
		usage:        newUsageCommand(),
		welcome:      newWelcomeCommand(update),
		window:       newWindowCommand(),
	}
}

// Run dispatches the configured application commands.
func (a *App) Run(args []string, stdout, stderr io.Writer) error {
	runLegacyHookMigrations()
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
	case "hook":
		return a.hook.Run(args[1:], stdout, stderr)
	case "init":
		return a.initCmd.Run(args[1:], stdout, stderr)
	case "kill":
		return a.kill.Run(args[1:], stdout, stderr)
	case "notify":
		return a.notify.Run(args[1:], stdout, stderr)
	case "pin":
		return a.pin.Run(args[1:], stdout, stderr)
	case "popup-wait-key":
		// Hidden helper: invoked from statusbar display-only popup payloads to
		// read a single key from /dev/tty and exit. Intentionally absent from
		// printUsage so `projmux help` stays focused on user-facing commands.
		return a.popupWaitKey.Run(args[1:], stdout, stderr)
	case "preview":
		return a.preview.Run(args[1:], stdout, stderr)
	case "prune":
		return a.prune.Run(args[1:], stdout, stderr)
	case "quit":
		return a.quit.Run(args[1:], stdout, stderr)
	case "sessions":
		return a.sessions.Run(args[1:], stdout, stderr)
	case "session-state":
		return a.sessionState.Run(args[1:], stdout, stderr)
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
	case "welcome":
		return a.welcome.Run(args[1:], stdout, stderr)
	case "window":
		return a.window.Run(args[1:], stdout, stderr)
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
	fmt.Fprintln(w, "  attention View and manage live tmux pane attention state")
	fmt.Fprintln(w, "  attach    Open tmux lifecycle entry helpers")
	fmt.Fprintln(w, "  current   Resolve the active tmux pane path")
	fmt.Fprintln(w, "  doctor    Diagnose runtime dependencies and suggest installs")
	fmt.Fprintln(w, "  focus     Switch the active client to a session/window/pane target")
	fmt.Fprintln(w, "  hook      List, edit, validate, and trust lifecycle hook config")
	fmt.Fprintln(w, "  init      Preview/apply supported terminal key delivery mappings")
	fmt.Fprintln(w, "  kill      Terminate tagged tmux sessions")
	fmt.Fprintln(w, "  notify    Manage the pending AI notify queue (push/list/ack/reconcile)")
	fmt.Fprintln(w, "  pin       Manage pinned project directories")
	fmt.Fprintln(w, "  preview   Manage persisted tmux preview selection")
	fmt.Fprintln(w, "  prune     Trim stale tmux lifecycle state")
	fmt.Fprintln(w, "  quit      Quit the app-owned projmux tmux runtime")
	fmt.Fprintln(w, "  sessions  Pick and open an existing tmux session")
	fmt.Fprintln(w, "  session-state  Inspect and manage saved tmux session snapshots")
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
	fmt.Fprintln(w, "  welcome   Reprint the shell welcome guide")
	fmt.Fprintln(w, "  window    Open recent window navigation surfaces")
	fmt.Fprintln(w, "  help      Show bootstrap help")
	fmt.Fprintln(w, "  version   Print the current version")
}
