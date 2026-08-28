//go:build windows

package codexbroker

import (
	"errors"
	"os"
)

// platformSupported is false here: the runtime's singleton, credential, and
// stale-artifact contracts are stated in Unix socket and filesystem-ownership
// terms, and there is no weaker substitute this package is willing to accept.
const platformSupported = false

func ownedByCurrentUser(os.FileInfo) bool { return false }

func tryLockExclusive(*os.File) (bool, error) {
	return false, errors.New("codex broker runtime requires Unix filesystem semantics")
}

func unlockFile(*os.File) error { return nil }
