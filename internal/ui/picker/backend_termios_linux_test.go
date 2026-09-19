package picker

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// openTestPTY opens a fresh pseudo-terminal pair and returns its terminal end.
func openTestPTY(t *testing.T) *os.File {
	t.Helper()
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
	return tty
}

// TestEnableRawTerminalMatchesSttyOnARealTerminal is the parity proof for
// replacing the stty processes: on the same pseudo-terminal, the ioctl raw mode
// is bit-for-bit what `stty raw -echo min 0 time 0` leaves, and the restore
// returns the exact prior state.
func TestEnableRawTerminalMatchesSttyOnARealTerminal(t *testing.T) {
	if _, err := exec.LookPath("stty"); err != nil {
		t.Skip("stty is not installed")
	}
	tty := openTestPTY(t)
	fd := int(tty.Fd())
	before, err := unix.IoctlGetTermios(fd, termiosGetRequest)
	if err != nil {
		t.Fatalf("read cooked termios: %v", err)
	}

	stty := exec.Command("stty", "raw", "-echo", "min", "0", "time", "0")
	stty.Stdin = tty
	if out, err := stty.CombinedOutput(); err != nil {
		t.Fatalf("stty raw: %v: %s", err, out)
	}
	bySTTY, err := unix.IoctlGetTermios(fd, termiosGetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if *bySTTY == *before || bySTTY.Lflag&unix.ECHO != 0 {
		t.Fatalf("stty left the pseudo-terminal cooked: %+v", *bySTTY)
	}
	if err := unix.IoctlSetTermios(fd, termiosSetRequest, before); err != nil {
		t.Fatal(err)
	}

	restore, ok := enableRawTerminal(tty)
	if !ok {
		t.Fatal("enableRawTerminal() on a pseudo-terminal reported no raw mode")
	}
	byIoctl, err := unix.IoctlGetTermios(fd, termiosGetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if *byIoctl != *bySTTY {
		t.Fatalf("ioctl raw termios = %+v, stty raw -echo min 0 time 0 = %+v", *byIoctl, *bySTTY)
	}
	restore()
	after, err := unix.IoctlGetTermios(fd, termiosGetRequest)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatalf("restored termios = %+v, want the prior state %+v", *after, *before)
	}
}

// TestDetectNativeLayoutReadsTheTerminalWindowSize covers the TIOCGWINSZ read
// that replaced `stty size`, including the default for a zero size.
func TestDetectNativeLayoutReadsTheTerminalWindowSize(t *testing.T) {
	tty := openTestPTY(t)
	if err := unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 37, Col: 121}); err != nil {
		t.Fatalf("set window size: %v", err)
	}
	if got, want := detectNativeLayout(tty), (nativeLayout{Rows: 37, Cols: 121}); got != want {
		t.Fatalf("detectNativeLayout() = %+v, want %+v", got, want)
	}
	if err := unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{}); err != nil {
		t.Fatalf("clear window size: %v", err)
	}
	if got, want := detectNativeLayout(tty), (nativeLayout{Rows: defaultNativeRows, Cols: defaultNativeCols}); got != want {
		t.Fatalf("detectNativeLayout(zero size) = %+v, want defaults %+v", got, want)
	}
}
