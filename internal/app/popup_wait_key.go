// Package app: popup-wait-key is a hidden helper used by the statusbar's
// display-only popups (cwd, usage HUD). Those popups want any-key dismissal,
// but the popup payload runs under whatever shell tmux happens to spawn for
// `display-popup -E <command>`. Embedding a raw `read -n1` works in bash and
// zsh but is non-portable (POSIX `sh` rejects `-n`, dash needs `dd`+`stty`
// gymnastics, fish needs yet another form). Routing through a hidden projmux
// subcommand removes that shell dependency: the popup payload just calls
// `<projmux> popup-wait-key` and we handle the raw 1-byte read in Go.
//
// The command is intentionally absent from the top-level `printUsage` so it
// doesn't pollute `projmux help` — it's an internal IPC surface, not a user
// command.
package app

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

// popupWaitKeyCommand reads one byte from the controlling terminal in raw mode
// and exits. Used as the close path for display-only statusbar popups.
type popupWaitKeyCommand struct {
	// openTTY lets tests substitute a stub instead of opening /dev/tty.
	openTTY func() (popupTTY, error)
	// setRawMode flips the tty into a min=1,time=0,no-echo,no-canon state so
	// a single keystroke completes the read. Substitutable for tests.
	setRawMode func(tty popupTTY) (restore func(), err error)
}

// popupTTY is the subset of *os.File popup-wait-key needs. Splitting it lets
// tests avoid touching the real /dev/tty.
type popupTTY interface {
	io.Reader
	io.Closer
	Name() string
}

func newPopupWaitKeyCommand() *popupWaitKeyCommand {
	return &popupWaitKeyCommand{
		openTTY:    defaultPopupTTY,
		setRawMode: defaultPopupRawMode,
	}
}

func defaultPopupTTY() (popupTTY, error) {
	// /dev/tty resolves to the controlling terminal of the calling process.
	// Under `tmux display-popup -E <cmd>`, tmux assigns the popup pane as the
	// controlling terminal of <cmd>, so /dev/tty is the popup's own input
	// stream — exactly what we want to read a single key from.
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// defaultPopupRawMode shells out to `stty` against the supplied tty. We do
// this rather than ioctl-via-syscall so the implementation stays
// dependency-free (no x/sys/unix or x/term in go.mod). `stty` is POSIX and
// present on every platform tmux already runs on.
func defaultPopupRawMode(tty popupTTY) (func(), error) {
	name := tty.Name()
	if name == "" {
		return func() {}, errors.New("popup-wait-key: tty has no name")
	}
	// Snapshot the current termios so we can restore it on exit.
	saved, err := exec.Command("stty", "-g", "-F", name).Output()
	if err != nil {
		// Some older `stty` builds (notably macOS prior to a certain era)
		// don't accept -F and expect the tty on stdin. Retry that shape.
		saved, err = popupSttyStdinGet(name)
		if err != nil {
			return func() {}, err
		}
	}
	settings := string(bytesTrimTrailingNewline(saved))

	// Configure for any-key reads: disable echo + canonical mode, read at
	// least one byte with no inter-byte timeout.
	if err := popupSttyApply(name, "-echo", "-icanon", "min", "1", "time", "0"); err != nil {
		return func() {}, err
	}
	restore := func() {
		_ = popupSttyApply(name, settings)
	}
	return restore, nil
}

// popupSttyApply runs `stty <args>` against the supplied tty path, trying the
// `-F <path>` shape first and falling back to stdin redirection so we cover
// stty implementations on both Linux and the BSDs.
func popupSttyApply(name string, args ...string) error {
	full := append([]string{"-F", name}, args...)
	if err := exec.Command("stty", full...).Run(); err == nil {
		return nil
	}
	// BSD-style fallback: stty reads termios from its stdin.
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command("stty", args...)
	cmd.Stdin = f
	return cmd.Run()
}

func popupSttyStdinGet(name string) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = f
	return cmd.Output()
}

func bytesTrimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// Run reads exactly one byte from /dev/tty in raw mode and returns. Errors
// are intentionally swallowed at the popup level: from the popup's
// perspective, the only observable behavior is "the wait ended", and printing
// a stack trace into the popup body on a failed terminal probe would be
// worse than just closing.
func (c *popupWaitKeyCommand) Run(_ []string, _ io.Writer, _ io.Writer) error {
	if c == nil || c.openTTY == nil {
		return errors.New("popup-wait-key: not configured")
	}
	tty, err := c.openTTY()
	if err != nil {
		// Best effort: if we can't open /dev/tty (e.g. the popup payload was
		// invoked without a controlling terminal during a test substrate),
		// fall back to a read from stdin so the popup still has *some* close
		// affordance. The Enter-only behavior we are replacing was strictly
		// worse than this fallback.
		return popupWaitKeyReadStdin()
	}
	defer tty.Close()

	if c.setRawMode != nil {
		if restore, err := c.setRawMode(tty); err == nil {
			defer restore()
		}
		// If raw-mode setup failed we still attempt the read; the worst
		// case (canonical mode) means the user has to press Enter, which is
		// no worse than the prior behavior we are replacing.
	}

	var buf [1]byte
	_, _ = tty.Read(buf[:])
	return nil
}

func popupWaitKeyReadStdin() error {
	var buf [1]byte
	_, _ = os.Stdin.Read(buf[:])
	return nil
}
