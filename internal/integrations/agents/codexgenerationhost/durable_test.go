package codexgenerationhost

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
)

const durableStopEscalationHelperEnv = "PROJMUX_DURABLE_STOP_ESCALATION_HELPER"

var durableStopEscalationGuard *os.File

func TestDurableStopEscalationHelper(t *testing.T) {
	if os.Getenv(durableStopEscalationHelperEnv) == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	durableStopEscalationGuard = os.NewFile(uintptr(3), "durable-stop-helper-guard")
	ready := os.NewFile(uintptr(4), "durable-stop-helper-ready")
	if durableStopEscalationGuard == nil || ready == nil {
		os.Exit(71)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(72)
	}
	if err := ready.Close(); err != nil {
		os.Exit(73)
	}
	select {}
}

func TestAwaitDurableProofCapturesPostReadinessSocketIdentity(t *testing.T) {
	root := shortDurableTestDir(t)
	lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
	socketPath := filepath.Join(root, "candidate.sock")
	listener, before := listenDurableTestSocket(t, socketPath)
	defer listener.Close()
	processGroupID, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	cfg := PrivateGenerationConfig{
		Endpoint:   EndpointIdentity{StateDomainID: "domain-one", EndpointGenerationID: "generation-new"},
		SocketPath: socketPath, LeaseRoot: lease.Root, RequiredProtocol: codexbundle.ProtocolRange{Min: 2, Max: 2},
		ready: func(_ context.Context, path string) error { return os.Chmod(path, 0o600) },
	}
	intent := DurableLaunchIntent{Endpoint: cfg.Endpoint, EndpointRuntimeID: "runtime-one", PID: os.Getpid(),
		ProcessGroupID: processGroupID, SocketPath: socketPath, ExecutablePath: lease.Paths(codexbundle.RoleServer)[0]}
	proof, err := awaitDurableProof(context.Background(), cfg, lease, intent)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileIdentity(latest); got == before || proof.SocketIdentity != got {
		t.Fatalf("socket identities before=%+v latest=%+v proof=%+v", before, got, proof.SocketIdentity)
	}
}

func TestCleanupDurableCandidateEscalatesExactStubbornGroupAndRepeatIsNoOp(t *testing.T) {
	root := shortDurableTestDir(t)
	guardPath := filepath.Join(root, ".projmux-launch-generation-new.json.guard")
	ownerGuard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(ownerGuard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		ownerGuard.Close()
		t.Fatal(err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		ownerGuard.Close()
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDurableStopEscalationHelper$")
	command.Env = append(os.Environ(), durableStopEscalationHelperEnv+"=1")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.ExtraFiles = []*os.File{ownerGuard, readyWrite}
	if err := command.Start(); err != nil {
		readyRead.Close()
		readyWrite.Close()
		ownerGuard.Close()
		t.Fatal(err)
	}
	_ = readyWrite.Close()
	_ = ownerGuard.Close()
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Wait()
		}
	})
	readyResult := make(chan error, 1)
	go func() {
		var ready [1]byte
		_, readErr := io.ReadFull(readyRead, ready[:])
		readyResult <- readErr
	}()
	select {
	case err := <-readyResult:
		_ = readyRead.Close()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = readyRead.Close()
		t.Fatal("durable stop helper readiness timed out")
	}
	processGroupID, err := syscall.Getpgid(command.Process.Pid)
	if err != nil || processGroupID != command.Process.Pid {
		t.Fatalf("durable stop helper process group = (%d,%v), want %d", processGroupID, err, command.Process.Pid)
	}
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	cfg := PrivateGenerationConfig{
		Endpoint:    EndpointIdentity{StateDomainID: "domain-one", EndpointGenerationID: "generation-new"},
		PrivateRoot: root, SocketPath: filepath.Join(root, "candidate.sock"),
	}
	intent := DurableLaunchIntent{Version: durableLaunchIntentVersion, OperationRef: "upgrade-one", Endpoint: cfg.Endpoint,
		EndpointRuntimeID: "runtime-one", ExecutablePath: executable, SocketPath: cfg.SocketPath,
		Phase: durableLaunchRunning, PID: command.Process.Pid, ProcessGroupID: processGroupID}
	intentPath := durableIntentPath(cfg)
	if err := writeDurableIntent(intentPath, intent); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, err := cleanupDurableCandidate(ctx, cfg, "upgrade-one", nil, 25*time.Millisecond)
	if err != nil || !started {
		t.Fatalf("stubborn exact cleanup = (%t,%v)", started, err)
	}
	_ = command.Wait()
	cleaned = true
	for _, path := range []string{intentPath, guardPath, cfg.SocketPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("exact cleanup retained %s: %v", path, err)
		}
	}
	if started, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil); err != nil || started {
		t.Fatalf("repeated exact cleanup = (%t,%v), want (false,nil)", started, err)
	}
}

func listenDurableTestSocket(t *testing.T, path string) (*net.UnixListener, FileIdentity) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	return listener, fileIdentity(info)
}

func shortDurableTestDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pmx-p4-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestRemoveCandidateSocketRefusesUnknownOrReboundIdentityWithoutUnlink(t *testing.T) {
	root := shortDurableTestDir(t)
	path := filepath.Join(root, "candidate.sock")
	first, firstIdentity := listenDurableTestSocket(t, path)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	rebound, _ := listenDurableTestSocket(t, path)
	defer func() {
		_ = rebound.Close()
		_ = os.Remove(path)
	}()
	if err := removeCandidateSocket(path, nil, nil); err == nil {
		t.Fatal("unpublished socket without exact owned identity was accepted")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("unknown socket was unlinked: %v", err)
	}
	if err := removeCandidateSocket(path, nil, &firstIdentity); err == nil {
		t.Fatal("rebound socket was accepted under the old identity")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("rebound socket was unlinked: %v", err)
	}
}

func TestCleanupDurableCandidateAfterSupervisorExitRemovesOnlyOwnedSocketAndReceipts(t *testing.T) {
	root := shortDurableTestDir(t)
	socket := filepath.Join(root, "candidate.sock")
	listener, identity := listenDurableTestSocket(t, socket)
	defer listener.Close()
	cfg := PrivateGenerationConfig{
		Endpoint:        EndpointIdentity{StateDomainID: "domain-one", EndpointGenerationID: "generation-new"},
		StateDomainPath: filepath.Join(root, "state-domain"), PrivateRoot: root, SocketPath: socket,
		LeaseRoot: filepath.Join(root, "lease"), RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1},
	}
	intent := DurableLaunchIntent{
		Version: durableLaunchIntentVersion, OperationRef: "upgrade-one", Endpoint: cfg.Endpoint,
		EndpointRuntimeID: "runtime-one", ExecutablePath: filepath.Join(root, "lease", "bin", "codex"),
		SocketPath: socket, SocketIdentity: &identity, Phase: durableLaunchRunning, PID: 999999, ProcessGroupID: 999999,
	}
	intentPath, guardPath := durableIntentPath(cfg), durableGuardPath(cfg)
	if err := writeDurableIntent(intentPath, intent); err != nil {
		t.Fatal(err)
	}
	guard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	started, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil)
	if err != nil || !started {
		t.Fatalf("cleanup = (%t,%v)", started, err)
	}
	for _, path := range []string{socket, intentPath, guardPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned candidate artifact remains at %s: %v", path, err)
		}
	}
}

func TestCleanupDurableCandidateRecoversOnlyExactUnlockedOrphanGuard(t *testing.T) {
	newConfig := func(t *testing.T) PrivateGenerationConfig {
		t.Helper()
		root := shortDurableTestDir(t)
		return PrivateGenerationConfig{
			Endpoint:    EndpointIdentity{StateDomainID: "domain-one", EndpointGenerationID: "generation-new"},
			PrivateRoot: root, SocketPath: filepath.Join(root, "candidate.sock"),
		}
	}
	t.Run("unlocked leftover is removed and repeat is a no-op", func(t *testing.T) {
		cfg := newConfig(t)
		guardPath := durableGuardPath(cfg)
		if err := os.WriteFile(guardPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		started, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil)
		if err != nil || started {
			t.Fatalf("orphan cleanup = (%t,%v)", started, err)
		}
		if _, err := os.Lstat(guardPath); !os.IsNotExist(err) {
			t.Fatalf("orphan guard remains: %v", err)
		}
		if started, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil); err != nil || started {
			t.Fatalf("repeat orphan cleanup = (%t,%v)", started, err)
		}
	})
	t.Run("locked guard refuses without unlink", func(t *testing.T) {
		cfg := newConfig(t)
		guardPath := durableGuardPath(cfg)
		guard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer guard.Close()
		if err := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer unix.Flock(int(guard.Fd()), unix.LOCK_UN) //nolint:errcheck -- test cleanup
		if _, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil); err == nil {
			t.Fatal("locked orphan guard was accepted")
		}
		if _, err := os.Lstat(guardPath); err != nil {
			t.Fatalf("locked orphan guard was unlinked: %v", err)
		}
	})
	t.Run("socket presence refuses without unlink", func(t *testing.T) {
		cfg := newConfig(t)
		guardPath := durableGuardPath(cfg)
		if err := os.WriteFile(guardPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		listener, _ := listenDurableTestSocket(t, cfg.SocketPath)
		defer listener.Close()
		if _, err := CleanupDurableCandidate(context.Background(), cfg, "upgrade-one", nil); err == nil {
			t.Fatal("orphan guard with socket was accepted")
		}
		for _, path := range []string{guardPath, cfg.SocketPath} {
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("refused path %s was unlinked: %v", path, err)
			}
		}
	})
}
