package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const routeSequenceSocket = "/tmp/fake-tmux/route-sequence"

// routeProofRunner is one scripted server that records every tmux exec whole,
// so a test can count invocations rather than reads. A read sequence is
// answered part by part, the way tmux runs it.
type routeProofRunner struct {
	execs   [][]string
	socket  string
	pid     string
	app     *string // nil: the option is unset and prints no line
	logical *string
	failKey string
	failErr error
}

func routeProofValue(value string) *string { return &value }

func newRouteProofRunner() *routeProofRunner {
	return &routeProofRunner{
		socket: routeSequenceSocket, pid: "4242",
		app: routeProofValue("1"), logical: routeProofValue(defaultAppSocket),
	}
}

func (r *routeProofRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.execs = append(r.execs, slices.Clone(args))
	if handled, out, err := answerTmuxReadSequence(ctx, name, args, r.answer); handled {
		return out, err
	}
	return r.answer(ctx, name, args...)
}

func (r *routeProofRunner) answer(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) >= 2 && (args[0] == "-S" || args[0] == "-L") {
		args = args[2:]
	}
	key := strings.Join(args, " ")
	if key == r.failKey {
		return nil, r.failErr
	}
	option := func(value *string) []byte {
		if value == nil {
			return nil
		}
		return []byte(*value + "\n")
	}
	switch key {
	case "display-message -p -F #{socket_path}":
		return []byte(r.socket + "\n"), nil
	case "display-message -p -F #{pid}":
		return []byte(r.pid + "\n"), nil
	case "show-options -gqv " + tmuxopts.AppGlobal:
		return option(r.app), nil
	case "show-options -gqv " + runtimeMutationSocketNameOption:
		return option(r.logical), nil
	}
	return nil, fmt.Errorf("route proof runner: unexpected tmux %q", key)
}

func routeSequenceTarget() tmuxTransport {
	return tmuxTransport{Kind: tmuxSocketPath, Value: routeSequenceSocket, Source: tmuxSocketPathSource}
}

func newRouteSequenceMaterializer(runner tmuxCommandRunner, class string) *materializer {
	target := routeSequenceTarget()
	return &materializer{
		runner: explicitTmuxRunner{runner: runner, target: target},
		target: target, expectedSocketPath: routeSequenceSocket, socketName: defaultAppSocket,
		routeAuthority: &runtimeMutationRouteAuthority{Class: class, ServerPID: "4242"},
	}
}

func routeSequenceRoute(class string) runtimeMutationRoute {
	return runtimeMutationRoute{
		target: routeSequenceTarget(), expectedSocketPath: routeSequenceSocket, socketName: defaultAppSocket,
		authority: &runtimeMutationRouteAuthority{Class: class, ServerPID: "4242"},
	}
}

// TestExactRouteProofIsOneTmuxInvocation pins the cost of one route proof on a
// bound physical socket with a captured app generation: the socket path, the
// server generation, and both ownership markers are read by ONE tmux exec, a
// ";"-separated sequence through the exact -S socket.
func TestExactRouteProofIsOneTmuxInvocation(t *testing.T) {
	t.Parallel()

	runner := newRouteProofRunner()
	runtime := newRouteSequenceMaterializer(runner, runtimeMutationRouteApp)
	if err := runtime.guardExactRoute(context.Background(), false, routeSequenceSocket); err != nil {
		t.Fatalf("guardExactRoute() = %v", err)
	}
	if len(runner.execs) != 1 {
		t.Fatalf("one route proof issued %d tmux execs, want 1:\n%s", len(runner.execs), joinedExecs(runner.execs))
	}
	boundary := []string{";", "display-message", "-p", "-F", routeReadBoundary, ";"}
	want := slices.Concat(
		[]string{"-S", routeSequenceSocket},
		routeReadSocketPath, boundary,
		routeReadServerPID, boundary,
		routeReadAppMarker, boundary,
		routeReadSocketNameMarker,
	)
	if !slices.Equal(runner.execs[0], want) {
		t.Fatalf("route proof argv =\n  %q\nwant\n  %q", runner.execs[0], want)
	}
}

func joinedExecs(execs [][]string) string {
	lines := make([]string, 0, len(execs))
	for _, argv := range execs {
		lines = append(lines, "  "+strings.Join(argv, " "))
	}
	return strings.Join(lines, "\n")
}

// TestRouteProofRefusalsKeepTheirTextInOneInvocation drives every value one
// proof compares to a wrong answer and pins the refusal each gives, for the
// materializer's exact-route proof and the resolved-route proof. The texts are
// the ones each separate read gave before the reads were sequenced.
func TestRouteProofRefusalsKeepTheirTextInOneInvocation(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected tmux failure")
	socketFailure := func(r *routeProofRunner) {
		r.failKey, r.failErr = "display-message -p -F #{socket_path}", injected
	}
	const (
		notAppOwned    = "planned runtime socket is not app-owned"
		markerDrifted  = "planned runtime socket logical route marker drifted"
		pidDrifted     = "planned runtime server generation drifted"
		classDrifted   = "planned standalone runtime route class drifted"
		appPIDDrifted  = "runtime mutation route: exact app server generation drifted"
		appNotAppOwned = "runtime mutation route: exact server is not app-owned"
	)
	for _, test := range []struct {
		name  string
		class string
		// appRoute runs only the exact proof's app-route variant, which does not
		// read the logical marker and has no resolved-route counterpart.
		appRoute     bool
		mutate       func(*routeProofRunner)
		wantExact    string
		wantResolved string
	}{
		{name: "proved", class: runtimeMutationRouteApp, mutate: func(*routeProofRunner) {}},
		{
			name: "socket path drift", class: runtimeMutationRouteApp,
			mutate:       func(r *routeProofRunner) { r.socket = "/tmp/fake-tmux/other" },
			wantExact:    fmt.Sprintf("tmux socket drifted: observed %q, planned invocation %q", "/tmp/fake-tmux/other", routeSequenceSocket),
			wantResolved: fmt.Sprintf("runtime socket drifted: observed %q, planned %q", "/tmp/fake-tmux/other", routeSequenceSocket),
		},
		{
			name: "socket path read failure", class: runtimeMutationRouteApp, mutate: socketFailure,
			wantExact:    fmt.Sprintf("reobserve exact tmux route -S=%s: %v", routeSequenceSocket, injected),
			wantResolved: fmt.Sprintf("reobserve planned runtime socket: %v", injected),
		},
		{
			name: "server pid drift", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.pid = "5252" },
			wantExact: pidDrifted, wantResolved: pidDrifted,
		},
		{
			name: "app marker absent", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.app = nil },
			wantExact: notAppOwned, wantResolved: notAppOwned,
		},
		{
			name: "app marker empty", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.app = routeProofValue("") },
			wantExact: notAppOwned, wantResolved: notAppOwned,
		},
		{
			name: "app marker zero", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.app = routeProofValue("0") },
			wantExact: notAppOwned, wantResolved: notAppOwned,
		},
		{
			name: "app marker multi-line", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.app = routeProofValue("1\nforeign") },
			wantExact: notAppOwned, wantResolved: notAppOwned,
		},
		{
			name: "socket name marker absent", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.logical = nil },
			wantExact: markerDrifted, wantResolved: markerDrifted,
		},
		{
			name: "socket name marker different", class: runtimeMutationRouteApp,
			mutate:    func(r *routeProofRunner) { r.logical = routeProofValue("foreign") },
			wantExact: markerDrifted, wantResolved: markerDrifted,
		},
		{
			name: "standalone proved", class: runtimeMutationRouteStandalone,
			mutate: func(r *routeProofRunner) { r.app, r.logical = nil, nil },
		},
		{
			name: "standalone with app marker", class: runtimeMutationRouteStandalone,
			mutate:    func(r *routeProofRunner) { r.logical = nil },
			wantExact: classDrifted, wantResolved: classDrifted,
		},
		{
			name: "standalone with socket name marker", class: runtimeMutationRouteStandaloneExplicit,
			mutate:    func(r *routeProofRunner) { r.app = nil },
			wantExact: classDrifted, wantResolved: classDrifted,
		},
		{name: "app route proved", class: runtimeMutationRouteApp, appRoute: true, mutate: func(r *routeProofRunner) { r.logical = nil }},
		{
			name: "app route server pid drift", class: runtimeMutationRouteApp, appRoute: true,
			mutate: func(r *routeProofRunner) { r.pid = "5252" }, wantExact: appPIDDrifted,
		},
		{
			name: "app route app marker zero", class: runtimeMutationRouteApp, appRoute: true,
			mutate: func(r *routeProofRunner) { r.app = routeProofValue("0") }, wantExact: appNotAppOwned,
		},
		{
			name: "app route app marker absent", class: runtimeMutationRouteApp, appRoute: true,
			mutate: func(r *routeProofRunner) { r.app = nil }, wantExact: appNotAppOwned,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			check := func(proof string, err error, want string) {
				t.Helper()
				got := ""
				if err != nil {
					got = err.Error()
				}
				if got != want {
					t.Fatalf("%s refusal = %q, want %q", proof, got, want)
				}
			}

			exact := newRouteProofRunner()
			test.mutate(exact)
			runtime := newRouteSequenceMaterializer(exact, test.class)
			check("exact-route proof", runtime.proveExactRouteOwnership(context.Background(), false, !test.appRoute, routeSequenceSocket), test.wantExact)
			if test.appRoute {
				return
			}
			resolved := newRouteProofRunner()
			test.mutate(resolved)
			check("resolved-route proof",
				proveResolvedRuntimeMutationRouteObserved(context.Background(), resolved, routeSequenceRoute(test.class), false, nil),
				test.wantResolved)
		})
	}
}

// TestRouteReadSequenceRefusesAValueThatPrintsTheBoundary: a marker value that
// itself prints the boundary line cannot shift the other reads' values. The
// sequence no longer splits into one section per read, and the proof refuses
// with its first read's text.
func TestRouteReadSequenceRefusesAValueThatPrintsTheBoundary(t *testing.T) {
	t.Parallel()

	runner := newRouteProofRunner()
	runner.app = routeProofValue("1\n" + routeReadBoundary + "\n" + defaultAppSocket)
	runtime := newRouteSequenceMaterializer(runner, runtimeMutationRouteApp)
	err := runtime.guardExactRoute(context.Background(), false, routeSequenceSocket)
	want := fmt.Sprintf("reobserve exact tmux route -S=%s: tmux read sequence returned 5 sections, want 4", routeSequenceSocket)
	if err == nil || err.Error() != want {
		t.Fatalf("boundary-printing marker = %v, want %q", err, want)
	}
}

// TestCreateReadsPaneDefaultsBeforeTheRegistryLock: a create's pane launch
// resolves the server's default-shell and default-command, and neither read
// runs while the Registry lock is held. The lock is observed where it is taken,
// at resourceStore.update.
func TestCreateReadsPaneDefaultsBeforeTheRegistryLock(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "window", argv: []string{"window", "--project", "uid:prj-target", "--name", "probe"}},
		{name: "pane", argv: []string{"pane", "--project", "uid:prj-target", "--window", "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCallBudgetFixture(t, 0)
			var held atomic.Bool
			update := fixture.create.store.update
			fixture.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
				held.Store(true)
				defer held.Store(false)
				return update(fn)
			}
			var underLock, total []string
			launched := []string{}
			fixture.tmux.beforeDispatch = func(_ *fakeTmux, args []string) {
				switch {
				case len(args) == 3 && args[0] == "show-options" && args[1] == "-gv" &&
					(args[2] == "default-shell" || args[2] == "default-command"):
					total = append(total, args[2])
					if held.Load() {
						underLock = append(underLock, args[2])
					}
				case len(args) > 0 && (args[0] == "new-window" || args[0] == "split-window"):
					launched = append(launched, strings.Join(args, " "))
				}
			}
			if _, stderr, err := runRoute(t, fixture.create, test.argv...); err != nil {
				t.Fatalf("create %s: %v (stderr %q)", test.name, err, stderr)
			}
			if len(underLock) != 0 {
				t.Fatalf("create %s read %q while the Registry lock was held", test.name, underLock)
			}
			if !slices.Equal(total, []string{"default-shell", "default-command"}) {
				t.Fatalf("create %s pane-default reads = %q, want one of each, before the lock", test.name, total)
			}
			if len(launched) != 1 || !strings.Contains(launched[0], "--argv0 -zsh -- /bin/zsh") {
				t.Fatalf("create %s launch = %q, want one login-shell launch of the resolved /bin/zsh", test.name, launched)
			}
		})
	}
}

// paneDefaultsRunner answers the two pane-default reads, separately or as one
// read sequence, and records every exec.
type paneDefaultsRunner struct {
	execs [][]string
	shell string
	err   error
}

func (r *paneDefaultsRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.execs = append(r.execs, slices.Clone(args))
	if handled, out, err := answerTmuxReadSequence(ctx, name, args, r.answer); handled {
		return out, err
	}
	return r.answer(ctx, name, args...)
}

func (r *paneDefaultsRunner) answer(_ context.Context, _ string, args ...string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	switch strings.Join(args[2:], " ") {
	case "show-options -gv default-shell":
		return []byte(r.shell + "\n"), nil
	case "show-options -gv default-command":
		return []byte("\n"), nil
	}
	return nil, fmt.Errorf("pane defaults runner: unexpected tmux %q", args)
}

// TestPaneDefaultsPrefetchAnswersOnlyForItsSocket: the pre-lock read is one
// tmux exec, and the launch takes it only while the route still reads through
// the socket it came from. A failed prefetch leaves nothing behind, and the
// launch then reads inside the lock as it always did.
func TestPaneDefaultsPrefetchAnswersOnlyForItsSocket(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &paneDefaultsRunner{shell: "/bin/fish"}
	runtime := &materializer{runner: runner, expectedSocketPath: routeSequenceSocket, lookupEnv: func(string) string { return "" }}
	clearMemo := runtime.prefetchPaneDefaults(ctx)
	if len(runner.execs) != 1 {
		t.Fatalf("prefetch issued %d tmux execs, want 1", len(runner.execs))
	}
	runner.shell = "/bin/changed"
	resolved, err := runtime.defaultPaneCommand(ctx)
	if err != nil || !slices.Equal(resolved.argv, []string{"/bin/fish"}) || len(runner.execs) != 1 {
		t.Fatalf("memoized launch = %v, %v after %d execs; want the prefetched /bin/fish and no read", resolved.argv, err, len(runner.execs))
	}

	runtime.expectedSocketPath = "/tmp/fake-tmux/rebound"
	resolved, err = runtime.defaultPaneCommand(ctx)
	if err != nil || !slices.Equal(resolved.argv, []string{"/bin/changed"}) || len(runner.execs) != 3 {
		t.Fatalf("launch after the route changed = %v, %v after %d execs; want two fresh reads", resolved.argv, err, len(runner.execs))
	}
	clearMemo()
	if runtime.paneDefaults != nil {
		t.Fatal("the transaction end left the pane-defaults memo in place")
	}

	failing := &paneDefaultsRunner{err: errors.New("injected tmux failure")}
	runtime = &materializer{runner: failing, expectedSocketPath: routeSequenceSocket}
	defer runtime.prefetchPaneDefaults(ctx)()
	if runtime.paneDefaults != nil {
		t.Fatal("a failed prefetch left a memo")
	}
	if _, err := runtime.defaultPaneCommand(ctx); err == nil || err.Error() != "read tmux default-shell: injected tmux failure" {
		t.Fatalf("launch after a failed prefetch = %v, want today's in-lock read error", err)
	}
}
