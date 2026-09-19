package picker

import "golang.org/x/sys/unix"

// Linux reads and writes termios with TCGETS/TCSETS.
const (
	termiosGetRequest = unix.TCGETS
	termiosSetRequest = unix.TCSETS
)

// `stty raw` on Linux also clears IUCLC and XCASE.
const (
	rawExtraIflagClear = unix.IUCLC
	rawExtraLflagClear = unix.XCASE
)
