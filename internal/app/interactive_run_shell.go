package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"unicode/utf8"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The guarded interactive routes. Each id is the argv a generated binding,
// context-menu item, or status-bar binding invokes, and each is a row in
// runShellOutputLedger.
const (
	interactiveRoutePaneMenu              = "internal tmux pane-menu"
	interactiveRouteWindowCreate          = "internal tmux window-create"
	interactiveRouteWindowRename          = "internal tmux window-rename"
	interactiveRoutePopupToggle           = "internal tmux popup-toggle"
	interactiveRouteAgentPaneLaunch       = "internal agent-pane launch"
	interactiveRouteStatusbarClick        = "internal statusbar click"
	interactiveRouteStatusbarUsageRefresh = "internal statusbar usage-refresh"
	interactiveRouteProjectOpen           = "switch open"
)

// interactiveRunShellMessageLimit bounds a converged failure line. A status
// message is a single line over the status bar, not a transcript: an unbounded
// reason would be truncated by tmux at whatever width the client happens to
// have, and the operator would lose the end of the sentence that names the
// refusal.
const interactiveRunShellMessageLimit = 200

// interactiveRunShellRoute is one guarded route.
type interactiveRunShellRoute struct {
	// ID is the ledger key.
	ID string
	// Prefix is the exact leading argv this route owns.
	Prefix []string
	// Label names the action in a converged failure line.
	Label string
}

// interactiveRunShellRoutes is the closed set of argv prefixes the guard owns.
//
// Everything else under `internal` is deliberately absent. `internal supervise`
// execs a managed pane's own process, `internal preview` and `internal status`
// write the payload a popup or the status bar renders, and `internal
// agent-pane picker` is a terminal UI. Buffering any of those would break the
// surface instead of protecting it.
func interactiveRunShellRoutes() []interactiveRunShellRoute {
	return []interactiveRunShellRoute{
		{ID: interactiveRoutePaneMenu, Prefix: []string{"internal", "tmux", "pane-menu"}, Label: "pane menu action"},
		{ID: interactiveRouteWindowCreate, Prefix: []string{"internal", "tmux", "window-create"}, Label: "Create Window"},
		{ID: interactiveRouteWindowRename, Prefix: []string{"internal", "tmux", "window-rename"}, Label: "Rename Window"},
		{ID: interactiveRoutePopupToggle, Prefix: []string{"internal", "tmux", "popup-toggle"}, Label: "popup"},
		{ID: interactiveRouteAgentPaneLaunch, Prefix: []string{"internal", "agent-pane", "launch-default"}, Label: "create Pane"},
		{ID: interactiveRouteAgentPaneLaunch, Prefix: []string{"internal", "agent-pane", "launch-provider"}, Label: "create Pane"},
		{ID: interactiveRouteAgentPaneLaunch, Prefix: []string{"internal", "agent-pane", "launch-shell"}, Label: "create Pane"},
		{ID: interactiveRouteStatusbarClick, Prefix: []string{"internal", "statusbar", "click"}, Label: "status bar action"},
		{ID: interactiveRouteStatusbarUsageRefresh, Prefix: []string{"internal", "statusbar", "usage-refresh"}, Label: "usage refresh"},
		{ID: interactiveRouteProjectOpen, Prefix: []string{"switch", "open"}, Label: "open current Project"},
	}
}

// matchInteractiveRunShellRoute reports whether this invocation is a generated
// interactive `run-shell` producer, and which client owns its feedback.
//
// A known client is half the match, not a detail of it. Converging means moving
// a result from the foreground job onto one exact client, so an invocation with
// no client to name has nothing to converge onto and keeps its writers and its
// exit code: a human running `projmux switch open` at a prompt, and a popup
// payload launched without an origin client, both stay exactly as documented.
// Every generated producer carries a client -- as `--client #{client_tty}` or
// through the binding env prefix -- which is what makes this rule safe.
func matchInteractiveRunShellRoute(args []string, lookupEnv func(string) string) (interactiveRunShellRoute, string, bool) {
	// A generated producer never asks for help. A human at a prompt does, and
	// the help boundary contract says that answer reaches their stdout.
	if argvRequestsHelp(args) {
		return interactiveRunShellRoute{}, "", false
	}
	for _, route := range interactiveRunShellRoutes() {
		if !argvHasPrefix(args, route.Prefix) {
			continue
		}
		client := interactiveRunShellClient(args, lookupEnv)
		if client == "" {
			return interactiveRunShellRoute{}, "", false
		}
		return route, client, true
	}
	return interactiveRunShellRoute{}, "", false
}

func argvRequestsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "help" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "help" || name == "h" {
			return true
		}
	}
	return false
}

func argvHasPrefix(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i, token := range prefix {
		if args[i] != token {
			return false
		}
	}
	return true
}

// interactiveRunShellClient resolves the exact client that must see the result.
// The explicit flag wins: a route that was handed `--client` was handed the
// client tmux resolved at key-press time, which is the only handle that stays
// correct when more than one client is attached to the session.
func interactiveRunShellClient(args []string, lookupEnv func(string) string) string {
	for i, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--client="); ok {
			return strings.TrimSpace(value)
		}
		if arg == "--client" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if arg == "--" {
			break
		}
	}
	if lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(lookupEnv(canonicalCreateTargetClientEnv))
}

// convergeInteractiveRunShellResult is the one place a generated interactive
// producer's result becomes operator-visible.
//
// Success is silent. Anything a route wrote to the foreground job on its way to
// a nil error is a diagnostic, not feedback -- every success path that has
// something to say already says it through its own bounded client message or
// the UI it opened -- and painting it would replace the operator's pane with a
// view-mode screen.
//
// Failure is one bounded line on the exact client, and the process still exits
// zero: tmux appends "'<command>' returned <n>" to a foreground job's output and
// shows that too, so a non-zero exit is itself an overlay.
//
// The single exception is a client that cannot be reached. Then there is no UI
// left to converge onto and the original error is returned rather than
// swallowed, because a silent exit-zero would claim the action succeeded.
func convergeInteractiveRunShellResult(runner tmuxRunner, route interactiveRunShellRoute, client string, err error, detail string) error {
	if err == nil {
		return nil
	}
	message := interactiveRunShellFailureMessage(route, err, detail)
	if displayErr := displayInteractiveRunShellMessage(runner, client, message); displayErr != nil {
		return err
	}
	return nil
}

func interactiveRunShellFailureMessage(route interactiveRunShellRoute, err error, detail string) string {
	reason := strings.TrimSpace(err.Error())
	if detail = strings.TrimSpace(detail); detail != "" && !strings.Contains(reason, detail) {
		if reason == "" {
			reason = detail
		} else {
			reason += ": " + detail
		}
	}
	return boundInteractiveRunShellMessage("projmux " + route.Label + " failed: " + reason)
}

// boundInteractiveRunShellMessage collapses a message to one bounded line.
func boundInteractiveRunShellMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if utf8.RuneCountInString(message) <= interactiveRunShellMessageLimit {
		return message
	}
	return string([]rune(message)[:interactiveRunShellMessageLimit-1]) + "…"
}

// displayInteractiveRunShellMessage sends one bounded message to the exact
// client the guard matched on.
func displayInteractiveRunShellMessage(runner tmuxRunner, client, message string) error {
	if runner == nil {
		runner = inttmux.ExecRunner{}
	}
	_, err := runner.Run(context.Background(), "tmux", "display-message", "-c", strings.TrimSpace(client), "-d", "10000", message)
	return err
}

// runGuardedInteractiveRoute executes one guarded invocation with the
// foreground job's writers replaced by buffers, then converges the result.
func runGuardedInteractiveRoute(runner tmuxRunner, route interactiveRunShellRoute, client string, run func(stdout, stderr io.Writer) error) error {
	var stdout, stderr bytes.Buffer
	err := run(&stdout, &stderr)
	return convergeInteractiveRunShellResult(runner, route, client, err, stderr.String())
}
