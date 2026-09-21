package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

const focusUIDTestSocket = "/tmp/pmx-focus-uid-test.sock"

// focusUIDTestRegistry is the Registry the uid focus tests resolve against. It
// mirrors focusUIDTestInventory: Project proj-a owns the live session alpha,
// whose windows @1/@2 hold panes %1/%2 and %3.
func focusUIDTestRegistry() coremetadata.Registry {
	owned := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	project := func(uid, session string) coremetadata.Project {
		p := coremetadata.Project{Kind: coremetadata.KindProject, Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid}}
		if session != "" {
			p.Status.Session = &coremetadata.SessionProjection{Name: session, Live: true, SocketPath: focusUIDTestSocket}
		}
		return p
	}
	window := func(uid, owner, runtimeID string) coremetadata.Window {
		return coremetadata.Window{
			Kind:     coremetadata.KindWindow,
			Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid, OwnerRef: owned(coremetadata.KindProject, owner)},
			Status:   coremetadata.WindowStatus{RuntimeID: runtimeID},
		}
	}
	pane := func(uid string, owner *coremetadata.OwnerRef, runtimeID string) coremetadata.Pane {
		p := coremetadata.Pane{Kind: coremetadata.KindPane, Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid, OwnerRef: owner}}
		p.Status.Activation.RuntimeID = runtimeID
		return p
	}
	reg := coremetadata.NewRegistry()
	reg.Projects = []coremetadata.Project{
		project("proj-a", "alpha"),
		project("proj-off", "beta"),
		project("proj-nosess", ""),
	}
	reg.Windows = []coremetadata.Window{
		window("win-main", "proj-a", "@1"),
		window("win-review", "proj-a", "@2"),
		window("win-norun", "proj-a", ""),
		window("win-gone", "proj-a", "@9"),
		window("win-off", "proj-off", "@5"),
	}
	reg.Agents = []coremetadata.Agent{{
		Kind:     coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: "agent-a", Name: "agent-a", OwnerRef: owned(coremetadata.KindWindow, "win-main")},
	}}
	reg.Panes = []coremetadata.Pane{
		pane("pane-log", owned(coremetadata.KindWindow, "win-main"), "%2"),
		pane("pane-edit", owned(coremetadata.KindWindow, "win-review"), "%3"),
		pane("pane-agent", owned(coremetadata.KindAgent, "agent-a"), "%1"),
		pane("pane-norun", owned(coremetadata.KindWindow, "win-main"), ""),
		pane("pane-gone", owned(coremetadata.KindWindow, "win-main"), "%9"),
		pane("pane-off", owned(coremetadata.KindWindow, "win-off"), "%5"),
	}
	return reg
}

func focusUIDTestInventory() liveTmuxInventory {
	inv := defaultLiveInventory()
	inv.panes["alpha:@2"] = [][2]string{{"%3", "edit"}}
	return inv
}

// newFocusUIDTestCommand wires a focus command over the fake live inventory and
// a fake Registry, and counts how often the Registry is loaded.
func newFocusUIDTestCommand(inv liveTmuxInventory) (*focusCommand, *focusFakeRunner, *int) {
	runner := newLiveFocusRunner(inv)
	cmd := newFocusTestCommand(runner, nil, nil)
	loads := 0
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		loads++
		return focusUIDTestRegistry(), nil
	}
	return cmd, runner, &loads
}

func assertNoUIDReachedTmux(t *testing.T, calls []focusFakeCall) {
	t.Helper()
	for _, call := range calls {
		for _, arg := range call.args {
			if strings.Contains(arg, selector.UIDPrefix) {
				t.Fatalf("a uid: selector reached tmux: %v (calls=%#v)", call.args, calls)
			}
		}
	}
}

func TestFocusUIDSelectorsResolveToLiveRuntimeIDs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		args       []string
		wantTarget string
		wantPane   string
	}{
		{name: "project uid", args: []string{"project", "uid:proj-a"}, wantTarget: "alpha"},
		{name: "window uid without --project", args: []string{"window", "uid:win-review"}, wantTarget: "alpha:@2"},
		{name: "pane uid without scope flags", args: []string{"pane", "uid:pane-log"}, wantTarget: "alpha:@1.%2", wantPane: "alpha:@1.%2"},
		{name: "agent-owned pane uid", args: []string{"pane", "uid:pane-agent"}, wantTarget: "alpha:@1.%1", wantPane: "alpha:@1.%1"},
		{name: "pane uid with matching uid flags", args: []string{"pane", "uid:pane-log", "--project", "uid:proj-a", "--window", "uid:win-main"}, wantTarget: "alpha:@1.%2"},
		{name: "pane uid with matching plain flags", args: []string{"pane", "uid:pane-edit", "-p", "alpha", "-w", "review"}, wantTarget: "alpha:@2.%3"},
		{name: "window uid with matching plain project", args: []string{"window", "uid:win-review", "--project", "alpha"}, wantTarget: "alpha:@2"},
		{name: "plain window under a uid project", args: []string{"window", "review", "--project", "uid:proj-a"}, wantTarget: "alpha:@2"},
		{name: "plain pane under uid scope flags", args: []string{"pane", "log", "--project", "uid:proj-a", "--window", "uid:win-main"}, wantTarget: "alpha:@1.%2", wantPane: "alpha:@1.%2"},
		{name: "plain pane under a uid window and plain project", args: []string{"pane", "edit", "--project", "alpha", "--window", "uid:win-review"}, wantTarget: "alpha:@2.%3"},
		{name: "plain pane and window under a uid project", args: []string{"pane", "log", "--project", "uid:proj-a", "--window", "main"}, wantTarget: "alpha:@1.%2"},
		{name: "explicit socket equal to the Project socket", args: []string{"pane", "uid:pane-log", "--socket", focusUIDTestSocket}, wantTarget: "alpha:@1.%2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, runner, loads := newFocusUIDTestCommand(focusUIDTestInventory())

			var stdout, stderr bytes.Buffer
			if err := cmd.Run(append(append([]string{}, test.args...), "--json"), &stdout, &stderr); err != nil {
				t.Fatalf("focus %v error = %v (stderr=%s)", test.args, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), `"ok":true`) || !strings.Contains(stdout.String(), `"target":"`+test.wantTarget+`"`) {
				t.Fatalf("focus %v result = %s, want ok target %q", test.args, stdout.String(), test.wantTarget)
			}
			if !strings.Contains(stdout.String(), `"socket":"`+focusUIDTestSocket+`"`) {
				t.Fatalf("focus %v socket = %s, want the Project socket %q", test.args, stdout.String(), focusUIDTestSocket)
			}
			if !focusCallsContain(runner.calls, "switch-client") {
				t.Fatalf("focus %v never moved the client: %#v", test.args, runner.calls)
			}
			if test.wantPane != "" && !sawTmuxArgPair(runner.calls, "-t", test.wantPane) {
				t.Fatalf("focus %v did not select pane %s: %#v", test.args, test.wantPane, runner.calls)
			}
			for _, call := range runner.calls {
				if len(call.args) < 2 || call.args[0] != "-S" || call.args[1] != focusUIDTestSocket {
					t.Fatalf("focus %v reached tmux off the Project socket: %v", test.args, call.args)
				}
			}
			assertNoUIDReachedTmux(t, runner.calls)
			assertCanonicalFocusCallsAreReadOrNavigationOnly(t, runner.calls)
			if *loads != 1 {
				t.Fatalf("focus %v loaded the Registry %d times, want 1", test.args, *loads)
			}
		})
	}
}

// focusUIDFailure runs one uid focus request that must not move the client and
// returns its error.
func focusUIDFailure(t *testing.T, args []string) error {
	t.Helper()
	cmd, runner, _ := newFocusUIDTestCommand(focusUIDTestInventory())
	var stdout, stderr bytes.Buffer
	err := cmd.Run(args, &stdout, &stderr)
	if err == nil {
		t.Fatalf("focus %v succeeded", args)
	}
	if stdout.Len() != 0 {
		t.Fatalf("focus %v wrote %q to stdout", args, stdout.String())
	}
	if focusCallsContain(runner.calls, "switch-client") || focusCallsContain(runner.calls, "select-pane") || focusCallsContain(runner.calls, "select-window") {
		t.Fatalf("focus %v moved the client: %#v", args, runner.calls)
	}
	assertNoUIDReachedTmux(t, runner.calls)
	assertCanonicalFocusCallsAreReadOrNavigationOnly(t, runner.calls)
	return err
}

func TestFocusUIDNotInRegistryIsNotResolved(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown pane", args: []string{"pane", "uid:pane-nope"}, want: "focus: pane uid:pane-nope is not in the Registry"},
		{name: "unknown window", args: []string{"window", "uid:win-nope"}, want: "focus: window uid:win-nope is not in the Registry"},
		{name: "unknown project", args: []string{"project", "uid:proj-nope"}, want: "focus: project uid:proj-nope is not in the Registry"},
		{name: "unknown project flag", args: []string{"window", "main", "--project", "uid:proj-nope"}, want: "focus: project uid:proj-nope is not in the Registry"},
		{name: "unknown window flag", args: []string{"pane", "log", "--project", "alpha", "--window", "uid:win-nope"}, want: "focus: window uid:win-nope is not in the Registry"},
		{name: "wrong kind", args: []string{"pane", "uid:win-main"}, want: "focus: pane uid:win-main is not in the Registry; it names a Window, not a Pane"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := focusUIDFailure(t, test.args)
			if got := exitCodeOf(err); got != focusExitNotResolved {
				t.Fatalf("focus %v exit = %d, want %d (err=%v)", test.args, got, focusExitNotResolved, err)
			}
			if err.Error() != test.want {
				t.Fatalf("focus %v error = %q, want %q", test.args, err, test.want)
			}
		})
	}
}

func TestFocusUIDWithoutLiveRuntimeIsNotResolved(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "window without runtimeID", args: []string{"window", "uid:win-norun"}, want: "Window uid:win-norun records no status.runtimeID"},
		{name: "pane without runtimeID", args: []string{"pane", "uid:pane-norun"}, want: "Pane uid:pane-norun records no status.activation.runtimeID"},
		{name: "window id not live", args: []string{"window", "uid:win-gone"}, want: "window @9 of Window uid:win-gone is not live"},
		{name: "pane id not live", args: []string{"pane", "uid:pane-gone"}, want: "pane %9 is not live in alpha:@1"},
		{name: "project session offline", args: []string{"project", "uid:proj-off"}, want: `session "beta" of Project uid:proj-off is not live`},
		{name: "pane under an offline session", args: []string{"pane", "uid:pane-off"}, want: `session "beta" of Project uid:proj-off is not live`},
		{name: "project without a session", args: []string{"project", "uid:proj-nosess"}, want: "Project uid:proj-nosess records no status.session.name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := focusUIDFailure(t, test.args)
			if got := exitCodeOf(err); got != focusExitNotResolved {
				t.Fatalf("focus %v exit = %d, want %d (err=%v)", test.args, got, focusExitNotResolved, err)
			}
			prefix := "focus: " + test.args[0] + " " + test.args[1] + " is in the Registry but has no live runtime: "
			if !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("focus %v error = %q, want %q ... %q", test.args, err, prefix, test.want)
			}
		})
	}
}

func TestFocusUIDScopeOrSocketMismatchIsNotResolved(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "window uid flag", args: []string{"pane", "uid:pane-log", "--window", "uid:win-review"}, want: "focus: pane uid:pane-log does not match the requested scope: --window uid:win-review is not its owning Window uid:win-main"},
		{name: "project uid flag", args: []string{"pane", "uid:pane-log", "--project", "uid:proj-off"}, want: "focus: pane uid:pane-log does not match the requested scope: --project uid:proj-off is not its owning Project uid:proj-a"},
		{name: "plain project flag", args: []string{"window", "uid:win-main", "--project", "beta"}, want: `focus: window uid:win-main does not match the requested scope: --project "beta" is not its Project uid:proj-a session "alpha"`},
		{name: "plain window flag", args: []string{"pane", "uid:pane-log", "--window", "review"}, want: `focus: pane uid:pane-log does not match the requested scope: --window "review" is not its owning Window uid:win-main (live @1)`},
		{name: "uid window flag outside the uid project flag", args: []string{"pane", "log", "--project", "uid:proj-off", "--window", "uid:win-main"}, want: "focus: window uid:win-main does not match the requested scope: --project uid:proj-off is not its owning Project uid:proj-a"},
		{name: "socket", args: []string{"pane", "uid:pane-log", "--socket", "/tmp/other.sock"}, want: `focus: pane uid:pane-log does not match the requested scope: --socket "/tmp/other.sock" is not its Project uid:proj-a socket "` + focusUIDTestSocket + `"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := focusUIDFailure(t, test.args)
			if got := exitCodeOf(err); got != focusExitNotResolved {
				t.Fatalf("focus %v exit = %d, want %d (err=%v)", test.args, got, focusExitNotResolved, err)
			}
			if err.Error() != test.want {
				t.Fatalf("focus %v error = %q, want %q", test.args, err, test.want)
			}
		})
	}
}

func TestFocusMalformedUIDReportsTheSelectorError(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		kind coremetadata.Kind
		raw  string
	}{
		{name: "empty pane uid", args: []string{"pane", "uid:"}, kind: coremetadata.KindPane, raw: "uid:"},
		{name: "empty project uid", args: []string{"project", "uid:"}, kind: coremetadata.KindProject, raw: "uid:"},
		{name: "empty project flag uid", args: []string{"window", "main", "--project", "uid:"}, kind: coremetadata.KindProject, raw: "uid:"},
		{name: "empty window flag uid", args: []string{"pane", "log", "-p", "alpha", "-w", "uid:"}, kind: coremetadata.KindWindow, raw: "uid:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, runner, loads := newFocusUIDTestCommand(focusUIDTestInventory())
			var stdout, stderr bytes.Buffer
			err := cmd.Run(test.args, &stdout, &stderr)
			_, want := selector.ParseRef(test.kind, test.raw)
			if want == nil {
				t.Fatalf("ParseRef(%s, %q) accepted the value", test.kind, test.raw)
			}
			if err == nil || err.Error() != want.Error() {
				t.Fatalf("focus %v error = %v, want the selector error %q", test.args, err, want)
			}
			if !IsUsageError(err) {
				t.Fatalf("focus %v error is not a usage error: %v", test.args, err)
			}
			if len(runner.calls) != 0 || *loads != 0 {
				t.Fatalf("focus %v reached tmux %d times and the Registry %d times", test.args, len(runner.calls), *loads)
			}
		})
	}
}

func TestFocusCoordinateRejectionIsUnchangedBesideUIDSelectors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args []string
		kind string
	}{
		{args: []string{"project", "sess:1.2"}, kind: "project"},
		{args: []string{"pane", "a.b", "--project", "alpha", "--window", "main"}, kind: "pane"},
		{args: []string{"window", "x:uid:win-main", "--project", "alpha"}, kind: "window"},
		{args: []string{"pane", "UID:pane-log"}, kind: "pane"},
	} {
		cmd, runner, loads := newFocusUIDTestCommand(focusUIDTestInventory())
		var stdout, stderr bytes.Buffer
		err := cmd.Run(test.args, &stdout, &stderr)
		want := "focus " + test.kind + " takes one " + test.kind + " reference, not a session:window.pane coordinate; machine-owned raw coordinates use `projmux internal focus --target`"
		if err == nil || err.Error() != want || !IsUsageError(err) {
			t.Fatalf("focus %v error = %v, want usage error %q", test.args, err, want)
		}
		if len(runner.calls) != 0 || *loads != 0 {
			t.Fatalf("focus %v reached tmux %d times and the Registry %d times", test.args, len(runner.calls), *loads)
		}
	}
}

func TestFocusNamePathNeverLoadsTheRegistry(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"project", "alpha"},
		{"window", "review", "--project", "alpha"},
		{"pane", "log", "--project", "alpha", "--window", "main"},
		{"pane", "%1", "--project", "alpha", "--window", "@1"},
		{"project", "gamma"},
		{"window", "nosuch", "--project", "alpha"},
		{"window", "main"},
	} {
		cmd, _, loads := newFocusUIDTestCommand(focusUIDTestInventory())
		var stdout, stderr bytes.Buffer
		_ = cmd.Run(args, &stdout, &stderr)
		if *loads != 0 {
			t.Fatalf("focus %v loaded the Registry %d times, want 0", args, *loads)
		}
	}
}

func TestFocusUIDRequiresAConfiguredRegistryLoader(t *testing.T) {
	t.Parallel()

	runner := newLiveFocusRunner(focusUIDTestInventory())
	cmd := newFocusTestCommand(runner, nil, nil)
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"pane", "uid:pane-log"}, &stdout, &stderr)
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("focus without a Registry loader = %v, calls %#v", err, runner.calls)
	}

	cmd.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, errors.New("disk on fire") }
	err = cmd.Run([]string{"pane", "uid:pane-log"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") || len(runner.calls) != 0 {
		t.Fatalf("focus with a failing Registry loader = %v, calls %#v", err, runner.calls)
	}
}

// TestFocusUIDFailureMessagesAreDistinct pins the four failure classes to four
// distinguishable messages: a caller reading stderr can tell a coordinate
// misuse, an unknown uid, a uid without a live runtime, and a scope mismatch
// apart.
func TestFocusUIDFailureMessagesAreDistinct(t *testing.T) {
	t.Parallel()

	cmd, _, _ := newFocusUIDTestCommand(focusUIDTestInventory())
	var stdout, stderr bytes.Buffer
	coordinate := cmd.Run([]string{"pane", "a:b.c", "-p", "alpha", "-w", "main"}, &stdout, &stderr)
	messages := []string{
		coordinate.Error(),
		focusUIDFailure(t, []string{"pane", "uid:pane-nope"}).Error(),
		focusUIDFailure(t, []string{"pane", "uid:pane-gone"}).Error(),
		focusUIDFailure(t, []string{"pane", "uid:pane-log", "--socket", "/tmp/other.sock"}).Error(),
	}
	markers := []string{
		"not a session:window.pane coordinate",
		"is not in the Registry",
		"is in the Registry but has no live runtime",
		"does not match the requested scope",
	}
	for i, message := range messages {
		for j, marker := range markers {
			if got := strings.Contains(message, marker); got != (i == j) {
				t.Fatalf("message %d %q contains marker %d %q = %v", i, message, j, marker, got)
			}
		}
		for j := range i {
			if messages[j] == message {
				t.Fatalf("messages %d and %d are identical: %q", j, i, message)
			}
		}
	}
	if !IsUsageError(coordinate) {
		t.Fatalf("coordinate error is not a usage error: %v", coordinate)
	}
}
