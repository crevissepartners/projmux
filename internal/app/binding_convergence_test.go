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
	cmd := &tmuxCommand{
		runner:            runner,
		resources:         store.store(),
		bindingReconciler: bindingFixtureReconciler(root),
	}
	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.convergeRuntimeBindings(context.Background(), target); err != nil {
		t.Fatalf("first convergence: %v", err)
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
	if err := cmd.convergeRuntimeBindings(context.Background(), target); err != nil {
		t.Fatalf("repeat convergence: %v", err)
	}
	if got := bindingWriteCalls(primary.calls); got != 0 {
		t.Fatalf("repeat convergence issued %d binding writes: %v", got, primary.calls)
	}
	if store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("repeat convergence rewrote registry: writes=%d", store.writes)
	}
}

func TestGeneratedLifecycleUsesSynchronousExactSocketPathBoundary(t *testing.T) {
	t.Parallel()

	config := tmuxAppConfig("/opt/projmux", "/bin/zsh", "")
	for _, hook := range []string{"after-new-window", "after-split-window"} {
		line := "set-hook -g " + hook
		if !strings.Contains(config, line) {
			t.Fatalf("generated app config missing %s", line)
		}
	}
	for _, want := range []string{"internal tmux reconcile-bindings", "--socket-path", "#{socket_path}", "--session", "#{session_id}"} {
		if !strings.Contains(config, want) {
			t.Fatalf("generated app config missing %q", want)
		}
	}
	for line := range strings.SplitSeq(config, "\n") {
		if strings.Contains(line, "reconcile-bindings") && strings.Contains(line, "run-shell -b") {
			t.Fatalf("binding lifecycle hook became asynchronous: %s", line)
		}
	}
	if strings.Contains(config, "reconcile-bindings --socket ") || strings.Contains(config, "reconcile-bindings >/dev") {
		t.Fatal("generated lifecycle has an implicit/default socket route")
	}

	var got explicitTmuxTarget
	path := filepath.Join(t.TempDir(), "managed.sock")
	server := newFakeTmux()
	server.addSession("alpha")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + path: server}}
	cmd := &tmuxCommand{runner: runner, bindingConverger: func(_ context.Context, target explicitTmuxTarget) error {
		got = target
		return nil
	}}
	var stderr bytes.Buffer
	if err := cmd.runReconcileBindings([]string{"--socket-path", path, "--session", "alpha"}, &stderr); err != nil {
		t.Fatalf("hidden lifecycle command: %v; stderr=%q", err, stderr.String())
	}
	if got.flag != "-S" || got.value != path {
		t.Fatalf("lifecycle target = %+v, want exact -S %s", got, path)
	}
	for _, args := range [][]string{{}, {"--socket-path", "relative", "--session", "alpha"}, {"--socket-path", path}, {"--socket-path", path, "--session", "alpha", "extra"}} {
		if err := cmd.runReconcileBindings(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("hidden lifecycle accepted implicit/malformed target: %v", args)
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
	converged := 0
	cmd := &tmuxCommand{
		runner: runner,
		bindingConverger: func(_ context.Context, _ explicitTmuxTarget) error {
			converged++
			return nil
		},
	}

	if err := cmd.runReconcileBindings([]string{"--socket-path", path, "--session", "alpha"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("active create hook: %v", err)
	}
	if converged != 0 {
		t.Fatalf("active create hook entered registry convergence %d times", converged)
	}

	delete(session.env, createOperationEnvironment)
	if err := cmd.runReconcileBindings([]string{"--socket-path", path, "--session", "alpha"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("standalone lifecycle hook: %v", err)
	}
	if converged != 1 {
		t.Fatalf("standalone lifecycle convergence calls = %d, want 1", converged)
	}
}

func TestLifecycleClearsStaleCreateLeaseBeforeSynchronousConvergence(t *testing.T) {
	t.Parallel()

	server := newFakeTmux()
	session := server.addSession("alpha")
	session.env[createOperationEnvironment] = "v1:999999999:1:op-stale"
	path := filepath.Join(t.TempDir(), "managed.sock")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + path: server}}
	converged := 0
	cmd := &tmuxCommand{
		runner: runner,
		bindingConverger: func(_ context.Context, _ explicitTmuxTarget) error {
			converged++
			return nil
		},
	}

	if err := cmd.runReconcileBindings([]string{"--socket-path", path, "--session", "alpha"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("stale create hook: %v", err)
	}
	if converged != 1 {
		t.Fatalf("stale create convergence calls = %d, want 1", converged)
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
	var targets []explicitTmuxTarget
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
		runner: runner,
		bindingConverger: func(_ context.Context, target explicitTmuxTarget) error {
			targets = append(targets, target)
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated"}, &stdout, &stderr); err != nil {
		t.Fatalf("apply: %v; stderr=%q", err, stderr.String())
	}
	if want := []explicitTmuxTarget{{flag: "-L", value: "isolated"}}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("convergence targets = %+v, want %+v", targets, want)
	}
	calls := runner.calls
	if len(calls) < 2 || !reflect.DeepEqual(calls[0].args[:2], []string{"-L", "isolated"}) || !slices.Contains(calls[1].args, "source-file") {
		t.Fatalf("reload did not precede same-socket convergence: %+v", calls)
	}
	if configWrites != 1 {
		t.Fatalf("first apply config writes = %d, want 1", configWrites)
	}

	// A normal repeat apply still reloads and converges, but an identical
	// generated config is not byte-written again.
	targets = nil
	runner.calls = nil
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if configWrites != 1 {
		t.Fatalf("repeat apply rewrote generated config: writes=%d", configWrites)
	}
	if want := []explicitTmuxTarget{{flag: "-L", value: "isolated"}}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("repeat convergence targets = %+v, want %+v", targets, want)
	}

	targets = nil
	runner.calls = nil
	if err := cmd.runApply([]string{"--config", configPath, "--socket", "isolated", "--no-reload"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 || len(runner.calls) != 0 {
		t.Fatalf("--no-reload touched live state: targets=%v calls=%v", targets, runner.calls)
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
	if len(targets) != 0 || len(runner.calls) != 0 {
		t.Fatalf("failed preflight touched live state: targets=%v calls=%v", targets, runner.calls)
	}
}
