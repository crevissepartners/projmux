package agentmessage

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// releaseLockRetryInterval is how often a release waiting behind another one
// retries the per-target lock.
const releaseLockRetryInterval = 10 * time.Millisecond

// HeldFor lists every held Claude coordination record addressed to agentUID,
// oldest acceptance first. It only reads: a store that does not exist yet has
// nothing held, and the read creates no directory or lock file for it.
func (s *Store) HeldFor(agentUID string) ([]Record, error) {
	if s == nil || s.path == "" {
		return nil, errors.New("agent message store path is empty")
	}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var held []Record
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.Adapter == "claude-coordination" && record.Delivery.State == coremessage.StateHeld &&
				record.Envelope.Target.AgentUID == agentUID {
				held = append(held, record)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(held, func(i, j int) bool {
		if !held[i].Envelope.AcceptedAt.Equal(held[j].Envelope.AcceptedAt) {
			return held[i].Envelope.AcceptedAt.Before(held[j].Envelope.AcceptedAt)
		}
		return held[i].Envelope.MessageRef < held[j].Envelope.MessageRef
	})
	return held, nil
}

// LockTargetRelease takes the lock that serializes releasing held messages to
// one target Agent. It is a sibling of the store lock, not the store lock
// itself, so a release holds it across provider submits without blocking any
// store reader or writer. A caller queues behind another release for at most
// wait and then gets ErrBusy. The returned function releases the lock.
func (s *Store) LockTargetRelease(agentUID string, wait time.Duration) (func(), error) {
	if s == nil || s.path == "" {
		return nil, errors.New("agent message store path is empty")
	}
	if !coremessage.ValidRef(agentUID) {
		return nil, coremessage.ErrInvalidEnvelope
	}
	dir := filepath.Dir(s.path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(agentUID))
	path := filepath.Join(dir, fmt.Sprintf("release-%x.flock", digest[:12]))
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- private store sibling named by a digest.
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !lockBusy(err) || !time.Now().Before(deadline) {
			_ = lock.Close()
			if lockBusy(err) {
				return nil, ErrBusy
			}
			return nil, err
		}
		time.Sleep(releaseLockRetryInterval)
	}
	return func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}, nil
}
