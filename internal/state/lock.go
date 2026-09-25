package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// LockWaitLimit bounds how long LinesFile.Update waits for another writer to
// release the file's lock.
//
// The picker runs each action command under a 2s deadline whose expiry
// SIGKILLs it (runNativeActionCommand in internal/ui/picker/backend.go). A lock
// holder only reads a few lines and replaces the file, so 500ms leaves at
// least 1.5s of that budget for process start, tmux queries, and the waiter's
// own write. Exceeding it returns ErrLockTimeout instead of waiting forever;
// the next key press retries.
const LockWaitLimit = 500 * time.Millisecond

// ErrLockTimeout reports that Update gave up waiting for the lock.
var ErrLockTimeout = errors.New("state: timed out waiting for lock")

// LockPath returns the persistent lock file Update serializes writers on. It
// is never removed, since removing it would let two writers hold locks on
// different inodes.
func (f LinesFile) LockPath() string {
	return filepath.Join(filepath.Dir(f.path), "."+filepath.Base(f.path)+".lock")
}

// WithLockWaitLimit returns a copy of f whose Update waits at most d for the
// lock. A non-positive d restores LockWaitLimit.
func (f LinesFile) WithLockWaitLimit(d time.Duration) LinesFile {
	f.lockWaitLimit = d
	return f
}

// Update runs a read-modify-write of the file under an exclusive lock, so
// concurrent Updates on the same path never lose each other's changes. update
// receives the current lines and returns the next lines and whether to write
// them; when it returns write=false or an error nothing is written. Plain Read
// stays unlocked because Write replaces the file atomically.
func (f LinesFile) Update(update func(lines []string) ([]string, bool, error)) error {
	if err := EnsurePrivateDir(filepath.Dir(f.path)); err != nil {
		return err
	}
	held, err := f.acquireLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(int(held.Fd()), unix.LOCK_UN)
		_ = held.Close()
	}()

	lines, err := f.Read()
	if err != nil {
		return err
	}
	if f.hooks != nil && f.hooks.afterRead != nil {
		f.hooks.afterRead()
	}
	next, write, err := update(lines)
	if err != nil || !write {
		return err
	}
	return f.Write(next)
}

// acquireLock opens the persistent lock file and takes an exclusive flock on
// it, waiting at most the configured limit.
func (f LinesFile) acquireLock() (*os.File, error) {
	lockPath := f.LockPath()
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, PrivateFileMode) // #nosec G304 -- path is the LinesFile's own private lock sibling
	if err != nil {
		return nil, fmt.Errorf("state: open lock %s: %w", lockPath, err)
	}
	RepairPrivateFile(lockPath)

	fd := int(held.Fd())
	switch err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		return held, nil
	case !errors.Is(err, unix.EWOULDBLOCK):
		_ = held.Close()
		return nil, fmt.Errorf("state: acquire lock %s: %w", lockPath, err)
	}
	if f.hooks != nil && f.hooks.afterContendedLock != nil {
		f.hooks.afterContendedLock()
	}

	limit := f.lockWaitLimit
	if limit <= 0 {
		limit = LockWaitLimit
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()

	granted := make(chan error, 1)
	go func() { granted <- unix.Flock(fd, unix.LOCK_EX) }()

	select {
	case err := <-granted:
		if err != nil {
			_ = held.Close()
			return nil, fmt.Errorf("state: acquire lock %s: %w", lockPath, err)
		}
		return held, nil
	case <-timer.C:
		// The kernel wait outlives the limit, so the descriptor goes to a
		// releaser instead of being abandoned: a grant that arrives after we
		// gave up must not leave the file locked by an Update that is no
		// longer running. The close happens only after the blocking call
		// returned.
		go func() {
			if err := <-granted; err == nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
			}
			_ = held.Close()
		}()
		return nil, fmt.Errorf("%w: %s after %s", ErrLockTimeout, lockPath, limit)
	}
}
