package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// stubRawArgv stands in for a leaf handler so the guard can be exercised
// without the command graph. It writes what it is told to write and returns
// what it is told to return -- which is the whole question the guard answers.
type stubRawArgv struct {
	stdout string
	stderr string
	err    error
	argv   [][]string
}

func (s *stubRawArgv) Run(args []string, stdout, stderr io.Writer) error {
	s.argv = append(s.argv, append([]string(nil), args...))
	if s.stdout != "" {
		_, _ = io.WriteString(stdout, s.stdout)
	}
	if s.stderr != "" {
		_, _ = io.WriteString(stderr, s.stderr)
	}
	return s.err
}

func guardedTestApp(t *testing.T, leaf *stubRawArgv, runner tmuxRunner) *App {
	t.Helper()
	return &App{
		internal:          &internalCommand{tmux: leaf, statusbar: leaf, ai: leaf},
		switcher:          nil,
		interactiveRunner: runner,
		lookupEnv:         func(string) string { return "" },
	}
}

func TestInteractiveRunShellGuardCoversEveryLedgeredRouteAndNothingElse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		argv    []string
		env     string
		want    string
		guarded bool
		client  string
	}{
		{name: "pane menu", argv: []string{"internal", "tmux", "pane-menu", "--client", "/dev/pts/3", "split-right", "%4"},
			guarded: true, want: interactiveRoutePaneMenu, client: "/dev/pts/3"},
		{name: "window create", argv: []string{"internal", "tmux", "window-create", "--client", "/dev/pts/3", "--anchor", "%4"},
			guarded: true, want: interactiveRouteWindowCreate, client: "/dev/pts/3"},
		{name: "window rename", argv: []string{"internal", "tmux", "window-rename", "--client=/dev/pts/5", "--anchor", "%4", "--", "notes"},
			guarded: true, want: interactiveRouteWindowRename, client: "/dev/pts/5"},
		{name: "popup toggle", argv: []string{"internal", "tmux", "popup-toggle", "--client", "/dev/pts/3", "--anchor", "%4", "sessionizer"},
			guarded: true, want: interactiveRoutePopupToggle, client: "/dev/pts/3"},
		{name: "direct split", argv: []string{"internal", "agent-pane", "launch-default", "right"}, env: "/dev/pts/9",
			guarded: true, want: interactiveRouteAgentPaneLaunch, client: "/dev/pts/9"},
		{name: "status click", argv: []string{"internal", "statusbar", "click", "notify", "--client", "/dev/pts/3"},
			guarded: true, want: interactiveRouteStatusbarClick, client: "/dev/pts/3"},
		{name: "usage refresh", argv: []string{"internal", "statusbar", "usage-refresh", "--client", "/dev/pts/3"},
			guarded: true, want: interactiveRouteStatusbarUsageRefresh, client: "/dev/pts/3"},
		{name: "project open from a binding", argv: []string{"switch", "open", "/repo"}, env: "/dev/pts/7",
			guarded: true, want: interactiveRouteProjectOpen, client: "/dev/pts/7"},

		// With no client to name there is nothing to converge onto, so the
		// invocation keeps its writers and its exit code.
		{name: "project open at a prompt", argv: []string{"switch", "open", "/repo"}},
		{name: "direct split with no origin client", argv: []string{"internal", "agent-pane", "launch-default", "down"}},
		{name: "pane menu with an empty client", argv: []string{"internal", "tmux", "pane-menu", "--client", "", "kill", "%4"}},
		{name: "public create", argv: []string{"create", "pane", "--placement", "right"}, env: "/dev/pts/7"},
		{name: "public rename", argv: []string{"rename", "window", "uid:win-1", "notes"}, env: "/dev/pts/7"},
		{name: "public describe", argv: []string{"describe", "window", "uid:win-1"}, env: "/dev/pts/7"},

		// Payload routes write the surface they are asked to render.
		{name: "popup payload", argv: []string{"internal", "preview", "session"}, env: "/dev/pts/7"},
		{name: "status segment", argv: []string{"internal", "status", "notify"}, env: "/dev/pts/7"},
		{name: "picker", argv: []string{"internal", "agent-pane", "picker", "right"}, env: "/dev/pts/7"},
		{name: "supervisor", argv: []string{"internal", "supervise", "--", "zsh"}, env: "/dev/pts/7"},

		// Help is a human at a prompt, and its answer belongs on stdout.
		{name: "help flag", argv: []string{"internal", "tmux", "pane-menu", "--help"}, env: "/dev/pts/7"},
		{name: "help subcommand", argv: []string{"internal", "statusbar", "help"}, env: "/dev/pts/7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			route, client, ok := matchInteractiveRunShellRoute(test.argv, func(string) string { return test.env })
			if ok != test.guarded {
				t.Fatalf("guarded = %t, want %t (route %q)", ok, test.guarded, route.ID)
			}
			if !test.guarded {
				return
			}
			if route.ID != test.want {
				t.Fatalf("route = %q, want %q", route.ID, test.want)
			}
			if client != test.client {
				t.Fatalf("client = %q, want %q", client, test.client)
			}
		})
	}
}

func TestInteractiveRunShellGuardKeepsSuccessSilent(t *testing.T) {
	t.Parallel()

	// A route that "succeeds loudly" is the case that matters: the projection a
	// canonical action prints, or a diagnostic on the way out.
	leaf := &stubRawArgv{stdout: "created: pane/uid=pan-7\n", stderr: "note: reconciled 1 window\n"}
	runner := &recordingTmuxRunner{}
	app := guardedTestApp(t, leaf, runner)

	var stdout, stderr bytes.Buffer
	if err := app.Run([]string{"internal", "tmux", "pane-menu", "--client", "/dev/pts/3", "split-right", "%4"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("guarded success reached tmux: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("guarded success sent %#v, want no message from the guard itself", runner.calls)
	}
	if len(leaf.argv) != 1 {
		t.Fatalf("route ran %d times, want 1", len(leaf.argv))
	}
}

func TestInteractiveRunShellGuardConvergesFailureOnTheExactClient(t *testing.T) {
	t.Parallel()

	leaf := &stubRawArgv{
		stderr: "anchor %4 left the managed enclosure\n",
		err:    errors.New("create pane refused"),
	}
	runner := &recordingTmuxRunner{}
	app := guardedTestApp(t, leaf, runner)

	var stdout, stderr bytes.Buffer
	// The exit code is the second overlay: tmux appends "returned N" to a
	// foreground job's output and paints that too.
	if err := app.Run([]string{"internal", "tmux", "pane-menu", "--client", "/dev/pts/3", "split-right", "%4"}, &stdout, &stderr); err != nil {
		t.Fatalf("converged failure escaped as an exit code: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("guarded failure reached tmux: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	want := recordedTmuxCall{name: "tmux", args: []string{
		"display-message", "-c", "/dev/pts/3", "-d", "10000",
		"projmux pane menu action failed: create pane refused: anchor %4 left the managed enclosure",
	}}
	if !reflect.DeepEqual(runner.calls, []recordedTmuxCall{want}) {
		t.Fatalf("tmux calls = %#v, want one bounded exact-client message %#v", runner.calls, want)
	}
}

func TestInteractiveRunShellGuardKeepsAnUndeliverableFailureVisible(t *testing.T) {
	t.Parallel()

	// With no client left to converge onto, exiting zero would claim the action
	// succeeded. The original error is the honest answer.
	refusal := errors.New("popup refused")
	runner := &recordingTmuxRunner{err: errors.New("no such client")}
	app := guardedTestApp(t, &stubRawArgv{err: refusal}, runner)

	err := app.Run([]string{"internal", "tmux", "popup-toggle", "--client", "/dev/pts/gone", "sessionizer"}, io.Discard, io.Discard)
	if !errors.Is(err, refusal) {
		t.Fatalf("Run() error = %v, want the original refusal", err)
	}
}

func TestInteractiveRunShellGuardLeavesUnguardedRoutesByteIdentical(t *testing.T) {
	t.Parallel()

	// The public projection is the scripting contract this track promised not
	// to touch.
	const projection = "created: window/uid=win-3 name=notes\n"
	leaf := &stubRawArgv{stdout: projection, stderr: "warn\n", err: errors.New("boom")}
	app := &App{create: nil, internal: &internalCommand{preview: leaf}, interactiveRunner: &recordingTmuxRunner{}, lookupEnv: func(string) string { return "/dev/pts/7" }}

	var stdout, stderr bytes.Buffer
	err := app.Run([]string{"internal", "preview", "session"}, &stdout, &stderr)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Run() error = %v, want the leaf error", err)
	}
	if stdout.String() != projection || stderr.String() != "warn\n" {
		t.Fatalf("unguarded route lost its writers: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestInteractiveRunShellMessageStaysOneBoundedLine(t *testing.T) {
	t.Parallel()

	route := interactiveRunShellRoute{ID: interactiveRoutePaneMenu, Label: "pane menu action"}
	message := interactiveRunShellFailureMessage(route, errors.New(strings.Repeat("refusal ", 80)), "detail\nwith\nlines")
	if strings.ContainsAny(message, "\n\r\t") {
		t.Fatalf("message spans more than one line: %q", message)
	}
	if got := len([]rune(message)); got > interactiveRunShellMessageLimit {
		t.Fatalf("message is %d runes, want at most %d", got, interactiveRunShellMessageLimit)
	}
	if !strings.HasPrefix(message, "projmux pane menu action failed: ") {
		t.Fatalf("message = %q, want the action named first", message)
	}
}

// TestWindowIntentsReportOneBoundedLineToTheExactClient pins the copy and the
// silence for the two producers the track was opened for.
func TestWindowIntentsReportOneBoundedLineToTheExactClient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "create",
			argv: []string{"window-create", "--client", "/dev/pts/2", "--anchor", "%9"},
			want: windowCreatedMessage,
		},
		{
			name: "rename",
			argv: []string{"window-rename", "--client", "/dev/pts/2", "--anchor", "%9", "--", "notes"},
			want: windowRenamedMessage + "notes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingTmuxRunner{}
			cmd := &tmuxCommand{
				runner: runner,
				windowCreate: func(_ windowCreateIntent, stdout, _ io.Writer) error {
					_, _ = io.WriteString(stdout, "created: window/uid=win-3 name=zsh\n")
					return nil
				},
				windowRename: func(intent windowRenameIntent, stdout, _ io.Writer) error {
					_, _ = fmt.Fprintf(stdout, "renamed: window/uid=win-3 -> %s\n", intent.displayName)
					return nil
				},
			}
			var stdout, stderr bytes.Buffer
			if err := cmd.Run(test.argv, &stdout, &stderr); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("Window intent wrote to the foreground job: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			want := recordedTmuxCall{name: "tmux", args: []string{
				"display-message", "-c", "/dev/pts/2", "-d", "10000", test.want,
			}}
			if !reflect.DeepEqual(runner.calls, []recordedTmuxCall{want}) {
				t.Fatalf("tmux calls = %#v, want %#v", runner.calls, want)
			}
		})
	}
}

// TestWindowIntentFailureKeepsTheMutationStoryStraight is the negative leg: a
// refusal is delivered as a refusal, and the message never invents a rollback
// the canonical route did not perform.
func TestWindowIntentFailureKeepsTheMutationStoryStraight(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{
		runner: runner,
		windowRename: func(_ windowRenameIntent, _, stderr io.Writer) error {
			_, _ = io.WriteString(stderr, "exact anchor drifted")
			return errors.New("rename window refused")
		},
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"window-rename", "--client", "/dev/pts/2", "--anchor", "%9", "--", "notes"}, &stdout, &stderr); err != nil {
		t.Fatalf("displayed refusal escaped as an exit code: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("Window intent wrote to the foreground job: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(runner.calls) != 1 {
		t.Fatalf("tmux calls = %#v, want one message", runner.calls)
	}
	message := runner.calls[0].args[len(runner.calls[0].args)-1]
	for _, want := range []string{"Rename Window failed", "rename window refused", "exact anchor drifted"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to contain %q", message, want)
		}
	}
	for _, banned := range []string{"rolled back", "reverted", windowRenamedMessage} {
		if strings.Contains(message, banned) {
			t.Fatalf("message = %q, must not claim %q", message, banned)
		}
	}
}
