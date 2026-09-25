package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/state"
)

// pausingPinStore is a real pins.Store whose first Update pauses inside the
// pin-file lock, after the stored set was loaded and before the pin authority
// decides anything. It also counts the updates that asked for a write.
type pausingPinStore struct {
	pins.Store
	loaded  chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	writes int
}

func (s *pausingPinStore) Update(update func(pins.Set) (pins.Set, bool, error)) error {
	return s.Store.Update(func(stored pins.Set) (pins.Set, bool, error) {
		if s.loaded != nil {
			s.once.Do(func() {
				close(s.loaded)
				<-s.release
			})
		}
		next, write, err := update(stored)
		if err == nil && write {
			s.mu.Lock()
			s.writes++
			s.mu.Unlock()
		}
		return next, write, err
	})
}

func (s *pausingPinStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func pinFileAuthority(store pinSetStore, refs ...pins.ProjectRef) pinAuthority {
	return pinAuthority{
		store:    store,
		projects: func() ([]pins.ProjectRef, error) { return refs, nil },
	}
}

func seedAppPinFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readAppPinFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(raw)
}

func probeAppPinLock(path string) error {
	probe, err := os.OpenFile(state.NewLinesFile(path).LockPath(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer probe.Close()
	return unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func lockTestProjectPin(t *testing.T, uid string) pins.Pin {
	t.Helper()
	pin, err := pins.ProjectPin(uid)
	if err != nil {
		t.Fatalf("ProjectPin(%q) error = %v", uid, err)
	}
	return pin
}

func lockTestCandidatePin(t *testing.T, path string) pins.Pin {
	t.Helper()
	pin, err := pins.CandidatePin(path)
	if err != nil {
		t.Fatalf("CandidatePin(%q) error = %v", path, err)
	}
	return pin
}

// TestPinAuthorityWritesKeepAnOverlappingPinWriteUnderTheLock pauses one pin
// write inside the pin-file lock and overlaps it with another. Each write reads,
// decides and writes under one lock, so the overlapping write lands on top of
// the paused one instead of being overwritten by its stale read.
func TestPinAuthorityWritesKeepAnOverlappingPinWriteUnderTheLock(t *testing.T) {
	t.Parallel()

	const typedSeed = "projmux-pins v2\nproject proj-keep\n"
	refs := []pins.ProjectRef{{UID: "proj-app", Root: "/srv/app"}}
	alpha := lockTestProjectPin(t, "proj-alpha")
	beta := lockTestCandidatePin(t, "/srv/beta")

	for _, test := range []struct {
		name  string
		seed  string
		first func(pinAuthority) error
		want  string
	}{
		{
			name:  "add",
			seed:  typedSeed,
			first: func(a pinAuthority) error { return a.add(alpha) },
			want:  "projmux-pins v2\nproject proj-keep\nproject proj-alpha\ncandidate /srv/beta\n",
		},
		{
			name:  "remove",
			seed:  "projmux-pins v2\nproject proj-keep\nproject proj-alpha\n",
			first: func(a pinAuthority) error { return a.remove(alpha) },
			want:  "projmux-pins v2\nproject proj-keep\ncandidate /srv/beta\n",
		},
		{
			name: "toggle",
			seed: typedSeed,
			first: func(a pinAuthority) error {
				pinned, err := a.toggle(alpha)
				if err == nil && !pinned {
					return errors.New("toggle() reported the pin as removed")
				}
				return err
			},
			want: "projmux-pins v2\nproject proj-keep\nproject proj-alpha\ncandidate /srv/beta\n",
		},
		{
			name:  "clear",
			seed:  typedSeed,
			first: func(a pinAuthority) error { return a.clear() },
			want:  "projmux-pins v2\ncandidate /srv/beta\n",
		},
		{
			name: "migrate",
			seed: "/srv/app\n/srv/loose\n",
			first: func(a pinAuthority) error {
				resolution, err := a.migrate()
				if err == nil && len(resolution.Moved) != 1 {
					return errors.New("migrate() did not move /srv/app onto its Project")
				}
				return err
			},
			want: "projmux-pins v2\nproject proj-app\ncandidate /srv/loose\ncandidate /srv/beta\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := seedAppPinFile(t, test.seed)
			paused := &pausingPinStore{
				Store:   pins.NewStore(path),
				loaded:  make(chan struct{}),
				release: make(chan struct{}),
			}

			firstDone := make(chan error, 1)
			go func() { firstDone <- test.first(pinFileAuthority(paused, refs...)) }()
			<-paused.loaded

			if err := probeAppPinLock(path); !errors.Is(err, unix.EWOULDBLOCK) {
				close(paused.release)
				<-firstDone
				t.Fatalf("Flock(LOCK_NB) while the %s is paused = %v, want EWOULDBLOCK", test.name, err)
			}

			secondDone := make(chan error, 1)
			go func() { secondDone <- pinFileAuthority(pins.NewStore(path), refs...).add(beta) }()

			close(paused.release)
			if err := <-firstDone; err != nil {
				t.Fatalf("%s error = %v", test.name, err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("overlapping add error = %v", err)
			}
			if got := readAppPinFile(t, path); got != test.want {
				t.Fatalf("pin file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPinAuthorityWriteGivesUpWhenThePinLockIsHeld(t *testing.T) {
	t.Parallel()

	const seed = "projmux-pins v2\nproject proj-keep\n"
	path := seedAppPinFile(t, seed)
	held, err := os.OpenFile(state.NewLinesFile(path).LockPath(), os.O_CREATE|os.O_RDWR, state.PrivateFileMode)
	if err != nil {
		t.Fatalf("OpenFile(lock) error = %v", err)
	}
	defer held.Close()
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("Flock() error = %v", err)
	}

	err = pinFileAuthority(pins.NewStore(path)).add(lockTestProjectPin(t, "proj-alpha"))
	if !errors.Is(err, state.ErrLockTimeout) {
		t.Fatalf("add() error = %v, want state.ErrLockTimeout", err)
	}
	if got := readAppPinFile(t, path); got != seed {
		t.Fatalf("pin file = %q, want unchanged %q", got, seed)
	}
}

func TestPinAuthorityRefusesAnAmbiguousLegacyFileUnderTheLockWithoutWriting(t *testing.T) {
	t.Parallel()

	const legacy = "/srv/app\n"
	refs := []pins.ProjectRef{{UID: "proj-one", Root: "/srv/app"}, {UID: "proj-two", Root: "/srv/app/"}}

	for name, op := range map[string]func(pinAuthority) error{
		"migrate": func(a pinAuthority) error { _, err := a.migrate(); return err },
		"add":     func(a pinAuthority) error { return a.add(lockTestProjectPin(t, "proj-alpha")) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := seedAppPinFile(t, legacy)
			store := &pausingPinStore{Store: pins.NewStore(path)}
			err := op(pinFileAuthority(store, refs...))
			var ambiguous *pins.AmbiguousMigrationError
			if !errors.As(err, &ambiguous) {
				t.Fatalf("%s error = %v, want *pins.AmbiguousMigrationError", name, err)
			}
			if ambiguous.Path != path || len(ambiguous.Ambiguous) != 1 || ambiguous.Ambiguous[0].Path != "/srv/app" {
				t.Fatalf("AmbiguousMigrationError = %+v", ambiguous)
			}
			if got := readAppPinFile(t, path); got != legacy {
				t.Fatalf("pin file = %q, want byte-identical %q", got, legacy)
			}
			if store.writeCount() != 0 {
				t.Fatalf("writes = %d, want 0", store.writeCount())
			}
		})
	}
}

func TestPinAuthorityRepeatedPinUnderTheLockWritesOnce(t *testing.T) {
	t.Parallel()

	path := seedAppPinFile(t, "projmux-pins v2\nproject proj-keep\n")
	store := &pausingPinStore{Store: pins.NewStore(path)}
	authority := pinFileAuthority(store)
	alpha := lockTestProjectPin(t, "proj-alpha")

	if err := authority.add(alpha); err != nil {
		t.Fatalf("first add() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if err := authority.add(alpha); err != nil {
		t.Fatalf("second add() error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := store.writeCount(); got != 1 {
		t.Fatalf("writes = %d, want 1", got)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the repeated add replaced the pin file")
	}
	if got, want := readAppPinFile(t, path), "projmux-pins v2\nproject proj-keep\nproject proj-alpha\n"; got != want {
		t.Fatalf("pin file = %q, want %q", got, want)
	}
}
