package usage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// holdStateLock takes the store's state lock through its own open file
// description, the way another projmux process would, and returns that file.
func holdStateLock(t *testing.T, store *Store) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.LockPath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	holder, err := os.OpenFile(store.LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}
	return holder
}

// lockFileDescriptors counts this process's descriptors open on the store's
// lock file, other than the holder's.
func lockFileDescriptors(t *testing.T, store *Store, holder *os.File) int {
	t.Helper()
	lockInfo, err := os.Stat(store.LockPath())
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatalf("read /dev/fd: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.Name() == strconv.Itoa(int(holder.Fd())) {
			continue
		}
		info, err := os.Stat(filepath.Join("/dev/fd", entry.Name()))
		if err == nil && os.SameFile(info, lockInfo) {
			count++
		}
	}
	return count
}

// tryLockExclusive reports whether a fresh opener gets the state lock
// without waiting, releasing it again when it does.
func tryLockExclusive(t *testing.T, store *Store) bool {
	t.Helper()
	probe, err := os.OpenFile(store.LockPath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer func() { _ = probe.Close() }()
	switch err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
		return true
	case errors.Is(err, unix.EWOULDBLOCK):
		return false
	default:
		t.Fatalf("flock probe: %v", err)
		return false
	}
}

// A long-lived caller refreshing on a timer against a stuck holder must not
// accumulate anything per timed-out call: no goroutine parked in flock, no
// descriptor on the lock file, and no waiter granted the lock after it gave
// up. Not parallel, so the goroutine count is this test's own.
func TestLockStateTimeoutsLeaveNoDescriptorOrGoroutineBehind(t *testing.T) {
	store := NewStore(t.TempDir())
	store.lockWaitLimit = 10 * time.Millisecond
	holder := holdStateLock(t, store)

	baseline := runtime.NumGoroutine()
	const waits = 25
	for i := range waits {
		release, err := store.lockState()
		if !errors.Is(err, ErrStateLockTimeout) {
			if release != nil {
				release()
			}
			t.Fatalf("lockState #%d err = %v, want ErrStateLockTimeout", i, err)
		}
	}

	// Checked while the holder still has the lock: nothing may wait for it.
	if got := lockFileDescriptors(t, store, holder); got != 0 {
		t.Fatalf("lock file descriptors after %d timeouts = %d, want 0", waits, got)
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Fatalf("goroutines after %d timeouts = %d, want at most the baseline %d", waits, got, baseline)
	}

	if err := unix.Flock(int(holder.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if !tryLockExclusive(t, store) {
		t.Fatal("another opener could not take the released lock at once; a timed-out waiter was granted it")
	}
}

func TestLockStateTakesTheLockWhenTheHolderLetsGoWithinTheLimit(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	store.lockWaitLimit = 5 * time.Second
	holder := holdStateLock(t, store)

	unlocked := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		unlocked <- unix.Flock(int(holder.Fd()), unix.LOCK_UN)
	}()

	started := time.Now()
	release, err := store.lockState()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("lockState err = %v, want the lock once the holder let go", err)
	}
	if err := <-unlocked; err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if elapsed >= store.lockWaitLimit {
		t.Fatalf("lockState took %s, want it granted before the %s limit", elapsed, store.lockWaitLimit)
	}
	if tryLockExclusive(t, store) {
		release()
		t.Fatal("another opener took the lock while lockState held it")
	}
	release()
	if !tryLockExclusive(t, store) {
		t.Fatal("another opener could not take the lock after release")
	}
}
