package app

import (
	"errors"
	"os"
	"syscall"
	"testing"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

func TestCodexAuthorityFenceSerializesOnlyTheExactPaneAndReleases(t *testing.T) {
	command := testAICommand(t.TempDir())
	releaseFirst, err := command.acquireCodexAuthorityFence("pane-phase0r-a")
	if err != nil {
		t.Fatal(err)
	}
	path, err := command.codexAuthorityFencePath("pane-phase0r-a")
	if err != nil {
		t.Fatal(err)
	}
	contender, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("same-Pane authority contender lock error = %v, want busy", err)
	}

	releaseOther, err := command.acquireCodexAuthorityFence("pane-phase0r-b")
	if err != nil {
		t.Fatalf("independent Pane authority fence: %v", err)
	}
	releaseOther()
	releaseFirst()
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("same-Pane authority fence after release: %v", err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	releaseAgain, err := command.acquireCodexAuthorityFence("pane-phase0r-a")
	if err != nil {
		t.Fatalf("same-Pane authority fence reacquire: %v", err)
	}
	releaseAgain()
}
