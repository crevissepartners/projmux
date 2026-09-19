package picker

import "golang.org/x/sys/unix"

// macOS reads and writes termios with TIOCGETA/TIOCSETA.
const (
	termiosGetRequest = unix.TIOCGETA
	termiosSetRequest = unix.TIOCSETA
)

// macOS has no IUCLC or XCASE flag to clear.
const (
	rawExtraIflagClear = 0
	rawExtraLflagClear = 0
)
