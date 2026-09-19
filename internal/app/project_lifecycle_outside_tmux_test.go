package app

// Outside tmux there is no inherited server, so `start project` and `stop
// project` must read liveness from the server their writes use: the app server
// behind `-L projmux`, proven app-owned by the invocation mutation route. These
// cases pin that the read, the stop's containment confirmation, and the stop's
// kill all use that one route, resolved once, and that an invocation carrying
// `$TMUX` keeps reading through the session inspector exactly as before.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// lifecycleAppServerRunner models the app server behind `-L projmux` for the
// lifecycle verbs. It answers `has-session` from `live` for exact `=name`
// targets only, and delegates everything else to base (a recordingTmuxRunner,
// whose logical `-L projmux` socket resolves to /tmp/tmux-1000/projmux, unless
// a test supplies a stricter runner).
type lifecycleAppServerRunner struct {
	base tmuxCommandRunner
	live map[string]bool
	// absent answers every call as tmux does when no server listens.
	absent bool
	// foreign blanks the @projmux_app ownership marker.
	foreign bool
	calls   []recordedTmuxCall
}

func (r *lifecycleAppServerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	if r.absent {
		return nil, appTypedCommandFailure{failure: inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on /tmp/tmux-1000/projmux",
		}}
	}
	if r.foreign && len(args) >= 3 && args[len(args)-3] == "show-options" && args[len(args)-1] == tmuxopts.AppGlobal {
		return []byte("\n"), nil
	}
	if i := slices.Index(args, "has-session"); i >= 0 {
		target := flagValue(args[i+1:], "-t")
		if strings.HasPrefix(target, "=") && r.live[strings.TrimPrefix(target, "=")] {
			return nil, nil
		}
		return nil, appTypedCommandFailure{failure: inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "can't find session: " + strings.TrimPrefix(target, "="),
		}}
	}
	if r.base == nil {
		r.base = &recordingTmuxRunner{}
	}
	return r.base.Run(ctx, name, args...)
}

// count reports how many calls carried exactly these arguments.
func (r *lifecycleAppServerRunner) count(args ...string) int {
	n := 0
	for _, call := range r.calls {
		if call.name == "tmux" && slices.Equal(call.args, args) {
			n++
		}
	}
	return n
}

// hasSessionCalls returns the argv of every has-session probe.
func (r *lifecycleAppServerRunner) hasSessionCalls() [][]string {
	var out [][]string
	for _, call := range r.calls {
		if slices.Contains(call.args, "has-session") {
			out = append(out, call.args)
		}
	}
	return out
}

// routeResolutions counts invocation-route resolutions: the default logical
// socket's ownership read happens once per resolution and nowhere else, since
// every later route guard reads through the physical `-S` path.
func (r *lifecycleAppServerRunner) routeResolutions() int {
	return r.count("-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal)
}

func lifecycleOutsideTmuxEnv(string) string { return "" }

// TestStopProjectOutsideTmuxStopsTheSessionOnTheAppServerRoute is acceptance 1:
// with no `$TMUX`, a live session on the app server is found, confirmed, and
// killed through the one route the invocation resolved. The inherited-host
// navigation view is empty here -- there is no inherited host -- so the stop
// can only succeed if its containment confirmation read the route's socket.
func TestStopProjectOutsideTmuxStopsTheSessionOnTheAppServerRoute(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		projectDir = "/src/alpha"
	)
	switcher, stop, _ := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, "%9", nil)
	sessionID := bindSidebarPopupManagedRow(t, switcher, stop, projectDir)
	sessionName, err := switcher.resolveTargetSession(projectDir)
	if err != nil {
		t.Fatalf("resolve fixture session: %v", err)
	}
	// The app server is the default logical socket, and the invocation has no
	// tmux environment of any kind.
	stop.logical = defaultAppSocket
	switcher.lookupEnv = lifecycleOutsideTmuxEnv
	switcher.navigation.reader.reader.lookupEnv = lifecycleOutsideTmuxEnv
	runner := &lifecycleAppServerRunner{base: stop, live: map[string]bool{sessionName: true}}
	switcher.tmuxRunner = runner

	if inherited, err := switcher.navigationView(context.Background()); err != nil {
		t.Fatalf("inherited navigation view: %v", err)
	} else {
		for _, row := range projectRowsOf(inherited) {
			if row.Runtime != nil {
				t.Fatalf("outside tmux the inherited view observed a runtime row: %#v", row)
			}
		}
	}

	store := &fakeResourceStore{registry: runtimeFixtureRegistry(), dirs: map[string]bool{projectDir: true}, now: resourceFixtureClock}
	cmd := &projectLifecycleCommand{
		verb: projectLifecycleStop, store: store.store(), switcher: switcher, lookupEnv: lifecycleOutsideTmuxEnv,
	}
	stdout, _, runErr := runRoute(t, cmd, "project", "uid:"+runtimeFixtureProject)
	if runErr != nil {
		t.Fatalf("outside-tmux stop project error = %v", runErr)
	}
	if !strings.Contains(stdout, "receipt operation=stop.project") || !strings.Contains(stdout, "runtime=stopped focus=unchanged") {
		t.Fatalf("outside-tmux stop project stdout = %q", stdout)
	}
	if !stop.killed || stop.killTarget != sessionID || stop.writes() != 1 {
		t.Fatalf("outside-tmux stop killed=%t target=%q writes=%d, want one kill-session -t %s",
			stop.killed, stop.killTarget, stop.writes(), sessionID)
	}
	if got := runner.routeResolutions(); got != 1 {
		t.Fatalf("outside-tmux stop resolved the mutation route %d times, want exactly once: %#v", got, runner.calls)
	}
	wantProbe := []string{"-S", socketPath, "has-session", "-t", "=" + sessionName}
	if probes := runner.hasSessionCalls(); len(probes) != 1 || !slices.Equal(probes[0], wantProbe) {
		t.Fatalf("outside-tmux liveness probes = %v, want exactly %v", probes, wantProbe)
	}
	if store.writes != 0 {
		t.Fatalf("outside-tmux stop wrote the lifecycle Registry %d times, want 0", store.writes)
	}
}

// TestStartProjectOutsideTmuxReportsAlreadyLiveFromTheAppServer is acceptance
// 2: the default tmux server (the session inspector) knows nothing about the
// session, the app server has it, and `start project` must report already-live
// without materializing anything.
func TestStartProjectOutsideTmuxReportsAlreadyLiveFromTheAppServer(t *testing.T) {
	t.Parallel()

	cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleStart, false, nil)
	runner := &lifecycleAppServerRunner{live: map[string]bool{"alpha": true}}
	cmd.switcher.tmuxRunner = runner

	stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
	if err != nil {
		t.Fatalf("outside-tmux start project error = %v", err)
	}
	if !strings.Contains(stdout, "receipt operation=start.project") ||
		!strings.Contains(stdout, "runtime="+string(cli.RuntimeAlreadyLive)+" focus=unchanged") {
		t.Fatalf("outside-tmux start project stdout = %q, want runtime=already-live", stdout)
	}
	if executor.authorizeCalled || executor.ensureSessionName != "" || executor.openSessionName != "" {
		t.Fatalf("an already-live start reached the materializer: %#v", executor)
	}
	if store.writes != 0 {
		t.Fatalf("an already-live start wrote the Registry %d times", store.writes)
	}
	if got := runner.routeResolutions(); got != 1 {
		t.Fatalf("outside-tmux start resolved the mutation route %d times, want once: %#v", got, runner.calls)
	}
	wantProbe := []string{"-S", "/tmp/tmux-1000/" + defaultAppSocket, "has-session", "-t", "=alpha"}
	if probes := runner.hasSessionCalls(); len(probes) != 1 || !slices.Equal(probes[0], wantProbe) {
		t.Fatalf("outside-tmux liveness probes = %v, want exactly %v", probes, wantProbe)
	}
	for _, call := range runner.calls {
		for _, arg := range call.args {
			if arg == "new-session" || arg == "new-window" || arg == "kill-session" || arg == "set-option" {
				t.Fatalf("an already-live start issued a tmux write: %v", call.args)
			}
		}
	}
}

// TestProjectLifecycleOutsideTmuxWithoutAnAppServerKeepsTheOfflineBehavior is
// acceptance 3 plus the not-app-owned case: an absent server, or a default
// logical socket the route cannot prove app-owned, is "not live". stop keeps
// its exact zero-write refusal and start keeps its materializing path.
func TestProjectLifecycleOutsideTmuxWithoutAnAppServerKeepsTheOfflineBehavior(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		absent  bool
		foreign bool
	}{
		{"no server", true, false},
		{"server not app-owned", false, true},
	} {
		t.Run(test.name+"/stop refuses", func(t *testing.T) {
			t.Parallel()
			cmd, store, executor := lifecycleVerbFixture(t, projectLifecycleStop, false, nil)
			// The session inspector claims the session so a regression to the
			// default-server read would be visible as a different outcome.
			executor.exists = map[string]bool{"alpha": true}
			runner := &lifecycleAppServerRunner{live: map[string]bool{"alpha": true}, absent: test.absent, foreign: test.foreign}
			cmd.switcher.tmuxRunner = runner

			stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
			want := "stop project: project/alpha has no live persistent session; nothing was changed"
			if err == nil || err.Error() != want || exitCodeOf(err) != 2 {
				t.Fatalf("outside-tmux stop error = %v (exit %d), want %q with exit 2", err, exitCodeOf(err), want)
			}
			if stdout != "" || store.writes != 0 || executor.killSessionName != "" {
				t.Fatalf("a refused stop changed state: stdout=%q writes=%d killed=%q", stdout, store.writes, executor.killSessionName)
			}
			if probes := runner.hasSessionCalls(); len(probes) != 0 {
				t.Fatalf("liveness probed a server the route did not establish: %v", probes)
			}
			for _, call := range runner.calls {
				if slices.Contains(call.args, "kill-session") {
					t.Fatalf("a refused stop issued %v", call.args)
				}
			}
		})
	}

	t.Run("no server/start materializes", func(t *testing.T) {
		t.Parallel()
		cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleStart, false, nil)
		executor.exists = map[string]bool{"alpha": true}
		runner := &lifecycleAppServerRunner{absent: true}
		cmd.switcher.tmuxRunner = runner
		stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err != nil {
			t.Fatalf("start project error = %v", err)
		}
		if !strings.Contains(stdout, "runtime="+string(cli.RuntimeMaterialized)) || !executor.authorizeCalled {
			t.Fatalf("start without a live app session = %q authorize=%t, want the materializing path", stdout, executor.authorizeCalled)
		}
	})
}

// TestProjectLifecycleInsideTmuxKeepsTheSessionInspector is the control: with
// `$TMUX` set, liveness is the session inspector's answer and the default app
// socket is never consulted for it, whatever the app server holds.
func TestProjectLifecycleInsideTmuxKeepsTheSessionInspector(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		inspector   map[string]bool
		appServer   map[string]bool
		wantRuntime cli.RuntimeEffect
	}{
		{"inspector live, app server empty", map[string]bool{"alpha": true}, nil, cli.RuntimeAlreadyLive},
		{"inspector empty, app server live", nil, map[string]bool{"alpha": true}, cli.RuntimeMaterialized},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleStart, true, test.inspector)
			executor.exists = test.inspector
			runner := &lifecycleAppServerRunner{live: test.appServer}
			cmd.switcher.tmuxRunner = runner

			stdout, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
			if err != nil {
				t.Fatalf("inside-tmux start project error = %v", err)
			}
			if !strings.Contains(stdout, "runtime="+string(test.wantRuntime)) {
				t.Fatalf("inside-tmux start project stdout = %q, want runtime=%s", stdout, test.wantRuntime)
			}
			if probes := runner.hasSessionCalls(); len(probes) != 0 {
				t.Fatalf("inside tmux liveness probed the app server: %v", probes)
			}
		})
	}

	t.Run("stop refuses on the inspector's answer", func(t *testing.T) {
		t.Parallel()
		cmd, _, executor := lifecycleVerbFixture(t, projectLifecycleStop, true, nil)
		runner := &lifecycleAppServerRunner{live: map[string]bool{"alpha": true}}
		cmd.switcher.tmuxRunner = runner
		_, _, err := runRoute(t, cmd, "project", "uid:prj-alpha")
		if err == nil || !strings.Contains(err.Error(), "no live persistent session; nothing was changed") {
			t.Fatalf("inside-tmux stop error = %v, want the inspector's offline refusal", err)
		}
		if executor.killSessionName != "" || len(runner.hasSessionCalls()) != 0 || runner.routeResolutions() != 0 {
			t.Fatalf("inside-tmux stop consulted the default app socket: %#v", runner.calls)
		}
	})
}
