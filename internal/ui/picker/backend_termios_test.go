package picker

import (
	"bytes"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRawTermiosIsSttyRawNoEchoMinZeroTimeZero pins the termios transform the
// picker applies before its first frame to what `stty raw -echo min 0 time 0`
// sets: every raw input flag and OPOST cleared, no canonical mode, signals, or
// echo, reads that return at once, and every other bit -- the control flags
// above all -- left exactly as it was.
func TestRawTermiosIsSttyRawNoEchoMinZeroTimeZero(t *testing.T) {
	var cooked unix.Termios
	cooked.Iflag = ^cooked.Iflag
	cooked.Oflag = ^cooked.Oflag
	cooked.Lflag = ^cooked.Lflag
	cooked.Cflag = ^cooked.Cflag
	for i := range cooked.Cc {
		cooked.Cc[i] = 0x55
	}

	raw := rawTermios(cooked)

	const iflag = unix.IGNBRK | unix.BRKINT | unix.IGNPAR | unix.PARMRK | unix.INPCK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY | unix.IMAXBEL | rawExtraIflagClear
	const lflag = unix.ICANON | unix.ISIG | unix.ECHO | rawExtraLflagClear
	if raw.Iflag&iflag != 0 {
		t.Fatalf("raw Iflag = %#x keeps %#x set", raw.Iflag, raw.Iflag&iflag)
	}
	if raw.Iflag != cooked.Iflag&^iflag {
		t.Fatalf("raw Iflag = %#x, want only the stty raw input flags cleared from %#x", raw.Iflag, cooked.Iflag)
	}
	if raw.Oflag != cooked.Oflag&^unix.OPOST {
		t.Fatalf("raw Oflag = %#x, want only OPOST cleared from %#x", raw.Oflag, cooked.Oflag)
	}
	if raw.Lflag&lflag != 0 || raw.Lflag != cooked.Lflag&^lflag {
		t.Fatalf("raw Lflag = %#x, want only ICANON, ISIG, ECHO (and XCASE on Linux) cleared from %#x", raw.Lflag, cooked.Lflag)
	}
	if raw.Cflag != cooked.Cflag {
		t.Fatalf("raw Cflag = %#x, want the control flags untouched (%#x)", raw.Cflag, cooked.Cflag)
	}
	if raw.Cc[unix.VMIN] != 0 || raw.Cc[unix.VTIME] != 0 {
		t.Fatalf("raw VMIN/VTIME = %d/%d, want 0/0", raw.Cc[unix.VMIN], raw.Cc[unix.VTIME])
	}
	for i := range raw.Cc {
		if i == unix.VMIN || i == unix.VTIME {
			continue
		}
		if raw.Cc[i] != cooked.Cc[i] {
			t.Fatalf("raw Cc[%d] = %#x, want the control character untouched (%#x)", i, raw.Cc[i], cooked.Cc[i])
		}
	}
	if raw.Ispeed != cooked.Ispeed || raw.Ospeed != cooked.Ospeed {
		t.Fatalf("raw speeds = %d/%d, want untouched %d/%d", raw.Ispeed, raw.Ospeed, cooked.Ispeed, cooked.Ospeed)
	}
}

// TestTerminalControlWithoutATerminalKeepsItsFallbacks keeps the contract the
// stty processes had: a reader that is not a file, or a file that is not a
// terminal, is left alone and the layout is the default one.
func TestTerminalControlWithoutATerminalKeepsItsFallbacks(t *testing.T) {
	if restore, ok := enableRawTerminal(bytes.NewReader(nil)); ok {
		restore()
		t.Fatal("enableRawTerminal() on a non-file reader reported raw mode")
	}
	file, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if restore, ok := enableRawTerminal(file); ok {
		restore()
		t.Fatal("enableRawTerminal() on a regular file reported raw mode")
	}
	want := nativeLayout{Rows: defaultNativeRows, Cols: defaultNativeCols}
	if got := detectNativeLayout(file); got != want {
		t.Fatalf("detectNativeLayout(regular file) = %+v, want defaults %+v", got, want)
	}
	if got := detectNativeLayout(bytes.NewReader(nil)); got != want {
		t.Fatalf("detectNativeLayout(non-file) = %+v, want defaults %+v", got, want)
	}
}
