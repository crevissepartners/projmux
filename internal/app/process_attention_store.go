package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// Only routing and lifecycle metadata belong here. Provider frames, question
// text, tool input, answers and diagnostics remain outside this store.
type processAttentionRecord struct {
	Binding        processhost.Binding
	Provider       string
	Sequence       uint64
	Badge          string
	AckSequence    uint64
	Terminal       bool
	PendingEnded   bool
	Pending        map[string]processAttentionPending
	NoticeSequence uint64
	NoticeKind     string
}

type processAttentionPending struct {
	Kind     string
	Sequence uint64
}

// The process stream has no picker action deadline. Give a burst of durable
// writers its own finite budget; a held lock still returns ErrLockTimeout.
const processAttentionLockWaitLimit = 2 * time.Second

type processAttentionStore struct {
	path string
	// Internal test seams for observing contention and exercising expiry.
	lockWaitLimit      time.Duration
	afterContendedLock func()
}

func newProcessAttentionStore(stateDir string) *processAttentionStore {
	return &processAttentionStore{path: filepath.Join(stateDir, "process-attention.json")}
}

func (s *processAttentionStore) read() (map[string]processAttentionRecord, error) {
	data, err := os.ReadFile(s.path) // #nosec G304 -- explicit per-user state path, never provider input.
	if errors.Is(err, os.ErrNotExist) {
		return map[string]processAttentionRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]processAttentionRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("invalid process attention store")
	}
	return records, nil
}

// Writers lock a persistent inode; process death releases the kernel lock.
// Never unlink the lock: another writer may already be waiting on that inode.
func (s *processAttentionStore) update(change func(map[string]processAttentionRecord) error) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	limit := s.lockWaitLimit
	if limit <= 0 {
		limit = processAttentionLockWaitLimit
	}
	lock, err := localstate.AcquireFileLock(s.path+".lock", limit, s.afterContendedLock)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	records, err := s.read()
	if err != nil {
		return err
	}
	if err = change(records); err != nil {
		return err
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".process-attention-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()
	if _, err = temp.Write(data); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), s.path); err != nil {
		return err
	}
	directory, err := os.Open(dir) // #nosec G304 -- EnsurePrivateDir validated this exact store parent.
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

// activate is an explicit generation CAS, never an event-side upsert. An old
// writer cannot reactivate its generation by replaying an observation.
func (s *processAttentionStore) activate(binding processhost.Binding, provider, previous string) error {
	if binding.Pane == "" || binding.Generation == "" || binding.Host == "" || (provider != "claude" && provider != "codex") {
		return processhost.ErrStale
	}
	return s.update(func(records map[string]processAttentionRecord) error {
		old, exists := records[binding.Pane]
		if exists && old.Binding == binding {
			return nil
		}
		if (exists && old.Binding.Generation != previous) || (!exists && previous != "") {
			return processhost.ErrStale
		}
		records[binding.Pane] = processAttentionRecord{Binding: binding, Provider: provider, Pending: map[string]processAttentionPending{}}
		return nil
	})
}

// clear mirrors attention clear: completion decoration is consumed, whereas
// active questions/approvals and the separate notification queue survive.
// The observation sequence also fences a newer question in the same generation.
func (s *processAttentionStore) clear(binding processhost.Binding, sequence uint64) error {
	return s.update(func(records map[string]processAttentionRecord) error {
		r, ok := records[binding.Pane]
		if !ok || r.Binding != binding || r.Sequence != sequence {
			return processhost.ErrStale
		}
		if r.Badge == aibadge.ResponseComplete {
			r.Badge = ""
			r.AckSequence = sequence
			records[binding.Pane] = r
		}
		return nil
	})
}
