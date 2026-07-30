package recentwindows

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const stateDirName = "recent-windows"

const lockFileSuffix = ".lock"

var (
	defaultLockMaxAttempts = 200
	defaultLockBaseDelay   = 2 * time.Millisecond
	defaultLockMaxDelay    = 50 * time.Millisecond
	defaultLockStaleAfter  = 30 * time.Second
)

type Clock func() time.Time

type Store struct {
	path     string
	lockPath string
	clock    Clock
	rng      *rand.Rand
}

func NewStore(path string) *Store {
	return &Store{
		path:     path,
		lockPath: path + lockFileSuffix,
		clock:    time.Now,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func NewDefaultStore(paths config.Paths, socket string) *Store {
	return NewStore(PathForSocket(paths.StateDir, socket))
}

func PathForSocket(stateDir, socket string) string {
	return filepath.Join(stateDir, stateDirName, SocketFileName(socket))
}

func SocketFileName(socket string) string {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		socket = "default"
	}
	return url.PathEscape(socket) + ".json"
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) SetClock(clock Clock) {
	if s != nil && clock != nil {
		s.clock = clock
	}
}

func (s *Store) Load() (State, error) {
	if s == nil {
		return State{Version: Version}, nil
	}
	var out State
	err := s.withLock(func() error {
		state, corrupt, err := s.read()
		if err != nil {
			return err
		}
		if corrupt {
			if err := s.backupCorrupt(); err != nil {
				return err
			}
			out = State{Version: Version}
			return nil
		}
		out = state
		return nil
	})
	if err != nil {
		return State{}, err
	}
	return out, nil
}

func (s *Store) Save(state State) error {
	if s == nil {
		return nil
	}
	return s.withLock(func() error {
		state = State{Version: Version, Entries: normalizeEntries(state.Entries, DefaultLimit)}
		return s.write(state)
	})
}

func (s *Store) Record(snapshot Snapshot, limit int) (State, error) {
	if s == nil {
		return State{}, nil
	}
	var out State
	err := s.withLock(func() error {
		state, corrupt, err := s.read()
		if err != nil {
			return err
		}
		if corrupt {
			if err := s.backupCorrupt(); err != nil {
				return err
			}
			state = State{Version: Version}
		}
		state, err = state.Record(snapshot, limit)
		if err != nil {
			return err
		}
		if err := s.write(state); err != nil {
			return err
		}
		out = state
		return nil
	})
	return out, err
}

func (s *Store) Candidates(current WindowKey, live []LiveWindow, limit int) ([]Candidate, error) {
	if s == nil {
		return nil, nil
	}
	var out []Candidate
	err := s.withLock(func() error {
		state, corrupt, err := s.read()
		if err != nil {
			return err
		}
		if corrupt {
			if err := s.backupCorrupt(); err != nil {
				return err
			}
			state = State{Version: Version}
		}
		candidates, pruned := state.Candidates(current, live, limit)
		if len(pruned.Entries) != len(state.Entries) {
			if err := s.write(pruned); err != nil {
				return err
			}
		}
		out = candidates
		return nil
	})
	return out, err
}

func (s *Store) read() (State, bool, error) {
	localstate.RepairPrivateFile(s.path)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Version: Version}, false, nil
		}
		return State{}, false, fmt.Errorf("recentwindows: read state %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return State{Version: Version}, false, nil
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, true, nil
	}
	if state.Version != Version {
		return State{}, true, nil
	}
	return State{Version: Version, Entries: normalizeEntries(state.Entries, DefaultLimit)}, false, nil
}

func (s *Store) write(state State) error {
	dir := filepath.Dir(s.path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("recentwindows: create state dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("recentwindows: encode state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("recentwindows: create temp state: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("recentwindows: write temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("recentwindows: close temp state: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("recentwindows: rename temp state: %w", err)
	}
	cleanup = false
	localstate.RepairPrivateFile(s.path)
	return nil
}

func (s *Store) backupCorrupt() error {
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("recentwindows: stat corrupt state %s: %w", s.path, err)
	}
	stamp := s.clock().UTC().Format("20060102T150405Z")
	backup := s.path + ".corrupt." + stamp
	for i := 1; ; i++ {
		if _, err := os.Stat(backup); err == nil {
			backup = fmt.Sprintf("%s.corrupt.%s.%d", s.path, stamp, i)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recentwindows: stat corrupt backup %s: %w", backup, err)
		}
		if err := os.Rename(s.path, backup); err == nil {
			return nil
		} else if errors.Is(err, os.ErrNotExist) {
			return nil
		} else {
			return fmt.Errorf("recentwindows: backup corrupt state %s: %w", s.path, err)
		}
	}
}

func (s *Store) withLock(fn func() error) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(s.lockPath)); err != nil {
		return fmt.Errorf("recentwindows: create lock dir: %w", err)
	}
	if err := s.acquireLock(); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(s.lockPath)
	}()
	return fn()
}

func (s *Store) acquireLock() error {
	delay := defaultLockBaseDelay
	for range defaultLockMaxAttempts {
		f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
			_ = f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("recentwindows: acquire lock: %w", err)
		}
		if s.tryBreakStaleLock() {
			continue
		}

		jitter := time.Duration(s.rng.Int63n(int64(defaultLockBaseDelay) + 1))
		time.Sleep(delay + jitter)
		if delay < defaultLockMaxDelay {
			delay *= 2
			if delay > defaultLockMaxDelay {
				delay = defaultLockMaxDelay
			}
		}
	}
	return fmt.Errorf("recentwindows: acquire lock: exhausted %d attempts on %s", defaultLockMaxAttempts, s.lockPath)
}

func (s *Store) tryBreakStaleLock() bool {
	info, err := os.Stat(s.lockPath)
	if err != nil {
		return false
	}
	if s.clock().Sub(info.ModTime()) < defaultLockStaleAfter {
		return false
	}
	if err := os.Remove(s.lockPath); err != nil {
		return false
	}
	return true
}
