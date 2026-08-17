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
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
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

func TestFocus_PrefersOriginClientEvenWhenTargetSessionElsewhere(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n50\tother\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n/dev/pts/9\tother\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0", "--client", "/dev/pts/9", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v (raw=%q)", err, stdout.String())
	}
	if res.Client != "/dev/pts/9" || res.OriginClient != "/dev/pts/9" {
		t.Fatalf("result = %#v, want origin client selected", res)
	}
	if !sawTmuxArgPair(runner.calls, "-c", "/dev/pts/9") {
		t.Fatalf("calls = %#v, want switch-client -c /dev/pts/9", runner.calls)
	}
}

func TestFocus_MissingOriginClientFallsBackToTargetSessionClient(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n50\tother\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tother\n/dev/pts/7\tworkspace\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0", "--client", "/dev/pts/missing", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v (raw=%q)", err, stdout.String())
	}
	if res.Client != "/dev/pts/7" || res.OriginClient != "/dev/pts/missing" {
		t.Fatalf("result = %#v, want target-session fallback while preserving origin", res)
	}
}

func TestFocus_NoOriginOrTargetClientFallsBackToStableFirstClient(t *testing.T) {
	t.Parallel()

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n50\tother\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/9\tother\n/dev/pts/3\tother\n"), nil
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
	if res.Client != "/dev/pts/3" || res.OriginClient != "" {
		t.Fatalf("result = %#v, want stable first fallback without origin", res)
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
	err := app.Run([]string{"internal", "focus", "--target", ""}, stdout, stderr)
	if err == nil {
		t.Fatalf("expected dispatcher to surface focus parse error, got nil")
	}
}

func TestFocus_URIResolvesToSwitchClient(t *testing.T) {
	t.Parallel()

	// Build a URI as the Toast XML would carry it. Round-tripping through
	// buildFocusURI guarantees this test stays aligned with the encoder.
	uri := buildFocusURI("%8", "/tmp/projmux.sock")

	var displayArgs []string
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "display-message"):
				// Record so we can assert the pane-id query was sent against
				// the URI's socket.
				displayArgs = append([]string(nil), args...)
				return []byte("workspace" + focusFieldSeparator + "1\n"), nil
			case containsArg(args, "list-sessions"):
				return []byte("100\tworkspace\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tworkspace\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--uri", uri, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
	}

	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v (raw=%q)", err, stdout.String())
	}
	if !res.OK {
		t.Fatalf("result = %#v, want ok=true", res)
	}
	if res.Target != "workspace:1.%8" {
		t.Fatalf("Target = %q, want workspace:1.%%8", res.Target)
	}
	if res.Socket != "/tmp/projmux.sock" {
		t.Fatalf("Socket = %q, want /tmp/projmux.sock (uri override)", res.Socket)
	}
	for _, sub := range []string{"display-message", "list-sessions", "list-clients", "switch-client", "select-window", "select-pane"} {
		if !sawSubcommand(runner.calls, sub) {
			t.Errorf("expected tmux call with %q, got %#v", sub, runner.calls)
		}
	}
	// display-message must be addressed at the pane id and run against the
	// uri's socket (-S /tmp/projmux.sock display-message -p -t %8 #S__SEP__#I).
	wantInDisplay := []string{"-S", "/tmp/projmux.sock", "display-message", "-p", "-t", "%8"}
	for _, w := range wantInDisplay {
		if !slices.Contains(displayArgs, w) {
			t.Fatalf("display-message args missing %q: %v", w, displayArgs)
		}
	}
}

func TestFocus_URITakesPrecedenceOverSocketFlag(t *testing.T) {
	t.Parallel()

	uri := buildFocusURI("%4", "/from/uri.sock")
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "display-message"):
				return []byte("ws" + focusFieldSeparator + "0\n"), nil
			case containsArg(args, "list-sessions"):
				return []byte("100\tws\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tws\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--uri", uri, "--socket", "/from/flag.sock", "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, c := range runner.calls {
		if slices.Contains(c.args, "/from/flag.sock") {
			t.Fatalf("URI socket should override --socket flag, but saw call with /from/flag.sock: %#v", c.args)
		}
	}
}

func TestFocus_URIRejectsCombinedWithTarget(t *testing.T) {
	t.Parallel()

	cmd := newFocusTestCommand(&focusFakeRunner{}, nil, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"--uri", "projmux://focus?pane_id=%251", "--target", "ws"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected --uri + --target combination to error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutually-exclusive message", err)
	}
}

func TestFocus_URIRejectsMalformedURI(t *testing.T) {
	t.Parallel()

	cmd := newFocusTestCommand(&focusFakeRunner{}, nil, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"--uri", "http://focus?pane_id=%251"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected wrong-scheme URI to error")
	}
}

func TestFocus_URISetsToastClickTelemetryKind(t *testing.T) {
	t.Parallel()

	uri := buildFocusURI("%2", "/sock")
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "display-message"):
				return []byte("ws" + focusFieldSeparator + "0\n"), nil
			case containsArg(args, "list-sessions"):
				return []byte("100\tws\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tws\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, map[string]string{"PROJMUX_FOCUS_DEBUG": "1"}, nil)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--uri", uri, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// Telemetry line is human-readable and contains `source=` + `kind=`
	// fields; uri-mode should surface source=toast kind=toast-click so
	// downstream debugging can distinguish click-driven focus.
	if !strings.Contains(stderr.String(), "source=toast") {
		t.Fatalf("stderr = %q, want source=toast", stderr.String())
	}
	if !strings.Contains(stderr.String(), "kind=toast-click") {
		t.Fatalf("stderr = %q, want kind=toast-click", stderr.String())
	}
}

func TestFocusURIToastClickAcksLatestAIQueueEntryAfterFocus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "selected-critical",
			Severity:  notify.SeverityCritical,
			Source:    notify.SourceAI,
			Session:   "ws",
			Window:    "0",
			Pane:      "%2",
			Socket:    "/sock",
			CreatedAt: now,
		},
		{
			ID:        "older-info",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "ws",
			Window:    "0",
			Pane:      "%2",
			Socket:    "/sock",
			CreatedAt: now.Add(-time.Minute),
		},
	}}
	uri := buildFocusURI("%2", "/sock")
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "display-message"):
				return []byte("ws" + focusFieldSeparator + "0\n"), nil
			case containsArg(args, "list-sessions"):
				return []byte("100\tws\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tws\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)
	cmd.notifyStoreFn = func() (notifyStore, error) { return store, nil }

	if err := cmd.Run([]string{"--uri", uri, "--json"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"selected-critical", "older-info"}
	if !slices.Equal(store.ackedIDs, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", store.ackedIDs, want)
	}
}

func TestFocus_URITargetWindowAtIndexZero(t *testing.T) {
	t.Parallel()

	// Pane that belongs to window index 0 should still produce a
	// session:0.%paneID target rather than collapsing to session:%paneID.
	uri := buildFocusURI("%99", "/sock")
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "display-message"):
				return []byte("only-session" + focusFieldSeparator + "0\n"), nil
			case containsArg(args, "list-sessions"):
				return []byte("100\tonly-session\t1\n"), nil
			case containsArg(args, "list-clients"):
				return []byte("/dev/pts/0\tonly-session\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newFocusTestCommand(runner, nil, nil)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--uri", uri, "--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	var res focusResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if res.Target != "only-session:0.%99" {
		t.Fatalf("Target = %q, want only-session:0.%%99", res.Target)
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

func sawTmuxArgPair(calls []focusFakeCall, key, value string) bool {
	for _, c := range calls {
		for i := 0; i+1 < len(c.args); i++ {
			if c.args[i] == key && c.args[i+1] == value {
				return true
			}
		}
	}
	return false
}

var _ focusCommandRunner = (*focusFakeRunner)(nil)
var _ focusNotifier = (*focusFakeNotifier)(nil)
