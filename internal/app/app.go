package app

import (
	"errors"
	"io"
	"os"
	"slices"
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
	"runtime attach",
	"runtime stop",
	"runtime sessions open/kill",
	"switch create/restore/open/kill",
	"internal tmux apply",
	"internal session-popup open",
	"window recent",
	"runtime prune",
	"internal focus switch-client",
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
	lifecycle   *diagnostics.LifecycleRecorder
	agent       *agentCommand
	ai          *aiCommand
	attention   *attentionCommand
	create      *createCommand
	attach      *attachCommand
	config      *configCommand
	delete      *deleteCommand
	describe    *describeCommand
	doctor      *doctorCommand
	diagnostics *diagnosticsCommand
	focus       *focusCommand
	get         *getCommand
	hook        *hookCommand
	internal    *internalCommand
	rebind      *rebindCommand
	reconcile   *resourceReconcileCommand
	rename      *renameCommand
	restore     *restoreCommand
	runtime     *runtimeCommand
	// runtimeDiagnostics is the Runtime diagnostics escape hatch handler, held
	// beside the namespace so the narrow fixtures that rebuild `runtime` can
	// still reach it.
	runtimeDiagnostics *runtimeDiagnosticsCommand
	keyBroker          *keyBrokerCommand
	kill               *killCommand
	notify             *notifyCommand
	notification       *notificationCommand
	pin                *pinCommand
	popupWaitKey       *popupWaitKeyCommand
	preview            *previewCommand
	prune              *pruneCommand
	quit               *quitCommand
	resources          *resourceCommand
	sessions           *sessionsCommand
	sessionState       *sessionStateCommand
	sessionPopup       *sessionPopupCommand
	settings           *settingsCommand
	setup              *setupCommand
	shell              *shellCommand
	status             *statusCommand
	statusbar          *statusbarCommand
	switcher           *switchCommand
	tag                *tagCommand
	tmux               *tmuxCommand
	update             *updateCommand
	usage              *usagecmd.Command
	welcome            *welcomeCommand
	window             *windowCommand
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
	windowCmd := newWindowCommand(recorder)
	recentWindowCmd := windowCmd.recent
	switcher.sessionStateDiagnostics = sessionStateDiagnostics
	attach := newAttachCommand(recorder)
	kill := newKillCommand(recorder)
	sessions := newSessionsCommand(recorder)
	update := newUpdateCommand()
	quit := newQuitCommand()
	notifyCmd := newNotifyCommand(newDefaultLivePaneLister())
	notifyCmd.diagnostics = notifyFocusDiagnostics
	notificationCmd := newNotificationCommand()
	notificationCmd.notify = notifyCmd
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
	tmuxCmd.ai = ai
	// The public config domain. Each route is a parity alias over the AI or
	// tmux handler that already owns the behavior, so the public spelling is a
	// second door onto one implementation rather than a second implementation.
	configCmd := newConfigCommand()
	configCmd.tmux = tmuxCmd
	configCmd.ai = ai
	attentionCmd := newAttentionCommand()
	attentionCmd.producer = newAttentionNotifyProducer(notifyFocusDiagnostics)
	focusCmd := newFocusCommand(recorder)
	focusCmd.notifyDiagnostics = notifyFocusDiagnostics
	resourcesCmd := newResourceCommand()
	resourcesCmd.diagnostics = resourceOperationalDiagnostics
	// Public resource repair is wired beside the resource command graph rather
	// than lifecycle/AI tmux wiring so those independent seams can rebase cleanly.
	reconcileCmd := newResourceReconcileCommand(tmuxCmd)
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
	// The Agent namespace. `create` and `agent` normalize the Agent spellings
	// the `ai` route mixes together; every subcommand except `agent resume`
	// forwards raw argv to the handler that already owns the behavior, so the
	// canonical spelling is a parity alias rather than a second implementation.
	usageCmd := usagecmd.New(nil)
	createCmd := newCreateCommand()
	createCmd.ai = ai
	createCmd.notify = notifyCmd
	createCmd.snapshots = sessionStateCmd
	// The same object serves both halves of `create agent`: raw argv for the
	// compatibility bridge, and the narrow launch seam for the canonical
	// resource-backed route.
	createCmd.agents = ai
	agentCmd := newAgentCommand()
	agentCmd.ai = ai
	agentCmd.usage = usageCmd
	// `agent resume` materializes its new managed Pane on the create command's
	// runtime -- the same transaction order, ledger, rollback, and detached
	// materializer -- while keeping its own launch seam, which can only build a
	// provider *resume* argv. Sharing the plumbing and not the launch is what
	// keeps the two verbs one implementation of "make a managed pane" and two
	// implementations of "which conversation does it join".
	agentCmd.rebind = newAgentRebinder(createCmd, ai)
	runtimeDiagnosticsCmd := newRuntimeDiagnosticsCommand(tmuxCmd.runner)
	runtimeDiagnosticsCmd.focus = focusCmd
	runtimeDiagnosticsCmd.attach = attach
	runtimeDiagnosticsCmd.inspect = resourcesCmd
	getCmd.runtimeDiag = runtimeDiagnosticsCmd.reader
	runtimeCmd := newRuntimeCommand()
	runtimeCmd.sessions = sessions
	runtimeCmd.diagnostics = runtimeDiagnosticsCmd
	runtimeCmd.attach = attach
	runtimeCmd.kill = kill
	runtimeCmd.tag = tagCmd
	runtimeCmd.prune = pruneCmd
	attach.switcher = switcher
	// The Registry-first primary navigation forwards every action to the route
	// that already owns it: focus moves a client, `attach project` is the one
	// route that materializes an offline Project, `agent resume` is the one that
	// revives an Agent, and the Runtime surface is the escape hatch. Nothing
	// here is a second implementation of a shipped behavior.
	if switcher.navigation != nil {
		switcher.navigation.focus = focusCmd
		switcher.navigation.attach = attach
		switcher.navigation.agent = agentCmd
		switcher.navigation.runtime = runtimeCmd
	}
	sessions.navigation = newRegistryNavigationReader(tmuxCmd.runner)
	sessions.runtime = runtimeCmd
	recentWindowCmd.navigation = newRegistryNavigationReader(tmuxCmd.runner)
	recentWindowCmd.runtime = runtimeCmd
	// The hidden internal plumbing namespace. Every entry is an alias over the
	// handler that already owns the behavior, so the generated tmux config can
	// move onto the canonical spellings without a second implementation.
	keyBrokerCmd := newKeyBrokerCommand()
	popupWaitKeyCmd := newPopupWaitKeyCommand()
	previewCmd := newPreviewCommand()
	sessionPopupCmd := newSessionPopupCommand(recorder)
	statusCmd := newStatusCommand()
	statusbarCmd := newStatusbarCommand()
	internalCmd := newInternalCommand()
	internalCmd.tmux = tmuxCmd
	internalCmd.status = statusCmd
	internalCmd.statusbar = statusbarCmd
	internalCmd.preview = previewCmd
	internalCmd.sessionPopup = sessionPopupCmd
	internalCmd.ai = ai
	internalCmd.focus = focusCmd
	internalCmd.keyBroker = keyBrokerCmd
	internalCmd.popupWaitKey = popupWaitKeyCmd
	diagnosticsCmd := newDiagnosticsCommand()
	diagnosticsCmd.ai = ai
	return &App{
		lifecycle:          recorder,
		agent:              agentCmd,
		ai:                 ai,
		attention:          attentionCmd,
		create:             createCmd,
		attach:             attach,
		config:             configCmd,
		delete:             deleteCmd,
		describe:           newDescribeCommand(),
		doctor:             newDoctorCommand(),
		diagnostics:        diagnosticsCmd,
		focus:              focusCmd,
		get:                getCmd,
		hook:               newHookCommand(),
		internal:           internalCmd,
		rebind:             newRebindCommand(),
		reconcile:          reconcileCmd,
		rename:             newRenameCommand(),
		restore:            restoreCmd,
		runtime:            runtimeCmd,
		runtimeDiagnostics: runtimeDiagnosticsCmd,
		keyBroker:          keyBrokerCmd,
		kill:               kill,
		notify:             notifyCmd,
		notification:       notificationCmd,
		pin:                newPinCommand(),
		popupWaitKey:       popupWaitKeyCmd,
		preview:            previewCmd,
		prune:              pruneCmd,
		quit:               quit,
		resources:          resourcesCmd,
		sessions:           sessions,
		sessionState:       sessionStateCmd,
		sessionPopup:       sessionPopupCmd,
		settings:           settingsCmd,
		setup:              newSetupCommand(initCmd),
		shell:              newShellCommand(update, recorder),
		status:             statusCmd,
		statusbar:          statusbarCmd,
		switcher:           switcher,
		tag:                tagCmd,
		tmux:               tmuxCmd,
		update:             update,
		usage:              usageCmd,
		welcome:            newWelcomeCommand(update),
		window:             windowCmd,
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
	internal := a.internal
	if internal == nil {
		// Small focused tests construct partial App values. Keep the canonical
		// internal namespace usable in those fixtures without restoring any
		// retired top-level handler.
		internal = &internalCommand{
			tmux: a.tmux, status: a.status, statusbar: a.statusbar,
			preview: a.preview, sessionPopup: a.sessionPopup, ai: a.ai,
			focus: a.focus, keyBroker: a.keyBroker, popupWaitKey: a.popupWaitKey,
		}
	}
	runtime := a.runtime
	if runtime == nil {
		// Focused handler tests predate the runtime namespace and wire only the
		// leaf under test. Preserve those narrow fixtures through the canonical
		// namespace without reviving a retired top-level spelling.
		runtime = &runtimeCommand{
			sessions: a.sessions, attach: a.attach, kill: a.kill,
			tag: a.tag, prune: a.prune,
			diagnostics: a.runtimeDiagnostics,
		}
	}
	commands := map[string]rawArgvCommand{
		"agent":     a.agent,
		"create":    a.create,
		"attention": a.attention,
		"attach": legacyRouteGate{
			name: "attach", target: a.attach, allowedFirst: []string{"project"},
			replacement: func([]string) string { return "`projmux runtime attach ...`" },
		},
		"config":      a.config,
		"delete":      a.delete,
		"describe":    a.describe,
		"doctor":      a.doctor,
		"diagnostics": a.diagnostics,
		"focus": legacyRouteGate{
			name: "focus", target: a.focus, allowedFirst: focusKinds,
			replacement: func([]string) string { return "`projmux focus project|window|pane ...`" },
		},
		"get":  a.get,
		"hook": a.hook,
		// The hidden internal plumbing namespace. It aliases the machine-invoked
		// routes below so generated tmux config, tmux hooks, and popup payloads
		// can emit one namespace instead of eight top-level tokens.
		"internal":     internal,
		"notification": a.notification,
		"pin": legacyRouteGate{
			name: "pin", target: a.pin, allowedFirst: []string{"project"},
			replacement: func([]string) string { return "`projmux pin project ...`" },
		},
		"prune": legacyRouteGate{
			name: "prune", target: a.prune, allowedFirst: []string{"project", "snapshot"},
			replacement: pruneReplacement,
		},
		"quit":      a.quit,
		"rebind":    a.rebind,
		"reconcile": a.reconcile,
		"rename":    a.rename,
		"resources": a.resources,
		"restore":   a.restore,
		"runtime":   runtime,
		"settings":  a.settings,
		"setup":     a.setup,
		"shell":     a.shell,
		"switch":    a.switcher,
		"update":    a.update,
		"welcome":   a.welcome,
		"window":    a.window,
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
	args = normalizeLegacyUpdaterHandoff(args)
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

// normalizeLegacyUpdaterHandoff preserves the post-replacement command emitted
// by the immutable v0.10.1 GitHub Release updater. That updater installs the new
// binary and then invokes it with the exact argv `tmux apply`; routing those two
// tokens through the current public convergence path lets the replacement
// migrate managed producers before it reloads the live server.
//
// This is intentionally a pre-Cobra argv seam rather than a route or handler:
// `tmux` stays absent from the catalog, help, generated reference, and top-level
// handler map. Any other old tmux argv (including extra flags or positional
// arguments) therefore retains the retired root unknown-command contract.
func normalizeLegacyUpdaterHandoff(args []string) []string {
	if slices.Equal(args, []string{"tmux", "apply"}) {
		return []string{"config", "apply"}
	}
	return args
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
	// Retired public routes and removed roots are side-effect-free. The old AI
	// root is gone entirely; stale unmanaged producer argv must fail before any
	// automatic migration or other mutation.
	switch args[0] {
	case "ai":
		return false
	case "current", "kill", "notify", "sessions", "session-state", "tag", "upgrade", "usage",
		"key-broker", "popup-wait-key", "preview", "session-popup", "status", "statusbar", "tmux":
		return false
	case "attach":
		if len(args) < 2 || args[1] != "project" {
			return false
		}
	case "focus":
		if len(args) < 2 || !slices.Contains(focusKinds, args[1]) {
			return false
		}
	case "pin":
		if len(args) < 2 || args[1] != "project" {
			return false
		}
	case "prune":
		if len(args) < 2 || (args[1] != "project" && args[1] != "snapshot") {
			return false
		}
	}
	switch args[0] {
	// Doctor is a read-only diagnostic, and `get`/`describe` are read-only
	// resource resolutions. None of them may trigger the otherwise automatic
	// legacy-hook filesystem migration: a read that resolves nothing must leave
	// zero mutations behind, including this one. The two delegating read kinds
	// (`get notifications`, `get snapshots`) therefore skip a pre-dispatch write
	// their current spellings still perform; their stdout, stderr, and exit code
	// are unchanged.
	case "doctor", "get", "describe", "reconcile":
		return false
	}
	if len(args) >= 2 && args[0] == "diagnostics" && (args[1] == "report" || args[1] == "agent-hook") {
		return false
	}
	return true
}
