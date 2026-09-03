package codexgenerationhost

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
)

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
