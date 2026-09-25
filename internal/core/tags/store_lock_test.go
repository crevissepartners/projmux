package tags

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/state"
)

// probeTagLock tries a non-blocking exclusive flock on the tag file's lock. A
// paused update that holds the lock makes it fail with EWOULDBLOCK.
func probeTagLock(store Store) error {
	probe, err := os.OpenFile(store.file.LockPath(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer probe.Close()
	return unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

type overlappingTagUpdate struct {
	// first pauses holding the lock after its read; second overlaps it.
	first  func(Store) error
	second func(Store) error
	want   string
}

func toggleTag(name string) func(Store) error {
	return func(s Store) error {
		_, err := s.Toggle(name)
		return err
	}
}

func clearTags(s Store) error { return s.Clear() }

func runOverlappingTagUpdate(t *testing.T, seed string, update overlappingTagUpdate) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tags")
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	read := make(chan struct{})
	release := make(chan struct{})
	paused := NewStore(path)
	paused.afterRead = func() {
		close(read)
		<-release
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- update.first(paused) }()
	<-read

	// The whole read-modify-write runs under the lock: another writer cannot
	// take it between the read and the write.
	if err := probeTagLock(paused); !errors.Is(err, unix.EWOULDBLOCK) {
		close(release)
		<-firstDone
		t.Fatalf("Flock(LOCK_NB) while the update is paused = %v, want EWOULDBLOCK", err)
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- update.second(NewStore(path)) }()

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first update error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second update error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(raw); got != update.want {
		t.Fatalf("file contents = %q, want %q", got, update.want)
	}
}

func TestStoreToggleKeepsAnOverlappingToggleUnderTheLock(t *testing.T) {
	t.Parallel()

	runOverlappingTagUpdate(t, "keep\n", overlappingTagUpdate{
		first:  toggleTag("alpha"),
		second: toggleTag("beta"),
		want:   "keep\nalpha\nbeta\n",
	})
}

func TestStoreToggleRemovalKeepsAnOverlappingToggleUnderTheLock(t *testing.T) {
	t.Parallel()

	runOverlappingTagUpdate(t, "alpha\nkeep\n", overlappingTagUpdate{
		first:  toggleTag("alpha"),
		second: toggleTag("beta"),
		want:   "keep\nbeta\n",
	})
}

func TestStoreClearAndAnOverlappingToggleBothLandUnderTheLock(t *testing.T) {
	t.Parallel()

	t.Run("clear first", func(t *testing.T) {
		t.Parallel()
		// The Toggle waits for the Clear, so it adds to the cleared file
		// instead of being erased by it.
		runOverlappingTagUpdate(t, "old\n", overlappingTagUpdate{
			first:  clearTags,
			second: toggleTag("beta"),
			want:   "beta\n",
		})
	})
	t.Run("toggle first", func(t *testing.T) {
		t.Parallel()
		// The Clear waits for the Toggle, so it erases the Toggle's tag
		// instead of an unlocked Toggle resurrecting the cleared tags.
		runOverlappingTagUpdate(t, "old\n", overlappingTagUpdate{
			first:  toggleTag("alpha"),
			second: clearTags,
			want:   "",
		})
	})
}

func TestStoreUpdatesGiveUpWhenTheTagLockIsHeld(t *testing.T) {
	t.Parallel()

	for name, op := range map[string]func(Store) error{
		"Toggle": toggleTag("alpha"),
		"Clear":  clearTags,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "tags")
			const content = "keep\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			store := Store{file: state.NewLinesFile(path).WithLockWaitLimit(20 * time.Millisecond)}

			held, err := os.OpenFile(store.file.LockPath(), os.O_CREATE|os.O_RDWR, state.PrivateFileMode)
			if err != nil {
				t.Fatalf("OpenFile(lock) error = %v", err)
			}
			defer held.Close()
			if err := unix.Flock(int(held.Fd()), unix.LOCK_EX); err != nil {
				t.Fatalf("Flock() error = %v", err)
			}

			if err := op(store); !errors.Is(err, state.ErrLockTimeout) {
				t.Fatalf("%s error = %v, want state.ErrLockTimeout", name, err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := string(raw); got != content {
				t.Fatalf("file contents = %q, want unchanged %q", got, content)
			}
		})
	}
}
