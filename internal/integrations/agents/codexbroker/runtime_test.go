package codexbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestConcurrentClientsShareOneRuntimeProcessAndOneUpstreamConnection is the
// singleton contract across real processes.
//
// Four separate client processes are started, held at one barrier, and then
// released together, so every one of them runs discovery, stale reclaim, and
// launch against the same state domain at the same instant. What they must
// observe is one runtime and one upstream connection: the socket bind is the
// mutex that makes a second host impossible, and the startup lock is what keeps
// a losing starter from reclaiming the winner's socket out from under it.
func TestConcurrentClientsShareOneRuntimeProcessAndOneUpstreamConnection(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	domain := discovery.Domain()
	ledger := filepath.Join(domain, "ledger.txt")
	gate := filepath.Join(domain, "gate")
	release := filepath.Join(domain, "release")

	const clients = 4
	reports := make([]string, clients)
	helpers := make([]*helperProcess, clients)
	for index := range clients {
		reports[index] = filepath.Join(domain, fmt.Sprintf("report-%d", index))
		helpers[index] = startHelper(t, helperRoleClient, domain, map[string]string{
			helperLedgerEnv:   ledger,
			helperGateEnv:     gate,
			helperReleaseEnv:  release,
			helperReportEnv:   reports[index],
			helperThreadEnv:   fmt.Sprintf("thread-%d", index),
			helperIdleEnv:     (500 * time.Millisecond).String(),
			helperProtocolEnv: "1:1",
		})
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatalf("open the concurrency barrier: %v", err)
	}
	waitFor(t, "every client to bind and report", func() bool {
		for _, path := range reports {
			if _, err := os.Stat(path); err != nil {
				return false
			}
		}
		return true
	})

	first := ""
	for index, path := range reports {
		payload, err := os.ReadFile(path) // #nosec G304 -- test-owned report path.
		if err != nil {
			t.Fatalf("read client %d report: %v", index, err)
		}
		report := strings.TrimSpace(string(payload))
		if index == 0 {
			first = report
			continue
		}
		if report != first {
			t.Fatalf("client %d observed %q, want the same runtime as %q", index, report, first)
		}
	}
	if !strings.HasSuffix(first, "protocol 1") {
		t.Fatalf("negotiated report = %q", first)
	}
	if runtimes := countLedger(t, ledger, "runtime "); runtimes != 1 {
		t.Fatalf("published runtimes = %d, want exactly one process; ledger=%v", runtimes, readLedger(t, ledger))
	}
	if opens := countLedger(t, ledger, "open "); opens != 1 {
		t.Fatalf("upstream connections = %d, want exactly one; ledger=%v", opens, readLedger(t, ledger))
	}
	if snapshots := countLedger(t, ledger, "snapshot "); snapshots != clients {
		t.Fatalf("thread snapshots = %d, want one per client", snapshots)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release the clients: %v", err)
	}
	for index, helper := range helpers {
		if err := helper.wait(); err != nil {
			t.Fatalf("client %d exited with %v", index, err)
		}
	}
	// Every client is gone, so the runtime holds no binding. Its bounded idle
	// shutdown must remove exactly what it published and leave no process.
	waitFor(t, "the shared runtime to idle out", func() bool {
		return countLedger(t, ledger, "stopped ") == 1
	})
	assertNoRuntimeArtifacts(t, discovery)
}

// TestStaleRuntimeArtifactsAreReclaimedOnlyByExactOwnerProof is the ownership
// fixture.
//
// Reclaim authority is three facts and nothing else: the artifact is an
// owner-private socket, dialing it is refused, and it is unchanged when the
// removal happens. The pid in the discovery record is deliberately not one of
// them, so the last row varies the pid across a live one, a never-used one, and
// none at all and requires the decision to be identical every time.
func TestStaleRuntimeArtifactsAreReclaimedOnlyByExactOwnerProof(t *testing.T) {
	t.Run("live runtime is reused, not replaced", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		host, _ := startTestHost(t, discovery, -1, ProtocolRange{})
		before := statOf(t, discovery.SocketPath())
		err := reclaimStale(discovery)
		if RefusalOf(err) != RefusalHostLive {
			t.Fatalf("reclaimStale(live) = %v, want host-live", err)
		}
		if after := statOf(t, discovery.SocketPath()); !os.SameFile(before, after) {
			t.Fatal("a live runtime's socket was replaced")
		}
		if host.Stats().Bindings != 0 {
			t.Fatal("the live runtime lost state during a reclaim probe")
		}
	})

	t.Run("dead socket is reclaimed", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		leaveDeadSocket(t, discovery)
		if err := reclaimStale(discovery); err != nil {
			t.Fatalf("reclaimStale(dead) = %v", err)
		}
		assertNoRuntimeArtifacts(t, discovery)
	})

	t.Run("foreign artifact kinds are left alone", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			place func(t *testing.T, path string)
		}{
			{name: "regular file", place: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "symlink", place: func(t *testing.T, path string) {
				if err := os.Symlink(filepath.Dir(path), path); err != nil {
					t.Fatal(err)
				}
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				discovery := newRuntimeDiscovery(t)
				if err := prepareDiscoveryDir(discovery); err != nil {
					t.Fatal(err)
				}
				test.place(t, discovery.SocketPath())
				before := lstatOf(t, discovery.SocketPath())
				if err := reclaimStale(discovery); RefusalOf(err) != RefusalDiscoveryUntrusted {
					t.Fatalf("reclaimStale(%s) = %v, want discovery-untrusted", test.name, err)
				}
				if after := lstatOf(t, discovery.SocketPath()); !os.SameFile(before, after) {
					t.Fatalf("a %s at the socket path was modified", test.name)
				}
			})
		}
	})

	t.Run("the record pid is never reclaim authority", func(t *testing.T) {
		for _, test := range []struct {
			name string
			pid  int
		}{
			{name: "live pid", pid: os.Getpid()},
			{name: "absent pid", pid: 0},
			{name: "foreign pid", pid: 1},
		} {
			t.Run(test.name, func(t *testing.T) {
				discovery := newRuntimeDiscovery(t)
				leaveDeadSocket(t, discovery)
				if err := writeRecord(discovery, discoveryRecord{
					Protocol: 1, MinProtocol: 1, Endpoint: discovery.Endpoint(),
					Runtime: "runtime-x", PID: test.pid, Credential: "credential-x",
				}); err != nil {
					t.Fatal(err)
				}
				if err := reclaimStale(discovery); err != nil {
					t.Fatalf("reclaimStale(%s) = %v; the pid changed a decision it may not participate in", test.name, err)
				}
				assertNoRuntimeArtifacts(t, discovery)
			})
		}
	})
}

// TestRuntimeCrashRevokesTheOldEpochBeforeANewRuntimeRestoresAuthority is the
// crash and restart contract.
//
// A killed runtime leaves its socket behind and its client holding a fence.
// The client must revoke that authority the moment the connection dies, refuse
// every use of it with zero upstream writes, and regain authority only after a
// new runtime has been started under exact owner proof and its snapshot has
// reopened the barrier.
func TestRuntimeCrashRevokesTheOldEpochBeforeANewRuntimeRestoresAuthority(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	ledger := filepath.Join(discovery.Domain(), "ledger.txt")
	crashed := startRuntimeProcess(t, discovery, ledger, 0, "1:1")

	conn, err := Dial(t.Context(), discovery, DialConfig{})
	if err != nil {
		t.Fatalf("Dial() = %v", err)
	}
	binding, fence := boundRemote(t, conn, "thread-one")
	if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/start"}); err != nil ||
		outcome != MutationApplied {
		t.Fatalf("Submit() = %s, %v", outcome, err)
	}
	writes := countLedger(t, ledger, "request ")
	if writes != 1 {
		t.Fatalf("upstream writes = %d, want one", writes)
	}
	oldRuntime := conn.Runtime()

	crashed.kill()
	waitFor(t, "the client to notice the crash", func() bool {
		select {
		case <-conn.Done():
			return true
		default:
			return false
		}
	})
	if _, ok := <-binding.Events(); ok {
		t.Fatal("the revoked binding delivered an event after its runtime died")
	}
	if got := binding.Revocation(); got != RefusalHostUnavailable {
		t.Fatalf("Revocation() = %s, want host-unavailable", got)
	}
	if _, err := binding.ControlAuthority(); RefusalOf(err) != RefusalHostUnavailable {
		t.Fatalf("ControlAuthority() after a crash = %v", err)
	}
	if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/steer"}); outcome != MutationRefused ||
		RefusalOf(err) != RefusalHostUnavailable {
		t.Fatalf("Submit() on a dead runtime = %s, %v", outcome, err)
	}
	if after := countLedger(t, ledger, "request "); after != writes {
		t.Fatalf("upstream writes = %d after the crash, want the pre-crash %d", after, writes)
	}

	// The crashed runtime left its socket behind. Ensure must prove it stale
	// and start a replacement rather than dialing a socket nobody serves.
	var launched *helperProcess
	restored, err := Ensure(t.Context(), discovery, EnsureConfig{
		Launch: launcherFunc(func() error {
			launched = startHelper(t, helperRoleRuntime, discovery.Domain(), map[string]string{
				helperLedgerEnv:   ledger,
				helperIdleEnv:     (2 * time.Second).String(),
				helperProtocolEnv: "1:1",
			})
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("Ensure() after a crash = %v", err)
	}
	defer restored.Close()
	if launched == nil {
		t.Fatal("Ensure did not start a replacement runtime")
	}
	if restored.Runtime() == oldRuntime {
		t.Fatal("the replacement reported the crashed runtime's identity")
	}
	revived, freshFence := boundRemote(t, restored, "thread-one")
	// The crashed authority stays dead. Its epochs are the epochs of a process
	// that no longer exists, and the only handle that could present them
	// refuses every use.
	if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/steer"}); outcome != MutationRefused ||
		RefusalOf(err) != RefusalHostUnavailable {
		t.Fatalf("the crashed binding accepted a mutation: %s, %v", outcome, err)
	}
	// Epoch fencing is live again on the restored binding: an epoch it never
	// issued writes nothing.
	stale := Fence{Connection: freshFence.Connection + 1, Binding: freshFence.Binding}
	if outcome, err := revived.Submit(t.Context(), stale, Mutation{Method: "turn/steer"}); outcome != MutationRefused ||
		RefusalOf(err) != RefusalStaleConnectionEpoch {
		t.Fatalf("a foreign epoch on the restored binding = %s, %v", outcome, err)
	}
	if after := countLedger(t, ledger, "request "); after != writes {
		t.Fatalf("a refused fence produced an upstream write: %d", after)
	}
	if outcome, err := revived.Submit(t.Context(), freshFence, Mutation{Method: "turn/steer"}); err != nil ||
		outcome != MutationApplied {
		t.Fatalf("Submit() on the restored authority = %s, %v", outcome, err)
	}
	if after := countLedger(t, ledger, "request "); after != writes+1 {
		t.Fatalf("upstream writes = %d, want exactly one more", after)
	}
}

// TestRestoredAuthorityOpensOnlyBehindTheSnapshotBarrier is the other half of
// the crash contract, driven deterministically.
//
// A bind is not authority. Until the runtime's snapshot for that exact thread
// has crossed the socket, control stays shut and a mutation attempted there
// reaches no endpoint at all, so a client that reconnected cannot write against
// state it has not yet been told about.
func TestRestoredAuthorityOpensOnlyBehindTheSnapshotBarrier(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	_, endpoint := startTestHost(t, discovery, -1, ProtocolRange{})
	endpoint.hold("snapshot:thread-one")
	conn := dialTestClient(t, discovery, ProtocolRange{})

	binding, err := conn.Bind(t.Context(), "thread-one", "/work/project", nil)
	if err != nil {
		t.Fatalf("Bind() = %v", err)
	}
	waitUntil(t, "the runtime to reach the held snapshot", func() bool {
		return endpoint.visited("snapshot:thread-one") == 1
	})
	if _, err := binding.ControlAuthority(); RefusalOf(err) != RefusalControlNotOpen {
		t.Fatalf("ControlAuthority() behind the barrier = %v, want control-not-open", err)
	}
	if outcome, err := binding.Submit(t.Context(), Fence{Connection: 1, Binding: 1},
		Mutation{Method: "turn/steer"}); outcome != MutationRefused || RefusalOf(err) != RefusalControlNotOpen {
		t.Fatalf("Submit() behind the barrier = %s, %v", outcome, err)
	}
	if methods := endpoint.methods(); len(methods) != 0 {
		t.Fatalf("a mutation behind the barrier reached the endpoint: %v", methods)
	}

	endpoint.release("snapshot:thread-one")
	if event := nextRemoteEvent(t, binding); event.Origin != EventOriginSnapshot {
		t.Fatalf("first event = %+v, want the barrier snapshot", event)
	}
	fence, err := binding.ControlAuthority()
	if err != nil {
		t.Fatalf("ControlAuthority() after the barrier = %v", err)
	}
	if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/steer"}); err != nil ||
		outcome != MutationApplied {
		t.Fatalf("Submit() after the barrier = %s, %v", outcome, err)
	}
}

// TestLastBindingRemovalIdlesTheRuntimeOutAndLeavesNoArtifact is the lifecycle
// contract. A runtime exists to hold one upstream connection for the bindings
// that asked for it; with none left it must stop, and stopping must remove
// exactly the socket and record it published.
func TestLastBindingRemovalIdlesTheRuntimeOutAndLeavesNoArtifact(t *testing.T) {
	before := runtime.NumGoroutine()
	discovery := newRuntimeDiscovery(t)
	host, _ := startTestHost(t, discovery, 150*time.Millisecond, ProtocolRange{})
	conn := dialTestClient(t, discovery, ProtocolRange{})
	binding, _ := boundRemote(t, conn, "thread-one")

	// A held binding suspends the timer entirely; the runtime must still be
	// serving well past the idle bound.
	select {
	case <-host.Done():
		t.Fatal("the runtime shut down while a binding was held")
	case <-time.After(500 * time.Millisecond):
	}
	if bindings := host.Stats().Bindings; bindings != 1 {
		t.Fatalf("Stats().Bindings = %d", bindings)
	}

	if err := binding.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	select {
	case <-host.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the runtime did not idle out after its last binding was removed")
	}
	assertNoRuntimeArtifacts(t, discovery)
	_ = conn.Close()
	assertNoGoroutineLeak(t, before)
}

// TestIncompatibleClientDrainsTheRuntimeWithoutSeveringLiveBindings is the
// rolling binary replacement matrix.
//
// One test binary stands in for two installed ones by giving the runtime and
// the client different protocol windows. A window that still overlaps is a
// reuse; a window that does not is a drain: the incompatible caller is refused
// by name, the live binding keeps running to completion, the runtime is not
// forcibly replaced, and only after the last binding drains does a new runtime
// start under exact owner proof.
func TestIncompatibleClientDrainsTheRuntimeWithoutSeveringLiveBindings(t *testing.T) {
	t.Run("overlapping windows reuse the running runtime", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		host, _ := startTestHost(t, discovery, -1, ProtocolRange{Minimum: 1, Preferred: 2})
		older := dialTestClient(t, discovery, ProtocolRange{Minimum: 1, Preferred: 1})
		if older.Protocol() != 1 {
			t.Fatalf("negotiated protocol = %d, want the shared version 1", older.Protocol())
		}
		if older.Runtime() != host.RuntimeID() {
			t.Fatal("the older client reached a different runtime")
		}
		newer := dialTestClient(t, discovery, ProtocolRange{Minimum: 1, Preferred: 2})
		if newer.Protocol() != 2 || newer.Runtime() != host.RuntimeID() {
			t.Fatalf("newer client = protocol %d runtime %q", newer.Protocol(), newer.Runtime())
		}
		if host.Stats().Draining {
			t.Fatal("a compatible client started a drain")
		}
	})

	t.Run("a disjoint window drains without severing live work", func(t *testing.T) {
		discovery := newRuntimeDiscovery(t)
		ledger := filepath.Join(discovery.Domain(), "ledger.txt")
		host, endpoint := startTestHost(t, discovery, -1, ProtocolRange{Minimum: 1, Preferred: 1})
		live := dialTestClient(t, discovery, ProtocolRange{Minimum: 1, Preferred: 1})
		binding, fence := boundRemote(t, live, "thread-one")
		runtimeBefore := host.RuntimeID()
		socketBefore := statOf(t, discovery.SocketPath())

		_, err := Dial(t.Context(), discovery, DialConfig{Protocol: ProtocolRange{Minimum: 2, Preferred: 2}})
		if RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("incompatible Dial() = %v, want drain-required", err)
		}
		// The refusal must not have cost the running client anything.
		if host.RuntimeID() != runtimeBefore {
			t.Fatal("the runtime was replaced by an incompatible client")
		}
		if after := statOf(t, discovery.SocketPath()); !os.SameFile(socketBefore, after) {
			t.Fatal("the published socket was replaced by an incompatible client")
		}
		if !host.Stats().Draining {
			t.Fatal("the incompatible client did not start a drain")
		}
		endpoint.push(codexappserver.Notification{
			Method: "item/updated",
			Params: json.RawMessage(`{"threadId":"thread-one"}`),
		})
		if event := nextRemoteEvent(t, binding); event.Origin != EventOriginLive {
			t.Fatalf("the live binding stopped delivering during the drain: %+v", event)
		}
		if outcome, err := binding.Submit(t.Context(), fence, Mutation{Method: "turn/steer"}); err != nil ||
			outcome != MutationApplied {
			t.Fatalf("the live binding lost control authority during the drain: %s, %v", outcome, err)
		}
		// A new bind is refused by name while the drain is pending, so the
		// drain is finite instead of being extended by fresh work.
		if _, err := live.Bind(t.Context(), "thread-two", "", nil); RefusalOf(err) != RefusalDrainRequired {
			t.Fatalf("bind during a drain = %v, want drain-required", err)
		}

		if err := binding.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		select {
		case <-host.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the drained runtime did not stop after its last binding was removed")
		}
		assertNoRuntimeArtifacts(t, discovery)

		// Only now may the replacement take the singleton, and it starts a new
		// runtime rather than adopting the identity of the one that drained.
		replacement, err := Ensure(t.Context(), discovery, EnsureConfig{
			Protocol: ProtocolRange{Minimum: 2, Preferred: 2},
			Launch: launcherFunc(func() error {
				startHelper(t, helperRoleRuntime, discovery.Domain(), map[string]string{
					helperLedgerEnv:   ledger,
					helperIdleEnv:     (2 * time.Second).String(),
					helperProtocolEnv: "1:2",
				})
				return nil
			}),
		})
		if err != nil {
			t.Fatalf("Ensure() after the drain = %v", err)
		}
		defer replacement.Close()
		if replacement.Protocol() != 2 {
			t.Fatalf("replacement protocol = %d, want 2", replacement.Protocol())
		}
		if replacement.Runtime() == runtimeBefore {
			t.Fatal("the replacement reported the drained runtime's identity")
		}
	})
}

// launcherFunc adapts a context-free test starter to the Launcher signature.
func launcherFunc(start func() error) Launcher {
	return func(context.Context) error { return start() }
}

// leaveDeadSocket publishes a socket with nothing listening behind it, which is
// exactly what a killed runtime leaves on the filesystem.
func leaveDeadSocket(t *testing.T, discovery Discovery) {
	t.Helper()
	if err := prepareDiscoveryDir(discovery); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: discovery.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatalf("place a dead socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("close the dead socket listener: %v", err)
	}
}

// assertNoRuntimeArtifacts requires the socket and the record to be gone.
func assertNoRuntimeArtifacts(t *testing.T, discovery Discovery) {
	t.Helper()
	waitFor(t, "the runtime socket and record to be removed", func() bool {
		_, socketErr := os.Lstat(discovery.SocketPath())
		_, recordErr := os.Lstat(discovery.RecordPath())
		return os.IsNotExist(socketErr) && os.IsNotExist(recordErr)
	})
}

func statOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	return info
}

func lstatOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", filepath.Base(path), err)
	}
	return info
}
