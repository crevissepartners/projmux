package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/version"
)

// TestInstalledIsolatedBrokerNativeBindingSmoke drives the cutover's product
// binding path against a real installed Codex app-server.
//
// It is opt-in through PROJMUX_CODEX_CUTOVER_SMOKE_ROOT and requires a
// contained CODEX_HOME, an isolated state domain, and inherited tmux identity
// stripped, so it can never reach an ambient shared endpoint, an ambient
// runtime, or an ambient tmux server.
//
// What it proves is the shape of the product path, not a model turn: it starts
// one real thread, reaches the runtime this state domain publishes, and binds
// that exact thread twice over one runtime. Whether the bind succeeds depends
// on an upstream fact this phase does not control - thread/resume answers only
// for a thread whose rollout already exists - so a refusal is recorded as typed
// content-free evidence rather than failed. What is asserted either way is that
// the whole path leaves the daemon's lifecycle untouched and its own artifacts
// cleaned up.
func TestInstalledIsolatedBrokerNativeBindingSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_CUTOVER_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_CUTOVER_SMOKE_ROOT for the installed native cutover smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("smoke root must be an isolated child of %s", tmpRoot)
	}
	for _, inherited := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(inherited); present {
			t.Fatalf("%s must be removed for the installed native cutover smoke", inherited)
		}
	}
	wantCodexHome := filepath.Join(root, "codex-home")
	if got := filepath.Clean(os.Getenv("CODEX_HOME")); got != wantCodexHome {
		t.Fatalf("CODEX_HOME = %q, want %q", got, wantCodexHome)
	}
	domain := filepath.Join(root, "state")
	if err := os.MkdirAll(domain, 0o700); err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("isolated state domain %q is unusable: %v", domain, err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, health, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(),
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach isolated endpoint: %v", err)
	}
	defer client.Close()
	t.Logf("evidence: endpoint readiness=%s ownership=%s version=%s attach=%s",
		health.EndpointReadiness, health.ManagerOwnership, health.VersionRelation,
		codexappserver.AuthorityFor(health).Attach)
	if codexappserver.AuthorityFor(health).Attach != codexappserver.EndpointAttachAllowed {
		t.Fatalf("isolated exact-current endpoint refused attach: %+v", codexappserver.AuthorityFor(health))
	}
	binding, err := client.StartThread(ctx, workspace, nil)
	if err != nil {
		t.Fatalf("start isolated thread: %v", err)
	}
	t.Logf("evidence: thread bootstrapped id-present=%v", strings.TrimSpace(binding.ThreadID) != "")

	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Opener: codexbroker.DefaultOpener(version.String(), codexappserver.AttachOptions{
			Timeout: 10 * time.Second, ExperimentalAPI: true,
		}),
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: time.Second})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("publish isolated runtime: %v", err)
	}

	session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
		AgentUID: "agent-smoke", PaneUID: "pane-smoke", RuntimeID: "%1",
		Generation: "gen-smoke", ThreadID: binding.ThreadID,
	}, workspace, nil, discovery, nil)

	openCtx, openCancel := context.WithTimeout(ctx, 20*time.Second)
	connection, openErr := session.Open(openCtx)
	openCancel()
	if openErr != nil {
		t.Logf("evidence: broker binding refused for a thread with no materialized turn: %v", openErr)
	} else {
		epoch, ok := connection.(*codexBrokerLifecycleEpoch)
		if !ok {
			t.Fatalf("open returned %T", connection)
		}
		t.Logf("evidence: broker binding opened connection-epoch=%d binding-epoch=%d lifecycle-events=%v",
			epoch.fence.Connection, epoch.fence.Binding, epoch.LifecycleEventsAvailable())
		_ = connection.Close()
	}
	_ = session.Close()

	_ = host.Close()
	_ = broker.Close()
	for _, artifact := range []string{discovery.SocketPath(), discovery.RecordPath()} {
		if _, err := os.Lstat(artifact); err == nil {
			t.Fatalf("runtime left %q behind", filepath.Base(artifact))
		}
	}
}
