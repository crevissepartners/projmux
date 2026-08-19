package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type routedTmuxCall struct {
	flag  string
	value string
	args  []string
}

// routedTmuxRunner is two independent fake tmux servers behind explicit -L/-S
// targets. There is intentionally no default server and no environment lookup.
type routedTmuxRunner struct {
	servers map[string]*fakeTmux
	calls   []routedTmuxCall
}

func (r *routedTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || args[0] != "-L" && args[0] != "-S" {
		return nil, fmt.Errorf("routed fake requires explicit tmux -L/-S, got %s %v", name, args)
	}
	target := args[0] + "\x00" + args[1]
	server := r.servers[target]
	if server == nil {
		return nil, fmt.Errorf("no fake tmux server for %q", target)
	}
	r.calls = append(r.calls, routedTmuxCall{flag: args[0], value: args[1], args: slices.Clone(args[2:])})
	return server.Run(ctx, name, args[2:]...)
}

func bindingFixture(t *testing.T, root string) coremetadata.Registry {
	t.Helper()
	registry := driftedRegistry(t, root)
	registry.Projects[0].Status.Session.Live = true
	registry.Windows[0].Metadata.DisplayName = "runtime-0"
	registry.Windows[1].Metadata.DisplayName = "runtime-1"
	return registry
}

func bindingFixtureServer() *fakeTmux {
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "runtime-0"
	second := &fakeTmuxWindow{id: tmux.mint("@"), name: "runtime-1", opts: map[string]string{}}
	second.panes = append(second.panes, &fakeTmuxPane{id: tmux.mint("%"), opts: map[string]string{}})
	session.windows = append(session.windows, second)
	return tmux
}

func bindingFixtureReconciler(root string) func(tmuxCommandRunner, sessionLister) *registryReconciler {
	return func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
		mirror := intmetadata.NewMirror(runner)
		return &registryReconciler{
			discoverRoots: func() ([]string, error) { return []string{root}, nil },
			liveSessions:  sessions.ExistingSessions,
			observeLegacy: mirror.ObserveLegacySessionTargets,
			mirror:        mirror,
			shell:         "/bin/zsh",
			sessionNameFor: func(string) string {
				return driftedSessionName
			},
		}
	}
}

// recordingTriggering records what a route handed the controller entrypoint and
// converges nothing. It is the seam that lets a route's argv contract be pinned
// without a reconciliation in the way.
type recordingTriggering struct {
	triggers []controllerTrigger
	err      error
}

func (r *recordingTriggering) run(_ context.Context, trigger controllerTrigger) (controllerTriggerOutcome, error) {
	r.triggers = append(r.triggers, trigger)
	if r.err != nil {
		return controllerTriggerOutcome{reason: trigger.reason}, r.err
	}
	return controllerTriggerOutcome{reason: trigger.reason, passes: 1, converged: true}, nil
}

func (r *recordingTriggering) targets() []explicitTmuxTarget {
	out := make([]explicitTmuxTarget, 0, len(r.triggers))
	for _, trigger := range r.triggers {
		out = append(out, trigger.target)
	}
	return out
}

// isolatedTriggerRunner builds the production controller entrypoint with its
// event log and worker lease under a temporary directory, so a test converges
// for real without sharing the user's state dir or another test's lease.
func isolatedTriggerRunner(t *testing.T, runner tmuxCommandRunner, store *resourceStore,
	newReconciler func(tmuxCommandRunner, sessionLister) *registryReconciler) *controllerTriggerRunner {
	t.Helper()
	return &controllerTriggerRunner{
		runner:        runner,
		store:         store,
		events:        controllerEventLog{dir: t.TempDir()},
		newReconciler: newReconciler,
	}
}

func bindingWriteCalls(calls [][]string) int {
	count := 0
	for _, call := range calls {
		if len(call) > 0 && slices.Contains([]string{"set-option", "rename-window"}, call[0]) {
			count++
		}
	}
	return count
}

// TestBindingConvergenceRepairsOnlyTheExplicitSocketAndThenBecomesANoop
// covers the bindable/repeat and two-socket cells. It uses the real matcher,
// reconciler and Mirror; only the registry file and tmux subprocess are fakes.
func TestBindingConvergenceRepairsOnlyTheExplicitSocketAndThenBecomesANoop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	primary := bindingFixtureServer()
	secondary := bindingFixtureServer()
	secondaryBefore := secondary.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary": primary,
		"-L\x00second":  secondary,
	}}
	store := &fakeResourceStore{registry: bindingFixture(t, root), dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	registryBefore := store.snapshot()
	trigger := isolatedTriggerRunner(t, runner, store.store(), bindingFixtureReconciler(root))
	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	first, err := trigger.run(context.Background(), controllerTrigger{
		reason: controllerTriggerConfigApply, target: target,
	})
	if err != nil {
		t.Fatalf("first convergence: %v", err)
	}
	if !first.converged || first.passes < 1 {
		t.Fatalf("first convergence did not report convergence: %s", first.describe())
	}

	session := primary.session(driftedSessionName)
	for wi, wantWindow := range []string{"win-first", "win-second"} {
		if got := session.windows[wi].opts[tmuxopts.WindowUID]; got != wantWindow {
			t.Fatalf("window %d uid = %q, want %q", wi, got, wantWindow)
		}
		wantPane := []string{"pan-first", "pan-second"}[wi]
		if got := session.windows[wi].panes[0].opts[tmuxopts.PaneUID]; got != wantPane {
			t.Fatalf("pane %d uid = %q, want %q", wi, got, wantPane)
		}
	}
	if got := store.snapshot(); got != registryBefore || store.writes != 0 {
		t.Fatalf("binding-only repair rewrote registry: writes=%d\n--- got ---\n%s\n--- want ---\n%s", store.writes, got, registryBefore)
	}
	if got := secondary.state(); got != secondaryBefore {
		t.Fatalf("secondary socket changed:\n--- got ---\n%s\n--- want ---\n%s", got, secondaryBefore)
	}
	for _, call := range runner.calls {
		if call.flag != "-L" || call.value != "primary" {
			t.Fatalf("convergence escaped explicit socket: %+v", call)
		}
	}

	primary.calls = nil
	runner.calls = nil
	repeat, err := trigger.run(context.Background(), controllerTrigger{
		reason: controllerTriggerConfigApply, target: target,
	})
	if err != nil {
		t.Fatalf("repeat convergence: %v", err)
	}
	if !repeat.converged || repeat.passes != 1 || repeat.changed != 0 {
		t.Fatalf("repeat convergence was not a single no-op pass: %s", repeat.describe())
	}
	if got := bindingWriteCalls(primary.calls); got != 0 {
		t.Fatalf("repeat convergence issued %d binding writes: %v", got, primary.calls)
	}
	if store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("repeat convergence rewrote registry: writes=%d", store.writes)
	}
}

// TestGeneratedLifecycleTriggersConvergeOnOneEntrypoint is the trigger-inventory
// assertion: every generated lifecycle hook, in both host configs, invokes the
// one controller route with an exact socket path and a declared reason, and no
// hook retains a route of its own.
func TestGeneratedLifecycleTriggersConvergeOnOneEntrypoint(t *testing.T) {
	t.Parallel()

	// The two exit hooks are in both configs; the two creation hooks are
	// app-config only. That asymmetry is the adoption boundary: a convergence
	// caused by a new runtime object mints and rebinds, and the standalone snippet
	// runs on every server the operator starts, where a raw `new-window` in a
	// session projmux does not own must stay unmanaged.
	hooks := map[string]controllerTriggerReason{
		"pane-exited":     controllerTriggerRuntimeExited,
		"after-kill-pane": controllerTriggerRuntimeExited,
	}
	appOnlyHooks := map[string]controllerTriggerReason{
		"after-new-window":   controllerTriggerRuntimeCreated,
		"after-split-window": controllerTriggerRuntimeCreated,
	}
	for name, config := range map[string]string{
		"app":        tmuxAppConfig("/opt/projmux", "/bin/zsh", ""),
		"standalone": tmuxStandaloneConfig("/opt/projmux", config.StatusbarDecorationOff),
	} {
		expected := map[string]controllerTriggerReason{}
		for hook, reason := range hooks {
			expected[hook] = reason
		}
		if name == "app" {
			for hook, reason := range appOnlyHooks {
				expected[hook] = reason
			}
		} else {
			for hook := range appOnlyHooks {
				if strings.Contains(config, "set-hook -g "+hook+" ") {
					t.Fatalf("standalone config installs the %s creation hook, which would adopt raw windows on every server the operator starts", hook)
				}
			}
		}
		for hook, reason := range expected {
			line := ""
			for candidate := range strings.SplitSeq(config, "\n") {
				if strings.HasPrefix(candidate, "set-hook -g "+hook+" ") {
					line = candidate
				}
			}
			if line == "" {
				t.Fatalf("%s config has no %s hook", name, hook)
			}
			for _, want := range []string{"internal tmux converge", "--socket-path", "#{socket_path}", "--session", "#{session_id}", "--reason " + string(reason)} {
				if !strings.Contains(line, want) {
					t.Fatalf("%s %s hook missing %q: %s", name, hook, want, line)
				}
			}
			// The creation boundary stays synchronous: the next implicit read
			// must not race the binding write. The exit boundary stays
			// backgrounded so closing a pane never waits on convergence.
			background := strings.Contains(line, "run-shell -b")
			if want := reason == controllerTriggerRuntimeExited; background != want {
				t.Fatalf("%s %s hook backgrounded = %t, want %t: %s", name, hook, background, want, line)
			}
		}
		// The two retired routes must not survive anywhere in either config.
		for _, retired := range []string{"reconcile-bindings", "release-dead-agent-panes"} {
			if strings.Contains(config, retired) {
				t.Fatalf("%s config still invokes the retired %s route", name, retired)
			}
		}
		if strings.Contains(config, "converge --socket ") || strings.Contains(config, "converge >/dev") {
			t.Fatalf("%s config has an implicit/default socket trigger", name)
		}
	}

	path := filepath.Join(t.TempDir(), "managed.sock")
	server := newFakeTmux()
	server.addSession("alpha")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + path: server}}
	recorder := &recordingTriggering{}
	cmd := &tmuxCommand{runner: runner, triggerRunner: recorder}
	var stderr bytes.Buffer
	if err := cmd.runConverge([]string{"--socket-path", path, "--session", "alpha", "--reason", "runtime-created"}, &stderr); err != nil {
		t.Fatalf("hidden lifecycle command: %v; stderr=%q", err, stderr.String())
	}
	want := []controllerTrigger{{reason: controllerTriggerRuntimeCreated, target: explicitTmuxTarget{flag: "-S", value: path}, session: "alpha"}}
	if !reflect.DeepEqual(recorder.triggers, want) {
		t.Fatalf("trigger = %+v, want %+v", recorder.triggers, want)
	}
	for _, args := range [][]string{
		{},
		{"--socket-path", "relative", "--session", "alpha", "--reason", "runtime-created"},
		{"--socket-path", path, "--session", "alpha"},
		{"--socket-path", path, "--session", "alpha", "--reason", "made-up"},
		{"--socket-path", path, "--session", "alpha", "--reason", "runtime-created", "extra"},
	} {
		before := len(recorder.triggers)
		if err := cmd.runConverge(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("hidden lifecycle accepted implicit/malformed trigger: %v", args)
		}
		if len(recorder.triggers) != before {
			t.Fatalf("refused trigger %v still reached the controller", args)
		}
	}
}

func TestLifecycleDefersOnlyALiveCreateOperationOnTheExactSession(t *testing.T) {
	t.Parallel()

	server := newFakeTmux()
	session := server.addSession("alpha")
	session.env[createOperationEnvironment] = newCreateOperationMarker("op-test")
	path := filepath.Join(t.TempDir(), "managed.sock")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + path: server}}
	store := &fakeResourceStore{registry: coremetadata.Registry{}, now: resourceFixtureClock}
	trigger := isolatedTriggerRunner(t, runner, store.store(), bindingFixtureReconciler(t.TempDir()))
	target, err := tmuxSocketPathTarget(path)
	if err != nil {
		t.Fatal(err)
	}

	active, err := trigger.run(context.Background(), controllerTrigger{
		reason: controllerTriggerRuntimeCreated, target: target, session: "alpha",
	})
	if err != nil {
		t.Fatalf("active create hook: %v", err)
	}
	if active.passes != 0 || active.deferred == "" {
		t.Fatalf("active create hook converged anyway: %s", active.describe())
	}
	if store.transactions != 0 {
		t.Fatalf("active create hook opened %d registry transactions", store.transactions)
	}

	delete(session.env, createOperationEnvironment)
	deferred, err := trigger.run(context.Background(), controllerTrigger{
		reason: controllerTriggerRuntimeCreated, target: target, session: "alpha",
	})
	if err != nil {
		t.Fatalf("standalone lifecycle hook: %v", err)
	}
	if deferred.deferred != "" || deferred.passes != 1 {
		t.Fatalf("standalone lifecycle hook did not converge once: %s", deferred.describe())
	}
}

func TestLifecycleClearsStaleCreateLeaseBeforeSynchronousConvergence(t *testing.T) {
	t.Parallel()

	server := newFakeTmux()
	session := server.addSession("alpha")
	session.env[createOperationEnvironment] = "v1:999999999:1:op-stale"
	path := filepath.Join(t.TempDir(), "managed.sock")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + path: server}}
	store := &fakeResourceStore{registry: coremetadata.Registry{}, now: resourceFixtureClock}
	trigger := isolatedTriggerRunner(t, runner, store.store(), bindingFixtureReconciler(t.TempDir()))
	target, err := tmuxSocketPathTarget(path)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := trigger.run(context.Background(), controllerTrigger{
		reason: controllerTriggerRuntimeCreated, target: target, session: "alpha",
	})
	if err != nil {
		t.Fatalf("stale create hook: %v", err)
	}
	if outcome.deferred != "" || outcome.passes != 1 {
		t.Fatalf("a stale lease blocked convergence: %s", outcome.describe())
	}
	if got := session.env[createOperationEnvironment]; got != "" {
		t.Fatalf("stale create lease survived: %q", got)
	}
}

func TestApplyConvergesOnlyAfterSuccessfulReloadOnTheSameSocket(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, "tmux.conf")
	runner := &recordingTmuxRunner{}
	recorder := &recordingTriggering{}
	configWrites := 0
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, ".config")
			case "XDG_STATE_HOME":
				return filepath.Join(home, ".state")
			default:
				return ""
			}
		},
		readFile: os.ReadFile,
		writeFile: func(path string, data []byte, mode os.FileMode) error {
			configWrites++
			return os.WriteFile(path, data, mode)
		},
		runner:        runner,
		triggerRunner: recorder,
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated"}, &stdout, &stderr); err != nil {
		t.Fatalf("apply: %v; stderr=%q", err, stderr.String())
	}
	if want := []explicitTmuxTarget{{flag: "-L", value: "isolated"}}; !reflect.DeepEqual(recorder.targets(), want) {
		t.Fatalf("convergence targets = %+v, want %+v", recorder.targets(), want)
	}
	// Apply carries no hook session: it is not caused by a create, so it has no
	// create-operation lease to defer to.
	for _, trigger := range recorder.triggers {
		if trigger.reason != controllerTriggerConfigApply || trigger.session != "" {
			t.Fatalf("apply trigger = %+v, want config-apply with no session", trigger)
		}
	}
	calls := runner.calls
	sourceIdx := slices.IndexFunc(calls, func(call recordedTmuxCall) bool {
		return slices.Contains(call.args, "source-file")
	})
	if len(calls) < 2 || !reflect.DeepEqual(calls[0].args[:2], []string{"-L", "isolated"}) || sourceIdx != len(calls)-1 {
		t.Fatalf("reload did not precede same-socket convergence: %+v", calls)
	}
	if configWrites != 1 {
		t.Fatalf("first apply config writes = %d, want 1", configWrites)
	}

	// A normal repeat apply still reloads and converges, but an identical
	// generated config is not byte-written again.
	recorder.triggers = nil
	runner.calls = nil
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if configWrites != 1 {
		t.Fatalf("repeat apply rewrote generated config: writes=%d", configWrites)
	}
	if want := []explicitTmuxTarget{{flag: "-L", value: "isolated"}}; !reflect.DeepEqual(recorder.targets(), want) {
		t.Fatalf("repeat convergence targets = %+v, want %+v", recorder.targets(), want)
	}

	recorder.triggers = nil
	runner.calls = nil
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated", "--no-reload"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.triggers) != 0 || len(runner.calls) != 0 {
		t.Fatalf("--no-reload touched live state: triggers=%v calls=%v", recorder.triggers, runner.calls)
	}

	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymap), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymap, []byte("[bindings.new-window]\nkeys = [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("malformed keymap preflight unexpectedly succeeded")
	}
	if len(recorder.triggers) != 0 || len(runner.calls) != 0 {
		t.Fatalf("failed preflight touched live state: triggers=%v calls=%v", recorder.triggers, runner.calls)
	}
}
