// Package app: statusbar dispatches click and keyboard activations from the
// projmux tmux status bar to per-segment handlers. The status bar wraps each
// segment in a tmux user-defined range (e.g. `#[range=user|notify]...`); the
// mouse binding `bind -n MouseDown1Status run-shell '<bin> statusbar click
// "#{mouse_status_range}" --client "#{client_tty}" --mouse-window
// "#{mouse_window}"'` invokes us with the matching range id, client tty, and
// the window id under the cursor, and the
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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	coreusage "github.com/crevissepartners/projmux/internal/core/usage"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// statusbarRangeID identifies one clickable segment in the projmux status bar.
type statusbarRangeID string

const (
	statusbarRangeSession  statusbarRangeID = "session"
	statusbarRangePwd      statusbarRangeID = "pwd"
	statusbarRangeKube     statusbarRangeID = "kube"
	statusbarRangeGit      statusbarRangeID = "git"
	statusbarRangeUsage    statusbarRangeID = "usage"
	statusbarRangeNotify   statusbarRangeID = "notify"
	statusbarRangeSettings statusbarRangeID = "settings"
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
	usageStateFn  func(context.Context) (statusbarUsageState, error)
	lookupEnv     func(string) string
	homeDir       func() (string, error)
	now           func() time.Time
}

// newStatusbarCommand builds the production wiring: real tmux + projmux exec
// runner, real binary resolution, and the on-disk notify queue.
func newStatusbarCommand() *statusbarCommand {
	return &statusbarCommand{
		runner:        statusbarExecRunner{},
		executable:    os.Executable,
		notifyStoreFn: defaultStatusbarNotifyStore,
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		now:           time.Now,
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
	ClientTTY   string
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
// Recognized flags: --socket, --client, --mouse-window, --mouse-x, --mouse-y. Both
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
		case "client":
			if !hasValue {
				v, ni, err := consumeValue(name, i)
				if err != nil {
					return "", opts, err
				}
				value = v
				i = ni
			}
			opts.ClientTTY = strings.TrimSpace(value)
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

	// tmux's built-in window-list ranges (the tabs on row 0) set
	// `#{mouse_status_range}` to `window|<idx>` — *not* a user-defined range
	// id and *not* empty. We must detect the `window` / `window|N` shape
	// directly and route to the window-list handler, otherwise the click
	// falls through unrecognized. Two important details:
	//
	//   1. Some tmux versions populate `#{mouse_window}` for window-list
	//      clicks, others leave it empty and only encode the index in the
	//      range token. Parsing the index out of `window|N` makes the
	//      handler robust across both shapes.
	//   2. We do this *before* the user-defined range dispatch so a hostile
	//      user range named "window" can't shadow the built-in.
	if isWindowListRangeToken(raw) {
		// Prefer the `#{mouse_window}` value (a unique window id like `3`,
		// addressed via `@3`) when populated. Some tmux versions / layouts
		// leave it empty for built-in window-range clicks and only encode
		// the winlink index in the range token (`window|<idx>`); in that
		// case we fall back to the index, addressed via `:<idx>` so tmux
		// resolves it against the current session's window list rather
		// than as a (different) window-id lookup.
		if opts.MouseWindow != "" {
			return c.handleWindowListClick(opts, stderr)
		}
		if idx := windowIndexFromRangeToken(raw); idx != "" {
			return c.selectWindow(stderr, ":"+idx)
		}
		return nil
	}

	if raw == "" {
		// tmux emits an empty `#{mouse_status_range}` when the click lands
		// outside any range — typically on status-bar whitespace. If
		// `--mouse-window` is non-empty we fall back to tmux's default
		// `select-window` behavior so users can still click a tab to switch
		// to it; otherwise the click is a noop.
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

// isWindowListRangeToken reports whether `raw` (the value of
// `#{mouse_status_range}` for the click) refers to tmux's built-in window-list
// range. tmux encodes these as `window|<idx>` (e.g. `window|3`) and, for the
// edge case where no index is attached, as the bare string `window`.
func isWindowListRangeToken(raw string) bool {
	if raw == "window" {
		return true
	}
	return strings.HasPrefix(raw, "window|")
}

// windowIndexFromRangeToken extracts the `<idx>` portion of a `window|<idx>`
// range token. Returns "" for the bare `window` form (no index encoded) or any
// non-window-list token.
func windowIndexFromRangeToken(raw string) string {
	const prefix = "window|"
	if !strings.HasPrefix(raw, prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
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
	return c.selectWindow(stderr, "@"+id)
}

// dispatchTable maps each known range id to its click handler. A method on the
// receiver returns it so handlers can capture `c` without leaking package
// state across goroutines.
func (c *statusbarCommand) dispatchTable() map[statusbarRangeID]func(statusbarClickOptions, io.Writer, io.Writer) error {
	return map[statusbarRangeID]func(statusbarClickOptions, io.Writer, io.Writer) error{
		statusbarRangeSession:  c.handleSession,
		statusbarRangePwd:      c.handlePwd,
		statusbarRangeKube:     c.handleKube,
		statusbarRangeGit:      c.handleGit,
		statusbarRangeUsage:    c.handleUsage,
		statusbarRangeNotify:   c.handleNotify,
		statusbarRangeSettings: c.handleSettings,
	}
}

// handleSession opens the project sidebar. It uses the same
// popup-toggle surface as the keyboard bindings so a second click/chord closes
// the scoped popup instead of stacking another one.
func (c *statusbarCommand) handleSession(opts statusbarClickOptions, _, stderr io.Writer) error {
	return c.handlePopupToggleWithClient(stderr, "session", "sessionizer-sidebar", opts.ClientTTY)
}

// handlePwd shows the current pane's path in a small popup. A plain
// display-message toast is too easy to miss and reads like a failure because
// tmux styles messages with the warning palette.
func (c *statusbarCommand) handlePwd(_ statusbarClickOptions, _, stderr io.Writer) error {
	if c.runner == nil {
		return c.runTmux(stderr, "display-message", "statusbar pwd: runner unavailable")
	}
	ctx := context.Background()
	out, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pane_current_path}")
	if err != nil {
		fmt.Fprintf(stderr, "statusbar pwd: read pane path: %v\n", err)
		return c.runTmux(stderr, "display-message", "statusbar pwd: path unavailable")
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return c.runTmux(stderr, "display-message", "statusbar pwd: path unavailable")
	}

	metadata := c.statusbarPathMetadata(ctx, path)
	// Binary path is best-effort: a failure here only loses the any-key
	// close path; the popup itself can still render and falls back to the
	// Enter-only close shape inside statusbarPopupCommand.
	binaryPath, binErr := c.resolveBinary()
	if binErr != nil {
		fmt.Fprintf(stderr, "statusbar pwd: resolve projmux binary for any-key close: %v\n", binErr)
		binaryPath = ""
	}
	popup := statusbarPathPopup(path, metadata, binaryPath)
	// The frame chrome (outer box + titlebar + divider) is drawn inside the
	// payload, so we suppress tmux's own popup border with `-B` and drop the
	// `-T` border title — `frame.go` renders an identical title bar inline so
	// duplicating it via tmux chrome would offset the visual header and
	// double-decorate the surface.
	if err := c.displayPopupNoFallback(popup.Command, intmux.PopupOptions{
		NoBorder:      true,
		Width:         strconv.Itoa(popup.Width),
		Height:        strconv.Itoa(popup.Height),
		CloseBehavior: intmux.PopupCloseOnExit,
	}); err == nil {
		return nil
	}

	return c.runTmux(stderr, "display-message", "path: "+shortenStatusbarToast(path, 180))
}

// handleKube opens the project switcher. There is no kube-specific filter
// surface yet; the switch picker is the existing navigation surface that
// already renders kube metadata for candidates.
func (c *statusbarCommand) handleKube(opts statusbarClickOptions, _, stderr io.Writer) error {
	return c.handlePopupToggleWithClient(stderr, "kube", "sessionizer", opts.ClientTTY)
}

// handleGit opens the project switcher. There is no git-specific filter
// surface yet; the switch picker is the existing navigation surface that
// already renders git metadata for candidates.
func (c *statusbarCommand) handleGit(opts statusbarClickOptions, _, stderr io.Writer) error {
	return c.handlePopupToggleWithClient(stderr, "git", "sessionizer", opts.ClientTTY)
}

func (c *statusbarCommand) handleSettings(opts statusbarClickOptions, _, stderr io.Writer) error {
	return c.handlePopupToggleWithClient(stderr, "settings", "ai-split-settings", opts.ClientTTY)
}

func (c *statusbarCommand) handlePopupToggleWithClient(stderr io.Writer, label, mode, clientTTY string) error {
	binaryPath, err := c.resolveBinary()
	if err != nil {
		return c.runTmux(stderr, "display-message", fmt.Sprintf("statusbar %s: cannot resolve projmux binary", label))
	}
	if c.runner == nil {
		return c.runTmux(stderr, "display-message", fmt.Sprintf("statusbar %s: runner unavailable", label))
	}
	args := []string{"tmux", "popup-toggle"}
	if strings.TrimSpace(clientTTY) != "" {
		args = append(args, "--client", strings.TrimSpace(clientTTY))
	}
	args = append(args, mode)
	if _, err := c.runner.Run(context.Background(), binaryPath, args...); err != nil {
		fmt.Fprintf(stderr, "statusbar %s: popup-toggle %s: %v\n", label, mode, err)
		return c.runTmux(stderr, "display-message", fmt.Sprintf("statusbar %s: popup failed", label))
	}
	return nil
}

// handleUsage opens a native-framed usage HUD popup so users can inspect
// per-window detail without leaving tmux. It stays a direct display-popup
// action, not a popup-toggle mode, so the existing status key behaviour does
// not introduce a new stacking popup surface.
func (c *statusbarCommand) handleUsage(_ statusbarClickOptions, _, stderr io.Writer) error {
	ctx := context.Background()
	state, err := c.loadUsageState(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "statusbar usage: load usage state: %v\n", err)
		state.LoadError = err
	}
	binaryPath, binErr := c.resolveBinary()
	if binErr != nil {
		fmt.Fprintf(stderr, "statusbar usage: resolve projmux binary for any-key close: %v\n", binErr)
		binaryPath = ""
	}
	popup := statusbarUsagePopup(state, c.nowTime(), binaryPath)
	// Same as `handlePwd`: the payload renders its own picker-style frame
	// chrome (outer box + titlebar + divider) so we drop tmux's `-T` border
	// title and pass `-B` to suppress tmux's popup border entirely. Mixing
	// both would double-decorate the popup and offset the geometry.
	if err := c.displayPopupNoFallback(popup.Command, intmux.PopupOptions{
		NoBorder:      true,
		Width:         strconv.Itoa(popup.Width),
		Height:        strconv.Itoa(popup.Height),
		CloseBehavior: intmux.PopupCloseOnExit,
	}); err == nil {
		return nil
	}
	return c.runTmux(stderr, "display-message", popup.Toast)
}

func (c *statusbarCommand) loadUsageState(ctx context.Context) (statusbarUsageState, error) {
	if c.usageStateFn != nil {
		return c.usageStateFn(ctx)
	}
	return c.defaultUsageState(ctx)
}

func (c *statusbarCommand) defaultUsageState(_ context.Context) (statusbarUsageState, error) {
	cmd := newUsageCommand()
	cmd.now = c.nowTime
	modelScope := cmd.ambientModelScope()
	mgr, err := cmd.managerForScope(modelScope)
	if err != nil {
		return statusbarUsageState{}, err
	}
	state, err := mgr.LoadState()
	if err != nil {
		return statusbarUsageState{}, err
	}
	state = filterUsageStateByModels(state, modelScope)
	unsupported := cmd.unsupportedUsageProviders("all", false)
	var cacheMTime time.Time
	stateDir, err := cmd.resolveStateDir()
	if err == nil {
		if info, statErr := os.Stat(coreusage.NewStore(stateDir).FilePath()); statErr == nil {
			cacheMTime = info.ModTime()
		}
	}
	out := statusbarUsageStateFromCache(state, cacheMTime)
	out.Unsupported = unsupported
	return out, nil
}

func statusbarUsageStateFromCache(state coreusage.State, cacheMTime time.Time) statusbarUsageState {
	out := statusbarUsageState{
		Snapshots:  coreusage.SortedSnapshots(state.Snapshots),
		LastSync:   latestUsageCollect(state.LastCollect),
		CacheMTime: cacheMTime,
	}
	if out.LastSync.IsZero() && !cacheMTime.IsZero() {
		out.LastSync = cacheMTime
		out.LastSyncSource = "cache mtime"
	}
	if out.LastSyncSource == "" && !out.LastSync.IsZero() {
		out.LastSyncSource = "last collect"
	}
	return out
}

func latestUsageCollect(values map[string]time.Time) time.Time {
	var latest time.Time
	for _, t := range values {
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

// handleNotify focuses and acknowledges the newest queued notification. If the
// queue is empty we surface that fact in tmux rather than silently doing
// nothing — that gives the keyboard shortcut useful feedback.
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

	// Gone fast path: when the head entry has no routable target, skip the
	// focus subprocess entirely. Inactive/stale entries still try to focus
	// because their pane can exist even though it no longer matches live
	// reply+agent state.
	//
	// The fast path STILL acks the entry: "ack-only" means we skip the focus
	// round-trip, *not* that we leave the row in the queue. Without the ack
	// here the next click would re-classify the same head entry as gone
	// and the user would be stuck repeatedly toasting the same row. The toast
	// remains as a UX signal that the focus side of the click was skipped.
	if display := c.classifyHeadDisplayBestEffort(head); display == notifyDisplayGone || strings.TrimSpace(target) == "" {
		if ackErr := ackFocusedNotification(store, head, entries); ackErr != nil {
			return c.runTmux(stderr, "display-message", fmt.Sprintf("%s; ack failed: %s", notifyAckOnlyToast(notifyDisplayGone), focusFailureSummary(ackErr)))
		}
		return c.runTmux(stderr, "display-message", notifyAckOnlyToast(notifyDisplayGone))
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
	if client := strings.TrimSpace(opts.ClientTTY); client != "" {
		args = append(args, "--client", client)
	}
	if _, runErr := c.runner.Run(context.Background(), binaryPath, args...); runErr != nil {
		// The focus subprocess exits with a deterministic code 2 when the
		// target session/window/pane cannot be resolved (see focus.go's
		// focusExitNotResolved). A click on that row is still a consume signal:
		// there is nowhere useful to focus, so clear the stuck notification
		// instead of making every later click repeat the same toast. Any other
		// exit code is treated as transient (tmux server churn, etc.) — keep the
		// entry so the user can retry, and surface the reason as a toast instead
		// of a tmux error popup.
		if isFocusTargetUnresolved(runErr) {
			if err := ackFocusedNotification(store, head, entries); err != nil {
				return c.runTmux(stderr, "display-message", fmt.Sprintf("notify target gone; ack failed: %s", focusFailureSummary(err)))
			}
			return c.runTmux(stderr, "display-message", "notify target gone; cleared")
		}
		return c.runTmux(stderr, "display-message", fmt.Sprintf("focus failed: %s", focusFailureSummary(runErr)))
	}
	if err := ackFocusedNotification(store, head, entries); err != nil {
		return c.runTmux(stderr, "display-message", fmt.Sprintf("focused; ack failed: %s", focusFailureSummary(err)))
	}
	return nil
}

// classifyHeadDisplayBestEffort returns the display classification for the
// click-target head entry, swallowing any tmux failure as
// [notifyDisplayLive]. Returning live on error preserves the legacy click
// behaviour (focus + ack) so a missing tmux server does not strand every
// click on a "cleared" toast.
//
// We also treat an empty live-pane map the same as a nil map (best-effort
// fallback). `listNotifyLivePanes` returns an empty slice when tmux replies
// successfully but the format result is empty or unrecognized (a common
// failure mode inside the docker e2e harness, which talks to a default tmux
// socket with no projmux options registered server-side). Without this
// nil/empty unification an `ai:`-prefixed head entry would be falsely tagged
// inactive on every click. Phase 6 still lets inactive entries attempt focus,
// but the nil/empty unification also avoids showing an inactive target-state
// hint when live data is unavailable.
// The sidebar/`--live` surfaces keep their stricter contract (empty live map
// means "no panes are in reply state, so anything ai-prefixed *is* stale")
// because they have richer context and are not on the click critical path.
func (c *statusbarCommand) classifyHeadDisplayBestEffort(head notify.Notification) notifyRowDisplayState {
	if c == nil || c.runner == nil {
		return classifyNotifyRowState(head, nil, nil)
	}
	panes, paneSet, err := (&notifyCommand{runner: c.runner}).listNotifyLivePanesAndSet()
	if err != nil {
		return classifyNotifyRowState(head, nil, nil)
	}
	// Real pane-inventory GONE is honoured even when no panes are in reply
	// state: a click on a head whose pane truly disappeared takes the ack-only
	// fast path. newNotifyLivePaneSet already returns nil for an empty/
	// unrecognized tmux reply, so passing paneSet through preserves the
	// docker-e2e best-effort fallback (nil paneSet ⇒ no membership GONE).
	liveByID := notifyLiveShouldQueueByID(panes)
	if len(liveByID) == 0 {
		return classifyNotifyRowState(head, nil, paneSet)
	}
	return classifyNotifyRowState(head, liveByID, paneSet)
}

// notifyAckOnlyToast renders the fast-path toast surfaced when a click lands
// on a gone head entry. The message stays inside tmux's
// display-message length budget so the segment never overflows the status
// line.
func notifyAckOnlyToast(display notifyRowDisplayState) string {
	switch display {
	case notifyDisplayGone:
		return "notify target gone; cleared"
	}
	return "notify ack-only; cleared"
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

type statusbarPathMetadata struct {
	Project string
	Git     string
}

type statusbarUsageState struct {
	Snapshots      []coreusage.Snapshot
	LastSync       time.Time
	LastSyncSource string
	CacheMTime     time.Time
	LoadError      error
	Unsupported    []usageUnsupportedProvider
}

type statusbarUsagePopupView struct {
	Title   string
	Toast   string
	Payload string
	Command string
	Width   int
	Height  int
}

const statusbarUsageSyncStaleAfter = time.Minute

func statusbarUsagePopup(state statusbarUsageState, now time.Time, binaryPath string) statusbarUsagePopupView {
	const width = 96
	title := "Usage"
	// The popup wears the native picker frame chrome: outer box (top+bottom
	// border = 2 rows), title bar + divider (2 rows), and the body content
	// laid out within the inner column budget the renderer reserves. We
	// derive the *inner* layout from ContentLayoutWithTitle so body wrap
	// width matches the chrome the renderer will draw around it.
	innerLayout := projmuxpicker.DefaultRenderer().ContentLayoutWithTitle(
		projmuxpicker.Layout{Rows: 0, Cols: width}, title,
	)
	lines := statusbarUsagePopupLines(state, now, innerLayout.Cols)
	bodyContent := strings.Join(lines, "\n")
	// Outer rows = inner rows + 2 (top/bottom border) + 2 (titlebar +
	// divider). RenderedTextLineCount strips trailing blank lines, so use
	// the raw len(lines) as the row budget instead — that way the frame
	// reserves a row for every body line we asked it to render.
	innerRows := max(len(lines), 1)
	height := innerRows + 4
	outerLayout := projmuxpicker.Layout{Rows: height, Cols: width}

	var framed strings.Builder
	projmuxpicker.DefaultRenderer().RenderFrameWithTitle(&framed, bodyContent, title, outerLayout)
	payload := framed.String()
	command := statusbarPopupCommand(payload, binaryPath)
	return statusbarUsagePopupView{
		Title:   title,
		Toast:   statusbarUsageToast(state),
		Payload: payload,
		Command: command,
		Width:   width,
		Height:  height,
	}
}

func statusbarUsagePopupLines(state statusbarUsageState, now time.Time, cols int) []string {
	rows := statusbarUsageRows(state.Snapshots)
	// The native picker frame chrome wrapping this body already renders the
	// `Usage` title bar (and the divider below it). Body opens with the
	// subdued subtitle so the popup mirrors the picker layout one-to-one
	// without duplicating the title or borrowing picker active-row ANSI.
	lines := []string{
		dimANSI("AI token usage windows from the local cache."),
		"",
		statusbarUsageSyncLine(state, now),
		"",
		statusbarUsageHeaderLine(),
		projmuxpicker.SeparatorLine(cols),
	}
	if state.LoadError != nil {
		lines = append(lines, dimANSI("usage data unavailable: "+statusbarUsageErrorSummary(state.LoadError)))
	} else if len(rows) == 0 && len(state.Unsupported) == 0 {
		lines = append(lines, dimANSI("No usage data available yet."))
	} else {
		for _, row := range rows {
			lines = append(lines, row.render())
		}
		for _, provider := range state.Unsupported {
			lines = append(lines, statusbarUnsupportedUsageLine(provider))
		}
	}
	lines = append(lines, statusbarPopupFooterLines(cols)...)
	return lines
}

func statusbarUsageToast(state statusbarUsageState) string {
	if state.LoadError != nil {
		return "usage unavailable"
	}
	rows := statusbarUsageRows(state.Snapshots)
	if len(rows) == 0 {
		if len(state.Unsupported) > 0 {
			return "usage: " + state.Unsupported[0].Model + " unsupported"
		}
		return "usage: no data"
	}
	parts := make([]string, 0, min(len(rows), 2))
	for _, row := range rows {
		parts = append(parts, row.model+" "+row.window+" "+row.pct)
		if len(parts) == 2 {
			break
		}
	}
	return "usage: " + strings.Join(parts, " · ")
}

func statusbarUnsupportedUsageLine(provider usageUnsupportedProvider) string {
	label := strings.TrimSpace(provider.Label)
	if label == "" {
		label = provider.Model
	}
	return dimANSI(fmt.Sprintf("%-8s %-6s %10s %10s %10s %6s  %-16s", label, "ctx", "-", "-", "-", "-", "unsupported"))
}

func statusbarUsageSyncLine(state statusbarUsageState, now time.Time) string {
	label := projmuxpicker.MutedStart + "sync" + projmuxpicker.Reset + "  "
	if state.LastSync.IsZero() {
		return label + dimANSI("never")
	}
	local := state.LastSync.Local()
	value := local.Format("2006-01-02 15:04:05")
	if age := usageSyncAge(state.LastSync, now); age >= time.Second {
		value += " (" + formatBackoffDuration(age.Round(time.Second)) + " ago)"
	}
	if state.LastSyncSource == "cache mtime" {
		value += " cache"
	}
	if usageSyncStale(state.LastSync, now) {
		return label + dimANSI(value)
	}
	return label + value
}

func usageSyncAge(last, now time.Time) time.Duration {
	if last.IsZero() || now.IsZero() {
		return 0
	}
	if last.After(now) {
		return 0
	}
	return now.Sub(last)
}

func usageSyncStale(last, now time.Time) bool {
	return usageSyncAge(last, now) > statusbarUsageSyncStaleAfter
}

type statusbarUsageRow struct {
	model     string
	window    string
	used      string
	limit     string
	remaining string
	pct       string
	pctValue  float64
	reset     string
}

func statusbarUsageRows(snaps []coreusage.Snapshot) []statusbarUsageRow {
	snaps = coreusage.SortedSnapshots(snaps)
	rows := make([]statusbarUsageRow, 0, len(snaps))
	for _, s := range snaps {
		if s.Pct == 0 && s.ResetsAt.IsZero() && s.Limit == 0 {
			continue
		}
		row := statusbarUsageRow{
			model:    modelDisplayLabel(s.Model),
			window:   string(s.Window),
			used:     usageCountText(s.Tokens),
			limit:    usageLimitText(s.Limit),
			pct:      percentText(s.Pct),
			pctValue: s.Pct,
			reset:    usageResetText(s.ResetsAt),
		}
		if s.Limit > 0 {
			row.remaining = formatUsageInt(max(s.Limit-s.Tokens, 0))
		} else {
			row.remaining = "-"
		}
		rows = append(rows, row)
	}
	return rows
}

func statusbarUsageHeaderLine() string {
	return projmuxpicker.MutedStart +
		fmt.Sprintf("%-8s %-6s %10s %10s %10s %6s  %-16s", "MODEL", "WIN", "USED", "LIMIT", "LEFT", "PCT", "RESET") +
		projmuxpicker.Reset
}

func (r statusbarUsageRow) render() string {
	return fmt.Sprintf("%-8s %-6s %10s %10s %10s %6s  %-16s",
		r.model,
		r.window,
		styleUnavailableRight(r.used, 10),
		styleUnavailableRight(r.limit, 10),
		styleUnavailableRight(r.remaining, 10),
		statusbarUsagePctANSI(r.pct, r.pctValue, 6),
		styleUnavailableLeft(r.reset, 16),
	)
}

func usageCountText(value int64) string {
	if value <= 0 {
		return "-"
	}
	return formatUsageInt(value)
}

func usageLimitText(value int64) string {
	if value <= 0 {
		return "-"
	}
	return formatUsageInt(value)
}

func usageResetText(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("Jan 02 15:04")
}

func formatUsageInt(value int64) string {
	if value == 0 {
		return "0"
	}
	neg := value < 0
	if neg {
		value = -value
	}
	s := strconv.FormatInt(value, 10)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

func styleUnavailableRight(value string, width int) string {
	if value == "-" || strings.TrimSpace(value) == "" {
		return dimANSI(fmt.Sprintf("%*s", width, "-"))
	}
	return fmt.Sprintf("%*s", width, value)
}

func styleUnavailableLeft(value string, width int) string {
	if value == "-" || strings.TrimSpace(value) == "" {
		return dimANSI(fmt.Sprintf("%-*s", width, "-"))
	}
	return fmt.Sprintf("%-*s", width, value)
}

func statusbarUsagePctANSI(text string, pct float64, width int) string {
	padded := fmt.Sprintf("%*s", width, text)
	switch {
	case pct >= 95:
		return redANSI(padded)
	case pct >= 80:
		return amberANSI(padded)
	default:
		return tealANSI(padded)
	}
}

func statusbarUsageErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "unknown"
	}
	if len(msg) > 72 {
		return msg[:69] + "..."
	}
	return msg
}

func dimANSI(value string) string {
	return projmuxpicker.MutedStart + value + projmuxpicker.Reset
}

// amberANSI/redANSI/tealANSI tint the native usage statusbar text from the
// semantic state role map (single source shared with the notify/usage tmux
// severity cluster). ANSI256FgStart adapts the role's tmux colourN to the
// 256-color SGR; the fallback role values keep this byte-identical.
func amberANSI(value string) string {
	return theme.ANSI256FgStart(theme.RenderRolesFromEffective(theme.EffectiveTheme{}).StateWarning) + value + projmuxpicker.Reset
}

func redANSI(value string) string {
	return theme.ANSI256FgStart(theme.RenderRolesFromEffective(theme.EffectiveTheme{}).StateCritical) + value + projmuxpicker.Reset
}

func tealANSI(value string) string {
	return theme.ANSI256FgStart(theme.RenderRolesFromEffective(theme.EffectiveTheme{}).StateSuccess) + value + projmuxpicker.Reset
}

func (c *statusbarCommand) statusbarPathMetadata(ctx context.Context, path string) statusbarPathMetadata {
	metadata := statusbarPathMetadata{Project: filepath.Base(path)}
	if metadata.Project == "." || metadata.Project == string(filepath.Separator) {
		metadata.Project = path
	}
	if c.runner == nil {
		return metadata
	}
	out, err := c.runner.Run(ctx, "git", "-C", path, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD")
	if err != nil {
		return metadata
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return metadata
	}
	root, branch := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if branch == "HEAD" || branch == "" {
		return metadata
	}
	repo := filepath.Base(root)
	if repo != "" && repo != "." && repo != metadata.Project {
		metadata.Git = branch + " in " + repo
	} else {
		metadata.Git = branch
	}
	return metadata
}

type statusbarPathPopupView struct {
	Title   string
	Toast   string
	Payload string
	Command string
	Width   int
	Height  int
}

func statusbarPathPopup(path string, metadata statusbarPathMetadata, binaryPath string) statusbarPathPopupView {
	title := "Current path"

	const width = 84
	// Reserve the inner column budget the picker frame renderer will leave
	// us after drawing the outer box + title bar. Body wraps to inner.Cols
	// so right-edge characters never collide with the frame vertical bar.
	innerLayout := projmuxpicker.DefaultRenderer().ContentLayoutWithTitle(
		projmuxpicker.Layout{Rows: 0, Cols: width}, title,
	)
	contentWidth := innerLayout.Cols
	// The frame chrome around this body renders the `Current path` title
	// bar; the body opens with a subdued subtitle, identical layout to the
	// native picker (description row right under the divider).
	lines := []string{
		dimANSI("Active pane working directory."),
		"",
	}
	lines = append(lines, statusbarFieldLines("cwd", path, contentWidth)...)
	if metadata.Project != "" || metadata.Git != "" {
		lines = append(lines, "", projmuxpicker.SeparatorLine(contentWidth))
		if metadata.Project != "" {
			lines = append(lines, statusbarFieldLines("project", metadata.Project, contentWidth)...)
		}
		if metadata.Git != "" {
			lines = append(lines, statusbarFieldLines("git", metadata.Git, contentWidth)...)
		}
	}
	lines = append(lines, statusbarPopupFooterLines(contentWidth)...)

	bodyContent := strings.Join(lines, "\n")
	// Outer rows = inner rows + 2 (top/bottom border) + 2 (titlebar +
	// divider). Use raw len(lines) — RenderedTextLineCount trims trailing
	// blank lines and the footer would lose its leading separator row.
	innerRows := max(len(lines), 1)
	height := innerRows + 4
	outerLayout := projmuxpicker.Layout{Rows: height, Cols: width}

	var framed strings.Builder
	projmuxpicker.DefaultRenderer().RenderFrameWithTitle(&framed, bodyContent, title, outerLayout)
	payload := framed.String()
	command := statusbarPopupCommand(payload, binaryPath)
	return statusbarPathPopupView{
		Title:   title,
		Toast:   "path",
		Payload: payload,
		Command: command,
		Width:   width,
		Height:  height,
	}
}

// statusbarPopupCommand renders a display-only popup payload plus its
// any-key close path. The close path prefers the hidden `popup-wait-key`
// subcommand so we don't depend on the popup's underlying shell supporting
// `read -n1` / `stty` directly. When the binary path is empty (resolveBinary
// failed) we fall back to the legacy Enter-only `IFS= read -r _` shape so
// the popup at least remains dismissable.
func statusbarPopupCommand(payload, binaryPath string) string {
	payload = strings.TrimRight(payload, "\r\n")
	prefix := "printf %s " + tmuxShellQuote(payload)
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return prefix + "; IFS= read -r _"
	}
	return prefix + "; " + tmuxShellQuote(binaryPath) + " popup-wait-key"
}

const displayOnlyPopupClosePrompt = "Press any key to close."

func displayOnlyPopupClosePromptLine() string {
	return projmuxpicker.MutedStart + displayOnlyPopupClosePrompt + projmuxpicker.Reset
}

func statusbarPopupFooterLines(cols int) []string {
	return []string{
		"",
		projmuxpicker.SeparatorLine(cols),
		displayOnlyPopupClosePromptLine(),
	}
}

func statusbarFieldLines(label, value string, cols int) []string {
	const labelWidth = 11
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	valueWidth := max(cols-labelWidth-2, 24)
	wrapped := wrapStatusbarText(value, valueWidth)
	if len(wrapped) == 0 {
		wrapped = []string{"-"}
	}
	lines := make([]string, 0, len(wrapped))
	prefix := projmuxpicker.MutedStart + fmt.Sprintf("%-*s", labelWidth, label) + projmuxpicker.Reset + "  "
	continuation := strings.Repeat(" ", labelWidth+2)
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, prefix+line)
		} else {
			lines = append(lines, continuation+line)
		}
	}
	return lines
}

func wrapStatusbarText(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if width <= 0 {
		return []string{value}
	}
	var lines []string
	var current strings.Builder
	currentWidth := 0
	for _, r := range value {
		runeWidth := projmuxpicker.RuneWidth(r)
		if currentWidth > 0 && currentWidth+runeWidth > width {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}
		current.WriteRune(r)
		currentWidth += runeWidth
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func shortenStatusbarToast(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return "..." + value[len(value)-(max-3):]
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

func (c *statusbarCommand) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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

func (c *statusbarCommand) selectWindow(stderr io.Writer, target string) error {
	if c.runner == nil {
		return c.runTmux(stderr, "select-window", "-t", target)
	}
	if err := intmux.NewRunner(c.runner).SelectWindow(context.Background(), intmux.SelectWindowOptions{Target: target}); err != nil {
		fmt.Fprintf(stderr, "statusbar: tmux select-window -t %s: %v\n", target, err)
	}
	return nil
}

func (c *statusbarCommand) displayPopupNoFallback(command string, options intmux.PopupOptions) error {
	if c.runner == nil {
		return errors.New("statusbar runner is not configured")
	}
	return intmux.NewRunner(c.runner).DisplayPopup(context.Background(), command, options)
}

func printStatusbarUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux statusbar click <range-id> [--socket <s>] [--client <tty>] [--mouse-window <id>] [--mouse-x N] [--mouse-y N]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Range ids: session pwd kube git usage notify settings")
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
