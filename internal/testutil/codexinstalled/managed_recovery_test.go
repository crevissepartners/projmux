package codexinstalled

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestManagedRecoveryRequiresNamespaceProofBeforeMutation(t *testing.T) {
	fixture := &Fixture{Root: t.TempDir(), ownsState: true, managed: true}
	if daemon, err := fixture.StartManagedRecovery(context.Background(), ManagerIsolation{}); err == nil || daemon != nil || fixture.managedStarted {
		t.Fatalf("unproved manager was mutated: daemon=%v err=%v started=%t", daemon, err, fixture.managedStarted)
	}
}

func TestManagedRecoveryRefusesWritableReleaseBeforeChangingCurrent(t *testing.T) {
	root := t.TempDir()
	fixture := &Fixture{Root: root, CodexHome: filepath.Join(root, "codex"), ownsState: true}
	release := t.TempDir()
	if err := fixture.SelectManagedRelease(release); err == nil {
		t.Fatal("writable release accepted")
	}
	if _, err := os.Lstat(fixture.CodexHome); !os.IsNotExist(err) {
		t.Fatalf("refused release created managed state: %v", err)
	}
}

func TestManagedRecoveryUnknownOwnerStopCannotSignalAmbientProcess(t *testing.T) {
	fixture := &Fixture{Root: t.TempDir(), managedStarted: true, managedPID: os.Getpid()}
	daemon := &ManagedDaemon{fixture: fixture, Proof: ManagedDaemonProof{PID: os.Getpid()}}
	if err := daemon.Stop(context.Background()); err == nil {
		t.Fatal("unproved ownership accepted")
	}
	if !fixture.managedStarted || fixture.managedPID != os.Getpid() {
		t.Fatal("refused stop altered ownership state")
	}
}

func TestManagedRecoveryColdStartRequiresExactAbsentState(t *testing.T) {
	for _, kind := range []string{"exact-missing", "generic-failure", "unknown-json", "existing-artifact", "existing-socket", "nonempty-manager-root", "replaced-root", "stopped-proof"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "codex")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(root)
			if err != nil {
				t.Fatal(err)
			}
			fixture := &Fixture{Root: root, CodexHome: home, SocketPath: filepath.Join(home, "app-server-control", "app-server-control.sock"), ownsState: true, cleanRootInfo: info, strictManaged: true}
			status := daemonVersion{}
			var probeErr error = &managedCommandError{action: "version", missingSocket: true, err: errors.New("exit 1")}
			want := false
			switch kind {
			case "exact-missing":
				want = true
			case "generic-failure":
				probeErr = errors.New("arbitrary version failure")
			case "unknown-json":
				probeErr = nil
				status = daemonVersion{Status: "stopped", Backend: "unknown"}
			case "existing-artifact":
				path := filepath.Join(home, "app-server-daemon", "app-server.pid")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"pid":1}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "existing-socket":
				if err := os.MkdirAll(filepath.Dir(fixture.SocketPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/missing", fixture.SocketPath); err != nil {
					t.Fatal(err)
				}
			case "nonempty-manager-root":
				path := filepath.Join(home, "app-server-daemon", "foreign.lock")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "replaced-root":
				fixture.cleanRootInfo = nil
			case "stopped-proof":
				fixture.strictManagedStopped = true
				path := filepath.Join(home, "app-server-daemon", "daemon.lock")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				want = true
			}
			if err := fixture.requireStoppedManagedState(status, probeErr); (err == nil) != want {
				t.Fatalf("cold start admitted=%t want=%t: %v", err == nil, want, err)
			}
		})
	}
}

func TestManagedRecoveryVersionMissingClassificationIsExact(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-path", "wrong-exit", "generic", "stdout"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			socket := filepath.Join(root, "owned.sock")
			stderr := "Error: failed to connect to " + socket + "\n\nCaused by:\n    No such file or directory (os error 2)\n"
			code := 1
			stdout := ""
			switch kind {
			case "wrong-path":
				stderr = strings.ReplaceAll(stderr, socket, "/foreign/socket")
			case "wrong-exit":
				code = 2
			case "generic":
				stderr = "permission denied\n"
			case "stdout":
				stdout = "unexpected"
			}
			executable := filepath.Join(root, "codex")
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\nprintf '%s' " + quote(stderr) + " >&2\nprintf '%s' " + quote(stdout) + "\nexit " + strconv.Itoa(code) + "\n"
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			fixture := &Fixture{realCodex: executable, CodexHome: root, SocketPath: socket}
			_, err := fixture.managedCommand(context.Background(), "version")
			var refusal *managedCommandError
			if !errors.As(err, &refusal) || refusal.missingSocket != (kind == "exact") {
				t.Fatalf("missing socket classification=%+v err=%v", refusal, err)
			}
		})
	}
}

func TestManagedRecoveryCleanupNeverFallsBackAfterUnknownStart(t *testing.T) {
	for _, state := range []string{"unknown-before-start", "unverified-start", "verified-live"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "unexpected-stop")
			executable := filepath.Join(root, "codex")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf mutation > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			fixture := &Fixture{Root: root, CodexHome: filepath.Join(root, "codex-home"), realCodex: executable, strictManaged: true, strictManagedUnresolved: true, managedStarted: state != "unknown-before-start"}
			if state == "verified-live" {
				child := exec.Command("sleep", "30")
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				exited := make(chan struct{})
				go func() { _ = child.Wait(); close(exited) }()
				defer func() { _ = child.Process.Kill(); <-exited }()
				fixture.managedPID = child.Process.Pid
				if err := fixture.Cleanup(); err == nil {
					t.Fatal("strict cleanup accepted a live manager")
				}
				select {
				case <-exited:
					t.Fatal("strict cleanup signalled its manager")
				default:
				}
			} else if err := fixture.Cleanup(); err == nil {
				t.Fatal("strict cleanup accepted unresolved ownership")
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatal("strict cleanup issued unchecked official stop")
			}
			if _, err := os.Lstat(root); err != nil {
				t.Fatal("strict cleanup removed unresolved state")
			}
		})
	}
}
