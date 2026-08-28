//go:build !windows

package codexbroker

import (
	"os"
	"syscall"
)

// platformSupported reports whether this build can host or reach a broker
// runtime. The runtime's authority model rests on Unix socket semantics and on
// filesystem ownership, so a platform without both is refused outright rather
// than served by a weaker check.
const platformSupported = true

// ownedByCurrentUser reports whether info names an object owned by the uid this
// process runs as. It is the only ownership authority in this package: a pid,
// a path, and a name are all forgeable or reusable, while the uid on the inode
// is what the kernel enforces.
func ownedByCurrentUser(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// tryLockExclusive takes the non-blocking exclusive advisory lock that
// serializes reclaim and launch. It reports whether the lock was taken; a
// refused lock is a normal outcome that means another starter is ahead.
func tryLockExclusive(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch err {
	case nil:
		return true, nil
	case syscall.EWOULDBLOCK:
		return false, nil
	default:
		return false, err
	}
}

// unlockFile releases the advisory lock.
func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
