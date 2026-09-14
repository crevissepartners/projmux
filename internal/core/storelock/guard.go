// Package storelock owns the file-lock state a JSON state store keeps beside
// its data file: the sibling lock path, the clock a stale lock is measured
// against, and the jitter source contenders back off with.
//
// It exists because two stores (notify and recentwindows) grew byte-identical
// copies of that state and of the stale-lock break. The retry loop and its
// error wording stay with each store; only the shared state and the two
// operations that read it live here.
package storelock

import (
	"math/rand"
	"os"
	"sync"
	"time"
)

// Guard is one store's file-lock state. Keep it in an unexported field so the
// owning store's public surface does not grow.
type Guard struct {
	// Path is the store's data file.
	Path string
	// LockPath is the sibling lock file acquired around every write.
	LockPath string
	// Clock returns the current time. Tests inject a fixed clock.
	Clock func() time.Time

	// rngMu serializes the concurrency-unsafe seeded source. Contenders reach
	// for jitter before they hold the file lock.
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewGuard roots a guard at path, locking through path+lockSuffix.
func NewGuard(path, lockSuffix string) Guard {
	return Guard{
		Path:     path,
		LockPath: path + lockSuffix,
		Clock:    time.Now,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Jitter returns a random backoff in [0, base] so multiple contenders do not
// synchronise on the same retry schedule.
func (g *Guard) Jitter(base time.Duration) time.Duration {
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return time.Duration(g.rng.Int63n(int64(base) + 1))
}

// TryBreakStale removes the lock file if it is older than staleAfter. The
// check is best-effort: if the stat or the remove fails we treat the lock as
// held and the caller retries.
func (g *Guard) TryBreakStale(staleAfter time.Duration) bool {
	info, err := os.Stat(g.LockPath)
	if err != nil {
		return false
	}
	if g.Clock().Sub(info.ModTime()) < staleAfter {
		return false
	}
	if err := os.Remove(g.LockPath); err != nil {
		return false
	}
	return true
}
