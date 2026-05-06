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
	"sort"
	"strconv"
	"strings"

	corefocus "github.com/crevissepartners/projmux/internal/core/focus"
)

// focusExitNotResolved is the exit code emitted when no session matches the
// requested target and no reasonable fallback is available. It is documented
// in the spec so callers (status bar, notification dispatch) can branch on it.
const focusExitNotResolved = 2

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
	runner       focusCommandRunner
	lookupEnv    func(string) string
	stdout       io.Writer
	stderr       io.Writer
	notifierOnce func(stderr io.Writer) focusNotifier
}

type focusOptions struct {
	Target string
	Socket string
	Source string
	Kind   string
	JSON   bool
}

type focusResult struct {
	OK       bool   `json:"ok"`
	Fallback string `json:"fallback,omitempty"`
	Target   string `json:"target,omitempty"`
	Socket   string `json:"socket,omitempty"`
	Note     string `json:"note,omitempty"`
}

func newFocusCommand() *focusCommand {
	return &focusCommand{
		runner:    focusExecRunner{},
		lookupEnv: os.Getenv,
		notifierOnce: func(stderr io.Writer) focusNotifier {
			// Reuse the existing notifier chain (WSL toast, notify-send, hook).
			ai := newAICommand()
			return ai.notificationNotifier()
		},
	}
}

// Run is the dispatcher entry point.
func (c *focusCommand) Run(args []string, stdout, stderr io.Writer) error {
	c.stdout = stdout
	c.stderr = stderr

	opts, err := parseFocusArgs(args, stderr)
	if err != nil {
		return err
	}

	target, err := corefocus.Parse(opts.Target)
	if err != nil {
		return err
	}

	socket := c.resolveSocket(opts.Socket)
	c.logTelemetry(opts, target, socket)

	res, err := c.execute(context.Background(), target, socket)
	res.Target = target.Raw
	res.Socket = socket

	if opts.JSON {
		if writeErr := writeFocusJSON(stdout, res); writeErr != nil {
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
		if errors.As(err, &notResolved) {
			return focusExitError{code: focusExitNotResolved, err: err}
		}
		return err
	}
	return nil
}

func parseFocusArgs(args []string, stderr io.Writer) (focusOptions, error) {
	fs := flag.NewFlagSet("focus", flag.ContinueOnError)
	fs.SetOutput(stderr)

	opts := focusOptions{}
	fs.StringVar(&opts.Target, "target", "", "Focus target SESSION[:WINDOW[.PANE]] (required)")
	fs.StringVar(&opts.Socket, "socket", "", "tmux socket path (overrides $TMUX)")
	fs.StringVar(&opts.Source, "source", "", "Telemetry label: ai|status-bar|external|os-notification")
	fs.StringVar(&opts.Kind, "kind", "", "Telemetry label: reply-ready|busy-cleared|segment-click|custom")
	fs.BoolVar(&opts.JSON, "json", false, "Emit a single-line JSON result")

	if err := fs.Parse(args); err != nil {
		return focusOptions{}, err
	}
	if fs.NArg() != 0 {
		return focusOptions{}, fmt.Errorf("focus does not accept positional arguments")
	}
	if strings.TrimSpace(opts.Target) == "" {
		return focusOptions{}, fmt.Errorf("--target is required")
	}
	return opts, nil
}

func (c *focusCommand) execute(ctx context.Context, target corefocus.Target, socket string) (focusResult, error) {
	inventory, err := c.listSessionInventory(ctx, socket)
	if err != nil {
		return focusResult{}, err
	}

	resolution, ok := corefocus.Resolve(target.Session, inventory)
	if !ok {
		return focusResult{},
			&focusUnresolvedError{session: target.Session, socket: socket}
	}

	clients, err := c.listClients(ctx, socket)
	if err != nil {
		return focusResult{}, err
	}

	// Decide whether any client on this socket can be redirected. If the
	// resolved session is itself attached we still use switch-client on a
	// client already viewing that session; otherwise pick any client and
	// redirect it. We do not force-detach other clients — multi-client
	// users may legitimately keep parallel views; a future config flag can
	// promote this to a "force focus" behavior.
	if len(clients) == 0 {
		if err := c.notifySessionReady(resolution.Name); err != nil {
			fmt.Fprintf(c.stderr, "focus: notify failed: %v\n", err)
		}
		return focusResult{
			OK:       true,
			Fallback: combineFallback(resolution.Fallback, "notify-only"),
			Note:     "no tmux client attached on socket; notification emitted",
		}, nil
	}

	clientName := pickFocusClient(clients, resolution.Name)
	if err := c.switchClient(ctx, socket, clientName, resolution.Name); err != nil {
		return focusResult{}, err
	}

	if target.HasWindow() {
		windowTarget := fmt.Sprintf("%s:%s", resolution.Name, target.WindowSelector())
		if err := c.selectWindow(ctx, socket, windowTarget); err != nil {
			// Window selection failure is recoverable when only an index was
			// provided (split layout could have changed). With an explicit
			// id we surface the error.
			if target.WindowID != "" {
				return focusResult{}, err
			}
			fmt.Fprintf(c.stderr, "focus: select-window %s failed: %v (stopping at session level)\n", windowTarget, err)
			return focusResult{
				OK:       true,
				Fallback: combineFallback(resolution.Fallback, "session-only"),
			}, nil
		}

		if target.HasPane() {
			paneTarget := fmt.Sprintf("%s.%s", windowTarget, target.PaneSelector())
			if err := c.selectPane(ctx, socket, paneTarget); err != nil {
				if target.PaneID != "" {
					return focusResult{}, err
				}
				fmt.Fprintf(c.stderr, "focus: select-pane %s failed: %v (stopping at window level)\n", paneTarget, err)
				return focusResult{
					OK:       true,
					Fallback: combineFallback(resolution.Fallback, "window-only"),
				}, nil
			}
		}
	}

	return focusResult{
		OK:       true,
		Fallback: resolution.Fallback,
	}, nil
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
	args := c.tmuxArgs(socket, "list-sessions", "-F", "#{session_activity}\t#{session_name}\t#{session_attached}")
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
	args := c.tmuxArgs(socket, "list-clients", "-F", "#{client_name}\t#{client_session}")
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
	tail := []string{"switch-client"}
	if clientName != "" {
		tail = append(tail, "-c", clientName)
	}
	tail = append(tail, "-t", sessionName)
	if _, err := c.runner.Run(ctx, "tmux", c.tmuxArgs(socket, tail...)...); err != nil {
		return fmt.Errorf("focus: switch-client to %q: %w", sessionName, err)
	}
	return nil
}

func (c *focusCommand) selectWindow(ctx context.Context, socket, target string) error {
	if _, err := c.runner.Run(ctx, "tmux", c.tmuxArgs(socket, "select-window", "-t", target)...); err != nil {
		return fmt.Errorf("focus: select-window %q: %w", target, err)
	}
	return nil
}

func (c *focusCommand) selectPane(ctx context.Context, socket, target string) error {
	if _, err := c.runner.Run(ctx, "tmux", c.tmuxArgs(socket, "select-pane", "-t", target)...); err != nil {
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
		Summary: "session ready: " + sessionName,
		Body:    "Click to switch to " + sessionName + ".",
		Urgency: "normal",
		AppName: "projmux",
		Tag:     "projmux.focus." + sessionName,
		Group:   "projmux.focus",
	})
}

func (c *focusCommand) logTelemetry(opts focusOptions, target corefocus.Target, socket string) {
	if c.stderr == nil {
		return
	}
	if strings.TrimSpace(c.lookupEnv("PROJMUX_FOCUS_DEBUG")) == "" {
		return
	}
	fmt.Fprintf(c.stderr,
		"focus: target=%s session=%s window=%s pane=%s socket=%s source=%s kind=%s\n",
		target.Raw, target.Session, target.WindowSelector(), target.PaneSelector(),
		socket, opts.Source, opts.Kind,
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

// pickFocusClient prefers a client already viewing the resolved session so
// the focus is immediate and visible; otherwise it picks the lexicographically
// first client to give a stable choice across runs.
func pickFocusClient(clients []focusClient, sessionName string) string {
	if len(clients) == 0 {
		return ""
	}
	names := make([]focusClient, 0, len(clients))
	names = append(names, clients...)
	sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
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
		fields := strings.SplitN(line, "\t", 3)
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
		fields := strings.SplitN(line, "\t", 2)
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
