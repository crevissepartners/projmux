package codexgenerationhost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
)

const ownedGenerationProcessHelperRole = "PROJMUX_OWNED_GENERATION_PROCESS_HELPER_ROLE"

func TestOwnedGenerationProcessSessionHelper(t *testing.T) {
	role := os.Getenv(ownedGenerationProcessHelperRole)
	if role == "" {
		return
	}
	socket := os.Getenv("PROJMUX_OWNED_GENERATION_PROCESS_HELPER_SOCKET")
	if role == "leader" {
		command := exec.Command(os.Args[0], "-test.run=^TestOwnedGenerationProcessSessionHelper$")
		command.Env = ownedProcessHelperEnvironment("descendant")
		if err := command.Start(); err != nil {
			os.Exit(41)
		}
		connectOwnedProcessHelper(socket, role)
		if err := command.Wait(); err != nil {
			os.Exit(42)
		}
		return
	}
	connectOwnedProcessHelper(socket, role)
	select {}
}

func connectOwnedProcessHelper(socket, role string) {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		os.Exit(43)
	}
	if _, err := fmt.Fprintln(connection, role); err != nil {
		os.Exit(44)
	}
	// Keep the connection and inherited session token live until the exact
	// private process-group signal terminates this helper.
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err != nil {
		os.Exit(45)
	}
}

func ownedProcessHelperEnvironment(role string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, ownedGenerationProcessHelperRole+"=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, ownedGenerationProcessHelperRole+"="+role)
}

func TestPrivateGenerationHostLaunchProofTableAndLeaseRetention(t *testing.T) {
	root := t.TempDir()
	lease, source := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
	current := filepath.Join(root, "current")
	if err := os.Symlink(source, current); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	config, processSlot := privateHostTestConfig(t, root, lease)
	host, err := StartPrivateGeneration(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	proof := host.Proof()
	if proof.PID == 0 || proof.ProcessGroupID != proof.PID || proof.EndpointRuntimeID == "" ||
		proof.SocketIdentity.Inode == 0 || proof.SocketIdentity.ChangeTimeSeconds == 0 ||
		proof.Executable.Inode == 0 || proof.Executable.ChangeTimeSeconds == 0 {
		t.Fatalf("incomplete launch proof: %+v", proof)
	}
	wantArgv := []string{lease.Paths(codexbundle.RoleServer)[0], "app-server", "--listen", "unix://" + config.SocketPath}
	if got := host.LaunchArgv(); !reflect.DeepEqual(got, wantArgv) {
		t.Fatalf("launch argv = %v, want %v", got, wantArgv)
	}
	if err := host.ReleaseLease(); HostRefusalOf(err) != HostRefusalLeaseHeld || !host.LeaseHeld() {
		t.Fatalf("pre-terminal release = %v held=%v", err, host.LeaseHeld())
	}

	for _, test := range []struct {
		name  string
		drift func(*LaunchProof)
	}{
		{name: "pid", drift: func(proof *LaunchProof) { proof.PID++ }},
		{name: "process group", drift: func(proof *LaunchProof) { proof.ProcessGroupID++ }},
		{name: "socket inode", drift: func(proof *LaunchProof) { proof.SocketIdentity.Inode++ }},
		{name: "executable inode", drift: func(proof *LaunchProof) { proof.Executable.Inode++ }},
		{name: "runtime", drift: func(proof *LaunchProof) { proof.EndpointRuntimeID += "-stale" }},
		{name: "bundle", drift: func(proof *LaunchProof) { proof.BundleID += "-stale" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			presented := proof
			test.drift(&presented)
			for _, action := range []struct {
				name string
				run  func(LaunchProof) error
			}{
				{name: "stop", run: host.Stop},
				{name: "restart", run: host.Restart},
				{name: "kill", run: host.Kill},
			} {
				if err := action.run(presented); HostRefusalOf(err) != HostRefusalLaunchProofMismatch {
					t.Fatalf("%s refusal = %v", action.name, err)
				}
			}
		})
	}
	if got := host.Mutations(); len(got) != 0 {
		t.Fatalf("drift emitted lifecycle mutations: %+v", got)
	}
	if processSlot.Load() == nil {
		t.Fatal("private process was not launched")
	}
}

func TestPrivateGenerationHostLivePIDSocketExecutableDriftKeepsLifecycleAtZero(t *testing.T) {
	for _, test := range []struct {
		name  string
		drift func(*testing.T, *PrivateGenerationHost, *fakeOwnedProcess)
		want  HostRefusal
	}{
		{
			name: "pid reuse after observed exit",
			drift: func(_ *testing.T, _ *PrivateGenerationHost, process *fakeOwnedProcess) {
				process.exit(errors.New("old process exited"))
			},
			want: HostRefusalProcessExited,
		},
		{
			name: "process group identity drift",
			drift: func(_ *testing.T, _ *PrivateGenerationHost, process *fakeOwnedProcess) {
				process.mu.Lock()
				process.processGroupID++
				process.processGroupValid = false
				process.mu.Unlock()
			},
			want: HostRefusalLaunchProofMismatch,
		},
		{
			name: "socket replacement with reused device and inode",
			drift: func(t *testing.T, host *PrivateGenerationHost, process *fakeOwnedProcess) {
				process.replaceSocket(t, host.Proof().SocketPath)
				replacement, err := os.Lstat(host.Proof().SocketPath)
				if err != nil {
					t.Fatal(err)
				}
				replacementIdentity := fileIdentity(replacement)
				host.mu.Lock()
				// Simulate immediate filesystem reuse: SameFile plus every legacy
				// dev/inode/mode/size axis matches. Only the captured change time
				// remains stale, so validation must still fail closed.
				host.socketInfo = replacement
				host.proof.SocketIdentity = replacementIdentity
				host.proof.SocketIdentity.ChangeTimeNanoseconds ^= 1
				host.mu.Unlock()
			},
			want: HostRefusalLaunchProofMismatch,
		},
		{
			name: "executable mode drift",
			drift: func(t *testing.T, host *PrivateGenerationHost, _ *fakeOwnedProcess) {
				if err := os.Chmod(host.Proof().ExecutablePath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: HostRefusalBundleDrift,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
			config, slot := privateHostTestConfig(t, root, lease)
			host, err := StartPrivateGeneration(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			process := slot.Load()
			if process == nil {
				t.Fatal("missing fake process")
			}
			test.drift(t, host, process)
			proof := host.Proof()
			for _, action := range []func(LaunchProof) error{host.Stop, host.Restart, host.Kill} {
				if err := action(proof); HostRefusalOf(err) != test.want {
					t.Fatalf("refusal = %v, want %s", err, test.want)
				}
			}
			if len(host.Mutations()) != 0 {
				t.Fatalf("live proof drift emitted mutations: %+v", host.Mutations())
			}
			process.mu.Lock()
			process.processGroupID = process.pid
			process.processGroupValid = true
			process.mu.Unlock()
		})
	}
}

func TestIncompleteOrDriftedSiblingBundleRefusesBeforePublishAndLifecycle(t *testing.T) {
	for _, test := range []struct {
		name   string
		paths  []string
		drift  func(*testing.T, codexbundle.Lease)
		refuse HostRefusal
	}{
		{
			name:   "helper absent from manifest",
			paths:  []string{"bin/codex", "bin/codex-code-mode-host", "codex-path/rg"},
			refuse: HostRefusalBundleIncomplete,
		},
		{
			name:   "extra caller-selected artifact",
			paths:  append(CompleteBundleArtifactPaths(), "bin/caller-optional-helper"),
			refuse: HostRefusalBundleIncomplete,
		},
		{
			name:  "helper hash drift",
			paths: CompleteBundleArtifactPaths(),
			drift: func(t *testing.T, lease codexbundle.Lease) {
				if err := os.WriteFile(filepath.Join(lease.Root, "codex-path/rg"), []byte("drifted helper"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			refuse: HostRefusalBundleDrift,
		},
		{
			name:  "helper mode drift",
			paths: CompleteBundleArtifactPaths(),
			drift: func(t *testing.T, lease codexbundle.Lease) {
				if err := os.Chmod(filepath.Join(lease.Root, "codex-resources/bwrap"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			refuse: HostRefusalBundleDrift,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, test.paths)
			if test.drift != nil {
				test.drift(t, lease)
			}
			config, _ := privateHostTestConfig(t, root, lease)
			var launches atomic.Int64
			config.launch = func(string, []string, []string) (ownedGenerationProcess, error) {
				launches.Add(1)
				return nil, errors.New("must not launch")
			}
			_, err := StartPrivateGeneration(context.Background(), config)
			if HostRefusalOf(err) != test.refuse {
				t.Fatalf("refusal = %v, want %s", err, test.refuse)
			}
			if launches.Load() != 0 {
				t.Fatalf("invalid bundle published %d processes", launches.Load())
			}
		})
	}

	// Drift after readiness also closes every lifecycle action before a signal.
	for _, drift := range []struct {
		name string
		run  func(*testing.T, codexbundle.Lease)
	}{
		{name: "hash", run: func(t *testing.T, lease codexbundle.Lease) {
			if err := os.WriteFile(filepath.Join(lease.Root, "codex-path/rg"), []byte("post-ready drift"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mode", run: func(t *testing.T, lease codexbundle.Lease) {
			if err := os.Chmod(filepath.Join(lease.Root, "codex-resources/bwrap"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run("post-ready helper "+drift.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
			config, _ := privateHostTestConfig(t, root, lease)
			host, err := StartPrivateGeneration(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			drift.run(t, lease)
			for _, action := range []func(LaunchProof) error{host.Stop, host.Restart, host.Kill} {
				if err := action(host.Proof()); HostRefusalOf(err) != HostRefusalBundleDrift {
					t.Fatalf("post-ready helper %s drift = %v", drift.name, err)
				}
			}
			if len(host.Mutations()) != 0 {
				t.Fatalf("post-ready helper drift mutated lifecycle: %+v", host.Mutations())
			}
		})
	}
}

func TestEveryQualifiedBundleArtifactIsMandatoryBeforePublish(t *testing.T) {
	qualified := CompleteBundleArtifactPaths()
	mutatedCopy := CompleteBundleArtifactPaths()
	mutatedCopy[0] = "caller/weakened"
	if reflect.DeepEqual(mutatedCopy, CompleteBundleArtifactPaths()) {
		t.Fatal("caller mutated the package-owned qualified artifact set")
	}
	for missingIndex, missing := range qualified {
		t.Run(missing, func(t *testing.T) {
			paths := append([]string(nil), qualified[:missingIndex]...)
			paths = append(paths, qualified[missingIndex+1:]...)
			root := t.TempDir()
			if missing == "bin/codex" {
				source := filepath.Join(root, "source")
				var specs []codexbundle.ArtifactSpec
				for _, path := range paths {
					full := filepath.Join(source, filepath.FromSlash(path))
					if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(full, []byte("helper"), 0o755); err != nil {
						t.Fatal(err)
					}
					specs = append(specs, codexbundle.ArtifactSpec{Path: path, Roles: []codexbundle.Role{codexbundle.RoleHelper}})
				}
				_, err := codexbundle.Inspect(source, "0.152.1", codexbundle.ProtocolRange{Min: 2, Max: 2}, specs)
				if codexbundle.RefusalOf(err) != codexbundle.RefusalBundleIncomplete {
					t.Fatalf("missing server/TUI lease = %v", err)
				}
				return
			}
			lease, _ := createCompleteTestLease(t, root, paths)
			config, _ := privateHostTestConfig(t, root, lease)
			var launches atomic.Int64
			config.launch = func(string, []string, []string) (ownedGenerationProcess, error) {
				launches.Add(1)
				return nil, errors.New("must not launch")
			}
			_, err := StartPrivateGeneration(context.Background(), config)
			if HostRefusalOf(err) != HostRefusalBundleIncomplete || launches.Load() != 0 {
				t.Fatalf("missing %s: refusal=%v launches=%d", missing, err, launches.Load())
			}
		})
	}

	// A manifest with the right role counts but the wrong artifact-to-role
	// assignment is still not the Phase 0-qualified complete bundle.
	root := t.TempDir()
	source := filepath.Join(root, "wrong-roles-source")
	specs := []codexbundle.ArtifactSpec{
		{Path: "bin/codex", Roles: []codexbundle.Role{codexbundle.RoleServer}},
		{Path: "bin/codex-code-mode-host", Roles: []codexbundle.Role{codexbundle.RoleTUI, codexbundle.RoleHelper}},
		{Path: "codex-path/rg", Roles: []codexbundle.Role{codexbundle.RoleHelper}},
		{Path: "codex-resources/bwrap", Roles: []codexbundle.Role{codexbundle.RoleHelper}},
	}
	for _, spec := range specs {
		path := filepath.Join(source, filepath.FromSlash(spec.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	manifest, err := codexbundle.Inspect(source, "0.152.1", protocol, specs)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := codexbundle.Create(filepath.Join(root, "wrong-role-store"), source, manifest, protocol)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := privateHostTestConfig(t, root, lease)
	var launches atomic.Int64
	config.launch = func(string, []string, []string) (ownedGenerationProcess, error) {
		launches.Add(1)
		return nil, errors.New("must not launch")
	}
	_, err = StartPrivateGeneration(context.Background(), config)
	if HostRefusalOf(err) != HostRefusalBundleIncomplete || launches.Load() != 0 {
		t.Fatalf("wrong qualified roles: refusal=%v launches=%d", err, launches.Load())
	}
}

func TestLaunchBoundaryBundleDriftRefusesPublicationAndCleansExactChild(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "executable", path: "bin/codex"},
		{name: "sibling helper", path: "codex-path/rg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
			config, slot := privateHostTestConfig(t, root, lease)
			launch := config.launch
			config.launch = func(executable string, args, environment []string) (ownedGenerationProcess, error) {
				path := filepath.Join(lease.Root, filepath.FromSlash(test.path))
				raw, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				raw[len(raw)-1] ^= 1
				if err := os.WriteFile(path, raw, 0o755); err != nil {
					return nil, err
				}
				return launch(executable, args, environment)
			}
			host, err := StartPrivateGeneration(context.Background(), config)
			if host != nil || HostRefusalOf(err) != HostRefusalBundleDrift {
				t.Fatalf("launch-boundary drift published host=%v err=%v", host, err)
			}
			process := slot.Load()
			if process == nil {
				t.Fatal("drift seam did not reach the exact child boundary")
			}
			select {
			case <-process.Done():
			default:
				t.Fatal("refused launch left its exact child live")
			}
			if _, statErr := os.Lstat(config.SocketPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refused launch left socket: %v", statErr)
			}
		})
	}
}

func TestProcessExitBarrierIsRepeatableAndCloseAfterObservationDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
	config, slot := privateHostTestConfig(t, root, lease)
	host, err := StartPrivateGeneration(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	process := slot.Load()
	process.exit(errors.New("observed exit"))
	for index := range 2 {
		select {
		case <-process.Done():
		case <-time.After(time.Second):
			t.Fatalf("exit observation %d blocked", index+1)
		}
	}
	if err := host.ReleaseLease(); HostRefusalOf(err) != HostRefusalLeaseHeld || !host.LeaseHeld() {
		t.Fatalf("process exit forged lease release: err=%v held=%v", err, host.LeaseHeld())
	}
	closed := make(chan struct{})
	go func() {
		_ = host.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked after an earlier exit observation")
	}

	earlyConfig, _ := privateHostTestConfig(t, t.TempDir(), lease)
	earlyConfig.launch = func(string, []string, []string) (ownedGenerationProcess, error) {
		process := newFakeOwnedProcess(9002, nil)
		process.exit(errors.New("early exit"))
		return process, nil
	}
	_, err = StartPrivateGeneration(context.Background(), earlyConfig)
	if HostRefusalOf(err) != HostRefusalProcessExited {
		t.Fatalf("early exit refusal = %v", err)
	}
}

func TestPrivateGenerationHostCloseWaitsForOwnedSessionDescendants(t *testing.T) {
	root := t.TempDir()
	lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
	config, slot := privateHostTestConfig(t, root, lease)
	host, err := StartPrivateGeneration(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	process := slot.Load()
	waitObserved := process.holdSessionCompletion()
	closed := make(chan error, 1)
	go func() { closed <- host.Close() }()
	select {
	case <-waitObserved:
	case <-time.After(time.Second):
		t.Fatal("Close did not reach the owned-session completion barrier")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the session descendant barrier: %v", err)
	default:
	}
	process.releaseSessionCompletion()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after session descendant completion")
	}
	if _, err := os.Lstat(config.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact private socket remains after session completion: %v", err)
	}
}

func TestPrivateGenerationHostCloseProcessSessionBarrierStateTable(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*fakeOwnedProcess)
	}{
		{
			name: "leader exits before token descendant",
			prepare: func(process *fakeOwnedProcess) {
				process.holdSessionCompletion()
				process.exit(errors.New("leader exited first"))
			},
		},
		{
			name: "token closes before live leader",
			prepare: func(process *fakeOwnedProcess) {
				process.releaseSessionCompletion()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
			config, slot := privateHostTestConfig(t, root, lease)
			host, err := StartPrivateGeneration(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			process := slot.Load()
			test.prepare(process)
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			process.mu.Lock()
			signals := process.sessionSignals
			process.mu.Unlock()
			if signals != 1 {
				t.Fatalf("exact private process-group signals = %d, want 1", signals)
			}
			select {
			case <-process.Done():
			default:
				t.Fatal("leader exit barrier remained open")
			}
			select {
			case <-process.SessionDone():
			default:
				t.Fatal("owned-session exit barrier remained open")
			}
		})
	}
}

func TestOwnedGenerationProcessCleanupSignalsExactSessionAndWaitsForDescendantExit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-owned-session-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "ready.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	environment := append(ownedProcessHelperEnvironment("leader"),
		"PROJMUX_OWNED_GENERATION_PROCESS_HELPER_SOCKET="+socket)
	process, err := launchOwnedGenerationProcess(
		os.Args[0], []string{"-test.run=^TestOwnedGenerationProcessSessionHelper$"}, environment,
	)
	if err != nil {
		t.Fatal(err)
	}
	terminated := false
	t.Cleanup(func() {
		if !terminated {
			_ = terminateOwnedSession(process)
		}
	})
	if process.ProcessGroupID() != process.PID() || process.ValidateProcessGroup() != nil {
		t.Fatalf("private Setsid proof pid=%d pgid=%d", process.PID(), process.ProcessGroupID())
	}
	if err := listener.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	readers := make([]*bufio.Reader, 0, 2)
	roles := make(map[string]bool, 2)
	connections := make([]*net.UnixConn, 0, 2)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range 2 {
		connection, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		reader := bufio.NewReader(connection)
		role, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		roles[strings.TrimSpace(role)] = true
		readers = append(readers, reader)
	}
	if !roles["leader"] || !roles["descendant"] {
		t.Fatalf("private session helper roles = %v", roles)
	}
	if err := terminateOwnedSession(process); err != nil {
		t.Fatal(err)
	}
	terminated = true
	for index, connection := range connections {
		if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := readers[index].ReadByte(); !errors.Is(err, io.EOF) {
			t.Fatalf("owned session connection %d remained live: %v", index, err)
		}
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("private process leader barrier remained open")
	}
	select {
	case <-process.SessionDone():
	default:
		t.Fatal("private process session barrier remained open")
	}
}

func TestPrivateGenerationHostRejectsOutsideSymlinkOrPermissiveRootWithZeroMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *PrivateGenerationConfig)
	}{
		{
			name: "outside private root",
			mutate: func(_ *testing.T, config *PrivateGenerationConfig) {
				config.SocketPath = filepath.Join(rootOfTestConfig(config), "app-server-control", "app-server-control.sock")
			},
		},
		{
			name: "symlink private root",
			mutate: func(t *testing.T, config *PrivateGenerationConfig) {
				link := filepath.Join(rootOfTestConfig(config), "private-root-link")
				if err := os.Symlink(config.PrivateRoot, link); err != nil {
					t.Fatal(err)
				}
				config.PrivateRoot = link
				config.SocketPath = filepath.Join(link, "codex-"+config.Endpoint.EndpointGenerationID+".sock")
			},
		},
		{
			name: "permissive private root",
			mutate: func(t *testing.T, config *PrivateGenerationConfig) {
				if err := os.Chmod(config.PrivateRoot, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
			config, _ := privateHostTestConfig(t, root, lease)
			test.mutate(t, &config)
			socketPath := config.SocketPath
			before, statErr := os.Lstat(config.PrivateRoot)
			var launches atomic.Int64
			config.launch = func(string, []string, []string) (ownedGenerationProcess, error) {
				launches.Add(1)
				return nil, errors.New("must not launch")
			}
			_, err := StartPrivateGeneration(context.Background(), config)
			if HostRefusalOf(err) != HostRefusalPrivateRootInvalid {
				t.Fatalf("refusal = %v", err)
			}
			if launches.Load() != 0 {
				t.Fatalf("invalid private root launched %d processes", launches.Load())
			}
			if _, socketErr := os.Lstat(socketPath); !errors.Is(socketErr, os.ErrNotExist) {
				t.Fatalf("invalid private root created a socket: %v", socketErr)
			}
			after, afterErr := os.Lstat(config.PrivateRoot)
			if statErr == nil && (afterErr != nil || before.Mode() != after.Mode() || !os.SameFile(before, after)) {
				t.Fatalf("private root was mutated: before=%v after=%v err=%v", before.Mode(), after.Mode(), afterErr)
			}
		})
	}
}

func TestOwnerPrivateRootStableSymlinkAncestorAndPublicationIdentity(t *testing.T) {
	root := t.TempDir()
	firstParent := filepath.Join(root, "first")
	secondParent := filepath.Join(root, "second")
	for _, parent := range []string{firstParent, secondParent} {
		if err := os.MkdirAll(filepath.Join(parent, "private"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ancestor := filepath.Join(root, "stable-system-like-link")
	if err := os.Symlink(firstParent, ancestor); err != nil {
		t.Fatal(err)
	}
	privateRoot := filepath.Join(ancestor, "private")
	initial, err := ownerPrivateDirectory(privateRoot)
	if err != nil {
		t.Fatalf("stable symlink ancestor was rejected: %v", err)
	}
	if err := revalidateOwnerPrivateDirectory(privateRoot, initial); err != nil {
		t.Fatalf("stable ancestor changed the same private leaf: %v", err)
	}
	if err := os.Remove(ancestor); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondParent, ancestor); err != nil {
		t.Fatal(err)
	}
	if err := revalidateOwnerPrivateDirectory(privateRoot, initial); HostRefusalOf(err) != HostRefusalPrivateRootInvalid {
		t.Fatalf("publication boundary accepted replaced ancestor: %v", err)
	}
}

func rootOfTestConfig(config *PrivateGenerationConfig) string {
	return filepath.Dir(config.StateDomainPath)
}

type fakeOwnedProcess struct {
	pid            int
	processGroupID int
	done           chan struct{}
	sessionDone    chan struct{}
	once           sync.Once
	sessionOnce    sync.Once

	mu                  sync.Mutex
	exitErr             error
	listener            *net.UnixListener
	processGroupValid   bool
	holdSession         bool
	sessionWaitObserved chan struct{}
	sessionWaitOnce     sync.Once
	sessionSignals      int
}

func newFakeOwnedProcess(pid int, listener *net.UnixListener) *fakeOwnedProcess {
	return &fakeOwnedProcess{
		pid: pid, processGroupID: pid, done: make(chan struct{}), sessionDone: make(chan struct{}),
		listener: listener, processGroupValid: true,
	}
}

func (process *fakeOwnedProcess) PID() int { return process.pid }
func (process *fakeOwnedProcess) ProcessGroupID() int {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.processGroupID
}
func (process *fakeOwnedProcess) ValidateProcessGroup() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if !process.processGroupValid || process.processGroupID != process.pid {
		return errors.New("fake private process group drift")
	}
	return nil
}
func (process *fakeOwnedProcess) Done() <-chan struct{} { return process.done }
func (process *fakeOwnedProcess) SessionDone() <-chan struct{} {
	process.mu.Lock()
	observed := process.sessionWaitObserved
	process.mu.Unlock()
	if observed != nil {
		process.sessionWaitOnce.Do(func() { close(observed) })
	}
	return process.sessionDone
}
func (process *fakeOwnedProcess) ExitError() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.exitErr
}
func (process *fakeOwnedProcess) Signal(os.Signal) error {
	if err := process.ValidateProcessGroup(); err != nil {
		return err
	}
	return process.SignalProcessGroup(os.Kill)
}
func (process *fakeOwnedProcess) SignalProcessGroup(os.Signal) error {
	process.mu.Lock()
	process.sessionSignals++
	process.mu.Unlock()
	select {
	case <-process.done:
		process.releaseSessionCompletion()
		return nil
	default:
	}
	process.exit(nil)
	return nil
}
func (process *fakeOwnedProcess) exit(err error) {
	process.once.Do(func() {
		process.mu.Lock()
		process.exitErr = err
		listener := process.listener
		process.listener = nil
		process.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		close(process.done)
		process.mu.Lock()
		holdSession := process.holdSession
		process.mu.Unlock()
		if !holdSession {
			process.sessionOnce.Do(func() { close(process.sessionDone) })
		}
	})
}
func (process *fakeOwnedProcess) holdSessionCompletion() <-chan struct{} {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.holdSession = true
	process.sessionWaitObserved = make(chan struct{})
	return process.sessionWaitObserved
}
func (process *fakeOwnedProcess) releaseSessionCompletion() {
	process.sessionOnce.Do(func() { close(process.sessionDone) })
}
func (process *fakeOwnedProcess) replaceSocket(t *testing.T, path string) {
	t.Helper()
	process.mu.Lock()
	old := process.listener
	process.listener = nil
	process.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	_ = os.Remove(path)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(true)
	process.mu.Lock()
	process.listener = listener
	process.mu.Unlock()
}

type fakeProcessSlot struct {
	mu      sync.Mutex
	process *fakeOwnedProcess
}

func (slot *fakeProcessSlot) Store(process *fakeOwnedProcess) {
	slot.mu.Lock()
	slot.process = process
	slot.mu.Unlock()
}
func (slot *fakeProcessSlot) Load() *fakeOwnedProcess {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.process
}

func privateHostTestConfig(t *testing.T, root string, lease codexbundle.Lease) (PrivateGenerationConfig, *fakeProcessSlot) {
	t.Helper()
	stateDomain := filepath.Join(root, "state-domain")
	socketRoot, err := os.MkdirTemp("/tmp", "pmx-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "codex-generation-152-1.sock")
	if err := os.MkdirAll(stateDomain, 0o700); err != nil {
		t.Fatal(err)
	}
	slot := &fakeProcessSlot{}
	config := PrivateGenerationConfig{
		Endpoint:         EndpointIdentity{StateDomainID: "domain-private", EndpointGenerationID: "generation-152-1"},
		StateDomainPath:  stateDomain,
		PrivateRoot:      socketRoot,
		SocketPath:       socket,
		LeaseRoot:        lease.Root,
		RequiredProtocol: codexbundle.ProtocolRange{Min: 2, Max: 2},
		ReadyTimeout:     2 * time.Second,
		Environment:      []string{"PATH=/usr/bin", "TMUX=/ambient", "TMUX_PANE=%999"},
	}
	config.ready = func(_ context.Context, path string) error {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return errors.New("private listener is not ready")
		}
		return nil
	}
	config.launch = func(_ string, _ []string, environment []string) (ownedGenerationProcess, error) {
		for _, entry := range environment {
			if entry == "TMUX=/ambient" || entry == "TMUX_PANE=%999" {
				return nil, errors.New("inherited tmux environment reached private host")
			}
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			return nil, err
		}
		listener.SetUnlinkOnClose(true)
		process := newFakeOwnedProcess(9001, listener)
		slot.Store(process)
		return process, nil
	}
	return config, slot
}

func createCompleteTestLease(t *testing.T, root string, paths []string) (codexbundle.Lease, string) {
	t.Helper()
	source := filepath.Join(root, "source")
	store := filepath.Join(root, "store")
	specs := make([]codexbundle.ArtifactSpec, 0, len(paths))
	for _, path := range paths {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("#!/bin/sh\nexit 0\n# "+path+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		roles := []codexbundle.Role{codexbundle.RoleHelper}
		if path == "bin/codex" {
			roles = []codexbundle.Role{codexbundle.RoleServer, codexbundle.RoleTUI}
		}
		specs = append(specs, codexbundle.ArtifactSpec{Path: path, Roles: roles})
	}
	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	manifest, err := codexbundle.Inspect(source, "0.152.1", protocol, specs)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := codexbundle.Create(store, source, manifest, protocol)
	if err != nil {
		t.Fatal(err)
	}
	return lease, source
}

func TestExactRoleTUIArtifactRefusesWrongAbsolutePathAndPostVerifyDrift(t *testing.T) {
	root := t.TempDir()
	lease, _ := createCompleteTestLease(t, root, CompleteBundleArtifactPaths())
	config := PrivateGenerationConfig{LeaseRoot: lease.Root, RequiredProtocol: codexbundle.ProtocolRange{Min: 2, Max: 2}}
	tuis := lease.Paths(codexbundle.RoleTUI)
	if len(tuis) != 1 {
		t.Fatalf("TUI paths = %v", tuis)
	}
	if err := observeExactTUIArtifact(config, lease.ID, tuis[0]); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(root, "other", "codex")
	if err := observeExactTUIArtifact(config, lease.ID, wrong); HostRefusalOf(err) != HostRefusalLaunchProofMismatch {
		t.Fatalf("wrong absolute TUI refusal = %v", err)
	}
	if err := os.WriteFile(tuis[0], []byte("drifted TUI bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := observeExactTUIArtifact(config, lease.ID, tuis[0]); HostRefusalOf(err) != HostRefusalBundleDrift {
		t.Fatalf("post-verify TUI drift refusal = %v", err)
	}
}

func TestPrivateGenerationEnvironmentRemovesInheritedTmuxRouting(t *testing.T) {
	t.Parallel()
	config := PrivateGenerationConfig{
		StateDomainPath: "/private/state",
		Environment:     []string{"TMUX=/ambient", "TMUX_PANE=%8", "CODEX_HOME=/mutable", "KEEP=yes"},
	}
	if got := privateGenerationEnvironment(config); !reflect.DeepEqual(got, []string{"KEEP=yes", "CODEX_HOME=/private/state"}) {
		t.Fatalf("environment = %v", got)
	}
}
