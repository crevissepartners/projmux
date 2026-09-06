// Package agentmessage persists the local provider-neutral coordination inbox.
// The store is private to the same-user host and contains only the public v1
// envelope: provider locators, credentials, thread IDs, and session secrets are
// never part of its model.
package agentmessage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	storeVersion      = 1
	storeDirName      = "agent-messages"
	storeFileName     = "messages.json"
	maxRecords        = 256
	maxStoreBytes     = 2 << 20
	terminalRetention = 24 * time.Hour
)

var (
	ErrCapacity       = errors.New("agent message store is at capacity")
	ErrMalformedStore = errors.New("malformed Agent message store")
	ErrNotFound       = errors.New("agent message not found")
)

type Record struct {
	Envelope        coremessage.Envelope `json:"envelope"`
	Delivery        coremessage.Delivery `json:"delivery"`
	Adapter         string               `json:"adapter"`
	HandoffObserved bool                 `json:"handoffObserved,omitempty"`
}

type diskState struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

type storeHooks struct {
	beforeRename func() error
}

type Store struct {
	path  string
	now   func() time.Time
	hooks storeHooks
}

func NewStore(stateDir string) *Store {
	return NewStoreAt(filepath.Join(stateDir, storeDirName, storeFileName))
}

func NewStoreAt(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Get(messageRef string) (Record, bool, error) {
	var record Record
	var found bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, candidate := range state.Records {
			if candidate.Envelope.MessageRef == messageRef {
				record, found = candidate, true
				break
			}
		}
		return nil
	})
	return record, found, err
}

// PutAccepted atomically installs the broker acceptance record. A repeated
// message ref returns the existing record only when the caller-controlled
// immutable envelope and original TTL match.
func (s *Store) PutAccepted(envelope coremessage.Envelope, adapter string) (Record, bool, error) {
	if err := envelope.Validate(); err != nil {
		return Record{}, false, err
	}
	if adapter != "codex-inbox" && adapter != "claude-coordination" {
		return Record{}, false, fmt.Errorf("invalid Agent message adapter %q", adapter)
	}
	if (adapter == "codex-inbox") != (envelope.Target.Provider == "codex") ||
		(adapter == "claude-coordination") != (envelope.Target.Provider == "claude") {
		return Record{}, false, fmt.Errorf("agent message adapter %q does not match target provider %q", adapter, envelope.Target.Provider)
	}
	var out Record
	var created bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, existing := range state.Records {
			if existing.Envelope.MessageRef != envelope.MessageRef {
				continue
			}
			if existing.Adapter != adapter || !existing.Envelope.SameRetry(envelope) {
				return coremessage.ErrRetryMismatch
			}
			out = existing
			return nil
		}
		now := s.clock()
		state.Records = pruneRecords(state.Records, now)
		if len(state.Records) >= maxRecords {
			return ErrCapacity
		}
		delivery, changed := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{
			Kind: coremessage.EventAccept, MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef,
			Target: envelope.Target, ObservedAt: envelope.AcceptedAt,
		})
		if !changed {
			return coremessage.ErrInvalidEnvelope
		}
		out = Record{Envelope: envelope, Delivery: delivery, Adapter: adapter}
		state.Records = append(state.Records, out)
		if err := s.writeLocked(state); err != nil {
			return err
		}
		created = true
		return nil
	})
	return out, created, err
}

func (s *Store) Apply(messageRef string, event coremessage.Event) (Record, bool, error) {
	var out Record
	var changed bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for i := range state.Records {
			if state.Records[i].Envelope.MessageRef != messageRef {
				continue
			}
			next, didChange := coremessage.Reduce(state.Records[i].Delivery, state.Records[i].Envelope, event)
			if didChange {
				state.Records[i].Delivery = next
				if err := s.writeLocked(state); err != nil {
					return err
				}
				changed = true
			}
			out = state.Records[i]
			return nil
		}
		return ErrNotFound
	})
	return out, changed, err
}

// MarkHandoff durably remembers the provider-private point after which a lost
// receipt is ambiguous. It does not invent a public lifecycle state: the
// public reducer remains accepted|held -> terminal.
func (s *Store) MarkHandoff(messageRef string) (Record, bool, error) {
	var out Record
	var changed bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for i := range state.Records {
			record := &state.Records[i]
			if record.Envelope.MessageRef != messageRef {
				continue
			}
			if record.Adapter != "claude-coordination" {
				return fmt.Errorf("agent message %q has no provider handoff phase", messageRef)
			}
			if !record.Delivery.State.Terminal() && !record.HandoffObserved {
				record.HandoffObserved = true
				if err := s.writeLocked(state); err != nil {
					return err
				}
				changed = true
			}
			out = *record
			return nil
		}
		return ErrNotFound
	})
	return out, changed, err
}

// Status expires an unclaimed pre-handoff record at its broker deadline. A
// terminal record is returned unchanged, and payload remains available only to
// the private caller which decides its public projection.
func (s *Store) Status(messageRef string, now time.Time) (Record, bool, error) {
	record, found, err := s.Get(messageRef)
	if err != nil || !found || record.Delivery.State.Terminal() || record.Envelope.Deadline.After(now) {
		return record, found, err
	}
	event := deadlineEvent(record, now)
	record, _, err = s.Apply(messageRef, event)
	return record, true, err
}

// Claim returns the oldest compatible full envelope and commits delivered in
// the same file lock. It is the Codex safe boundary: target self read, not model
// processing, reply, user input, or app-server history mutation.
func (s *Store) Claim(target coremessage.Route, now time.Time) (Record, bool, error) {
	if !target.Valid() {
		return Record{}, false, coremessage.ErrInvalidEnvelope
	}
	var out Record
	var claimed bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		changed := false
		for i := range state.Records {
			record := &state.Records[i]
			if record.Adapter == "codex-inbox" && record.Envelope.Target.Same(target) &&
				!record.Delivery.State.Terminal() && !record.Envelope.Deadline.After(now) {
				next, didChange := coremessage.Reduce(record.Delivery, record.Envelope, deadlineEvent(*record, now))
				if didChange {
					record.Delivery, changed = next, true
				}
			}
		}
		order := make([]int, len(state.Records))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool {
			return state.Records[order[i]].Envelope.AcceptedAt.Before(state.Records[order[j]].Envelope.AcceptedAt)
		})
		for _, index := range order {
			record := &state.Records[index]
			if record.Adapter != "codex-inbox" || (record.Delivery.State != coremessage.StateAccepted && record.Delivery.State != coremessage.StateHeld) ||
				!record.Envelope.Target.Same(target) {
				continue
			}
			next, didChange := coremessage.Reduce(record.Delivery, record.Envelope, coremessage.Event{
				Kind: coremessage.EventDeliver, MessageRef: record.Envelope.MessageRef,
				ConversationRef: record.Envelope.ConversationRef, Target: record.Envelope.Target,
				Reason: "target-self-claim", ObservedAt: now,
			})
			if didChange {
				record.Delivery = next
				out, changed, claimed = *record, true, true
			}
			break
		}
		if changed {
			return s.writeLocked(state)
		}
		return nil
	})
	return out, claimed, err
}

func deadlineEvent(record Record, now time.Time) coremessage.Event {
	kind, reason, unknown := coremessage.EventExpire, "deadline-expired", false
	if record.HandoffObserved {
		kind, reason, unknown = coremessage.EventFail, "provider-handoff-outcome-unknown", true
	}
	return coremessage.Event{Kind: kind, MessageRef: record.Envelope.MessageRef,
		ConversationRef: record.Envelope.ConversationRef, Target: record.Envelope.Target,
		Reason: reason, ObservedAt: now, OutcomeUnknown: unknown}
}

func (s *Store) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *Store) withLock(fn func() error) error {
	if s == nil || s.path == "" {
		return errors.New("agent message store path is empty")
	}
	dir := filepath.Dir(s.path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".flock", os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- private store sibling.
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck -- releasing an owned advisory lock.
	return fn()
}

func (s *Store) loadLocked() (diskState, error) {
	data, err := os.ReadFile(s.path) // #nosec G304 -- explicit private store path.
	if errors.Is(err, os.ErrNotExist) {
		return diskState{Version: storeVersion}, nil
	}
	if err != nil {
		return diskState{}, err
	}
	if len(data) == 0 || len(data) > maxStoreBytes {
		return diskState{}, ErrMalformedStore
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state diskState
	if decoder.Decode(&state) != nil || state.Version != storeVersion || len(state.Records) > maxRecords {
		return diskState{}, ErrMalformedStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return diskState{}, ErrMalformedStore
	}
	seen := make(map[string]bool, len(state.Records))
	for _, record := range state.Records {
		if !validRecord(record) || seen[record.Envelope.MessageRef] {
			return diskState{}, ErrMalformedStore
		}
		seen[record.Envelope.MessageRef] = true
	}
	return state, nil
}

func validRecord(record Record) bool {
	if record.Envelope.Validate() != nil || record.Delivery.MessageRef != record.Envelope.MessageRef ||
		record.Delivery.ConversationRef != record.Envelope.ConversationRef ||
		!record.Delivery.AcceptedAt.Equal(record.Envelope.AcceptedAt) {
		return false
	}
	if (record.Adapter == "codex-inbox") != (record.Envelope.Target.Provider == "codex") ||
		(record.Adapter == "claude-coordination") != (record.Envelope.Target.Provider == "claude") ||
		(record.HandoffObserved && record.Adapter != "claude-coordination") {
		return false
	}
	switch record.Delivery.State {
	case coremessage.StateAccepted, coremessage.StateHeld:
		return record.Delivery.TerminalAt.IsZero() && !record.Delivery.OutcomeUnknown
	case coremessage.StateDelivered, coremessage.StateRefused, coremessage.StateExpired, coremessage.StateStale:
		return !record.Delivery.TerminalAt.IsZero() && !record.Delivery.OutcomeUnknown
	case coremessage.StateFailed:
		return !record.Delivery.TerminalAt.IsZero()
	default:
		return false
	}
}

func (s *Store) writeLocked(state diskState) error {
	state.Version = storeVersion
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxStoreBytes || len(state.Records) > maxRecords {
		return ErrCapacity
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".messages.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if s.hooks.beforeRename != nil {
		if err := s.hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	committed = true
	localstate.RepairPrivateFile(s.path)
	directory, err := os.Open(dir) // #nosec G304 -- withLock validated this exact parent as a private directory before writeLocked.
	if err == nil {
		if syncErr := directory.Sync(); syncErr != nil && !errors.Is(syncErr, fs.ErrInvalid) && !errors.Is(syncErr, fs.ErrPermission) {
			_ = directory.Close()
			return syncErr
		}
		_ = directory.Close()
	}
	return nil
}

func pruneRecords(records []Record, now time.Time) []Record {
	cutoff := now.Add(-terminalRetention)
	out := records[:0]
	for _, record := range records {
		if record.Delivery.State.Terminal() && !record.Delivery.TerminalAt.IsZero() && record.Delivery.TerminalAt.Before(cutoff) {
			continue
		}
		out = append(out, record)
	}
	if len(out) < maxRecords {
		return out
	}
	// At capacity, reclaim the oldest terminal records first. Non-terminal
	// records are never silently evicted.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Envelope.AcceptedAt.Before(out[j].Envelope.AcceptedAt) })
	for len(out) >= maxRecords {
		index := -1
		for i := range out {
			if out[i].Delivery.State.Terminal() {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		out = append(out[:index], out[index+1:]...)
	}
	return out
}
