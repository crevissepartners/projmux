package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corefocus "github.com/crevissepartners/projmux/internal/core/focus"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// focusExitNotResolved is the exit code emitted when an explicit target cannot
// be resolved and no reasonable fallback is available. It is documented in the
// spec so callers (status bar, notification dispatch) can branch on it.
const focusExitNotResolved = 2

const focusFieldSeparator = "__PROJMUX_FOCUS_SEP__"

// focusCommandRunner abstracts shelling out so tests can stub tmux.
type focusCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// focusNotifier delivers a desktop-side "session ready" message when the
// focus request cannot be honored on the current display (for example, the
// requested socket has no attached client at all).
type focusNotifier interface {
	Notify(notification aiNotification) error
}

type focusCommand struct {
	diagnostics       *diagnostics.LifecycleRecorder
	notifyDiagnostics *diagnostics.NotifyFocusRecorder
	runner            focusCommandRunner
	lookupEnv         func(string) string
	homeDir           func() (string, error)
	stdout            io.Writer
	stderr            io.Writer
	notifierOnce      func(stderr io.Writer) focusNotifier
	notifyStoreFn     func() (notifyStore, error)
}

type focusOptions struct {
	Target string
	Socket string
	Client string
	Source string
	Kind   string
	URI    string
	JSON   bool
	// ExactOnly is set by the canonical `focus <kind>` routes. Those routes
	// move the client to an already-live exact-one target, so a session
	// fallback match and a window/pane coordinate that does not resolve are
	// both "not resolved" (exit 2) rather than a degraded success. The legacy
	// `focus --target` spelling leaves it false and keeps its fallbacks.
	ExactOnly bool
	// NavKind is the canonical navigation kind ("project", "window", "pane"),
	// or "" for the legacy --target/--uri spellings. The three Nav* fields
	// below carry the operator's references until they are resolved into a
	// tmux coordinate against the live inventory.
	NavKind    string
	NavProject string
	NavWindow  string
	NavRef     string
}

type focusResult struct {
	OK              bool   `json:"ok"`
	Fallback        string `json:"fallback,omitempty"`
	Target          string `json:"target,omitempty"`
	Socket          string `json:"socket,omitempty"`
	OriginClient    string `json:"origin_client,omitempty"`
	ResolvedSession string `json:"resolved_session,omitempty"`
	Client          string `json:"client,omitempty"`
	Dispatch        string `json:"dispatch,omitempty"`
	SessionState    string `json:"session_state,omitempty"`
	WindowState     string `json:"window_state,omitempty"`
	PaneState       string `json:"pane_state,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Note            string `json:"note,omitempty"`
}

func newFocusCommand(recorders ...*diagnostics.LifecycleRecorder) *focusCommand {
	var recorder *diagnostics.LifecycleRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	cmd := &focusCommand{
		diagnostics:   recorder,
		runner:        focusExecRunner{},
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		notifyStoreFn: defaultStatusNotifyStore,
	}
	cmd.notifierOnce = func(stderr io.Writer) focusNotifier {
		// Reuse the existing notifier chain (WSL toast, notify-send, hook).
		ai := newAICommand()
		ai.notifyDiagnostics = cmd.notifyDiagnostics
		return ai.notificationNotifier()
	}
	return cmd
}

// focusKinds lists the resource kinds the canonical `focus` verb navigates to.
var focusKinds = []string{"project", "window", "pane"}

// Run is the dispatcher entry point.
//
// It accepts two spellings that share one dispatch: the legacy
// `focus --target SESSION[:WINDOW[.PANE]]` coordinate, and the canonical
// `focus <kind> <ref>` navigation. Neither one ever creates a tmux session,
// window, or pane: focus only redirects an already attached client, so an
// offline target is a not-resolved exit rather than an implicit materialization.
func (c *focusCommand) Run(args []string, stdout, stderr io.Writer) error {
	c.stdout = stdout
	c.stderr = stderr

	if len(args) > 0 && slices.Contains(focusKinds, args[0]) {
		opts, err := parseCanonicalFocusArgs(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return c.dispatch(opts, stdout, stderr)
	}
	opts, err := parseFocusArgs(args, stderr)
	if err != nil {
		return err
	}
	return c.dispatch(opts, stdout, stderr)
}

// dispatch runs one resolved focus request.
func (c *focusCommand) dispatch(opts focusOptions, stdout, stderr io.Writer) (runErr error) {
	diagnosticsStarted := time.Now()
	var diagnosticsTarget corefocus.Target
	var diagnosticsSocket string
	var diagnosticsResult focusResult
	defer func() {
		if c.notifyDiagnostics == nil {
			return
		}
		telemetry := newFocusTelemetry(opts, diagnosticsTarget, diagnosticsSocket)
		disposition, code := focusDiagnosticOutcome(diagnosticsResult, runErr)
		c.notifyDiagnostics.RecordFocus(disposition, telemetry.provider, telemetry.category, telemetry.route, code, diagnosticsStarted)
	}()

	if err := c.resolveURIOption(&opts); err != nil {
		return err
	}

	socket := c.resolveSocket(opts.Socket)
	diagnosticsSocket = socket
	resolvedTarget, err := c.resolveNavigationTarget(context.Background(), socket, opts)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return err
	}
	opts.Target = resolvedTarget

	target, err := corefocus.Parse(opts.Target)
	if err != nil {
		return err
	}
	diagnosticsTarget = target
	c.logTelemetry(newFocusTelemetry(opts, target, socket))

	res, err := c.execute(context.Background(), target, socket, opts.Client, opts.ExactOnly)
	res.Target = target.Raw
	res.Socket = socket
	res.OriginClient = strings.TrimSpace(opts.Client)
	if err != nil {
		res.OK = false
		if res.Reason == "" {
			res.Reason = "dispatch-failed"
		}
		if res.Note == "" {
			res.Note = err.Error()
		}
	}
	diagnosticsResult = res

	if opts.JSON {
		if writeErr := writeFocusJSON(stdout, res); writeErr != nil {
			diagnosticsResult.Reason = "output-failed"
			return writeErr
		}
	}

	if err != nil {
		// On non-JSON output paths we emit a short diagnostic to stderr; the
		// JSON path already conveys the failure via ok:false.
		if !opts.JSON {
			fmt.Fprintln(stderr, err.Error())
		}
		var notResolved *focusUnresolvedError
		if errors.As(err, &notResolved) || focusResultIsUnresolvedID(res) {
			return focusExitError{code: focusExitNotResolved, err: err}
		}
		return err
	}
	if opts.URI != "" && res.Dispatch != "notify-only" {
		if ackErr := c.ackFocusedURIQueueEntry(target, socket); ackErr != nil && !opts.JSON {
			fmt.Fprintf(stderr, "focus: notify ack failed: %v\n", ackErr)
		}
	}
	return nil
}

func focusResultIsUnresolvedID(res focusResult) bool {
	switch res.Reason {
	case "window-id-unresolved", "pane-id-unresolved":
		return true
	default:
		return false
	}
}

func (c *focusCommand) ackFocusedURIQueueEntry(target corefocus.Target, socket string) error {
	if c.notifyStoreFn == nil || !target.HasPane() {
		return nil
	}
	store, err := c.notifyStoreFn()
	if err != nil {
		return nil
	}
	entries, err := store.List()
	if err != nil {
		return err
	}
	selected, ok := latestAINotificationForFocusTarget(entries, target, socket)
	if !ok {
		return nil
	}
	return ackFocusedNotification(store, selected, entries)
}

func latestAINotificationForFocusTarget(entries []notify.Notification, target corefocus.Target, socket string) (notify.Notification, bool) {
	targetPane := strings.TrimSpace(target.PaneSelector())
	if strings.TrimSpace(target.Session) == "" || targetPane == "" {
		return notify.Notification{}, false
	}
	targetSocket := strings.TrimSpace(socket)
	var selected notify.Notification
	for _, entry := range entries {
		if entry.Source != notify.SourceAI {
			continue
		}
		if strings.TrimSpace(entry.Session) != strings.TrimSpace(target.Session) {
			continue
		}
		if strings.TrimSpace(entry.Pane) != targetPane {
			continue
		}
		entrySocket := strings.TrimSpace(entry.Socket)
		if targetSocket != "" && entrySocket != "" && entrySocket != targetSocket {
			continue
		}
		if selected.ID == "" || entry.CreatedAt.After(selected.CreatedAt) {
			selected = entry
		}
	}
	return selected, selected.ID != ""
}

func parseFocusArgs(args []string, stderr io.Writer) (focusOptions, error) {
	fs := flag.NewFlagSet("focus", flag.ContinueOnError)
	fs.SetOutput(stderr)

	opts := focusOptions{}
	fs.StringVar(&opts.Target, "target", "", "Focus target SESSION[:WINDOW[.PANE]] (mutually exclusive with --uri)")
	fs.StringVar(&opts.Socket, "socket", "", "tmux socket path (overrides $TMUX)")
	fs.StringVar(&opts.Client, "client", "", "preferred origin tmux client tty")
	fs.StringVar(&opts.Source, "source", "", "Telemetry label: ai|status-bar|external|os-notification|toast")
	fs.StringVar(&opts.Kind, "kind", "", "Telemetry label: reply-ready|busy-cleared|segment-click|toast-click|custom")
	fs.StringVar(&opts.URI, "uri", "", "projmux:// URI compatibility input (resolves to --target via tmux)")
	fs.BoolVar(&opts.JSON, "json", false, "Emit a single-line JSON result")

	if err := fs.Parse(args); err != nil {
		return focusOptions{}, err
	}
	if fs.NArg() != 0 {
		return focusOptions{}, fmt.Errorf("focus does not accept positional arguments")
	}
	// --uri and --target are alternative entry points and must not be
	// combined: --uri carries everything --target+--socket+--source would
	// have carried, so accepting both would silently let the URI override
	// (or be overridden by) explicit flags in ways callers can't predict.
	if strings.TrimSpace(opts.URI) != "" && strings.TrimSpace(opts.Target) != "" {
		return focusOptions{}, fmt.Errorf("--uri and --target are mutually exclusive")
	}
	if strings.TrimSpace(opts.URI) == "" && strings.TrimSpace(opts.Target) == "" {
		return focusOptions{}, fmt.Errorf("one of --target or --uri is required")
	}
	return opts, nil
}

// parseCanonicalFocusArgs parses one `focus project|window|pane <ref>`
// invocation into the shared focus request.
//
// The refs are runtime coordinates: a Project ref names its live tmux session,
// and the Window and Pane refs name a window and a pane inside it. Each kind
// requires the scope above it, because a Window ref alone does not identify a
// target. Nothing here reads or writes the resource registry and nothing here
// creates a runtime.
func parseCanonicalFocusArgs(kind string, args []string, stderr io.Writer) (focusOptions, error) {
	spelling := "focus " + kind
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)

	opts := focusOptions{ExactOnly: true}
	var project, window string
	if kind != "project" {
		fs.StringVar(&project, "project", "", "the live Project whose runtime scopes the target")
		fs.StringVar(&project, "p", "", "the live Project whose runtime scopes the target (alias of --project)")
	}
	if kind == "pane" {
		fs.StringVar(&window, "window", "", "the live Window that owns the target Pane")
		fs.StringVar(&window, "w", "", "the live Window that owns the target Pane (alias of --window)")
	}
	fs.StringVar(&opts.Socket, "socket", "", "tmux socket path (overrides $TMUX)")
	fs.StringVar(&opts.Client, "client", "", "preferred origin tmux client tty")
	fs.StringVar(&opts.Source, "source", "", "Telemetry label: ai|status-bar|external|os-notification|toast")
	fs.StringVar(&opts.Kind, "kind", "", "Telemetry label: reply-ready|busy-cleared|segment-click|toast-click|custom")
	fs.BoolVar(&opts.JSON, "json", false, "Emit a single-line JSON result")

	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return focusOptions{}, err
		}
		return focusOptions{}, usageError(err.Error())
	}
	if len(refs) != 1 {
		return focusOptions{}, usageError(fmt.Sprintf("%s requires exactly one resource reference", spelling))
	}
	ref := strings.TrimSpace(refs[0])
	if ref == "" {
		return focusOptions{}, usageError(spelling + " requires a non-empty resource reference")
	}
	// A canonical ref names one resource. Accepting a raw `session:window.pane`
	// coordinate here would quietly reintroduce the legacy target grammar under
	// a kind that promises something narrower.
	if strings.ContainsAny(ref, ":.") {
		return focusOptions{}, usageError(fmt.Sprintf(
			"%s takes one %s reference, not a session:window.pane coordinate; machine-owned raw coordinates use `projmux internal focus --target`", spelling, kind))
	}

	opts.NavKind = kind
	opts.NavRef = ref
	opts.NavProject = strings.TrimSpace(project)
	opts.NavWindow = strings.TrimSpace(window)
	switch kind {
	case "project":
		opts.Target = ref
	case "window":
		if opts.NavProject == "" {
			return focusOptions{}, usageError(spelling + " requires --project <ref>")
		}
	case "pane":
		if opts.NavProject == "" {
			return focusOptions{}, usageError(spelling + " requires --project <ref>")
		}
		if opts.NavWindow == "" {
			return focusOptions{}, usageError(spelling + " requires --window <ref>")
		}
	}
	return opts, nil
}

// resolveNavigationTarget turns a canonical `focus <kind> <ref>` request into the
// tmux coordinate the shared dispatch understands.
//
// Every lookup here is a read: list-windows and list-panes only report the live
// inventory. A reference that matches nothing, or matches more than one live
// resource, is a not-resolved exit rather than a create, which is what keeps
// `focus` free of materialization at the Window and Pane levels too.
func (c *focusCommand) resolveNavigationTarget(ctx context.Context, socket string, opts focusOptions) (string, error) {
	if opts.NavKind == "" || opts.NavKind == "project" {
		return opts.Target, nil
	}
	windowID, err := c.resolveLiveWindow(ctx, socket, opts.NavProject, navWindowRef(opts))
	if err != nil {
		return "", err
	}
	if opts.NavKind == "window" {
		return opts.NavProject + ":" + windowID, nil
	}
	paneID, err := c.resolveLivePane(ctx, socket, opts.NavProject, windowID, opts.NavRef)
	if err != nil {
		return "", err
	}
	return opts.NavProject + ":" + windowID + "." + paneID, nil
}

// navWindowRef returns the Window reference of a navigation request: the
// positional reference for `focus window`, and --window for `focus pane`.
func navWindowRef(opts focusOptions) string {
	if opts.NavKind == "window" {
		return opts.NavRef
	}
	return opts.NavWindow
}

// resolveLiveWindow maps a Window name (or a raw `@id`) to the live tmux window
// id inside one session.
func (c *focusCommand) resolveLiveWindow(ctx context.Context, socket, session, ref string) (string, error) {
	format := strings.Join([]string{"#{window_id}", "#{window_name}", "#{@" + strings.TrimPrefix(tmuxopts.WindowName, "@") + "}"}, focusFieldSeparator)
	rows, err := c.listTargets(ctx, socket, "list-windows", session, format)
	if err != nil {
		return "", newFocusNotResolved("window %q in session %q: %v", ref, session, err)
	}
	return pickLiveTarget("window", ref, session, rows)
}

// resolveLivePane maps a Pane name (or a raw `%id`) to the live tmux pane id
// inside one window.
func (c *focusCommand) resolveLivePane(ctx context.Context, socket, session, windowID, ref string) (string, error) {
	format := strings.Join([]string{"#{pane_id}", "#{@" + strings.TrimPrefix(tmuxopts.PaneName, "@") + "}"}, focusFieldSeparator)
	rows, err := c.listTargets(ctx, socket, "list-panes", session+":"+windowID, format)
	if err != nil {
		return "", newFocusNotResolved("pane %q in %s:%s: %v", ref, session, windowID, err)
	}
	return pickLiveTarget("pane", ref, session+":"+windowID, rows)
}

// listTargets runs one read-only tmux inventory query and splits its rows.
func (c *focusCommand) listTargets(ctx context.Context, socket, verb, target, format string) ([][]string, error) {
	if c.runner == nil {
		return nil, errors.New("focus runner is not configured")
	}
	out, err := c.runner.Run(ctx, "tmux", c.tmuxArgs(socket, verb, "-t", target, "-F", format)...)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, focusFieldSeparator)
		if len(fields) == 1 {
			fields = strings.Split(line, "\t")
		}
		rows = append(rows, fields)
	}
	return rows, nil
}

// pickLiveTarget selects the exact-one live row a reference addresses.
//
// The first field of every row is the tmux id; the remaining fields are the
// candidate names. A reference matches either the id itself or one of the names
// exactly. Substring and prefix matching are deliberately absent: this route
// promises an exact-one target.
func pickLiveTarget(kind, ref, scope string, rows [][]string) (string, error) {
	var matched []string
	for _, fields := range rows {
		if len(fields) == 0 || fields[0] == "" {
			continue
		}
		if fields[0] == ref {
			matched = append(matched, fields[0])
			continue
		}
		for _, name := range fields[1:] {
			if strings.TrimSpace(name) == ref {
				matched = append(matched, fields[0])
				break
			}
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return "", newFocusNotResolved("%s %q is not live in %s", kind, ref, scope)
	default:
		return "", newFocusNotResolved("%s %q matched %d live %ss in %s, want exactly one", kind, ref, len(matched), kind, scope)
	}
}

// newFocusNotResolved builds the deterministic not-resolved exit used by the
// canonical navigation routes.
func newFocusNotResolved(format string, args ...any) error {
	return focusExitError{code: focusExitNotResolved, err: fmt.Errorf("focus: "+format, args...)}
}

func (c *focusCommand) execute(ctx context.Context, target corefocus.Target, socket, preferredClient string, exactOnly bool) (focusResult, error) {
	preferredClient = strings.TrimSpace(preferredClient)
	inventory, err := c.listSessionInventory(ctx, socket)
	if err != nil {
		return focusResult{}, err
	}

	resolution, ok := corefocus.Resolve(target.Session, inventory)
	if !ok {
		return focusResult{
				OriginClient: preferredClient,
				SessionState: "unresolved",
				Reason:       "session-unresolved",
			},
			&focusUnresolvedError{session: target.Session, socket: socket}
	}
	// The canonical routes address one already-live target. A fallback match is
	// a different live session, so it is not the requested target and must not
	// be silently focused instead of it.
	if exactOnly && resolution.Fallback != "" {
		return focusResult{
				OriginClient: preferredClient,
				SessionState: "unresolved",
				Reason:       "session-unresolved",
			},
			&focusUnresolvedError{session: target.Session, socket: socket}
	}

	clients, err := c.listClients(ctx, socket)
	if err != nil {
		return focusResult{
			OriginClient:    preferredClient,
			ResolvedSession: resolution.Name,
			SessionState:    focusSessionState(resolution),
			Reason:          "list-clients-failed",
		}, err
	}

	base := focusResult{
		OriginClient:    preferredClient,
		ResolvedSession: resolution.Name,
		SessionState:    focusSessionState(resolution),
	}

	// Socket policy: an explicit --socket wins; otherwise $TMUX provides the
	// socket path. Dispatch redirects one suitable attached client on that
	// socket and never force-detaches other clients. If the socket has no
	// attached clients, focus degrades to a desktop notification only.
	if len(clients) == 0 {
		if err := c.notifySessionReady(resolution.Name); err != nil {
			fmt.Fprintf(c.stderr, "focus: notify failed: %v\n", err)
		}
		base.OK = true
		base.Fallback = combineFallback(resolution.Fallback, "notify-only")
		base.Dispatch = "notify-only"
		base.Reason = "no-attached-client"
		base.Note = "no tmux client attached on socket; notification emitted"
		return base, nil
	}

	clientName := pickFocusClient(clients, resolution.Name, preferredClient)
	base.Client = clientName
	base.Dispatch = "switch-client"
	if err := c.switchClient(ctx, socket, clientName, resolution.Name); err != nil {
		base.Reason = "switch-client-failed"
		return base, err
	}
	// tmux pane focus succeeded. `projmux focus` deliberately stops at the
	// tmux layer: it never asks a host-window focus adapter to bring the
	// terminal window forward, regardless of the desktop notification mode.
	if target.HasWindow() {
		windowTarget := fmt.Sprintf("%s:%s", resolution.Name, target.WindowSelector())
		if err := c.selectWindow(ctx, socket, windowTarget); err != nil {
			// Window selection failure is recoverable when only an index was
			// provided (split layout could have changed). With an explicit
			// id, and for the canonical exact-one routes, we surface the error.
			if target.WindowID != "" || exactOnly {
				base.WindowState = "id-unresolved"
				base.Reason = "window-id-unresolved"
				return base, err
			}
			fmt.Fprintf(c.stderr, "focus: select-window %s failed: %v (stopping at session level)\n", windowTarget, err)
			base.OK = true
			base.Fallback = combineFallback(resolution.Fallback, "session-only")
			base.WindowState = "index-fallback-session"
			base.Reason = "window-index-unresolved"
			base.Note = "window index did not resolve; focused the resolved session only"
			return base, nil
		}
		base.WindowState = "selected"

		if target.HasPane() {
			paneTarget := fmt.Sprintf("%s.%s", windowTarget, target.PaneSelector())
			if err := c.selectPane(ctx, socket, paneTarget); err != nil {
				if target.PaneID != "" || exactOnly {
					base.PaneState = "id-unresolved"
					base.Reason = "pane-id-unresolved"
					return base, err
				}
				fmt.Fprintf(c.stderr, "focus: select-pane %s failed: %v (stopping at window level)\n", paneTarget, err)
				base.OK = true
				base.Fallback = combineFallback(resolution.Fallback, "window-only")
				base.PaneState = "index-fallback-window"
				base.Reason = "pane-index-unresolved"
				base.Note = "pane index did not resolve; focused the resolved window only"
				return base, nil
			}
			base.PaneState = "selected"
		}
	}

	base.OK = true
	base.Fallback = resolution.Fallback
	return base, nil
}

func focusSessionState(resolution corefocus.Resolution) string {
	if resolution.Fallback != "" {
		return "fallback"
	}
	return "exact"
}

// resolveURIOption inflates a `--uri` invocation into the existing
// `--target`/`--socket`/`--source` shape so the rest of focus dispatch is
// unchanged.
//
// `--uri` is a compatibility input: projmux no longer emits clickable Toasts
// and registers no `projmux://` protocol handler (see notification_uri.go), so
// no in-product producer reaches this path. It stays wired for handlers
// installed before 0.11.0 or by the user.
//
// The URI hands us a tmux pane id like `%8` plus the originating tmux socket.
// corefocus.Target requires a SESSION[:WINDOW[.PANE]] coordinate, so we
// resolve the pane id to its enclosing session + window via
// `tmux display-message -p -t %N '#S__SEP__#I'` against the URI's socket.
// The translated target preserves the explicit pane id (`session:window.%8`)
// so the pane-level focus stays exact even if a layout change shifts pane
// indices between URI emission and invocation.
//
// Telemetry default: `--kind toast-click` is applied when the caller hasn't
// already set it. `--source` follows the URI's hint (defaults to `toast`).
func (c *focusCommand) resolveURIOption(opts *focusOptions) error {
	raw := strings.TrimSpace(opts.URI)
	if raw == "" {
		return nil
	}
	parsed, err := parseFocusURI(raw)
	if err != nil {
		return err
	}
	// URI's socket wins over an explicit --socket (the URI carries the socket
	// from the producer side; an additional --socket flag would generally be a
	// misuse and we surface that by overriding).
	if strings.TrimSpace(parsed.Socket) != "" {
		opts.Socket = parsed.Socket
	}
	// URI's source wins over --source — the URI path is the canonical
	// telemetry source for this invocation.
	if strings.TrimSpace(parsed.Source) != "" {
		opts.Source = parsed.Source
	}
	if strings.TrimSpace(opts.Kind) == "" && opts.Source == focusURISourceDef {
		opts.Kind = "toast-click"
	}

	socket := c.resolveSocket(opts.Socket)
	target, err := c.translatePaneIDToTarget(context.Background(), socket, parsed.PaneID)
	if err != nil {
		return err
	}
	opts.Target = target
	return nil
}

// translatePaneIDToTarget resolves a tmux pane id (`%N`) to the
// `SESSION[:WINDOW.%N]` coordinate corefocus.Target expects. The pane id is
// preserved on both ends — we read only the session and window index/id —
// so that the resulting target still pins down the exact pane the toast
// referenced.
func (c *focusCommand) translatePaneIDToTarget(ctx context.Context, socket, paneID string) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", errors.New("focus: uri pane_id is empty")
	}
	format := "#S" + focusFieldSeparator + "#I"
	args := c.tmuxArgs(socket, "display-message", "-p", "-t", paneID, format)
	out, err := c.runner.Run(ctx, "tmux", args...)
	if err != nil {
		return "", fmt.Errorf("focus: resolve uri pane %q via tmux: %w", paneID, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("focus: tmux returned no metadata for pane %q", paneID)
	}
	fields := strings.SplitN(line, focusFieldSeparator, 2)
	if len(fields) != 2 {
		// Be tolerant of users whose tmux drops our separator and falls
		// back to tab (rare but documented in the test fixtures).
		fields = strings.SplitN(line, "\t", 2)
	}
	if len(fields) != 2 {
		return "", fmt.Errorf("focus: parse pane metadata %q", line)
	}
	session := strings.TrimSpace(fields[0])
	window := strings.TrimSpace(fields[1])
	if session == "" {
		return "", fmt.Errorf("focus: pane %q has no session", paneID)
	}
	if window == "" {
		return session + ":" + paneID, nil
	}
	return session + ":" + window + "." + paneID, nil
}

func (c *focusCommand) resolveSocket(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	tmuxEnv := strings.TrimSpace(c.lookupEnv("TMUX"))
	if tmuxEnv == "" {
		return ""
	}
	// $TMUX has the form "<socket>,<pid>,<session-id>"; we need only the path.
	first, _, _ := strings.Cut(tmuxEnv, ",")
	return strings.TrimSpace(first)
}

func (c *focusCommand) tmuxArgs(socket string, args ...string) []string {
	if socket == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "-S", socket)
	return append(out, args...)
}

func (c *focusCommand) listSessionInventory(ctx context.Context, socket string) ([]corefocus.Candidate, error) {
	format := strings.Join([]string{"#{session_activity}", "#{session_name}", "#{session_attached}"}, focusFieldSeparator)
	args := c.tmuxArgs(socket, "list-sessions", "-F", format)
	out, err := c.runner.Run(ctx, "tmux", args...)
	if err != nil {
		if isNoServerLikeError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("focus: list-sessions: %w", err)
	}
	return parseFocusInventory(out)
}

func (c *focusCommand) listClients(ctx context.Context, socket string) ([]focusClient, error) {
	format := strings.Join([]string{"#{client_name}", "#{client_session}"}, focusFieldSeparator)
	args := c.tmuxArgs(socket, "list-clients", "-F", format)
	out, err := c.runner.Run(ctx, "tmux", args...)
	if err != nil {
		if isNoServerLikeError(err) {
			return nil, nil
		}
		// list-clients exits non-zero when there is no server. Treat any error
		// not matching the no-server signature as fatal so the caller knows
		// the focus failed for a non-fallback reason.
		return nil, fmt.Errorf("focus: list-clients: %w", err)
	}
	return parseFocusClients(out), nil
}

func (c *focusCommand) switchClient(ctx context.Context, socket, clientName, sessionName string) error {
	if c.runner == nil {
		return errors.New("focus runner is not configured")
	}
	if c.diagnostics != nil {
		c.diagnostics.Mark(diagnostics.OperationSessionSwitch)
	}
	if err := intmux.NewRunner(c.runner).SwitchClient(ctx, intmux.SwitchClientOptions{
		Socket: socket,
		Client: clientName,
		Target: sessionName,
	}); err != nil {
		if c.diagnostics != nil {
			c.diagnostics.SealFailure(diagnostics.OperationSessionSwitch)
		}
		return fmt.Errorf("focus: switch-client to %q: %w", sessionName, err)
	}
	if c.diagnostics != nil {
		c.diagnostics.SealSuccess()
	}
	return nil
}

func (c *focusCommand) selectWindow(ctx context.Context, socket, target string) error {
	if c.runner == nil {
		return errors.New("focus runner is not configured")
	}
	if err := intmux.NewRunner(c.runner).SelectWindow(ctx, intmux.SelectWindowOptions{
		Socket: socket,
		Target: target,
	}); err != nil {
		return fmt.Errorf("focus: select-window %q: %w", target, err)
	}
	return nil
}

func (c *focusCommand) selectPane(ctx context.Context, socket, target string) error {
	if c.runner == nil {
		return errors.New("focus runner is not configured")
	}
	if err := intmux.NewRunner(c.runner).SelectPane(ctx, intmux.SelectPaneOptions{
		Socket: socket,
		Target: target,
	}); err != nil {
		return fmt.Errorf("focus: select-pane %q: %w", target, err)
	}
	return nil
}

func (c *focusCommand) notifySessionReady(sessionName string) error {
	if c.notifierOnce == nil {
		return errors.New("focus notifier is not configured")
	}
	notifier := c.notifierOnce(c.stderr)
	if notifier == nil {
		return errors.New("focus notifier is not available")
	}
	return notifier.Notify(aiNotification{
		Summary:            "session ready: " + sessionName,
		Body:               "Click to switch to " + sessionName + ".",
		Urgency:            "normal",
		AppName:            "projmux",
		Tag:                "projmux.focus." + sessionName,
		Group:              "projmux.focus",
		diagnosticProvider: diagnostics.ProviderProjmux,
		diagnosticCategory: diagnostics.CategorySessionReady,
	})
}

func (c *focusCommand) logTelemetry(telemetry focusTelemetry) {
	if c.stderr == nil {
		return
	}
	if strings.TrimSpace(c.lookupEnv("PROJMUX_FOCUS_DEBUG")) == "" {
		return
	}
	fmt.Fprintf(c.stderr,
		"focus: target=%s session=%s window=%s pane=%s socket=%s client=%s source=%s kind=%s\n",
		telemetry.target.Raw, telemetry.target.Session, telemetry.target.WindowSelector(), telemetry.target.PaneSelector(),
		telemetry.socket, telemetry.opts.Client, telemetry.opts.Source, telemetry.opts.Kind,
	)
}

func writeFocusJSON(stdout io.Writer, res focusResult) error {
	payload, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("focus: marshal result: %w", err)
	}
	if _, err := stdout.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func combineFallback(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "+")
}

type focusClient struct {
	Name    string
	Session string
}

// pickFocusClient prefers the explicit origin client, then a client already
// viewing the resolved session, then the lexicographically first client.
func pickFocusClient(clients []focusClient, sessionName, preferredClient string) string {
	if len(clients) == 0 {
		return ""
	}
	preferredClient = strings.TrimSpace(preferredClient)
	names := make([]focusClient, 0, len(clients))
	names = append(names, clients...)
	sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
	if preferredClient != "" {
		for _, c := range names {
			if c.Name == preferredClient {
				return c.Name
			}
		}
	}
	for _, c := range names {
		if c.Session == sessionName {
			return c.Name
		}
	}
	return names[0].Name
}

type focusUnresolvedError struct {
	session string
	socket  string
}

func (e *focusUnresolvedError) Error() string {
	if e.socket == "" {
		return fmt.Sprintf("focus: session %q not found and no fallback matched", e.session)
	}
	return fmt.Sprintf("focus: session %q not found on socket %q and no fallback matched", e.session, e.socket)
}

// focusExitError carries a desired process exit code up to main(). The CLI
// entrypoint maps this to os.Exit; tests inspect the wrapped error directly.
type focusExitError struct {
	code int
	err  error
}

func (e focusExitError) Error() string { return e.err.Error() }
func (e focusExitError) Unwrap() error { return e.err }
func (e focusExitError) ExitCode() int { return e.code }

type focusExecRunner struct{}

func (focusExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
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

func isNoServerLikeError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no server running on") ||
		strings.Contains(msg, "failed to connect to server") ||
		(strings.Contains(msg, "error connecting to ") && strings.Contains(msg, "(No such file or directory)"))
}

func parseFocusInventory(out []byte) ([]corefocus.Candidate, error) {
	type row struct {
		activity int64
		name     string
		attached bool
		order    int
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}

	lines := strings.Split(trimmed, "\n")
	rows := make([]row, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, focusFieldSeparator, 3)
		if len(fields) != 3 {
			fields = strings.SplitN(line, "\t", 3)
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("focus: parse list-sessions row %q", line)
		}
		activity, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			activity = 0
		}
		attachedCount, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			attachedCount = 0
		}
		name := strings.TrimSpace(fields[1])
		if name == "" {
			continue
		}
		rows = append(rows, row{activity: activity, name: name, attached: attachedCount > 0, order: i})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].activity == rows[j].activity {
			return rows[i].order < rows[j].order
		}
		return rows[i].activity > rows[j].activity
	})

	out2 := make([]corefocus.Candidate, 0, len(rows))
	for _, r := range rows {
		out2 = append(out2, corefocus.Candidate{Name: r.name, Attached: r.attached})
	}
	return out2, nil
}

func parseFocusClients(out []byte) []focusClient {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	clients := make([]focusClient, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, focusFieldSeparator, 2)
		if len(fields) != 2 {
			fields = strings.SplitN(line, "\t", 2)
		}
		name := strings.TrimSpace(fields[0])
		session := ""
		if len(fields) == 2 {
			session = strings.TrimSpace(fields[1])
		}
		if name == "" {
			continue
		}
		clients = append(clients, focusClient{Name: name, Session: session})
	}
	return clients
}
