package app

// Interactive tmux `run-shell` output ledger.
//
// tmux draws a foreground `run-shell` job's stdout, its stderr, and its
// "'<command>' returned <n>" line in a temporary view-mode screen over the pane
// the key was pressed in. For a generated projmux binding that means every byte
// a route writes -- a CLI projection, a stray diagnostic, a usage error -- hides
// the Codex/shell process the operator is looking at behind `[0/1452]` until
// they close the overlay. The pane is intact underneath, but the action reads as
// if it replaced it.
//
// The fix is not "redirect the three producers we caught". It is a closed set:
// every generated `run-shell` producer declares where its result is allowed to
// surface, and nothing reaches tmux's view-mode by default. This file owns that
// declaration; run_shell_output_ledger_test.go proves the set is closed against
// the generated config and against the package source, so a new producer cannot
// be added without classifying it.
//
// The public CLI is deliberately untouched. `projmux create`/`rename` keep their
// stdout projection for scripting; the tmux adapter consumes it instead of
// letting tmux paint it.

// runShellOutputChannel is where one producer's result is allowed to surface.
// The five values are the whole classification: a producer is exactly one of
// them, and forbiddenOverlay exists only so the enforcement test has a name for
// what it refuses.
type runShellOutputChannel string

const (
	// runShellChannelIntentionalUI is a producer whose result is the UI it
	// explicitly opens: a popup or a menu. The origin pane's mode is unchanged.
	runShellChannelIntentionalUI runShellOutputChannel = "intentional-ui"
	// runShellChannelSilent is a producer that writes zero bytes to the
	// foreground job on success and reports failure through its own client
	// message.
	runShellChannelSilent runShellOutputChannel = "stdout-stderr-zero"
	// runShellChannelRedirect is a producer whose output is discarded by an
	// explicit shell redirect. Hooks live here: they are machine convergence,
	// not operator feedback.
	runShellChannelRedirect runShellOutputChannel = "redirect"
	// runShellChannelExactClientMessage is a producer that reports its result as
	// a bounded `display-message` on the exact client that ran the action.
	runShellChannelExactClientMessage runShellOutputChannel = "exact-client-message"
	// runShellChannelForbiddenOverlay is the classification no row may hold: a
	// producer whose stdout, stderr, or exit status reaches tmux's view-mode.
	runShellChannelForbiddenOverlay runShellOutputChannel = "forbidden-overlay"
)

// runShellSurface is the generated construct a producer is emitted into. It is
// half of a ledger row's identity: `internal tmux popup-toggle` is one command
// but two producers, one bound to a key and one to a context-menu item, and each
// has to be audited where it lives.
type runShellSurface string

const (
	runShellSurfaceKeybinding runShellSurface = "keybinding"
	runShellSurfacePaneMenu   runShellSurface = "pane-menu"
	runShellSurfaceStatusbar  runShellSurface = "statusbar"
	runShellSurfaceHook       runShellSurface = "hook"
	runShellSurfaceStartup    runShellSurface = "startup"
	// runShellSurfaceRuntime is a `run-shell` projmux hands to tmux at runtime
	// rather than writing into generated config. It never appears in the config
	// sweep and is held to the source sweep instead.
	runShellSurfaceRuntime runShellSurface = "runtime-command"
)

// runShellProducer is one classified `run-shell` producer.
type runShellProducer struct {
	// ID is the stable ledger key used by tests and Archive evidence.
	ID string
	// Surface and Match together attribute one generated occurrence to this row.
	// Match is a substring of the shell command tmux runs.
	Surface runShellSurface
	Match   string
	// Channel is the one classification this producer holds.
	Channel runShellOutputChannel
	// Background is true when the producer is emitted as `run-shell -b`. tmux
	// never paints a background job's output, so background plus an explicit
	// redirect is the hook contract.
	Background bool
	// Redirected and ExitGuarded record the `>/dev/null 2>&1` and `|| true`
	// halves of that contract.
	Redirected  bool
	ExitGuarded bool
	// Route is the guarded argv prefix this producer invokes, empty when the
	// producer is not an interactive projmux route (hooks, control sentinels).
	// A foreground producer must name one: the guard is what makes its silence
	// structural instead of a property of whatever the route happens to write.
	Route string
	// RuntimeInstalled marks a producer projmux writes onto a live tmux server
	// instead of into generated config -- a per-pane provider hook, a detached
	// continuation. It never appears in a config sweep and is held to the source
	// sweep instead.
	RuntimeInstalled bool
	// ControlSentinel marks the allowlisted non-UI rows the C-1 contract puts
	// out of scope: their output is a control signal read by projmux, not
	// feedback shown to an operator. The allowlist is closed by test.
	ControlSentinel bool
	// Note records why this row is classified the way it is.
	Note string
}

// runShellOutputLedger is the closed set of `run-shell` producers.
func runShellOutputLedger() []runShellProducer {
	return []runShellProducer{
		// --- generated keybindings -------------------------------------------
		{
			ID: "catalog.popup-toggle", Surface: runShellSurfaceKeybinding,
			Match: "internal tmux popup-toggle", Channel: runShellChannelIntentionalUI,
			Route: interactiveRoutePopupToggle,
			Note:  "the popup is the declared UI; the launcher itself stays silent and routes failures to the exact client",
		},
		{
			ID: "catalog.window-create", Surface: runShellSurfaceKeybinding,
			Match: "internal tmux window-create", Channel: runShellChannelExactClientMessage,
			Route: interactiveRouteWindowCreate,
			Note:  "canonical create output is consumed in-process; the client sees the bounded create line",
		},
		{
			ID: "catalog.window-rename", Surface: runShellSurfaceKeybinding,
			Match: "internal tmux window-rename", Channel: runShellChannelExactClientMessage,
			Route: interactiveRouteWindowRename,
			Note:  "runs inside command-prompt; the rename projection is consumed in-process",
		},
		{
			ID: "catalog.agent-pane-launch", Surface: runShellSurfaceKeybinding,
			Match: "internal agent-pane launch-", Channel: runShellChannelSilent,
			Route: interactiveRouteAgentPaneLaunch,
			Note:  "direct split hotkeys already discard the create projection; the new Pane is the feedback",
		},
		{
			ID: "catalog.project-open", Surface: runShellSurfaceKeybinding,
			Match: "switch open ", Channel: runShellChannelSilent,
			Route: interactiveRouteProjectOpen,
			Note:  "the public route is unchanged; the guard applies only under the binding's client env",
		},

		// --- pane context menu -----------------------------------------------
		{
			ID: "pane-menu.resume-popup", Surface: runShellSurfacePaneMenu,
			Match: "internal tmux popup-toggle", Channel: runShellChannelIntentionalUI,
			Route: interactiveRoutePopupToggle,
			Note:  "the AI Resume Picker item opens the same declared popup as the key binding",
		},
		{
			ID: "pane-menu.action", Surface: runShellSurfacePaneMenu,
			Match: "internal tmux pane-menu", Channel: runShellChannelExactClientMessage,
			Route: interactiveRoutePaneMenu,
			Note:  "split and kill consume the canonical projection and report one bounded line to the clicking client",
		},

		// --- status bar -------------------------------------------------------
		{
			ID: "statusbar.click", Surface: runShellSurfaceStatusbar,
			Match: "internal statusbar click", Channel: runShellChannelExactClientMessage,
			Route: interactiveRouteStatusbarClick,
			Note:  "each range opens its own popup or toast; diagnostics never reach the foreground job",
		},
		{
			ID: "statusbar.usage-refresh", Surface: runShellSurfaceStatusbar,
			Match: "internal statusbar usage-refresh", Channel: runShellChannelExactClientMessage,
			Route: interactiveRouteStatusbarUsageRefresh,
			Note:  "refresh reopens the usage HUD popup; adapter failures degrade to the cached HUD",
		},

		// --- generated hooks --------------------------------------------------
		{
			ID: "hook.attention-arm", Surface: runShellSurfaceHook,
			Match: "attention arm #{hook_pane}", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "pane-focus-out attention state; machine convergence, never operator feedback",
		},
		{
			ID: "hook.attention-clear-focus", Surface: runShellSurfaceHook,
			Match: "attention clear #{hook_pane}", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "pane-focus-in attention state",
		},
		{
			ID: "hook.attention-clear-select", Surface: runShellSurfaceHook,
			Match: "attention clear #{pane_id}", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "after-select-pane attention state",
		},
		{
			ID: "hook.pane-exit-converge", Surface: runShellSurfaceHook,
			Match: "internal tmux rebalance-panes", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "pane-exited/pane-died/after-kill-pane rebalance plus lifecycle convergence",
		},
		{
			ID: "hook.window-unlinked-converge", Surface: runShellSurfaceHook,
			Match: "--reason window-unlinked", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "window-unlinked lifecycle convergence",
		},
		{
			ID: "hook.runtime-created-converge", Surface: runShellSurfaceHook,
			Match: "--reason runtime-created", Channel: runShellChannelRedirect,
			Redirected: true, ExitGuarded: true,
			Note: "after-new-window/after-split-window convergence is synchronous on purpose so the next read does not race the binding write; it stays redirected and exit-guarded so a refusal cannot paint the new pane",
		},
		{
			ID: "hook.recent-window-record", Surface: runShellSurfaceHook,
			Match: "window record", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "after-select-window/client-session-changed recent-window journal",
		},
		{
			ID: "hook.welcome-popup", Surface: runShellSurfaceHook,
			Match: "welcome --popup", Channel: runShellChannelRedirect,
			Background: true, Redirected: true,
			Note: "client-attached welcome popup; the popup is its own UI and the launcher is redirected",
		},
		{
			ID: "hook.agent-bell-ingest", Surface: runShellSurfaceHook,
			Match: "internal agent-hook ingest bell", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true, RuntimeInstalled: true,
			Note: "per-pane alert-bell provider ingest installed by the AI integration",
		},

		// --- startup ----------------------------------------------------------
		{
			ID: "startup.recent-window-record", Surface: runShellSurfaceStartup,
			Match: "window record", Channel: runShellChannelRedirect,
			Background: true, Redirected: true, ExitGuarded: true,
			Note: "one record at source time so the journal has the attached window before any hook fires",
		},

		// --- runtime commands -------------------------------------------------
		{
			ID: "runtime.sidebar-open-continuation", Surface: runShellSurfaceRuntime,
			Match: "switch sidebar-open", Channel: runShellChannelRedirect,
			Background: true, ExitGuarded: true,
			Note: "detached continuation; sidebar-open reopens the picker with its own actionable error, so the job is kept successful with `|| :`",
		},
		{
			ID: "runtime.sidebar-trust-reopen", Surface: runShellSurfaceRuntime,
			Match: "popup-toggle", Channel: runShellChannelIntentionalUI,
			Background: true,
			Note:       "reopens the sidebar popup after the trust gate; background, so tmux paints nothing",
		},
		{
			ID: "runtime.sidebar-commit-record", Surface: runShellSurfaceRuntime,
			Match: "window record", Channel: runShellChannelRedirect,
			Background: true,
			Note:       "records the committed sidebar selection, detached like the record hooks",
		},
		{
			ID: "runtime.mutation-queue-marker", Surface: runShellSurfaceRuntime,
			Match: "set-environment", Channel: runShellChannelRedirect,
			Background: true,
			Note:       "queued runtime mutation continuation; the marker is the receipt, the job prints nothing",
		},
		{
			ID: "runtime.quit-refusal-sentinel", Surface: runShellSurfaceRuntime,
			Match: "exit 73", Channel: runShellChannelSilent,
			ControlSentinel: true,
			Note:            "the if-shell else branch of `projmux quit`: a zero-byte non-zero exit read by projmux as \"this server is not the app runtime\". Not operator feedback, and the allowlist is closed",
		},
	}
}

// runShellSourceSite is one place in the package that spells `run-shell`.
type runShellSourceSite struct {
	File    string
	Snippet string
}

// runShellSourceSites is the closed set of source lines allowed to emit a
// `run-shell`. A new producer fails the source sweep until it is registered
// here and classified in runShellOutputLedger, which is the whole point: the
// overlay this track removed was not a typo, it was an unclassified producer.
func runShellSourceSites() []runShellSourceSite {
	return []runShellSourceSite{
		{File: "keybinding_catalog.go", Snippet: "internal tmux popup-toggle"},
		{File: "keybinding_catalog.go", Snippet: "tmuxPaneEnvPrefix+bin"},
		{File: "ai_integrate.go", Snippet: "agent-hook ingest bell"},
		{File: "ai_integrate.go", Snippet: "legacyAIIngestCommand"},
		{File: "runtime_mutation_plan.go", Snippet: `"run-shell", "-b", command`},
		{File: "tmux.go", Snippet: "attention arm #{hook_pane}"},
		{File: "tmux.go", Snippet: "attention clear #{hook_pane}"},
		{File: "tmux.go", Snippet: "attention clear #{pane_id}"},
		{File: "tmux.go", Snippet: `return "run-shell -b " + tmuxConfigQuote(`},
		{File: "tmux.go", Snippet: `return "run-shell " + tmuxConfigQuote(`},
		{File: "tmux.go", Snippet: `body := "run-shell -b " + tmuxConfigQuote(command)`},
		{File: "tmux.go", Snippet: `"run-shell -b " + tmuxConfigQuote(command),`},
		{File: "tmux.go", Snippet: "welcome --popup"},
		{File: "tmux.go", Snippet: "{ run-shell "},
		{File: "tmux.go", Snippet: `"bind-key -T projmux-status " + key + " run-shell "`},
		{File: "tmux.go", Snippet: `"run-shell " + tmuxConfigQuote(bin+" internal tmux pane-menu`},
		{File: "switch.go", Snippet: `"tmux", "run-shell", "-b", command`},
		{File: "quit.go", Snippet: "exit 73"},
	}
}
