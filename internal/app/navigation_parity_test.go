package app

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

// canonicalFocusAllowedTmuxVerbs is the complete runtime surface canonical
// Project/Window/Pane focus may reach: inventory reads and client navigation.
// An allowlist closes zero-topology-write coverage over future tmux mutation
// verbs without relying on an inevitably incomplete mutation inventory.
var canonicalFocusAllowedTmuxVerbs = []string{
	"display-message", "list-sessions", "list-windows", "list-panes", "list-clients",
	"switch-client", "select-window", "select-pane",
}

func focusCallsContain(calls []focusFakeCall, verb string) bool {
	for _, call := range calls {
		if slices.Contains(call.args, verb) {
			return true
		}
	}
	return false
}

func assertCanonicalFocusCallsAreReadOrNavigationOnly(t *testing.T, calls []focusFakeCall) {
	t.Helper()
	for _, call := range calls {
		argv := tmuxCommandArgv(call.args)
		if len(argv) == 0 || !slices.Contains(canonicalFocusAllowedTmuxVerbs, argv[0]) {
			t.Fatalf("canonical focus reached non-read/non-navigation tmux call: name=%q args=%v calls=%#v", call.name, call.args, calls)
		}
	}
}

// liveTmuxInventory describes the live sessions, windows, and panes a focus test
// runs against. It is the whole runtime the fake tmux answers from.
type liveTmuxInventory struct {
	// sessions is the raw list-sessions payload.
	sessions string
	// clients is the raw list-clients payload.
	clients string
	// windows maps a session name to `id<SEP>name` rows.
	windows map[string][][2]string
	// panes maps a `session:@id` target to `id<SEP>name` rows.
	panes map[string][][2]string
}

func defaultLiveInventory() liveTmuxInventory {
	return liveTmuxInventory{
		sessions: "100\talpha\t1\n90\talpha-worktree\t0\n",
		clients:  "/dev/pts/0\talpha\n",
		windows:  map[string][][2]string{"alpha": {{"@1", "main"}, {"@2", "review"}}},
		panes:    map[string][][2]string{"alpha:@1": {{"%1", "zsh"}, {"%2", "log"}}},
	}
}

func rowsPayload(rows [][2]string) []byte {
	var b strings.Builder
	for _, row := range rows {
		b.WriteString(row[0])
		b.WriteString(focusFieldSeparator)
		b.WriteString(row[1])
		b.WriteString(focusFieldSeparator)
		b.WriteString(row[1])
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// newLiveFocusRunner answers the read-only tmux inventory queries from a fixed
// live inventory and lets every dispatch verb succeed.
func newLiveFocusRunner(inv liveTmuxInventory) *focusFakeRunner {
	return &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			target := ""
			for i, arg := range args {
				if arg == "-t" && i+1 < len(args) {
					target = args[i+1]
				}
			}
			switch {
			case containsArg(args, "list-sessions"):
				return []byte(inv.sessions), nil
			case containsArg(args, "list-clients"):
				return []byte(inv.clients), nil
			case containsArg(args, "list-windows"):
				rows, ok := inv.windows[target]
				if !ok {
					return nil, errors.New("can't find session: " + target)
				}
				return rowsPayload(rows), nil
			case containsArg(args, "list-panes"):
				rows, ok := inv.panes[target]
				if !ok {
					return nil, errors.New("can't find window: " + target)
				}
				return rowsPayload(rows), nil
			}
			return nil, nil
		},
	}
}

func exitCodeOf(err error) int {
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	if IsUsageError(err) {
		return 2
	}
	if err == nil {
		return 0
	}
	return 1
}

// TestFocusKindOnAnOfflineTargetExitsTwoAndMaterializesNothing is acceptance
// criterion 1.
//
// Every case names a target that is not live in the tmux inventory, at each of
// the three kinds. The assertion is two-sided: the exit code is 2, and the tmux
// call log contains no verb that could have created the target.
func TestFocusKindOnAnOfflineTargetExitsTwoAndMaterializesNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "an offline Project is never created",
			args: []string{"project", "gamma"},
		},
		{
			name: "a prefix-match fallback is not the requested Project",
			args: []string{"project", "alpha-worktre"},
		},
		{
			name: "an offline Window is not degraded to a session focus",
			args: []string{"window", "nosuch", "--project", "alpha"},
		},
		{
			name: "a Window under an offline Project resolves nothing",
			args: []string{"window", "main", "--project", "gamma"},
		},
		{
			name: "an offline Pane is not degraded to a window focus",
			args: []string{"pane", "nosuch", "--project", "alpha", "--window", "main"},
		},
		{
			name: "a Pane under an offline Window resolves nothing",
			args: []string{"pane", "zsh", "--project", "alpha", "--window", "review"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newLiveFocusRunner(defaultLiveInventory())
			cmd := newFocusTestCommand(runner, nil, nil)

			var stdout, stderr bytes.Buffer
			err := cmd.Run(test.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("focus %v succeeded against an offline target", test.args)
			}
			if got := exitCodeOf(err); got != focusExitNotResolved {
				t.Fatalf("focus %v exit code = %d, want %d (err=%v)", test.args, got, focusExitNotResolved, err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("focus %v wrote %q to stdout, want 0 bytes", test.args, stdout.String())
			}
			assertCanonicalFocusCallsAreReadOrNavigationOnly(t, runner.calls)
			if focusCallsContain(runner.calls, "switch-client") {
				t.Fatalf("focus %v moved the client to an unresolved target: %#v", test.args, runner.calls)
			}
		})
	}
}

// TestFocusKindRefusesAnAmbiguousLiveTarget keeps the exact-one promise honest
// when two live resources answer to the same reference.
func TestFocusKindRefusesAnAmbiguousLiveTarget(t *testing.T) {
	t.Parallel()

	inv := defaultLiveInventory()
	inv.windows["alpha"] = [][2]string{{"@1", "main"}, {"@3", "main"}}
	runner := newLiveFocusRunner(inv)

	var stdout, stderr bytes.Buffer
	err := newFocusTestCommand(runner, nil, nil).Run([]string{"window", "main", "--project", "alpha"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("an ambiguous window focus succeeded")
	}
	if got := exitCodeOf(err); got != focusExitNotResolved {
		t.Fatalf("exit code = %d, want %d (err=%v)", got, focusExitNotResolved, err)
	}
	if !strings.Contains(err.Error(), "want exactly one") {
		t.Fatalf("error = %v, want the exact-one message", err)
	}
	if focusCallsContain(runner.calls, "switch-client") {
		t.Fatalf("an ambiguous focus still moved the client: %#v", runner.calls)
	}
	assertCanonicalFocusCallsAreReadOrNavigationOnly(t, runner.calls)
}

// TestFocusKindMovesTheClientToAnAlreadyLiveTarget is the positive half: the
// same routes do move the client when the exact target is live, and they still
// never materialize.
func TestFocusKindMovesTheClientToAnAlreadyLiveTarget(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		args       []string
		wantTarget string
	}{
		{name: "project", args: []string{"project", "alpha"}, wantTarget: "alpha"},
		{name: "window by name", args: []string{"window", "review", "--project", "alpha"}, wantTarget: "alpha:@2"},
		{name: "window by live id", args: []string{"window", "@1", "--project", "alpha"}, wantTarget: "alpha:@1"},
		{name: "pane by name", args: []string{"pane", "log", "--project", "alpha", "--window", "main"}, wantTarget: "alpha:@1.%2"},
		{name: "pane by live id", args: []string{"pane", "%1", "--project", "alpha", "--window", "main"}, wantTarget: "alpha:@1.%1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newLiveFocusRunner(defaultLiveInventory())
			cmd := newFocusTestCommand(runner, nil, nil)

			var stdout, stderr bytes.Buffer
			if err := cmd.Run(append(append([]string{}, test.args...), "--json"), &stdout, &stderr); err != nil {
				t.Fatalf("focus %v error = %v (stderr=%s)", test.args, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), `"ok":true`) {
				t.Fatalf("focus %v result = %s", test.args, stdout.String())
			}
			if !strings.Contains(stdout.String(), `"target":"`+test.wantTarget+`"`) {
				t.Fatalf("focus %v composed target = %s, want %q", test.args, stdout.String(), test.wantTarget)
			}
			if !focusCallsContain(runner.calls, "switch-client") {
				t.Fatalf("focus %v never moved the client: %#v", test.args, runner.calls)
			}
			assertCanonicalFocusCallsAreReadOrNavigationOnly(t, runner.calls)
		})
	}
}

// TestFocusKindUsageErrorsNeverReachTmux keeps the canonical grammar failures on
// the exit-2 path with zero runtime access.
func TestFocusKindUsageErrorsNeverReachTmux(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no reference", args: []string{"project"}, want: "exactly one resource reference"},
		{name: "two references", args: []string{"project", "alpha", "beta"}, want: "exactly one resource reference"},
		{name: "window without a project scope", args: []string{"window", "main"}, want: "requires --project"},
		{name: "pane without a window scope", args: []string{"pane", "zsh", "--project", "alpha"}, want: "requires --window"},
		{name: "raw coordinate", args: []string{"project", "alpha:main.1"}, want: "not a session:window.pane coordinate"},
		{name: "unknown flag", args: []string{"project", "alpha", "--bogus"}, want: "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newLiveFocusRunner(defaultLiveInventory())
			cmd := newFocusTestCommand(runner, nil, nil)

			var stdout, stderr bytes.Buffer
			err := cmd.Run(test.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("focus %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("focus %v error is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("focus %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("focus %v reached tmux: %#v", test.args, runner.calls)
			}
			if stdout.Len() != 0 {
				t.Fatalf("focus %v wrote %q to stdout, want 0 bytes", test.args, stdout.String())
			}
		})
	}
}

// TestLegacyFocusTargetKeepsItsFallbacks is acceptance criterion 6 for the focus
// route: the exact-one rule belongs to the canonical spelling only, and the
// current `--target` spelling still degrades exactly as before.
func TestLegacyFocusTargetKeepsItsFallbacks(t *testing.T) {
	t.Parallel()

	// A prefix-match fallback still succeeds on the legacy spelling.
	legacyInv := defaultLiveInventory()
	legacyInv.sessions = "100\talpha-worktree\t1\n"
	legacyInv.clients = "/dev/pts/0\talpha-worktree\n"
	runner := newLiveFocusRunner(legacyInv)
	var stdout, stderr bytes.Buffer
	if err := newFocusTestCommand(runner, nil, nil).Run([]string{"--target", "alpha", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("legacy focus fallback error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"fallback":"prefix-match"`) || !strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatalf("legacy focus fallback result = %s", stdout.String())
	}

	// A window index that does not resolve still degrades to the session focus.
	windowFallbackInv := defaultLiveInventory()
	windowFallbackInv.sessions = "100\talpha\t1\n"
	runner = &focusFakeRunner{respond: func(args []string) ([]byte, error) {
		switch {
		case containsArg(args, "list-sessions"):
			return []byte(windowFallbackInv.sessions), nil
		case containsArg(args, "list-clients"):
			return []byte(windowFallbackInv.clients), nil
		case containsArg(args, "select-window"):
			return nil, errors.New("can't find window")
		}
		return nil, nil
	}}
	stdout.Reset()
	stderr.Reset()
	if err := newFocusTestCommand(runner, nil, nil).Run([]string{"--target", "alpha:9", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("legacy focus window fallback error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"window_state":"index-fallback-session"`) {
		t.Fatalf("legacy focus window fallback result = %s", stdout.String())
	}
}

// recordingRawArgv captures the argv a canonical alias forwards.
type recordingRawArgv struct {
	calls  [][]string
	stdout string
	stderr string
	err    error
}

func (r *recordingRawArgv) Run(args []string, stdout, stderr io.Writer) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.stdout != "" {
		_, _ = io.WriteString(stdout, r.stdout)
	}
	if r.stderr != "" {
		_, _ = io.WriteString(stderr, r.stderr)
	}
	return r.err
}

// TestAttachProjectIsTheOnlyOutsideTmuxMaterializingEntry is acceptance
// criterion 2.
func TestAttachProjectIsTheOnlyOutsideTmuxMaterializingEntry(t *testing.T) {
	t.Parallel()

	t.Run("outside tmux it forwards to the project open path", func(t *testing.T) {
		t.Parallel()
		switcher := &recordingRawArgv{stdout: "opened\n"}
		cmd := &attachCommand{lookupEnv: func(string) string { return "" }, switcher: switcher}
		stdout, stderr, err := runRoute(t, cmd, "project", "/srv/alpha")
		if err != nil {
			t.Fatalf("attach project error = %v (stderr=%s)", err, stderr)
		}
		if len(switcher.calls) != 1 || strings.Join(switcher.calls[0], " ") != "open /srv/alpha" {
			t.Fatalf("forwarded argv = %#v, want [open /srv/alpha]", switcher.calls)
		}
		if stdout != "opened\n" {
			t.Fatalf("stdout = %q, want the forwarded handler output verbatim", stdout)
		}
	})

	t.Run("inside a tmux client it refuses with exit 2 and points at focus", func(t *testing.T) {
		t.Parallel()
		switcher := &recordingRawArgv{}
		cmd := &attachCommand{
			lookupEnv: func(name string) string {
				if name == "TMUX" {
					return "/tmp/tmux-1000/projmux,123,0"
				}
				return ""
			},
			switcher: switcher,
		}
		stdout, _, err := runRoute(t, cmd, "project", "/srv/alpha")
		if err == nil {
			t.Fatal("attach project ran inside a tmux client")
		}
		if !IsUsageError(err) {
			t.Fatalf("inside-tmux refusal is not a usage error: %v", err)
		}
		if !strings.Contains(err.Error(), "projmux focus project /srv/alpha") {
			t.Fatalf("refusal does not hand over to focus: %v", err)
		}
		if len(switcher.calls) != 0 {
			t.Fatalf("the refused attach still materialized: %#v", switcher.calls)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want 0 bytes", stdout)
		}
	})

	t.Run("usage failures never reach the open path", func(t *testing.T) {
		t.Parallel()
		for _, args := range [][]string{
			{"project"},
			{"project", "/srv/alpha", "/srv/beta"},
			{"project", "--bogus"},
		} {
			switcher := &recordingRawArgv{}
			cmd := &attachCommand{lookupEnv: func(string) string { return "" }, switcher: switcher}
			if _, _, err := runRoute(t, cmd, args...); err == nil || !IsUsageError(err) {
				t.Fatalf("attach %v error = %v, want a usage error", args, err)
			}
			if len(switcher.calls) != 0 {
				t.Fatalf("attach %v reached the open path: %#v", args, switcher.calls)
			}
		}
	})
}

// TestRuntimeDomainForwardsRawArgvToTheCurrentHandlers is the runtime-domain
// parity table. Each canonical spelling must hand the existing handler exactly
// the argv the current spelling would have produced, and must relay its streams
// and its error untouched.
func TestRuntimeDomainForwardsRawArgvToTheCurrentHandlers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		args  []string
		route string
		want  []string
	}{
		{name: "sessions", args: []string{"sessions", "--ui=popup"}, route: "sessions", want: []string{"--ui=popup"}},
		{name: "attach", args: []string{"attach", "--keep=5", "--fallback=ephemeral"}, route: "attach", want: []string{"auto", "--keep=5", "--fallback=ephemeral"}},
		{name: "stop", args: []string{"stop", "alpha", "beta"}, route: "kill", want: []string{"tagged", "alpha", "beta"}},
		{name: "tag", args: []string{"tag", "toggle", "alpha"}, route: "tag", want: []string{"toggle", "alpha"}},
		{name: "prune", args: []string{"prune", "--keep=2"}, route: "prune", want: []string{"ephemeral", "--keep=2"}},
		{name: "payload tails survive", args: []string{"stop", "--", "--yes"}, route: "kill", want: []string{"tagged", "--", "--yes"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			targets := map[string]*recordingRawArgv{
				"sessions": {stdout: "sessions-out\n", stderr: "sessions-err\n"},
				"attach":   {stdout: "attach-out\n"},
				"kill":     {stdout: "kill-out\n"},
				"tag":      {stdout: "tag-out\n"},
				"prune":    {stdout: "prune-out\n"},
			}
			cmd := &runtimeCommand{
				sessions: targets["sessions"],
				attach:   targets["attach"],
				kill:     targets["kill"],
				tag:      targets["tag"],
				prune:    targets["prune"],
			}
			stdout, stderr, err := runRoute(t, cmd, test.args...)
			if err != nil {
				t.Fatalf("runtime %v error = %v", test.args, err)
			}
			target := targets[test.route]
			if len(target.calls) != 1 {
				t.Fatalf("runtime %v reached %s %d times, want 1", test.args, test.route, len(target.calls))
			}
			if strings.Join(target.calls[0], "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("runtime %v forwarded %#v, want %#v", test.args, target.calls[0], test.want)
			}
			if stdout != target.stdout || stderr != target.stderr {
				t.Fatalf("runtime %v relayed stdout=%q stderr=%q, want %q/%q", test.args, stdout, stderr, target.stdout, target.stderr)
			}
			for route, other := range targets {
				if route != test.route && len(other.calls) != 0 {
					t.Fatalf("runtime %v also reached %s", test.args, route)
				}
			}
		})
	}

	// The handler error crosses the alias unchanged, so the exit code is the old
	// route's exit code.
	failing := &recordingRawArgv{err: usageError("kill tagged requires a selection")}
	cmd := &runtimeCommand{kill: failing}
	_, _, err := runRoute(t, cmd, "stop")
	if err == nil || !IsUsageError(err) {
		t.Fatalf("relayed error = %v, want the handler's usage error", err)
	}

	// Unknown runtime subcommands are usage errors that reach no handler.
	for _, args := range [][]string{nil, {"open"}, {"bogus"}} {
		targets := &recordingRawArgv{}
		cmd := &runtimeCommand{sessions: targets, attach: targets, kill: targets, tag: targets, prune: targets}
		if _, _, err := runRoute(t, cmd, args...); err == nil || !IsUsageError(err) {
			t.Fatalf("runtime %v error = %v, want a usage error", args, err)
		}
		if len(targets.calls) != 0 {
			t.Fatalf("runtime %v reached a handler: %#v", args, targets.calls)
		}
	}
}

// TestCanonicalAliasesForwardToTheirCurrentHandlers covers the remaining
// delegating spellings in one table.
func TestCanonicalAliasesForwardToTheirCurrentHandlers(t *testing.T) {
	t.Parallel()

	t.Run("get delegates the two non-registry kinds", func(t *testing.T) {
		t.Parallel()
		notify := &recordingRawArgv{stdout: "notify-out\n"}
		snapshots := &recordingRawArgv{stdout: "snapshot-out\n"}
		cmd := &getCommand{notify: notify, snapshots: snapshots, currentPath: &stubCurrentPath{}}

		if stdout, _, err := runRoute(t, cmd, "notifications", "--json"); err != nil || stdout != "notify-out\n" {
			t.Fatalf("get notifications stdout=%q err=%v", stdout, err)
		}
		if strings.Join(notify.calls[0], " ") != "list --json" {
			t.Fatalf("get notifications forwarded %#v", notify.calls)
		}
		if stdout, _, err := runRoute(t, cmd, "snapshots"); err != nil || stdout != "snapshot-out\n" {
			t.Fatalf("get snapshots stdout=%q err=%v", stdout, err)
		}
		if strings.Join(snapshots.calls[0], " ") != "status" {
			t.Fatalf("get snapshots forwarded %#v", snapshots.calls)
		}
	})

	t.Run("delete delegates notification and snapshot", func(t *testing.T) {
		t.Parallel()
		notify := &recordingRawArgv{}
		snapshots := &recordingRawArgv{}
		cmd := &deleteCommand{notify: notify, snapshots: snapshots, resolveKinds: deleteRegistryKinds, confirm: newConfirmer()}

		if _, _, err := runRoute(t, cmd, "notification", "abc123"); err != nil {
			t.Fatalf("delete notification error = %v", err)
		}
		if strings.Join(notify.calls[0], " ") != "ack abc123" {
			t.Fatalf("delete notification forwarded %#v", notify.calls)
		}
		if _, _, err := runRoute(t, cmd, "snapshot", "alpha"); err != nil {
			t.Fatalf("delete snapshot error = %v", err)
		}
		if strings.Join(snapshots.calls[0], " ") != "delete alpha" {
			t.Fatalf("delete snapshot forwarded %#v", snapshots.calls)
		}
	})

	t.Run("restore delegates snapshot", func(t *testing.T) {
		t.Parallel()
		snapshots := &recordingRawArgv{}
		cmd := &restoreCommand{snapshots: snapshots}
		if _, _, err := runRoute(t, cmd, "snapshot", "alpha", "--dry-run"); err != nil {
			t.Fatalf("restore snapshot error = %v", err)
		}
		if strings.Join(snapshots.calls[0], " ") != "restore alpha --dry-run" {
			t.Fatalf("restore snapshot forwarded %#v", snapshots.calls)
		}
		for _, args := range [][]string{nil, {"session"}} {
			if _, _, err := runRoute(t, cmd, args...); err == nil || !IsUsageError(err) {
				t.Fatalf("restore %v error = %v, want a usage error", args, err)
			}
		}
	})
}
