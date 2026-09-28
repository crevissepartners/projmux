package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// outsideTmuxStartupRoute is the production Continue route resolver as a
// process outside tmux sees it: no TMUX, no TMUX_PANE, no private anchor.
func outsideTmuxStartupRoute(runner tmuxCommandRunner) func(context.Context, string) (runtimeMutationRoute, error) {
	return func(ctx context.Context, anchor string) (runtimeMutationRoute, error) {
		return resolveInvocationRuntimeMutationRouteWithAnchor(ctx, runner, func(string) string { return "" }, anchor)
	}
}

// TestContinueWithNoAppServerBringsItUp is `start project` with no app server
// running: Continue does not refuse before the topology run for want of an
// existing server. It starts the app server the way `create window` does and
// materializes the saved topology on it.
func TestContinueWithNoAppServerBringsItUp(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
	server.serverAbsent = true
	activation.resolveRoute = outsideTmuxStartupRoute(activation.runner)
	// A fresh server starts from the generated app config, as it does for
	// create; the fixture materializer otherwise has none to hand tmux.
	configPath := filepath.Join(t.TempDir(), "projmux", "tmux.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("set-option -g @projmux_app 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := &fakeSessionMaterializer{tmux: server}
	activation.newMaterializer = func(exact tmuxCommandRunner, warn io.Writer) *materializer {
		return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: sessions, warn: warn, configPath: configPath}
	}

	materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil || !materialized {
		t.Fatalf("MaterializeProjectTopology() with no app server = %t, %v; want true, nil", materialized, err)
	}
	session := server.session("beta")
	if session == nil || session.opts[tmuxopts.ProjectUIDSession] != "prj-beta" || len(session.windows) != 2 {
		t.Fatalf("no-server Continue topology = %s", server.state())
	}
	if got := len(session.windows[0].panes); got != 2 {
		t.Fatalf("main window panes = %d, want 2\n%s", got, server.state())
	}
	started := slices.IndexFunc(server.calls, func(call []string) bool { return slices.Contains(tmuxCommandArgv(call), "new-session") })
	if started < 0 || !slices.Contains(server.calls[started], configPath) {
		t.Fatalf("no-server Continue did not start the server from the app config: %#v", server.calls)
	}
	project, _ := store.registry.Project("prj-beta")
	if project.Status.Session == nil || project.Status.Session.Name != "beta" || !project.Status.Session.Live {
		t.Fatalf("Project status.session = %+v", project.Status.Session)
	}
}

// routeReadFailingRunner answers the socket-path probe with one fixed failure
// and passes every other call through, so a test can choose why the route
// cannot be read.
type routeReadFailingRunner struct {
	inner   tmuxCommandRunner
	failure error
}

func (r routeReadFailingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if slices.Contains(args, "#{socket_path}") {
		return nil, r.failure
	}
	return r.inner.Run(ctx, name, args...)
}

// TestContinueStillRefusesARouteThatIsNotAnAbsentServer keeps every other
// route failure a refusal: only a server that is not running lets Continue
// start one. A server it cannot connect to for another reason, and a server
// that is running but is not the app's, are not absence.
func TestContinueStillRefusesARouteThatIsNotAnAbsentServer(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*registryProjectTopologyMaterializer, *fakeTmux)
		want  string
	}{
		{
			name: "socket the client cannot connect to",
			setup: func(activation *registryProjectTopologyMaterializer, _ *fakeTmux) {
				activation.runner = routeReadFailingRunner{inner: activation.runner, failure: appTypedCommandFailure{failure: inttmux.CommandFailure{
					Kind: inttmux.CommandFailureExit, Stderr: "error connecting to /tmp/fake-tmux/projmux (Permission denied)",
				}}}
				activation.resolveRoute = outsideTmuxStartupRoute(activation.runner)
			},
			want: "probe default logical socket",
		},
		{
			name: "socket the client cannot connect to, read by the recovery itself",
			setup: func(activation *registryProjectTopologyMaterializer, _ *fakeTmux) {
				activation.runner = routeReadFailingRunner{inner: activation.runner, failure: appTypedCommandFailure{failure: inttmux.CommandFailure{
					Kind: inttmux.CommandFailureExit, Stderr: "error connecting to /tmp/fake-tmux/projmux (Permission denied)",
				}}}
			},
			want: "typed tmux command failure",
		},
		{
			name: "running server without the app marker",
			setup: func(activation *registryProjectTopologyMaterializer, server *fakeTmux) {
				server.appMarker = ""
				activation.resolveRoute = outsideTmuxStartupRoute(activation.runner)
			},
			want: "not app-owned",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
			test.setup(activation, server)
			writes := store.writes

			materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
			if err == nil || materialized || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("MaterializeProjectTopology() = %t, %v; want a refusal naming %q", materialized, err, test.want)
			}
			if store.writes != writes {
				t.Fatalf("refused Continue wrote the Registry: %d -> %d", writes, store.writes)
			}
			for _, call := range server.calls {
				if argv := tmuxCommandArgv(call); slices.Contains(argv, "new-session") || slices.Contains(argv, "new-window") {
					t.Fatalf("refused Continue mutated the runtime: %#v", call)
				}
			}
		})
	}
}
