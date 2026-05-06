// Package app: statusbar dispatches click and keyboard activations from the
// projmux tmux status bar to per-segment handlers. The status bar wraps each
// segment in a tmux user-defined range (e.g. `#[range=user|notify]...`); the
// mouse binding `bind -n MouseDown1Status run-shell '<bin> statusbar click
// "#{mouse_status_range}" --mouse-window "#{mouse_window}"'` invokes us with
// the matching range id and the window id under the cursor, and the
// `prefix s {u,n,g,k,p,s}` shortcuts call us with hard-coded ids.
//
// When the click lands outside any user-defined range (e.g. on a window-list
// entry) we fall back to `select-window -t @<mouse_window>` so we restore
// tmux's default click-to-switch-window UX after taking over the bind.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

// statusbarRangeID identifies one clickable segment in the projmux status bar.
type statusbarRangeID string

const (
	statusbarRangeSession statusbarRangeID = "session"
	statusbarRangePwd     statusbarRangeID = "pwd"
	statusbarRangeKube    statusbarRangeID = "kube"
	statusbarRangeGit     statusbarRangeID = "git"
	statusbarRangeUsage   statusbarRangeID = "usage"
	statusbarRangeNotify  statusbarRangeID = "notify"
)

// statusbarRunner abstracts the tmux/projmux invocations the click handlers
// emit. Tests substitute a recording fake so they can assert on every shelled
// command without spawning real subprocesses.
type statusbarRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// statusbarCommand is the dispatch entry point invoked by tmux for both mouse
// clicks and keyboard shortcuts. The intentionally tiny surface keeps the
// dispatcher cheap to call from a tmux status interval.
type statusbarCommand struct {
	runner        statusbarRunner
	executable    func() (string, error)
	notifyStoreFn func() (notifyStore, error)
}

// newStatusbarCommand builds the production wiring: real tmux + projmux exec
// runner, real binary resolution, and the on-disk notify queue.
func newStatusbarCommand() *statusbarCommand {
	return &statusbarCommand{
		runner:        statusbarExecRunner{},
		executable:    os.Executable,
		notifyStoreFn: defaultStatusbarNotifyStore,
	}
}

func defaultStatusbarNotifyStore() (notifyStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve default config paths: %w", err)
	}
	return notify.NewDefaultStore(paths), nil
}

// Run is the CLI entry: `projmux statusbar <subcommand> ...`.
func (c *statusbarCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printStatusbarUsage(stderr)
		return usageError("statusbar requires a subcommand")
	}
	switch args[0] {
	case "click":
		return c.runClick(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printStatusbarUsage(stdout)
		return nil
	default:
		printStatusbarUsage(stderr)
		return usageError(fmt.Sprintf("unknown statusbar subcommand: %s", args[0]))
	}
}

// statusbarClickOptions captures the parsed argv. mouseX/mouseY are reserved
// for future telemetry — see runClick.
type statusbarClickOptions struct {
	RangeID     statusbarRangeID
	Socket      string
	MouseWindow string
	MouseX      int
	MouseY      int
}

// parseStatusbarClickArgs parses the argv handed to `projmux statusbar click`
// in an order-flexible way. The standard `flag` package stops at the first
// non-flag token, which means that when tmux emits args in the natural
// "<range-id> --mouse-window <id>" order the trailing flag pair is misread as
// extra positionals and the whole click is rejected with exit 2 — re-introducing
// the very tmux error popup we are trying to avoid. We therefore walk argv
// ourselves: any token starting with `-` (or `--`) is treated as a flag plus
// optional value, anything else is a positional. Up to one positional is
// allowed; everything beyond that is a UsageError.
//
// Recognized flags: --socket, --mouse-window, --mouse-x, --mouse-y. Both
// space-separated (`--flag value`) and equals (`--flag=value`) forms are
// accepted. Unknown flags are reported as UsageError so typos don't silently
// swallow values.
func parseStatusbarClickArgs(args []string) (string, statusbarClickOptions, error) {
	var (
		opts        statusbarClickOptions
		positionals []string
		mouseX      = -1
		mouseY      = -1
	)
	opts.MouseX = mouseX
	opts.MouseY = mouseY

	consumeValue := func(name string, i int) (string, int, error) {
		if i+1 >= len(args) {
			return "", i, usageError(fmt.Sprintf("flag --%s requires a value", name))
		}
		return args[i+1], i + 1, nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		// Strip leading dashes and split on `=` for the equals form.
		name := strings.TrimLeft(a, "-")
		value := ""
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasValue = true
		}
		switch name {
		case "socket":
			if !hasValue {
				v, ni, err := consumeValue(name, i)
				if err != nil {
					return "", opts, err
				}
				value = v
				i = ni
			}
			opts.Socket = strings.TrimSpace(value)
		case "mouse-window":
			if !hasValue {
				v, ni, err := consumeValue(name, i)
				if err != nil {
					return "", opts, err
				}
				value = v
				i = ni
			}
			opts.MouseWindow = strings.TrimSpace(value)
		case "mouse-x":
			if !hasValue {
				v, ni, err := consumeValue(name, i)
				if err != nil {
					return "", opts, err
				}
				value = v
				i = ni
			}
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return "", opts, usageError(fmt.Sprintf("flag --mouse-x: %v", err))
			}
			opts.MouseX = n
		case "mouse-y":
			if !hasValue {
				v, ni, err := consumeValue(name, i)
				if err != nil {
					return "", opts, err
				}
				value = v
				i = ni
			}
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return "", opts, usageError(fmt.Sprintf("flag --mouse-y: %v", err))
			}
			opts.MouseY = n
		case "help", "h":
			return "", opts, usageError("statusbar click: help requested")
		default:
			return "", opts, usageError(fmt.Sprintf("unknown flag --%s", name))
		}
	}

	if len(positionals) > 1 {
		return "", opts, usageError("statusbar click accepts at most 1 <range-id> argument")
	}
	raw := ""
	if len(positionals) == 1 {
		raw = strings.TrimSpace(positionals[0])
	}
	opts.RangeID = statusbarRangeID(raw)
	return raw, opts, nil
}

func (c *statusbarCommand) runClick(args []string, stdout, stderr io.Writer) error {
	raw, opts, err := parseStatusbarClickArgs(args)
	if err != nil {
		printStatusbarUsage(stderr)
		return err
	}
	// MouseX/MouseY are intentionally unused today. The fields are wired
	// through so we can plumb click telemetry without changing the bind.
	_ = opts.MouseX
	_ = opts.MouseY

	if raw == "" {
		// tmux emits an empty `#{mouse_status_range}` when the click lands
		// outside any user-defined range — typically on a window-list entry
		// or status-bar whitespace. If `--mouse-window` is non-empty we
		// fall back to tmux's default `select-window` behavior so users can
		// still click a tab to switch to it; otherwise the click is a noop.
		if opts.MouseWindow != "" {
			return c.handleWindowListClick(opts, stderr)
		}
		return nil
	}

	handler, ok := c.dispatchTable()[opts.RangeID]
	if !ok {
		// Unknown range id (something other than a known projmux range and
		// not an empty range): tmux's default behavior would not have
		// invoked us at all, so we treat it as a noop. If a window id is
		// available we still perform the window-list passthrough so users
		// can click on, e.g., a custom right-side range without losing the
		// window-switch affordance.
		if opts.MouseWindow != "" {
			return c.handleWindowListClick(opts, stderr)
		}
		return nil
	}
	return handler(opts, stdout, stderr)
}

// handleWindowListClick restores tmux's default window-list click behavior
// (`select-window -t =`) when the click lands on a window entry rather than a
// projmux user-defined range. The id arrives as a numeric string from
// `#{mouse_window}` (e.g. "3"); tmux requires the `@` prefix to interpret it
// as a window id rather than a name. We strip any leading `@` first so we
// never end up with `@@3`.
func (c *statusbarCommand) handleWindowListClick(opts statusbarClickOptions, stderr io.Writer) error {
	id := strings.TrimPrefix(strings.TrimSpace(opts.MouseWindow), "@")
	if id == "" {
		return nil
	}
	return c.runTmux(stderr, "select-window", "-t", "@"+id)
}

// dispatchTable maps each known range id to its click handler. A method on the
// receiver returns it so handlers can capture `c` without leaking package
// state across goroutines.
func (c *statusbarCommand) dispatchTable() map[statusbarRangeID]func(statusbarClickOptions, io.Writer, io.Writer) error {
	return map[statusbarRangeID]func(statusbarClickOptions, io.Writer, io.Writer) error{
		statusbarRangeSession: c.handleSession,
		statusbarRangePwd:     c.handlePwd,
		statusbarRangeKube:    c.handleKube,
		statusbarRangeGit:     c.handleGit,
		statusbarRangeUsage:   c.handleUsage,
		statusbarRangeNotify:  c.handleNotify,
	}
}

// handleSession surfaces the active session name for now. TODO: open a session
// picker (see `projmux switch --ui=popup`) so the click maps to a meaningful
// navigation action. For now we keep parity with the other "todo" segments.
func (c *statusbarCommand) handleSession(_ statusbarClickOptions, _, stderr io.Writer) error {
	// TODO: replace with a session picker popup once the picker UI hardens.
	return c.runTmux(stderr, "display-message", "session: #{session_name}")
}

// handlePwd shows the current pane's path. This mirrors what tmux already
// stores in #{pane_current_path} but surfaces it in a place users can copy.
func (c *statusbarCommand) handlePwd(_ statusbarClickOptions, _, stderr io.Writer) error {
	return c.runTmux(stderr, "display-message", "#{pane_current_path}")
}

// handleKube is a placeholder for the "switch to a session whose kube context
// matches a filter" action. TODO: wire to `projmux switch --filter=kube`.
func (c *statusbarCommand) handleKube(_ statusbarClickOptions, _, stderr io.Writer) error {
	// TODO: wire to `projmux switch --filter=kube` once the switcher exposes
	// filter flags.
	return c.runTmux(stderr, "display-message", "kube clicker - TODO: wire to switch --filter=kube")
}

// handleGit is a placeholder for the "switch to a dirty git session" action.
// TODO: wire to `projmux switch --filter=git-dirty`.
func (c *statusbarCommand) handleGit(_ statusbarClickOptions, _, stderr io.Writer) error {
	// TODO: wire to `projmux switch --filter=git-dirty` once the switcher
	// exposes filter flags.
	return c.runTmux(stderr, "display-message", "git clicker - TODO: wire to switch --filter=git-dirty")
}

// handleUsage opens the full `projmux usage` table inside a popup so users can
// see per-window detail without leaving tmux. Falls back to display-message if
// the popup itself fails (defensive — tmux 3.4+ supports popups everywhere).
func (c *statusbarCommand) handleUsage(_ statusbarClickOptions, _, stderr io.Writer) error {
	binaryPath, err := c.resolveBinary()
	if err != nil {
		return c.runTmux(stderr, "display-message", "statusbar usage: cannot resolve projmux binary")
	}
	popupShell := tmuxShellQuote(binaryPath) + " usage; read -n1 -s"
	if err := c.runTmuxNoFallback(stderr, "display-popup", "-E", "-h", "60%", "-w", "80%", popupShell); err == nil {
		return nil
	}
	// Fallback path: inline the rendered table into a one-shot message.
	out, runErr := c.runner.Run(context.Background(), binaryPath, "usage")
	if runErr != nil {
		return c.runTmux(stderr, "display-message", "statusbar usage: invocation failed")
	}
	return c.runTmux(stderr, "display-message", strings.TrimSpace(string(out)))
}

// handleNotify focuses the origin pane of the newest queued notification. If
// the queue is empty we surface that fact in tmux rather than silently doing
// nothing — that gives the keyboard shortcut a useful interactive ack.
//
// This handler MUST NOT return an error to its caller (tmux's run-shell). A
// non-zero exit from run-shell triggers a "...returned N" error popup, which
// is hostile UX for a status-bar click. Every failure mode here resolves to
// a tmux display-message toast and a nil return; the only place we still
// surface errors is the binary-path resolution check, because if we cannot
// even find the projmux executable there is nothing useful to display and
// the failure is a packaging bug, not a runtime UX glitch.
func (c *statusbarCommand) handleNotify(opts statusbarClickOptions, _, stderr io.Writer) error {
	if c.notifyStoreFn == nil {
		return c.runTmux(stderr, "display-message", "no notifications")
	}
	store, err := c.notifyStoreFn()
	if err != nil {
		return c.runTmux(stderr, "display-message", "no notifications")
	}
	entries, err := store.List()
	if err != nil || len(entries) == 0 {
		return c.runTmux(stderr, "display-message", "no notifications")
	}

	head := entries[0]
	target := notify.FormatTarget(notify.Target{
		Session: head.Session,
		Window:  head.Window,
		Pane:    head.Pane,
	})
	if strings.TrimSpace(target) == "" {
		return c.runTmux(stderr, "display-message", "notification has no routable target")
	}

	binaryPath, err := c.resolveBinary()
	if err != nil {
		return fmt.Errorf("statusbar notify: resolve projmux binary: %w", err)
	}

	socket := opts.Socket
	if socket == "" {
		socket = strings.TrimSpace(head.Socket)
	}

	args := []string{"focus", "--target", target, "--source", "status-bar", "--kind", "segment-click"}
	if socket != "" {
		args = append(args, "--socket", socket)
	}
	if _, runErr := c.runner.Run(context.Background(), binaryPath, args...); runErr != nil {
		// The focus subprocess exits with a deterministic code 2 when the
		// target session/window/pane cannot be resolved (see focus.go's
		// focusExitNotResolved). Treat that as "the queue entry is junk":
		// ack it so the next click moves on, and tell the user via a
		// non-error toast. Any other exit code is treated as transient
		// (network hiccup, tmux server churn, etc.) — keep the entry so
		// the user can retry, and surface the reason as a toast instead
		// of a tmux error popup.
		if isFocusTargetUnresolved(runErr) {
			if ackErr := store.Ack(head.ID); ackErr != nil {
				fmt.Fprintf(stderr, "statusbar notify: ack %s: %v\n", head.ID, ackErr)
			}
			return c.runTmux(stderr, "display-message", "notify target gone, dropping entry")
		}
		return c.runTmux(stderr, "display-message", fmt.Sprintf("focus failed: %s", focusFailureSummary(runErr)))
	}
	// Click-to-focus has no separate producer to ack the entry, so the
	// click itself is the consume signal: a successful focus dispatch is
	// the user telling us "I have handled this notification". We swallow
	// the ack error because the focus already succeeded — the user has
	// been navigated to the pane, and a stale queue entry will be cleaned
	// up by the next reconcile pass anyway.
	if ackErr := store.Ack(head.ID); ackErr != nil {
		fmt.Fprintf(stderr, "statusbar notify: ack %s: %v\n", head.ID, ackErr)
	}
	return nil
}

// isFocusTargetUnresolved reports whether the focus subprocess error
// represents the deterministic "target could not be resolved" exit
// (focus.go: focusExitNotResolved == 2). We accept three signals:
//
//   - The wrapped error is an *exec.ExitError with ExitCode() == 2 (the
//     normal in-process subprocess case).
//   - The wrapped error is any other type that exposes ExitCode() == 2 —
//     this covers test fakes and the in-process focusExitError shape.
//   - The wrapped error is an app.UsageError. UsageErrors also exit the
//     subprocess with code 2, and treating them as "target unresolved"
//     keeps the click loop from getting stuck on a malformed queue entry.
func isFocusTargetUnresolved(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) && coded.ExitCode() == focusExitNotResolved {
		return true
	}
	if IsUsageError(err) {
		return true
	}
	return false
}

// focusFailureSummary renders a short human-readable reason for a transient
// focus failure. We strip the wrapper prefixes the runner adds so the toast
// stays under tmux's display-message length budget.
func focusFailureSummary(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	if idx := strings.LastIndex(msg, ": "); idx >= 0 && idx < len(msg)-2 {
		msg = msg[idx+2:]
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "unknown"
	}
	return msg
}

func (c *statusbarCommand) resolveBinary() (string, error) {
	if c.executable == nil {
		return "", errors.New("statusbar binary resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve statusbar projmux binary: %w", err)
	}
	if strings.TrimSpace(binaryPath) == "" {
		return "", errors.New("statusbar projmux binary path is empty")
	}
	return binaryPath, nil
}

// runTmux invokes tmux with the supplied args and swallows any error after
// reporting it on stderr. We do not fail the whole click handler if tmux
// declines (e.g. no client attached) — the click is best-effort UX.
func (c *statusbarCommand) runTmux(stderr io.Writer, args ...string) error {
	if err := c.runTmuxNoFallback(stderr, args...); err != nil {
		fmt.Fprintf(stderr, "statusbar: tmux %s: %v\n", strings.Join(args, " "), err)
	}
	return nil
}

func (c *statusbarCommand) runTmuxNoFallback(_ io.Writer, args ...string) error {
	if c.runner == nil {
		return errors.New("statusbar runner is not configured")
	}
	_, err := c.runner.Run(context.Background(), "tmux", args...)
	return err
}

func printStatusbarUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux statusbar click <range-id> [--socket <s>] [--mouse-window <id>] [--mouse-x N] [--mouse-y N]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Range ids: session pwd kube git usage notify")
}

type statusbarExecRunner struct{}

func (statusbarExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}
