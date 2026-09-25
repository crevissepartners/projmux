package preview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/state"
)

type lockedStoreOp struct {
	name string
	run  func(Store) error
	// want is the file after the op and a concurrent WriteSelection("other").
	want string
}

func lockedStoreOps() []lockedStoreOp {
	return []lockedStoreOp{
		{
			name: "WriteSelection",
			run:  func(s Store) error { return s.WriteSelection("app", "2", "5") },
			want: "app\t2\t5\nother\t9\t9\n",
		},
		{
			name: "Delete",
			run:  func(s Store) error { return s.Delete("app") },
			want: "other\t9\t9\n",
		},
		{
			name: "CycleWindowSelection",
			run: func(s Store) error {
				result, err := s.CycleWindowSelection("app", []Window{
					{Index: "1", Active: true},
					{Index: "2"},
				}, []Pane{
					{WindowIndex: "1", Index: "0", Active: true},
					{WindowIndex: "2", Index: "5"},
				}, DirectionNext)
				if err == nil && (!result.Changed || result.Cursor != (Cursor{WindowIndex: "2", PaneIndex: "5"})) {
					return errors.New("cycle did not move to window 2 pane 5")
				}
				return err
			},
			want: "app\t2\t5\nother\t9\t9\n",
		},
	}
}

func TestStoreSelectionUpdatesHoldTheStateLock(t *testing.T) {
	t.Parallel()

	for _, op := range lockedStoreOps() {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "preview-state")
			if err := os.WriteFile(path, []byte("app\t1\t0\n"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			loaded := make(chan struct{})
			release := make(chan struct{})
			store := NewStore(path)
			store.afterLoad = func() {
				close(loaded)
				<-release
			}

			opDone := make(chan error, 1)
			go func() { opDone <- op.run(store) }()
			<-loaded

			// The whole read-modify-write runs under the lock: another writer
			// cannot take it between the load and the write.
			probe, err := os.OpenFile(store.file.LockPath(), os.O_RDWR, 0)
			if err != nil {
				close(release)
				t.Fatalf("OpenFile(lock) error = %v", err)
			}
			flockErr := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			_ = probe.Close()
			if !errors.Is(flockErr, unix.EWOULDBLOCK) {
				close(release)
				<-opDone
				t.Fatalf("Flock(LOCK_NB) during %s = %v, want EWOULDBLOCK", op.name, flockErr)
			}

			otherDone := make(chan error, 1)
			go func() { otherDone <- NewStore(path).WriteSelection("other", "9", "9") }()

			close(release)
			if err := <-opDone; err != nil {
				t.Fatalf("%s error = %v", op.name, err)
			}
			if err := <-otherDone; err != nil {
				t.Fatalf("WriteSelection(other) error = %v", err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := string(raw); got != op.want {
				t.Fatalf("file contents = %q, want %q", got, op.want)
			}
		})
	}
}

func TestStoreSelectionUpdatesGiveUpWhenTheLockIsHeld(t *testing.T) {
	t.Parallel()

	for _, op := range lockedStoreOps() {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "preview-state")
			const content = "app\t1\t0\n"
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

			if err := op.run(store); !errors.Is(err, state.ErrLockTimeout) {
				t.Fatalf("%s error = %v, want state.ErrLockTimeout", op.name, err)
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
