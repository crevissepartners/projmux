package hooks

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// TestIsInteractiveReaderAcceptsPty proves the terminal check still accepts a
// real terminal: the terminal end of a fresh pseudo-terminal pair.
func TestIsInteractiveReaderAcceptsPty(t *testing.T) {
	t.Parallel()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlock pseudo-terminal: %v", err)
	}
	index, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("name pseudo-terminal: %v", err)
	}
	tty, err := os.OpenFile("/dev/pts/"+strconv.Itoa(index), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = tty.Close() })

	if !isInteractiveReader(tty) {
		t.Fatalf("isInteractiveReader(pty) = false, want true")
	}
}
