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

// TestInstalledIsolatedRetiredObserverMatrixSmoke is the retirement's final
// operational proof against a real installed Codex app-server.
//
// It is opt-in through PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT and carries the same
// containment as the cutover smoke above: a contained CODEX_HOME, an isolated
// state domain, and inherited tmux identity stripped.
//
// What it proves is the number the per-Agent observer retirement is measured
// by. The retired producer opened one upstream app-server connection per
// managed Agent and owned a private control endpoint for each; two Agents now
// share one broker connection, one runtime, and one set of artifacts, one
// Agent's control traffic leaves the other's epoch untouched, and unbinding
// them both leaves nothing behind.
func TestInstalledIsolatedRetiredObserverMatrixSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT for the installed retirement matrix smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("smoke root must be an isolated child of %s", tmpRoot)
	}
	for _, inherited := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(inherited); present {
			t.Fatalf("%s must be removed for the installed retirement matrix smoke", inherited)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Two managed Agents, each with its own exact thread. The prompted create
	// is the product's own, and it is what materializes each thread's rollout.
	type managed struct {
		agentUID   string
		paneUID    string
		runtimeID  string
		generation string
		threadID   string
	}
	agents := []managed{
		{agentUID: "agent-retire-a", paneUID: "pane-retire-a", runtimeID: "%1", generation: "gen-retire-a"},
		{agentUID: "agent-retire-b", paneUID: "pane-retire-b", runtimeID: "%2", generation: "gen-retire-b"},
	}
	for i := range agents {
		created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
			"Reply with the single word OK and nothing else.", agents[i].generation)
		if err != nil {
			t.Fatalf("prompted create for %s: %v", agents[i].agentUID, err)
		}
		if strings.TrimSpace(created.ThreadID) == "" || strings.TrimSpace(created.TurnID) == "" {
			t.Fatalf("prompted create for %s returned no exact thread and turn", agents[i].agentUID)
		}
		agents[i].threadID = created.ThreadID
		settleCodexTurn(ctx, t, workspace, created.ThreadID)
	}
	t.Logf("evidence: managed Agents created=%d distinct-threads=%v",
		len(agents), agents[0].threadID != agents[1].threadID)

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
	defer func() {
		_ = host.Close()
		_ = broker.Close()
	}()

	sessions := make([]*codexBrokerObserverSession, 0, len(agents))
	epochs := make([]*codexBrokerLifecycleEpoch, 0, len(agents))
	for _, agent := range agents {
		session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
			AgentUID: agent.agentUID, PaneUID: agent.paneUID, RuntimeID: agent.runtimeID,
			Generation: agent.generation, ThreadID: agent.threadID,
		}, workspace, nil, discovery, nil)
		sessions = append(sessions, session)
		openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
		connection, openErr := session.Open(openCtx)
		openCancel()
		if openErr != nil {
			t.Fatalf("broker binding refused for %s: %v", agent.agentUID, openErr)
		}
		epoch, ok := connection.(*codexBrokerLifecycleEpoch)
		if !ok {
			t.Fatalf("open returned %T", connection)
		}
		epochs = append(epochs, epoch)
	}
	defer func() {
		for i := range sessions {
			_ = epochs[i].Close()
			_ = sessions[i].Close()
		}
	}()

	// One runtime, one upstream connection, two bindings. This is the whole
	// retirement in one reading: the retired producer would be showing two
	// connections and two private control endpoints here.
	stats := dialInstalledSmokeTelemetry(ctx, t, discovery)
	t.Logf("evidence: runtime=%s connections=%d bindings=%d open-attempts=%d clients=%d",
		stats.Runtime, stats.Broker.Connects-stats.Broker.Disconnects, stats.Broker.Bindings,
		stats.Broker.OpenAttempts, stats.Host.LiveSessions)
	if open := stats.Broker.Connects - stats.Broker.Disconnects; open != 1 {
		t.Fatalf("open upstream connections = %d for %d managed Agents, want exactly 1", open, len(agents))
	}
	if stats.Broker.Bindings != len(agents) {
		t.Fatalf("bindings = %d, want one per managed Agent (%d)", stats.Broker.Bindings, len(agents))
	}
	if stats.Broker.OpenAttempts != 1 {
		t.Fatalf("upstream open attempts = %d, want the single shared connection", stats.Broker.OpenAttempts)
	}
	if stats.Runtime != host.RuntimeID() {
		t.Fatalf("telemetry runtime = %q, want the single published runtime %q", stats.Runtime, host.RuntimeID())
	}

	// The projected diagnostic is what an operator actually reads.
	projected := projectCodexBrokerTelemetry(stats)
	if projected.State != codexBrokerStateRunning || projected.Connections != 1 || projected.Bindings != len(agents) {
		t.Fatalf("projected diagnostic = %+v, want one running connection with one binding per Agent", projected)
	}
	if projected.Evictions != 0 || projected.SnapshotFailures != 0 {
		t.Fatalf("a healthy matrix reported binding faults: %+v", projected)
	}

	// Control on one Agent must leave the other's epoch alone. The steered turn
	// is started through the same fenced wire that carries it, so it is
	// provably the exact in-progress turn of that exact Agent.
	beforeFence := epochs[1].fence
	started, err := epochs[0].StartExactTurn(ctx, agents[0].threadID,
		"Write the numbers 1 through 400, one per line, and nothing else.")
	if err != nil {
		t.Fatalf("start the turn to steer on %s: %v", agents[0].agentUID, err)
	}
	snapshot, err := epochs[0].ReadLifecycleSnapshot(ctx, agents[0].threadID)
	if err != nil {
		t.Fatalf("read the lifecycle snapshot for %s: %v", agents[0].agentUID, err)
	}
	if snapshot.TurnID != started.TurnID || snapshot.TurnState != codexappserver.TurnStateInProgress {
		t.Fatalf("turn to steer is not the exact in-progress turn of %s: turn-state=%s matches=%v",
			agents[0].agentUID, snapshot.TurnState, snapshot.TurnID == started.TurnID)
	}
	steered, err := epochs[0].SteerExactTurn(ctx, agents[0].threadID, started.TurnID, "Stop at 5 instead.")
	if err != nil {
		t.Fatalf("steer the exact active turn of %s: %v", agents[0].agentUID, err)
	}
	if steered.TurnID != started.TurnID {
		t.Fatalf("steer answered for turn %q, want the exact active turn", steered.TurnID)
	}
	if _, err := epochs[0].InterruptExactTurn(ctx, agents[0].threadID, started.TurnID); err != nil {
		t.Fatalf("interrupt the steered turn of %s: %v", agents[0].agentUID, err)
	}

	// The sibling's authority is untouched by all of that, and its own exact
	// thread still reads back through its own fence.
	if epochs[1].fence != beforeFence {
		t.Fatalf("sibling fence moved from %+v to %+v during the other Agent's turn", beforeFence, epochs[1].fence)
	}
	siblingSnapshot, err := epochs[1].ReadLifecycleSnapshot(ctx, agents[1].threadID)
	if err != nil {
		t.Fatalf("read the sibling lifecycle snapshot through its own fence: %v", err)
	}
	if siblingSnapshot.ThreadID != agents[1].threadID {
		t.Fatalf("sibling snapshot answered for thread %q, want %q", siblingSnapshot.ThreadID, agents[1].threadID)
	}
	t.Logf("evidence: sibling containment fence-unchanged=true thread-matches=%v",
		siblingSnapshot.ThreadID == agents[1].threadID)

	// Releasing one Agent leaves the other bound on the same connection.
	_ = epochs[0].Close()
	_ = sessions[0].Close()
	released := dialInstalledSmokeTelemetry(ctx, t, discovery)
	t.Logf("evidence: after releasing one Agent connections=%d bindings=%d",
		released.Broker.Connects-released.Broker.Disconnects, released.Broker.Bindings)
	if released.Broker.Bindings != len(agents)-1 {
		t.Fatalf("bindings after one release = %d, want %d", released.Broker.Bindings, len(agents)-1)
	}
	if open := released.Broker.Connects - released.Broker.Disconnects; open != 1 {
		t.Fatalf("open upstream connections after one release = %d, want the shared connection kept", open)
	}

	_ = epochs[1].Close()
	_ = sessions[1].Close()
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
	for _, argv := range recorded {
		if !slices.Contains(codexInstalledSmokeReadOnlyArgv, argv) {
			t.Fatalf("codex argv %q is outside the read-only set %v", argv, codexInstalledSmokeReadOnlyArgv)
		}
	}
}

// dialInstalledSmokeTelemetry reads the published runtime's content-free
// telemetry over its own local IPC, which is the same path Doctor and Settings
// take.
func dialInstalledSmokeTelemetry(ctx context.Context, t *testing.T, discovery codexbroker.Discovery) codexbroker.RuntimeTelemetry {
	t.Helper()
	conn, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("reach the published runtime for telemetry: %v", err)
	}
	defer conn.Close()
	telemetry, err := conn.Stats(ctx)
	if err != nil {
		t.Fatalf("read runtime telemetry: %v", err)
	}
	return telemetry
}

// TestInstalledIsolatedBrokerApprovalLeaseSmoke observes one real upstream
// approval server request end to end.
//
// It is the last observation the fake-endpoint suite cannot make. An approval
// never occurs on a no-turn thread, so every earlier proof of the lease's
// response-once authority was made against a scripted endpoint. Here the
// request is issued by a real Codex app-server, delivered over the broker's
// shared connection, minted into a single-use lease bound to the raw JSON-RPC
// id and both epochs, spent exactly once, and refused on the second attempt.
//
// It is opt-in through PROJMUX_CODEX_APPROVAL_SMOKE_ROOT, whose contained
// CODEX_HOME must be configured to require approval; the decision this test
// sends is the first non-executing one the endpoint itself offers, so nothing
// the model proposed is ever run.
func TestInstalledIsolatedBrokerApprovalLeaseSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_APPROVAL_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_APPROVAL_SMOKE_ROOT for the installed approval lease smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("smoke root must be an isolated child of %s", tmpRoot)
	}
	for _, inherited := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(inherited); present {
			t.Fatalf("%s must be removed for the installed approval lease smoke", inherited)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
		"Reply with the single word OK and nothing else.", "gen-approval")
	if err != nil {
		t.Fatalf("prompted create against the isolated endpoint: %v", err)
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
	defer func() {
		_ = host.Close()
		_ = broker.Close()
	}()

	session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
		AgentUID: "agent-approval", PaneUID: "pane-approval", RuntimeID: "%1",
		Generation: "gen-approval", ThreadID: created.ThreadID,
	}, workspace, nil, discovery, nil)
	defer func() { _ = session.Close() }()
	openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
	connection, openErr := session.Open(openCtx)
	openCancel()
	if openErr != nil {
		t.Fatalf("broker binding refused for the created thread: %v", openErr)
	}
	epoch, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok {
		t.Fatalf("open returned %T", connection)
	}
	defer func() { _ = epoch.Close() }()

	started, err := epoch.StartExactTurn(ctx, created.ThreadID,
		"Use your shell tool to run exactly this one command and nothing else: printf probe > ./projmux-approval-probe.txt")
	if err != nil {
		t.Fatalf("start the turn that should request approval: %v", err)
	}

	// The approval arrives on the broker's own stream, which is what mints the
	// lease. Anything else on that stream is ordinary lifecycle traffic.
	deadline := time.After(5 * time.Minute)
	var envelope codexappserver.ApprovalEnvelope
	for envelope.RequestID == "" {
		select {
		case notification, open := <-epoch.Notifications():
			if !open {
				t.Fatal("the broker stream ended before an approval arrived")
			}
			decoded, recognized, decodeErr := codexappserver.DecodeApprovalEnvelope(notification)
			if decodeErr != nil {
				t.Fatalf("decode the real approval request: %v", decodeErr)
			}
			if recognized {
				envelope = decoded
			}
		case <-deadline:
			_, _ = epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID)
			t.Fatalf("no upstream approval server request arrived for turn %s", started.TurnID)
		case <-ctx.Done():
			t.Fatalf("waiting for the approval was cancelled: %v", ctx.Err())
		}
	}
	t.Logf("evidence: upstream approval kind=%s thread-matches=%v turn-matches=%v raw-id-present=%v decisions=%v",
		envelope.Kind, envelope.ThreadID == created.ThreadID, envelope.TurnID == started.TurnID,
		len(envelope.RawRequestID) > 0, envelope.Decisions)
	if envelope.ThreadID != created.ThreadID || envelope.TurnID != started.TurnID {
		t.Fatalf("approval identity = thread %q turn %q, want the exact bound thread and started turn",
			envelope.ThreadID, envelope.TurnID)
	}
	// The offered set belongs to the endpoint, not to this test: current Codex
	// answers a command approval with accept/cancel, while other shapes offer
	// decline. Pick the first offered decision that executes nothing, because
	// accepting would run whatever the model proposed inside the smoke root.
	decision := codexappserver.ApprovalDecision("")
	for _, safe := range []codexappserver.ApprovalDecision{
		codexappserver.DecisionDecline, codexappserver.DecisionCancel,
	} {
		if slices.Contains(envelope.Decisions, safe) {
			decision = safe
			break
		}
	}
	if decision == "" {
		t.Fatalf("approval offered no non-executing decision: %v", envelope.Decisions)
	}

	result, err := codexappserver.ApprovalResponse(envelope, decision)
	if err != nil {
		t.Fatalf("build the %s response: %v", decision, err)
	}
	if err := epoch.RespondServerRequest(ctx, envelope.RawRequestID, result); err != nil {
		t.Fatalf("answer the real approval through the broker lease with %s: %v", decision, err)
	}
	// The lease is single use. A second answer for the same raw id must be
	// refused by the epoch before it can reach the wire again.
	if err := epoch.RespondServerRequest(ctx, envelope.RawRequestID, result); err == nil {
		t.Fatal("a spent approval lease answered a second time")
	}
	t.Logf("evidence: approval lease spent once with decision=%s and refused on the second answer", decision)

	if _, err := epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID); err != nil {
		t.Logf("interrupt after the %s approval: %v", decision, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "projmux-approval-probe.txt")); err == nil {
		t.Fatalf("the command answered with %s wrote its file anyway", decision)
	}

	recorded := ledger()
	t.Logf("evidence: codex argv recorded=%d distinct=%v", len(recorded), distinctCodexArgv(recorded))
	for _, argv := range recorded {
		if !slices.Contains(codexInstalledSmokeReadOnlyArgv, argv) {
			t.Fatalf("codex argv %q is outside the read-only set %v", argv, codexInstalledSmokeReadOnlyArgv)
		}
	}
}
