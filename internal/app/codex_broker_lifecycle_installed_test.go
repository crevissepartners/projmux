package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/version"
)

// codexInstalledSmokeReadOnlyArgv is the complete set of `codex` argv this
// whole path is allowed to run. Both entries are read-only: one opens the
// official stdio proxy to an endpoint that is already there, the other prints
// the local version JSON. Every daemon lifecycle mutation - start, stop,
// restart, bootstrap, remote-control, login, config - is absent by
// construction, so an argv the ledger records outside this set is the
// violation itself and needs no separate pattern list.
var codexInstalledSmokeReadOnlyArgv = []string{
	"app-server proxy",
	"app-server daemon version",
}

// TestInstalledIsolatedBrokerNativeBindingSmoke drives the cutover's product
// lifecycle and control path against a real installed Codex app-server.
//
// It is opt-in through PROJMUX_CODEX_CUTOVER_SMOKE_ROOT and requires a
// contained CODEX_HOME, an isolated state domain, and inherited tmux identity
// stripped, so it can never reach an ambient shared endpoint, an ambient
// runtime, or an ambient tmux server. The endpoint it talks to is a direct
// `codex app-server --listen unix://` under that contained CODEX_HOME, which
// upstream reports as running with no daemon backend: the unmanaged,
// exact-current endpoint this phase widened attach for.
//
// What it proves is the acceptance the fake-endpoint suite cannot: on that
// unmanaged endpoint, the product reaches native ready and steers its own
// exact active turn through the broker's fenced control wire, while every
// `codex` argv the whole path ran stays inside the read-only set above.
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
	ledger := installCodexArgvLedger(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Native ready on the unmanaged endpoint. The attach is the product's own,
	// and requiring unmanaged ownership here is what keeps this test from
	// quietly passing against a daemon-managed ambient endpoint.
	client, health, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(),
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach isolated endpoint: %v", err)
	}
	authority := codexappserver.AuthorityFor(health)
	t.Logf("evidence: endpoint readiness=%s ownership=%s version=%s attach=%s lifecycle=%s",
		health.EndpointReadiness, health.ManagerOwnership, health.VersionRelation, authority.Attach, authority.Lifecycle)
	if authority.Attach != codexappserver.EndpointAttachAllowed {
		_ = client.Close()
		t.Fatalf("isolated exact-current endpoint refused attach: %+v", authority)
	}
	if health.ManagerOwnership != codexappserver.ManagerUnmanaged {
		_ = client.Close()
		t.Fatalf("ownership = %s, want %s: this smoke must run against an unmanaged endpoint",
			health.ManagerOwnership, codexappserver.ManagerUnmanaged)
	}
	if authority.Lifecycle != codexappserver.DaemonLifecycleAuthorityNone {
		_ = client.Close()
		t.Fatalf("lifecycle authority = %s, want %s for an unmanaged endpoint",
			authority.Lifecycle, codexappserver.DaemonLifecycleAuthorityNone)
	}
	_ = client.Close()

	// The product's own prompted create. It is what materializes the thread's
	// rollout, which is the upstream precondition the broker's pre-turn
	// thread/resume bootstrap needs.
	created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
		"Reply with the single word OK and nothing else.", "gen-smoke")
	if err != nil {
		t.Fatalf("prompted create against the isolated endpoint: %v", err)
	}
	t.Logf("evidence: prompted create thread-present=%v turn-present=%v",
		strings.TrimSpace(created.ThreadID) != "", strings.TrimSpace(created.TurnID) != "")
	if strings.TrimSpace(created.ThreadID) == "" || strings.TrimSpace(created.TurnID) == "" {
		t.Fatalf("prompted create returned no exact thread and turn")
	}
	settleCodexTurn(ctx, t, workspace, created.ThreadID)

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
		Generation: "gen-smoke", ThreadID: created.ThreadID,
	}, workspace, nil, discovery, nil)

	openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
	connection, openErr := session.Open(openCtx)
	openCancel()
	if openErr != nil {
		_ = session.Close()
		_ = host.Close()
		_ = broker.Close()
		t.Fatalf("broker binding refused for the created thread: %v", openErr)
	}
	epoch, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok {
		t.Fatalf("open returned %T", connection)
	}
	t.Logf("evidence: broker binding opened connection-epoch=%d binding-epoch=%d lifecycle-events=%v",
		epoch.fence.Connection, epoch.fence.Binding, epoch.LifecycleEventsAvailable())

	// Start the turn this test will steer through the same fenced wire that
	// carries it, so the steered turn is provably the exact in-progress one and
	// not a race against a turn some other caller may already have finished.
	started, err := epoch.StartExactTurn(ctx, created.ThreadID, "Write the numbers 1 through 400, one per line, and nothing else.")
	if err != nil {
		t.Fatalf("start the turn to steer through the broker epoch: %v", err)
	}
	snapshot, err := epoch.ReadLifecycleSnapshot(ctx, created.ThreadID)
	if err != nil {
		t.Fatalf("read the lifecycle snapshot through the epoch fence: %v", err)
	}
	t.Logf("evidence: fenced snapshot thread-state=%s turn-state=%s turn-matches-started=%v",
		snapshot.ThreadState, snapshot.TurnState, snapshot.TurnID == started.TurnID)
	if snapshot.TurnID != started.TurnID || snapshot.TurnState != codexappserver.TurnStateInProgress {
		t.Fatalf("turn to steer is not the exact in-progress turn: snapshot turn-state=%s matches=%v",
			snapshot.TurnState, snapshot.TurnID == started.TurnID)
	}

	steered, steerErr := epoch.SteerExactTurn(ctx, created.ThreadID, started.TurnID, "Stop at 5 instead.")
	if steerErr != nil {
		t.Fatalf("steer the exact active turn through the broker epoch: %v", steerErr)
	}
	if steered.TurnID != started.TurnID {
		t.Fatalf("steer answered for turn %q, want the exact active turn", steered.TurnID)
	}
	t.Logf("evidence: exact active turn steered through the broker epoch turn-matches=%v", steered.TurnID == started.TurnID)

	if _, err := epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID); err != nil {
		t.Fatalf("interrupt the steered turn through the broker epoch: %v", err)
	}

	_ = connection.Close()
	_ = session.Close()
	_ = host.Close()
	_ = broker.Close()
	for _, artifact := range []string{discovery.SocketPath(), discovery.RecordPath()} {
		if _, err := os.Lstat(artifact); err == nil {
			t.Fatalf("runtime left %q behind", filepath.Base(artifact))
		}
	}

	recorded := ledger()
	t.Logf("evidence: codex argv recorded=%d distinct=%v", len(recorded), distinctCodexArgv(recorded))
	if len(recorded) == 0 {
		t.Fatalf("no codex argv was recorded, so the zero-lifecycle-mutation claim is unproven")
	}
	if !slices.Contains(recorded, "app-server proxy") {
		t.Fatalf("the recorded argv never opened the official proxy, so the ledger did not observe the product path")
	}
	for _, argv := range recorded {
		if !slices.Contains(codexInstalledSmokeReadOnlyArgv, argv) {
			t.Fatalf("codex argv %q is outside the read-only set %v", argv, codexInstalledSmokeReadOnlyArgv)
		}
	}
}

// installCodexArgvLedger puts a recording shim in front of the installed codex
// for this test process and returns the reader of everything it observed.
//
// The shim is the only way to make the zero-daemon-lifecycle-mutation half of
// the acceptance observable against a real endpoint: the product resolves
// `codex` through PATH on every proxy open and every readiness observation, so
// a shim there sees the complete argv set of the whole path, including the
// broker's own opener.
func installCodexArgvLedger(t *testing.T, root string) func() []string {
	t.Helper()
	real, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("installed codex is required for this smoke: %v", err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "codex-argv.log")
	shim := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", recordPath, real)
	shimPath := filepath.Join(bin, "codex")
	if err := os.WriteFile(shimPath, []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if resolved, err := exec.LookPath("codex"); err != nil || resolved != shimPath {
		t.Fatalf("codex resolves to %q (%v), want the recording shim %q", resolved, err, shimPath)
	}
	return func() []string {
		raw, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatalf("read the codex argv ledger: %v", err)
		}
		var argv []string
		for line := range strings.SplitSeq(string(raw), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				argv = append(argv, line)
			}
		}
		return argv
	}
}

// distinctCodexArgv renders the ledger as its ordered distinct argv, which is
// the part worth reading in a log line.
func distinctCodexArgv(recorded []string) []string {
	var distinct []string
	for _, argv := range recorded {
		if !slices.Contains(distinct, argv) {
			distinct = append(distinct, argv)
		}
	}
	return distinct
}

// settleCodexTurn waits for the created thread to leave its first turn, so the
// turn this test starts and steers is the only one in progress.
func settleCodexTurn(ctx context.Context, t *testing.T, workspace, threadID string) {
	t.Helper()
	client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(),
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach to settle the created turn: %v", err)
	}
	defer client.Close()
	if _, err := client.ResumeThread(ctx, threadID, workspace, nil); err != nil {
		t.Fatalf("resume the created thread to settle its turn: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		snapshot, err := client.ReadLifecycleSnapshot(ctx, threadID)
		if err != nil {
			t.Fatalf("read the created thread while settling: %v", err)
		}
		if snapshot.TurnState != codexappserver.TurnStateInProgress {
			t.Logf("evidence: created turn settled turn-state=%s thread-state=%s", snapshot.TurnState, snapshot.ThreadState)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("created turn stayed in progress past the settle deadline")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("settling the created turn was cancelled: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
