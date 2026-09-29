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

// stateLockRetryInterval is the pause between non-blocking attempts while
// another holder keeps the state lock. It bounds how late a waiter notices a
// release; a healthy holder is done within one or two intervals.
const stateLockRetryInterval = 5 * time.Millisecond

// lockState takes the exclusive state lock, waiting at most the store's
// limit, and returns the function that releases it. The lock only covers a
// load-modify-save of snapshots.json; callers must release it before any
// adapter call.
//
// The wait retries LOCK_EX|LOCK_NB on the calling goroutine instead of
// parking a helper in a blocking flock: a blocking wait cannot be cancelled,
// so every timed-out call would leave a goroutine, the descriptor, and an OS
// thread behind until the holder let go, and Go never returns the thread. A
// long-lived caller refreshing on a timer against a stuck holder would pile
// them up. With retries, a timed-out call has closed its descriptor before it
// returns and can never be granted the lock later. Waiters are not served in
// arrival order.
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

	limit := s.lockWaitLimit
	if limit <= 0 {
		limit = StateLockWaitLimit
	}
	deadline := time.Now().Add(limit)
	fd := int(held.Fd())
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = held.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = held.Close()
			return nil, fmt.Errorf("usage: acquire state lock %s: %w", lockPath, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = held.Close()
			return nil, fmt.Errorf("%w: %s after %s", ErrStateLockTimeout, lockPath, limit)
		}
		time.Sleep(min(stateLockRetryInterval, remaining))
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
