package pins

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/state"
)

const lockTestSeed = "projmux-pins v2\nproject proj-keep\n"

func withPin(pin Pin) func(Set) (Set, bool, error) {
	return func(set Set) (Set, bool, error) {
		next := set.With(pin)
		return next, !next.Equal(set), nil
	}
}

func withoutPin(pin Pin) func(Set) (Set, bool, error) {
	return func(set Set) (Set, bool, error) {
		next := set.Without(pin)
		return next, !next.Equal(set), nil
	}
}

func seedPinFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readPinFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(raw)
}

func TestStoreUpdateKeepsAnOverlappingUpdateUnderTheLock(t *testing.T) {
	t.Parallel()

	alpha := Pin{Kind: KindProject, Value: "proj-alpha"}
	beta := Pin{Kind: KindCandidate, Value: "/srv/beta"}
	keep := Pin{Kind: KindProject, Value: "proj-keep"}

	for _, test := range []struct {
		name   string
		first  func(Set) (Set, bool, error)
		second func(Set) (Set, bool, error)
		want   string
	}{
		{
			name:   "add then add",
			first:  withPin(alpha),
			second: withPin(beta),
			want:   "projmux-pins v2\nproject proj-keep\nproject proj-alpha\ncandidate /srv/beta\n",
		},
		{
			name:   "remove then add",
			first:  withoutPin(keep),
			second: withPin(beta),
			want:   "projmux-pins v2\ncandidate /srv/beta\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := seedPinFile(t, lockTestSeed)
			loaded := make(chan struct{})
			release := make(chan struct{})
			paused := NewStore(path)
			paused.afterLoad = func() {
				close(loaded)
				<-release
			}

			firstDone := make(chan error, 1)
			go func() { firstDone <- paused.Update(test.first) }()
			<-loaded

			// The whole read-decide-write runs under the lock: another writer
			// cannot take it between the load and the write.
			if err := probePinLock(paused); !errors.Is(err, unix.EWOULDBLOCK) {
				close(release)
				<-firstDone
				t.Fatalf("Flock(LOCK_NB) while the update is paused = %v, want EWOULDBLOCK", err)
			}

			secondDone := make(chan error, 1)
			go func() { secondDone <- NewStore(path).Update(test.second) }()

			close(release)
			if err := <-firstDone; err != nil {
				t.Fatalf("first Update() error = %v", err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("second Update() error = %v", err)
			}
			if got := readPinFile(t, path); got != test.want {
				t.Fatalf("file contents = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStoreUpdateGivesUpWhenThePinLockIsHeld(t *testing.T) {
	t.Parallel()

	path := seedPinFile(t, lockTestSeed)
	store := Store{file: state.NewLinesFile(path).WithLockWaitLimit(20 * time.Millisecond)}

	held, err := os.OpenFile(store.file.LockPath(), os.O_CREATE|os.O_RDWR, state.PrivateFileMode)
	if err != nil {
		t.Fatalf("OpenFile(lock) error = %v", err)
	}
	defer held.Close()
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("Flock() error = %v", err)
	}

	called := false
	err = store.Update(func(set Set) (Set, bool, error) {
		called = true
		return set.With(Pin{Kind: KindProject, Value: "proj-alpha"}), true, nil
	})
	if !errors.Is(err, state.ErrLockTimeout) {
		t.Fatalf("Update() error = %v, want state.ErrLockTimeout", err)
	}
	if called {
		t.Fatal("Update() ran the callback without holding the lock")
	}
	if got := readPinFile(t, path); got != lockTestSeed {
		t.Fatalf("file contents = %q, want unchanged %q", got, lockTestSeed)
	}
}

func TestStoreUpdateWritesNothingWhenTheCallbackDeclines(t *testing.T) {
	t.Parallel()

	refusal := errors.New("refused")
	for _, test := range []struct {
		name    string
		update  func(Set) (Set, bool, error)
		wantErr error
	}{
		{
			name: "write false",
			update: func(set Set) (Set, bool, error) {
				return set.With(Pin{Kind: KindProject, Value: "proj-alpha"}), false, nil
			},
		},
		{
			name: "callback error",
			update: func(set Set) (Set, bool, error) {
				return set.With(Pin{Kind: KindProject, Value: "proj-alpha"}), true, refusal
			},
			wantErr: refusal,
		},
		{
			name: "invalid pin",
			update: func(set Set) (Set, bool, error) {
				return set.With(Pin{Kind: KindProject, Value: "not-a-project-uid"}), true, nil
			},
			wantErr: ErrInvalidPin,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// A legacy file proves "nothing written" also means "not migrated".
			const legacy = "/srv/legacy\n"
			path := seedPinFile(t, legacy)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat() error = %v", err)
			}

			err = NewStore(path).Update(test.update)
			if test.wantErr == nil && err != nil {
				t.Fatalf("Update() error = %v, want nil", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
			}
			if got := readPinFile(t, path); got != legacy {
				t.Fatalf("file contents = %q, want unchanged %q", got, legacy)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat() error = %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("Update() replaced the pin file although it wrote nothing")
			}
		})
	}
}

func TestStoreUpdateHandsTheCallbackTheStoredSetAsLoadReadsIt(t *testing.T) {
	t.Parallel()

	path := seedPinFile(t, "/srv/legacy\n")
	store := NewStore(path)
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	var seen Set
	if err := store.Update(func(set Set) (Set, bool, error) {
		seen = set
		return set, false, nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if seen.Format != FormatLegacy || !seen.Equal(loaded) {
		t.Fatalf("Update() saw %#v, want the Load() reading %#v", seen, loaded)
	}
}

func probePinLock(store Store) error {
	probe, err := os.OpenFile(store.file.LockPath(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer probe.Close()
	return unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
