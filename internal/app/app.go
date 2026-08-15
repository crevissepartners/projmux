package app

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/version"
)

// legacyHookMigrationOnce ensures the best-effort declarative migration only
// runs once per process invocation. Settings UI entry points still trigger
// migration directly so users who never set PROJMUX_CWD still see the
// converted entries.
var legacyHookMigrationOnce sync.Once

// lifecycleMutationSurfaceInventory is the maintained Phase 2 production
// inventory. Each entry names an explicit CLI route that reaches a shared
// recorder-backed session/apply mutation; automatic hooks and read-only probes
// are intentionally absent.
var lifecycleMutationSurfaceInventory = []string{
	"attach auto",
	"current",
	"kill tagged",
	"sessions open/kill",
	"switch create/restore/open/kill",
	"tmux apply",
	"session-popup open",
	"window recent",
	"prune ephemeral",
	"focus switch-client",
	"shell open-app",
	"snapshot replay create",
	"popup-toggle cancel restore",
}

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

// RunWithLifecycleDiagnostics executes one CLI invocation with the shared
// process recorder used by runtime lifecycle operations.
func RunWithLifecycleDiagnostics(args []string, stdout, stderr io.Writer, recorder *diagnostics.LifecycleRecorder) error {
	if recorder == nil {
		return New().Run(args, stdout, stderr)
	}
	return NewWithLifecycleDiagnostics(recorder).Run(args, stdout, stderr)
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
	lifecycle    *diagnostics.LifecycleRecorder
	ai           *aiCommand
	attention    *attentionCommand
	attach       *attachCommand
	current      *currentCommand
	delete       *deleteCommand
	describe     *describeCommand
	doctor       *doctorCommand
	diagnostics  *diagnosticsCommand
	focus        *focusCommand
	get          *getCommand
	hook         *hookCommand
	rebind       *rebindCommand
	rename       *renameCommand
	restore      *restoreCommand
	runtime      *runtimeCommand
	keyBroker    *keyBrokerCommand
	kill         *killCommand
	notify       *notifyCommand
	pin          *pinCommand
	popupWaitKey *popupWaitKeyCommand
	preview      *previewCommand
	prune        *pruneCommand
	quit         *quitCommand
	resources    *resourceCommand
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
	usage        *usagecmd.Command
	welcome      *welcomeCommand
	window       *windowCommand
}

// New builds the default application graph.
func New() *App {
	return NewWithLifecycleDiagnostics(nil)
}

// NewWithLifecycleDiagnostics builds the application graph with one recorder
// shared by every Phase 2 lifecycle surface.
func NewWithLifecycleDiagnostics(recorder *diagnostics.LifecycleRecorder) *App {
	sessionStateDiagnostics := recorder.SessionState()
	notifyFocusDiagnostics := recorder.NotifyFocus()
	aiOperationalDiagnostics := recorder.AI()
	resourceOperationalDiagnostics := recorder.Resource()
	ai := newAICommand()
	ai.notifyDiagnostics = notifyFocusDiagnostics
	ai.operationalDiagnostics = aiOperationalDiagnostics
	ai.producer = newAttentionNotifyProducer(notifyFocusDiagnostics)
	switcher := newSwitchCommand(recorder)
	switcher.sessionStateDiagnostics = sessionStateDiagnostics
	attach := newAttachCommand(recorder)
	kill := newKillCommand(recorder)
	sessions := newSessionsCommand(recorder)
	update := newUpdateCommand()
	quit := newQuitCommand()
	notifyCmd := newNotifyCommand(newDefaultLivePaneLister())
	notifyCmd.diagnostics = notifyFocusDiagnostics
	pruneCmd := newPruneCommand(recorder)
	pruneCmd.sessionStateDiagnostics = sessionStateDiagnostics
	previewCleaner := newKilledSessionPreviewCleaner()
	cleanupKilledSession := previewCleaner.cleanup
	attach.cleanupKilledSession = cleanupKilledSession
	kill.cleanupKilledSession = cleanupKilledSession
	sessions.cleanupKilledSession = cleanupKilledSession
	switcher.cleanupKilledSession = cleanupKilledSession
	pruneCmd.cleanupKilledSession = cleanupKilledSession
	pruneCmd.reconcileNotify = func() {
		_ = notifyCmd.runReconcileWithOwnership(nil, io.Discard, io.Discard, false)
	}
	initCmd := newInitCommand()
	sessionStateCmd := newSessionStateCommand()
	sessionStateCmd.diagnostics = sessionStateDiagnostics
	settingsCmd := newSettingsCommand(ai, switcher, update, quit)
	settingsCmd.sessionStateDiagnostics = sessionStateDiagnostics
	tmuxCmd := newTmuxCommand(recorder)
	tmuxCmd.sessionStateDiagnostics = sessionStateDiagnostics
	attentionCmd := newAttentionCommand()
	attentionCmd.producer = newAttentionNotifyProducer(notifyFocusDiagnostics)
	focusCmd := newFocusCommand(recorder)
	focusCmd.notifyDiagnostics = notifyFocusDiagnostics
	resourcesCmd := newResourceCommand()
	resourcesCmd.diagnostics = resourceOperationalDiagnostics
	// Canonical verb-to-kind routes. The registry-backed kinds own their own
	// handler; the kinds whose behavior already exists forward raw argv to the
	// current handler, so the canonical spelling is a parity alias rather than a
	// second implementation.
	tagCmd := newTagCommand()
	getCmd := newGetCommand()
	getCmd.notify = notifyCmd
	getCmd.snapshots = sessionStateCmd
	deleteCmd := newDeleteCommand()
	deleteCmd.notify = notifyCmd
	deleteCmd.snapshots = sessionStateCmd
	restoreCmd := newRestoreCommand()
	restoreCmd.snapshots = sessionStateCmd
	runtimeCmd := newRuntimeCommand()
	runtimeCmd.sessions = sessions
	runtimeCmd.attach = attach
	runtimeCmd.kill = kill
	runtimeCmd.tag = tagCmd
	runtimeCmd.prune = pruneCmd
	attach.switcher = switcher
	return &App{
		lifecycle:    recorder,
		ai:           ai,
		attention:    attentionCmd,
		attach:       attach,
		current:      newCurrentCommand(recorder),
		delete:       deleteCmd,
		describe:     newDescribeCommand(),
		doctor:       newDoctorCommand(),
		diagnostics:  newDiagnosticsCommand(),
		focus:        focusCmd,
		get:          getCmd,
		hook:         newHookCommand(),
		rebind:       newRebindCommand(),
		rename:       newRenameCommand(),
		restore:      restoreCmd,
		runtime:      runtimeCmd,
		keyBroker:    newKeyBrokerCommand(),
		kill:         kill,
		notify:       notifyCmd,
		pin:          newPinCommand(),
		popupWaitKey: newPopupWaitKeyCommand(),
		preview:      newPreviewCommand(),
		prune:        pruneCmd,
		quit:         quit,
		resources:    resourcesCmd,
		sessions:     sessions,
		sessionState: sessionStateCmd,
		sessionPopup: newSessionPopupCommand(recorder),
		settings:     settingsCmd,
		setup:        newSetupCommand(initCmd),
		shell:        newShellCommand(update, recorder),
		status:       newStatusCommand(),
		statusbar:    newStatusbarCommand(),
		switcher:     switcher,
		tag:          tagCmd,
		tmux:         tmuxCmd,
		update:       update,
		upgrade:      newUpgradeCommand(),
		usage:        usagecmd.New(nil),
		welcome:      newWelcomeCommand(update),
		window:       newWindowCommand(recorder),
	}
}

func recorderFrom(recorders []*diagnostics.LifecycleRecorder) *diagnostics.LifecycleRecorder {
	if len(recorders) == 0 {
		return nil
	}
	return recorders[0]
}

func lifecycleOpenOperation(lookupEnv func(string) string) func() diagnostics.Operation {
	return func() diagnostics.Operation {
		if lookupEnv != nil && strings.TrimSpace(lookupEnv("TMUX")) != "" {
			return diagnostics.OperationSessionSwitch
		}
		return diagnostics.OperationSessionAttach
	}
}

// rawArgvCommand is the shared shape of every existing command handler. Phase 0
// bridges forward raw argv to these handlers without reinterpreting it.
type rawArgvCommand interface {
	Run(args []string, stdout, stderr io.Writer) error
}

// routeHandlers binds every manifest route token to its existing handler. The
// map is the single wiring point between the Cobra route catalog and the
// current command graph; `help` and `version` are owned by the root policy.
func (a *App) routeHandlers() map[string]cli.Handler {
	commands := map[string]rawArgvCommand{
		"ai":          a.ai,
		"attention":   a.attention,
		"attach":      a.attach,
		"current":     a.current,
		"delete":      a.delete,
		"describe":    a.describe,
		"doctor":      a.doctor,
		"diagnostics": a.diagnostics,
		"focus":       a.focus,
		"get":         a.get,
		"hook":        a.hook,
		// Hidden Darwin helper: captures physical portable key chords while a
		// projmux tmux client is focused and feeds them through its root table.
		"key-broker": a.keyBroker,
		"kill":       a.kill,
		"notify":     a.notify,
		"pin":        a.pin,
		// Hidden helper: invoked from statusbar display-only popup payloads to
		// read a single key from /dev/tty and exit. Intentionally absent from
		// the primary help listing so `projmux help` stays focused on
		// user-facing commands.
		"popup-wait-key": a.popupWaitKey,
		"preview":        a.preview,
		"prune":          a.prune,
		"quit":           a.quit,
		"rebind":         a.rebind,
		"rename":         a.rename,
		"resources":      a.resources,
		"restore":        a.restore,
		"runtime":        a.runtime,
		"sessions":       a.sessions,
		"session-state":  a.sessionState,
		"session-popup":  a.sessionPopup,
		"settings":       a.settings,
		"setup":          a.setup,
		"shell":          a.shell,
		"status":         a.status,
		"statusbar":      a.statusbar,
		"switch":         a.switcher,
		"tag":            a.tag,
		"tmux":           a.tmux,
		"update":         a.update,
		"upgrade":        a.upgrade,
		"usage":          a.usage,
		"welcome":        a.welcome,
		"window":         a.window,
	}
	handlers := make(map[string]cli.Handler, len(commands))
	for token, command := range commands {
		handlers[token] = command.Run
	}
	return handlers
}

// Run dispatches the configured application commands through the Cobra root.
func (a *App) Run(args []string, stdout, stderr io.Writer) (err error) {
	if a.lifecycle != nil {
		finish := a.lifecycle.BeginCommand()
		defer func() { finish(err) }()
	}
	// Doctor, explicit support-report collection, and every help invocation are
	// strict source-read-only boundaries. In particular, they must not trigger
	// the otherwise automatic legacy-hook filesystem migration before dispatch.
	if shouldRunLegacyHookMigrations(args) {
		runLegacyHookMigrations()
	}
	root, buildErr := cli.NewRoot(cli.RootOptions{
		Stdout:   stdout,
		Stderr:   stderr,
		Version:  version.String(),
		Handlers: a.routeHandlers(),
	})
	if buildErr != nil {
		return buildErr
	}
	return root.Execute(args)
}

func shouldRunLegacyHookMigrations(args []string) bool {
	// Help is a strict read-only boundary: exit 0, no operational error, and
	// no tmux/runtime or filesystem migration access.
	if cli.HelpRequested(args) {
		return false
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	// Doctor is a read-only diagnostic, and `get`/`describe` are read-only
	// resource resolutions. None of them may trigger the otherwise automatic
	// legacy-hook filesystem migration: a read that resolves nothing must leave
	// zero mutations behind, including this one. The two delegating read kinds
	// (`get notifications`, `get snapshots`) therefore skip a pre-dispatch write
	// their current spellings still perform; their stdout, stderr, and exit code
	// are unchanged.
	case "doctor", "get", "describe":
		return false
	}
	return !(len(args) >= 2 && args[0] == "diagnostics" && args[1] == "report")
}
