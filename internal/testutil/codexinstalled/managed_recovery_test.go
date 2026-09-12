package codexinstalled

import (
	"context"
	"os"
	"path/filepath"
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
