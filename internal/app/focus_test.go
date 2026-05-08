package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

type focusFakeCall struct {
	name string
	args []string
}

type focusFakeRunner struct {
	calls   []focusFakeCall
	respond func(args []string) ([]byte, error)
}

func (f *focusFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, focusFakeCall{name: name, args: append([]string(nil), args...)})
	if f.respond == nil {
		return nil, nil
	}
	return f.respond(args)
}

type focusFakeNotifier struct {
	notifications []aiNotification
	err           error
}

func (n *focusFakeNotifier) Notify(notification aiNotification) error {
	n.notifications = append(n.notifications, notification)
	return n.err
}

func newFocusTestCommand(runner *focusFakeRunner, env map[string]string, notifier *focusFakeNotifier) *focusCommand {
	c := &focusCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			if env == nil {
				return ""
			}
			return env[name]
		},
	}
	if notifier != nil {
		c.notifierOnce = func(_ io.Writer) focusNotifier { return notifier }
	}
	return c
}

func TestFocus_SwitchesAttachedSession(t *testing.T) {
	t.Parallel()

	listSessions := []byte("100\tworkspace\t1\n")
	listClients := []byte("/dev/pts/0\tworkspace\n")

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return listSessions, nil
			case containsArg(args, "list-clients"):
				return listClients, nil
			}
			return nil, nil
		},
	}

	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v (raw=%q)", err, stdout.String())
	}
	if !res.OK || res.Fallback != "" {
		t.Fatalf("result = %#v, want ok=true fallback=\"\"", res)
	}

	for _, sub := range []string{"list-sessions", "list-clients", "switch-client", "select-window", "select-pane"} {
		if !sawSubcommand(runner.calls, sub) {
			t.Errorf("expected tmux call with %q, got %#v", sub, runner.calls)
		}
	}
}

func TestFocus_FallbackPrefixMatch(t *testing.T) {
	t.Parallel()

	listSessions := []byte("100\tfoo-main\t1\n50\tfoo-feat\t0\n")
	listClients := []byte("/dev/pts/0\tfoo-main\n")

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return listSessions, nil
			case containsArg(args, "list-clients"):
				return listClients, nil
			}
			return nil, nil
		},
	}

	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "foo-feat-baz", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !res.OK || res.Fallback != "prefix-match" {
		t.Fatalf("result = %#v, want ok=true fallback=prefix-match", res)
	}
	if res.ResolvedSession != "foo-feat" || res.SessionState != "fallback" {
		t.Fatalf("result = %#v, want resolved session fallback detail", res)
	}
}

func TestFocus_NoClientNotifyOnly(t *testing.T) {
	t.Parallel()

	listSessions := []byte("100\tworkspace\t0\n")

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return listSessions, nil
			case containsArg(args, "list-clients"):
				return []byte(""), nil
			}
			return nil, nil
		},
	}
	notifier := &focusFakeNotifier{}
	cmd := newFocusTestCommand(runner, nil, notifier)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !res.OK || res.Fallback != "notify-only" {
		t.Fatalf("result = %#v, want notify-only fallback", res)
	}
	if len(notifier.notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.notifications))
	}
	if !strings.Contains(notifier.notifications[0].Summary, "session ready") {
		t.Fatalf("notification summary = %q, want session ready prefix", notifier.notifications[0].Summary)
	}
	if sawSubcommand(runner.calls, "switch-client") {
		t.Fatalf("did not expect switch-client when no clients attached, calls=%#v", runner.calls)
	}
}

func TestFocus_UnresolvedSessionExitCode(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tunrelated\t0\n"), nil
			case containsArg(args, "list-clients"):
				return []byte(""), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"--target", "needle:1", "--json"}, stdout, stderr)
	if err == nil {
		t.Fatalf("expected error for unresolved session, got nil")
	}
	var coded focusExitError
	if !errors.As(err, &coded) {
		t.Fatalf("error %v does not unwrap to focusExitError", err)
	}
	if coded.ExitCode() != focusExitNotResolved {
		t.Fatalf("exit code = %d, want %d", coded.ExitCode(), focusExitNotResolved)
	}
	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if res.OK {
		t.Fatalf("result = %#v, want ok=false", res)
	}
	if res.Reason != "session-unresolved" || res.SessionState != "unresolved" {
		t.Fatalf("result = %#v, want unresolved session diagnostics", res)
	}
	if !strings.Contains(res.Note, "session \"needle\" not found") {
		t.Fatalf("result note = %q, want unresolved target explanation", res.Note)
	}
}

func TestFocus_WindowIndexFailureFallsBackToSession(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n"), nil
			case containsArg(args, "select-window"):
				return nil, errors.New("can't find window: 9")
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:9", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !res.OK || res.Fallback != "session-only" || res.Reason != "window-index-unresolved" {
		t.Fatalf("result = %#v, want session-only window-index fallback", res)
	}
	if res.WindowState != "index-fallback-session" {
		t.Fatalf("WindowState = %q, want index-fallback-session", res.WindowState)
	}
	if sawSubcommand(runner.calls, "select-pane") {
		t.Fatalf("did not expect pane selection after window fallback, calls=%#v", runner.calls)
	}
}

func TestFocus_WindowIDFailureIsHardDiagnostic(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n"), nil
			case containsArg(args, "select-window"):
				return nil, errors.New("can't find window: @99")
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"--target", "workspace:@99", "--json"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected hard error for unresolved explicit window id")
	}
	var coded focusExitError
	if errors.As(err, &coded) {
		t.Fatalf("explicit window id failure must not use unresolved-target exit code: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if res.OK || res.Reason != "window-id-unresolved" || res.WindowState != "id-unresolved" {
		t.Fatalf("result = %#v, want hard window id diagnostics", res)
	}
}

func TestFocus_PaneIndexFailureFallsBackToWindow(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n"), nil
			case containsArg(args, "select-pane"):
				return nil, errors.New("can't find pane: 9")
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.9", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !res.OK || res.Fallback != "window-only" || res.Reason != "pane-index-unresolved" {
		t.Fatalf("result = %#v, want window-only pane-index fallback", res)
	}
	if res.WindowState != "selected" || res.PaneState != "index-fallback-window" {
		t.Fatalf("result = %#v, want selected window and pane fallback detail", res)
	}
}

func TestFocus_PaneIDFailureIsHardDiagnostic(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n"), nil
			case containsArg(args, "select-pane"):
				return nil, errors.New("can't find pane: %99")
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"--target", "workspace:1.%99", "--json"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected hard error for unresolved explicit pane id")
	}
	var coded focusExitError
	if errors.As(err, &coded) {
		t.Fatalf("explicit pane id failure must not use unresolved-target exit code: %v", err)
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if res.OK || res.Reason != "pane-id-unresolved" || res.PaneState != "id-unresolved" {
		t.Fatalf("result = %#v, want hard pane id diagnostics", res)
	}
}

func TestFocus_SocketArgPropagates(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\twork\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\twork\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "work", "--socket", "/tmp/projmux.sock", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, c := range runner.calls {
		if len(c.args) < 2 || c.args[0] != "-S" || c.args[1] != "/tmp/projmux.sock" {
			t.Fatalf("expected every tmux call to start with -S /tmp/projmux.sock, got %#v", c.args)
		}
	}
}

func TestFocus_SocketInferredFromTMUXEnv(t *testing.T) {
	t.Parallel()

	cmd := newFocusTestCommand(&focusFakeRunner{}, map[string]string{"TMUX": "/tmp/derived.sock,1234,1"}, nil)
	got := cmd.resolveSocket("")
	if got != "/tmp/derived.sock" {
		t.Fatalf("resolveSocket = %q, want /tmp/derived.sock", got)
	}
}

func TestFocus_RequiresTarget(t *testing.T) {
	t.Parallel()

	cmd := newFocusTestCommand(&focusFakeRunner{}, nil, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{}, stdout, stderr); err == nil {
		t.Fatalf("expected error when --target missing, got nil")
	}
}

func TestFocus_AppDispatcherRoutesFocus(t *testing.T) {
	t.Parallel()

	app := New()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	// Use a benign argument set that should fail target parsing fast (so the
	// command is reachable through the dispatcher without shelling out).
	err := app.Run([]string{"focus", "--target", ""}, stdout, stderr)
	if err == nil {
		t.Fatalf("expected dispatcher to surface focus parse error, got nil")
	}
}

func containsArg(args []string, needle string) bool {
	return slices.Contains(args, needle)
}

func sawSubcommand(calls []focusFakeCall, sub string) bool {
	for _, c := range calls {
		if slices.Contains(c.args, sub) {
			return true
		}
	}
	return false
}

var _ focusCommandRunner = (*focusFakeRunner)(nil)
var _ focusNotifier = (*focusFakeNotifier)(nil)
