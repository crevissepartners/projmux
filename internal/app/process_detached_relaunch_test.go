package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetachedProcessRelaunchFixturePhysicalPaths(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	got := hostMoveFixturePhysicalDir(t, filepath.Join(alias, "tmux"))
	want, err := filepath.EvalSymlinks(filepath.Join(real, "tmux"))
	if err != nil || got != want {
		t.Fatalf("planned socket root %q differs from physical %q: %v", got, want, err)
	}
}

func TestDetachedProcessRelaunchFixtureControlPathBound(t *testing.T) {
	before := os.Getenv("TMPDIR")
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 100))
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	roots := []string{long, os.TempDir(), repoRootForGate(t)}
	first := codexHostMoveFixtureShortState(t, roots...)
	second := codexHostMoveFixtureShortState(t, roots...)
	if first == second {
		t.Fatal("fixture shared an existing state directory")
	}
	for _, root := range []string{first, second} {
		path, err := agentControlSocketPath(filepath.Join(root, "projmux"), phase6CLIEndpoint(), phase6Identity())
		if err != nil {
			t.Fatalf("fixture still exceeds native control bound: %s: %v", root, err)
		}
		if len(path) > agentControlMaxSocketLen {
			t.Fatal("control path exceeds public bound", path)
		}
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory: %v %v", info, err)
		}
	}
	if os.Getenv("TMPDIR") != before {
		t.Fatal("fixture changed TMPDIR")
	}
}

func TestDetachedRelaunchRecoveryPreservesDestinationContract(t *testing.T) {
	reg := coremetadata.Registry{}
	target := coremetadata.Agent{}
	target.Metadata.UID, target.Metadata.Name, target.Spec.Provider = "agt-source", "source", aiModeClaude
	request := agentRelaunchRequest{detach: true, host: "process", model: "new-model"}
	recovery := tmuxTransferRecovery(reg, target, request)
	if strings.Contains(recovery, "--detach") || strings.Contains(recovery, "--host process") || strings.Contains(recovery, "--model") {
		t.Fatal("tmux recovery retained target-only options", recovery)
	}
	retry := relaunchRecoveryCommand(reg, target, request)
	if !strings.Contains(retry, "agent relaunch") || !strings.Contains(retry, "--detach") || !strings.Contains(retry, "--host process") {
		t.Fatal("detached retry lost lifetime/destination", retry)
	}
}
