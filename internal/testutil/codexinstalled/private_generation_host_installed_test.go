package codexinstalled

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

const generationHostSmokeRootEnv = "PROJMUX_CODEX_GENERATION_HOST_SMOKE_ROOT"

// TestInstalledPrivateGenerationHostDualListenerSmoke is the Phase 2 product
// host smoke. It launches only exact leased 0.152.0/0.152.1 private processes,
// waits for initialized listener handshakes, and removes only the two socket
// roots and processes it proved. The ambient/default endpoint is observed
// before and after and is never attached, stopped, restarted, killed, or
// adopted.
func TestInstalledPrivateGenerationHostDualListenerSmoke(t *testing.T) {
	root, enabled, err := SmokeRoot(generationHostSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for the Phase 2 private generation host smoke", generationHostSmokeRootEnv)
	}
	if err := validateInheritedEnvironment(); err != nil {
		t.Fatal(err)
	}
	oldBinary := exactGenerationBinary(t, generationOldEnv, "0.152.0")
	newBinary := exactGenerationBinary(t, generationNewEnv, "0.152.1")
	stateSource := filepath.Clean(os.Getenv(generationStateEnv))
	if !filepath.IsAbs(stateSource) {
		t.Fatalf("%s must be absolute", generationStateEnv)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("private host smoke root must start empty: entries=%d err=%v", len(entries), err)
	}
	bundleRoot, bundleEnabled, err := privateBundleSmokeRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !bundleEnabled {
		t.Fatalf("set %s to a second isolated temp child", generationBundleRootEnv)
	}
	if err := os.MkdirAll(bundleRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bundleInfo, err := os.Lstat(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	bundleStat, statOK := bundleInfo.Sys().(*syscall.Stat_t)
	if !bundleInfo.IsDir() || bundleInfo.Mode()&os.ModeSymlink != 0 ||
		bundleInfo.Mode().Perm() != 0o700 || !statOK || int64(bundleStat.Uid) != int64(os.Geteuid()) {
		t.Fatal("private host bundle root must be exact owner-private 0700")
	}
	if entries, err := os.ReadDir(bundleRoot); err != nil || len(entries) != 0 {
		t.Fatalf("private host bundle root must start empty: entries=%d err=%v", len(entries), err)
	}
	rootRemoved, bundleRootRemoved := false, false
	t.Cleanup(func() {
		if !rootRemoved {
			_ = os.RemoveAll(root)
		}
		if !bundleRootRemoved {
			_ = os.RemoveAll(bundleRoot)
		}
	})

	ambient := captureAmbientEndpoint(t, stateSource)
	stateDomain := filepath.Join(root, "shared-state-domain")
	oldPrivateRoot := filepath.Join(root, "private-old")
	newPrivateRoot := filepath.Join(root, "private-new")
	for _, dir := range []string{stateDomain, oldPrivateRoot, newPrivateRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	copySharedCodexConfig(t, stateSource, stateDomain)
	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	store := filepath.Join(bundleRoot, "bundle-store")
	oldLease := leaseInstalledBundle(t, store, oldBinary, "0.152.0", protocol)
	newLease := leaseInstalledBundle(t, store, newBinary, "0.152.1", protocol)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	oldHost, err := codexgenerationhost.StartPrivateGeneration(ctx, codexgenerationhost.PrivateGenerationConfig{
		Endpoint:        codexgenerationhost.EndpointIdentity{StateDomainID: "installed-shared", EndpointGenerationID: "generation-0.152.0"},
		StateDomainPath: stateDomain, PrivateRoot: oldPrivateRoot,
		SocketPath: filepath.Join(oldPrivateRoot, "codex-generation-0.152.0.sock"),
		LeaseRoot:  oldLease.Root, RequiredProtocol: protocol,
	})
	if err != nil {
		t.Fatalf("start old private host: %v", err)
	}
	defer oldHost.Close()
	newHost, err := codexgenerationhost.StartPrivateGeneration(ctx, codexgenerationhost.PrivateGenerationConfig{
		Endpoint:        codexgenerationhost.EndpointIdentity{StateDomainID: "installed-shared", EndpointGenerationID: "generation-0.152.1"},
		StateDomainPath: stateDomain, PrivateRoot: newPrivateRoot,
		SocketPath: filepath.Join(newPrivateRoot, "codex-generation-0.152.1.sock"),
		LeaseRoot:  newLease.Root, RequiredProtocol: protocol,
	})
	if err != nil {
		t.Fatalf("start new private host: %v", err)
	}
	defer newHost.Close()

	for name, host := range map[string]*codexgenerationhost.PrivateGenerationHost{"old": oldHost, "new": newHost} {
		proof := host.Proof()
		if proof.PID <= 0 || proof.EndpointRuntimeID == "" || proof.SocketIdentity.Inode == 0 ||
			proof.Executable.Inode == 0 || !host.LeaseHeld() {
			t.Fatalf("%s private host proof = %+v held=%v", name, proof, host.LeaseHeld())
		}
		argv := host.LaunchArgv()
		if len(argv) != 4 || argv[0] != proof.ExecutablePath || argv[3] != "unix://"+proof.SocketPath {
			t.Fatalf("%s launch argv = %v", name, argv)
		}
		if len(host.Mutations()) != 0 {
			t.Fatalf("%s host had lifecycle mutations before cleanup: %+v", name, host.Mutations())
		}
	}
	if oldHost.Proof().EndpointRuntimeID == newHost.Proof().EndpointRuntimeID ||
		oldHost.Proof().SocketPath == newHost.Proof().SocketPath || oldLease.ID == newLease.ID {
		t.Fatal("dual private generations collapsed runtime, socket, or leased-version identity")
	}
	if err := oldHost.ReleaseLease(); codexgenerationhost.HostRefusalOf(err) != codexgenerationhost.HostRefusalLeaseHeld {
		t.Fatalf("old pre-terminal lease release = %v", err)
	}
	if err := newHost.ReleaseLease(); codexgenerationhost.HostRefusalOf(err) != codexgenerationhost.HostRefusalLeaseHeld {
		t.Fatalf("new pre-terminal lease release = %v", err)
	}
	assertAmbientEndpointUnchanged(t, ambient)

	if err := oldHost.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newHost.Close(); err != nil {
		t.Fatal(err)
	}
	for _, socket := range []string{oldHost.Proof().SocketPath, newHost.Proof().SocketPath} {
		if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("private socket remains after exact cleanup: %s: %v", filepath.Base(socket), err)
		}
	}
	assertAmbientEndpointUnchanged(t, ambient)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rootRemoved = true
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("exact private host smoke root remains: %v", err)
	}
	if err := os.RemoveAll(bundleRoot); err != nil {
		t.Fatal(err)
	}
	bundleRootRemoved = true
	if _, err := os.Lstat(bundleRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("exact private host bundle root remains: %v", err)
	}
}

func privateBundleSmokeRoot() (string, bool, error) {
	root := strings.TrimSpace(os.Getenv(generationBundleRootEnv))
	if root == "" {
		return "", false, nil
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || filepath.Base(root) == "." {
		return "", true, errors.New("private host bundle root must be absolute")
	}
	for _, parent := range []string{filepath.Clean(os.TempDir()), filepath.Clean("/var/tmp")} {
		if root != parent && strings.HasPrefix(root, parent+string(filepath.Separator)) {
			return root, true, nil
		}
	}
	return "", true, errors.New("private host bundle root must be an isolated child of the system temp or /var/tmp")
}

type ambientEndpointSnapshot struct {
	socketPath string
	socketInfo fs.FileInfo
	pidPath    string
	pidInfo    fs.FileInfo
	pidBytes   []byte
}

func captureAmbientEndpoint(t *testing.T, codexHome string) ambientEndpointSnapshot {
	t.Helper()
	snapshot := ambientEndpointSnapshot{
		socketPath: filepath.Join(codexHome, "app-server-control", "app-server-control.sock"),
		pidPath:    filepath.Join(codexHome, "app-server-daemon", "app-server.pid"),
	}
	snapshot.socketInfo, _ = os.Lstat(snapshot.socketPath)
	snapshot.pidInfo, _ = os.Lstat(snapshot.pidPath)
	if snapshot.pidInfo != nil && snapshot.pidInfo.Mode().IsRegular() {
		snapshot.pidBytes, _ = os.ReadFile(snapshot.pidPath)
	}
	return snapshot
}

func assertAmbientEndpointUnchanged(t *testing.T, before ambientEndpointSnapshot) {
	t.Helper()
	afterSocket, socketErr := os.Lstat(before.socketPath)
	if before.socketInfo == nil {
		if !errors.Is(socketErr, fs.ErrNotExist) {
			t.Fatalf("ambient socket appeared: %v", socketErr)
		}
	} else if socketErr != nil || !os.SameFile(before.socketInfo, afterSocket) || before.socketInfo.Mode() != afterSocket.Mode() {
		t.Fatalf("ambient socket changed: err=%v", socketErr)
	}
	afterPID, pidErr := os.Lstat(before.pidPath)
	if before.pidInfo == nil {
		if !errors.Is(pidErr, fs.ErrNotExist) {
			t.Fatalf("ambient pid record appeared: %v", pidErr)
		}
	} else {
		afterBytes, _ := os.ReadFile(before.pidPath)
		if pidErr != nil || !os.SameFile(before.pidInfo, afterPID) || before.pidInfo.Mode() != afterPID.Mode() ||
			string(before.pidBytes) != string(afterBytes) {
			t.Fatalf("ambient pid record changed: err=%v", pidErr)
		}
	}
}
