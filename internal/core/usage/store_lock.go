package usage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

// StateLockWaitLimit bounds how long a snapshot read-modify-write waits for
// another process to release the state lock.
//
// A holder only reads and replaces snapshots.json — no adapter call ever runs
// under the lock — so a healthy wait is milliseconds even with a burst of
// status-line redraws. The limit exists so a stuck holder turns a refresh
// into ErrStateLockTimeout instead of hanging the tmux status line; the next
// tick retries.
const StateLockWaitLimit = time.Second

// ErrStateLockTimeout reports that a snapshot read-modify-write gave up
// waiting for the state lock. Nothing was collected or written.
var ErrStateLockTimeout = errors.New("usage: timed out waiting for the snapshot state lock")

// stateLockFileName is the persistent lock file next to snapshots.json. It is
// never removed, since removing it would let two writers hold locks on
// different inodes.
const stateLockFileName = "." + snapshotFileName + ".lock"

// LockPath returns the lock file that serializes snapshot read-modify-writes
// across processes sharing this store's directory.
func (s *Store) LockPath() string {
	return filepath.Join(s.baseDir, stateLockFileName)
}

// lockState takes the exclusive state lock, waiting at most the store's
// limit, and returns the function that releases it. The lock only covers a
// load-modify-save of snapshots.json; callers must release it before any
// adapter call.
func (s *Store) lockState() (func(), error) {
	if err := localstate.EnsurePrivateDir(s.baseDir); err != nil {
		return nil, fmt.Errorf("usage: create cache dir %s: %w", s.baseDir, err)
	}
	lockPath := s.LockPath()
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- path is the store's own private lock sibling
	if err != nil {
		return nil, fmt.Errorf("usage: open state lock %s: %w", lockPath, err)
	}
	localstate.RepairPrivateFile(lockPath)
	release := func() {
		_ = unix.Flock(int(held.Fd()), unix.LOCK_UN)
		_ = held.Close()
	}

	fd := int(held.Fd())
	switch err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		return release, nil
	case !errors.Is(err, unix.EWOULDBLOCK):
		_ = held.Close()
		return nil, fmt.Errorf("usage: acquire state lock %s: %w", lockPath, err)
	}

	limit := s.lockWaitLimit
	if limit <= 0 {
		limit = StateLockWaitLimit
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()

	granted := make(chan error, 1)
	go func() { granted <- unix.Flock(fd, unix.LOCK_EX) }()

	select {
	case err := <-granted:
		if err != nil {
			_ = held.Close()
			return nil, fmt.Errorf("usage: acquire state lock %s: %w", lockPath, err)
		}
		return release, nil
	case <-timer.C:
		// The kernel wait outlives the limit, so the descriptor goes to a
		// releaser instead of being abandoned: a grant that arrives after we
		// gave up must not leave the file locked by a caller that is no
		// longer running. The close happens only after the blocking call
		// returned.
		go func() {
			if err := <-granted; err == nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
			}
			_ = held.Close()
		}()
		return nil, fmt.Errorf("%w: %s after %s", ErrStateLockTimeout, lockPath, limit)
	}
}

// withStateLock runs fn under the state lock.
func (s *Store) withStateLock(fn func() error) error {
	release, err := s.lockState()
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
