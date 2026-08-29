package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

// acquireControllerLease takes the advisory whole-file lock at path without
// blocking.
//
// The lock is released by the kernel when the process exits, however it exits.
// That is the whole reason this is `flock` rather than a create-exclusive
// lockfile with a staleness timeout: a controller worker that is killed, panics,
// or is stopped mid-pass must not be able to leave a lease that blocks every
// later convergence until some timeout guesses it is gone. There is nothing to
// break, so there is no rule for when breaking it is safe.
func acquireControllerLease(path string) (func(), bool, error) {
	// #nosec G304 -- path is derived from projmux's own state dir and a hash of
	// the exact tmux target; no caller-supplied path reaches it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		return nil, false, fmt.Errorf("open controller worker lease: %w", err)
	}
	localstate.RepairPrivateFile(path)
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return func() {}, false, nil
		}
		return nil, false, fmt.Errorf("lock controller worker lease: %w", err)
	}
	release := func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	return release, true, nil
}
