// Package agentmessage persists the local provider-neutral coordination inbox.
// The store is private to the same-user host and contains only the public v2
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
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	storeVersion       = 2
	legacyStoreVersion = 1
	storeDirName       = "agent-messages"
	storeFileName      = "messages.json"
	maxRecords         = 256
	maxStoreBytes      = 2 << 20
	terminalRetention  = 24 * time.Hour
	// defaultLockWait bounds how long the broker view queues behind another
	// holder. A holder keeps the lock for a couple of fsyncs, and message
	// deadlines are minutes, so this absorbs contention without blocking.
	defaultLockWait   = 2 * time.Second
	lockRetryInterval = 2 * time.Millisecond
)

var (
	ErrCapacity       = errors.New("agent message store is at capacity")
	ErrMalformedStore = errors.New("malformed Agent message store")
	ErrNotFound       = errors.New("agent message not found")
	ErrBusy           = errors.New("agent message store is busy")
)

type Record struct {
	Envelope        coremessage.Envelope `json:"envelope"`
	Delivery        coremessage.Delivery `json:"delivery"`
	Adapter         string               `json:"adapter"`
	HandoffObserved bool                 `json:"handoffObserved,omitempty"`
}

// ReplyConflictError preserves the prior immutable attempt when a second
// attempt cannot be authorized. The caller can report its public delivery
// cause without exposing payload or provider-private errors.
type ReplyConflictError struct {
	Previous Record
	Reason   string
}

func (e *ReplyConflictError) Error() string { return e.Reason }
func (e *ReplyConflictError) Unwrap() error { return coremessage.ErrRetryMismatch }

// KnownZeroReply is deliberately a closed list of Claude pre-write outcomes.
// A false unknown flag alone is not evidence that delivery did not happen.
// Codex delivery outcomes are outside this recovery contract.
func KnownZeroReply(record Record) bool {
	if record.Envelope.ReplyTo == "" || record.Adapter != "claude-coordination" || record.Delivery.OutcomeUnknown {
		return false
	}
	if record.Delivery.State == coremessage.StateRefused {
		return record.Delivery.Reason == "provider-frame-unsupported" || record.Delivery.Reason == "claude-private-frame-unsupported"
	}
	if record.Delivery.State != coremessage.StateFailed {
		return false
	}
	switch record.Delivery.Reason {
	case "provider-frame-invalid-auth", "provider-frame-invalid-content", "provider-frame-build-failed",
		"provider-prewrite-refused", "provider-write-zero", "broker-handoff-persist-failed":
		return true
	}
	value, ok := strings.CutPrefix(record.Delivery.Reason, "provider-frame-too-large: frameBytes=")
	sizeText, _, separated := strings.Cut(value, " ")
	size, err := strconv.Atoi(sizeText)
	return ok && separated && err == nil && size > 8192 &&
		record.Delivery.Reason == fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", size)
}

type diskState struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

type storeHooks struct {
	beforeHistoryAppend func() error
	beforeRename        func() error
	afterLock           func()
}

type Store struct {
	path            string
	now             func() time.Time
	hooks           storeHooks
	nonblocking     bool
	lockWait        time.Duration
	historyMaxBytes int
}

func NewStore(stateDir string) *Store {
	return NewStoreAt(filepath.Join(stateDir, storeDirName, storeFileName))
}

// NewNonblockingStore is the helper's reply-commit view of the same durable
// inbox. Lock contention refuses immediately, before any durable write, so no
// delayed writer may land after its caller gave up with obsolete correlation.
func NewNonblockingStore(stateDir string) *Store {
	store := NewStore(stateDir)
	store.nonblocking = true
	return store
}

// NewBoundedWaitStore is the broker view of the same durable inbox. Lock
// contention waits up to a short bound and only then refuses, so one holder
// does not turn a delivered message into a failed receipt.
func NewBoundedWaitStore(stateDir string) *Store {
	store := NewStore(stateDir)
	store.lockWait = defaultLockWait
	return store
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
		kept, reclaimed := pruneRecords(state.Records, now)
		state.Records = kept
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
		if err := s.writeLocked(state, newHistoryRecords(reclaimed, now)); err != nil {
			return err
		}
		created = true
		return nil
	})
	return out, created, err
}

// adapterForTarget keeps the stored adapter in step with the target provider.
// A reply used to be pinned to the Codex inbox because that was the only
// direction explicit replies could take.
func adapterForTarget(target coremessage.Route) string {
	if target.Provider == "codex" {
		return "codex-inbox"
	}
	return "claude-coordination"
}

// Reply returns the latest durable attempt, including a failed attempt. It is
// diagnostic evidence only; PutReply decides retry admission under one lock.
func (s *Store) Reply(originalRef string) (Record, bool, error) {
	if originalRef == "" {
		return Record{}, false, coremessage.ErrInvalidEnvelope
	}
	var out Record
	var found bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.Envelope.ReplyTo == originalRef {
				out, found = record, true
			}
		}
		return nil
	})
	return out, found, err
}

// PutReply atomically authorizes one attempt on an exact reversed route. A
// same-ref replay only returns its immutable receipt. A fresh ref can follow
// known-zero failures while the original correlation is still live; every
// earlier attempt remains stored and any other outcome closes this lane.
func (s *Store) PutReply(originalRef, messageRef, payload string, source, target coremessage.Route, acceptedAt, deadline time.Time) (Record, bool, error) {
	var out Record
	var created bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		originalIndex := -1
		for i := range state.Records {
			if state.Records[i].Envelope.MessageRef == originalRef {
				originalIndex = i
			}
		}
		if originalIndex < 0 {
			return ErrNotFound
		}
		original := state.Records[originalIndex]
		candidate := replyEnvelope(original.Envelope, messageRef, payload, source, target, acceptedAt, deadline)
		var previous Record
		for i := range state.Records {
			record := state.Records[i]
			if record.Envelope.MessageRef != messageRef {
				continue
			}
			if record.Adapter != adapterForTarget(candidate.Target) || !record.Envelope.SameRetry(candidate) {
				return &ReplyConflictError{Previous: record, Reason: "reply-ref-envelope-mismatch"}
			}
			out = record
			return nil
		}
		attempted := false
		for _, record := range state.Records {
			if record.Envelope.ReplyTo != originalRef {
				continue
			}
			previous, attempted = record, true
			if !KnownZeroReply(record) {
				return &ReplyConflictError{Previous: record, Reason: "reply-already-committed"}
			}
		}
		if original.Delivery.State != coremessage.StateDelivered ||
			!source.Same(original.Envelope.Target) || !target.Same(original.Envelope.Source) {
			return &ReplyConflictError{Previous: previous, Reason: "invalid-explicit-reply-correlation"}
		}
		if !original.Envelope.Deadline.After(s.clock()) || !deadline.After(s.clock()) {
			return &ReplyConflictError{Previous: previous, Reason: "explicit-reply-deadline-expired"}
		}
		envelope := candidate
		if envelope.Deadline.After(original.Envelope.Deadline) {
			return &ReplyConflictError{Previous: previous, Reason: "explicit-reply-deadline-extended"}
		}
		if err := coremessage.ValidateReply(original.Envelope, envelope); err != nil {
			return err
		}
		// A first attempt is a new acceptance rather than recovery, so it makes
		// room under the same rule an accepted message does, pinning the original
		// and every attempt already stored against it. Recovery never removes or
		// resets an old attempt to make room.
		now := s.clock()
		var reclaimed []reclaimedRecord
		if !attempted {
			state.Records, reclaimed = pruneRecords(state.Records, now, originalRef)
		}
		if len(state.Records) >= maxRecords {
			return ErrCapacity
		}
		delivery, changed := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: envelope.AcceptedAt})
		if !changed {
			return coremessage.ErrInvalidEnvelope
		}
		out = Record{Envelope: envelope, Delivery: delivery, Adapter: adapterForTarget(envelope.Target)}
		state.Records = append(state.Records, out)
		if err := s.writeLocked(state, newHistoryRecords(reclaimed, now)); err != nil {
			return err
		}
		created = true
		return nil
	})
	return out, created, err
}

func replyEnvelope(original coremessage.Envelope, messageRef, payload string, source, target coremessage.Route, acceptedAt, deadline time.Time) coremessage.Envelope {
	return coremessage.Envelope{Version: coremessage.Version, MessageRef: messageRef,
		ConversationRef: original.ConversationRef, ReplyTo: original.MessageRef, Source: source, Target: target,
		Authority: coremessage.PeerAuthority(), Payload: payload, AcceptedAt: acceptedAt.UTC(), Deadline: deadline.UTC()}
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
				if err := s.writeLocked(state, nil); err != nil {
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
				if err := s.writeLocked(state, nil); err != nil {
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

// MarkHandoffMatching is MarkHandoff for a caller holding the envelope. It
// verifies the stored attempt is the same retry on adapter and records the
// handoff under one lock, so no other writer lands between check and write.
// A missing or different attempt is coremessage.ErrInvalidEnvelope.
func (s *Store) MarkHandoffMatching(envelope coremessage.Envelope, adapter string) (Record, bool, error) {
	var out Record
	var changed bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		record, err := matchingRecord(&state, envelope, adapter)
		if err != nil {
			return err
		}
		if !record.Delivery.State.Terminal() && !record.HandoffObserved {
			record.HandoffObserved = true
			if err := s.writeLocked(state, nil); err != nil {
				return err
			}
			changed = true
		}
		out = *record
		return nil
	})
	return out, changed, err
}

// ApplyMatching is Apply for a caller holding the envelope, with the same
// single-lock check as MarkHandoffMatching.
func (s *Store) ApplyMatching(envelope coremessage.Envelope, adapter string, event coremessage.Event) (Record, bool, error) {
	var out Record
	var changed bool
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		record, err := matchingRecord(&state, envelope, adapter)
		if err != nil {
			return err
		}
		if next, didChange := coremessage.Reduce(record.Delivery, record.Envelope, event); didChange {
			record.Delivery = next
			if err := s.writeLocked(state, nil); err != nil {
				return err
			}
			changed = true
		}
		out = *record
		return nil
	})
	return out, changed, err
}

func matchingRecord(state *diskState, envelope coremessage.Envelope, adapter string) (*Record, error) {
	for i := range state.Records {
		record := &state.Records[i]
		if record.Envelope.MessageRef != envelope.MessageRef {
			continue
		}
		if record.Adapter != adapter || !record.Envelope.SameRetry(envelope) {
			return nil, coremessage.ErrInvalidEnvelope
		}
		return record, nil
	}
	return nil, coremessage.ErrInvalidEnvelope
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
			return s.writeLocked(state, nil)
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
	operation := unix.LOCK_EX
	if s.nonblocking || s.lockWait > 0 {
		operation |= unix.LOCK_NB
	}
	if err := s.flock(int(lock.Fd()), operation); err != nil {
		if lockBusy(err) {
			return ErrBusy
		}
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck -- releasing an owned advisory lock.
	if s.hooks.afterLock != nil {
		s.hooks.afterLock()
	}
	return fn()
}

// flock retries a non-blocking attempt until the view's bound. The deadline
// uses the monotonic wall clock, never the injectable record clock.
func (s *Store) flock(fd, operation int) error {
	err := unix.Flock(fd, operation)
	if s.nonblocking || s.lockWait <= 0 {
		return err
	}
	deadline := time.Now().Add(s.lockWait)
	for lockBusy(err) && time.Now().Before(deadline) {
		time.Sleep(lockRetryInterval)
		err = unix.Flock(fd, operation)
	}
	return err
}

func lockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
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
	if decoder.Decode(&state) != nil || (state.Version != storeVersion && state.Version != legacyStoreVersion) || len(state.Records) > maxRecords {
		return diskState{}, ErrMalformedStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return diskState{}, ErrMalformedStore
	}
	if state.Version == legacyStoreVersion {
		for i := range state.Records {
			if state.Records[i].Envelope.Version != 1 {
				return diskState{}, ErrMalformedStore
			}
			state.Records[i].Envelope.Version = coremessage.Version
			state.Records[i].Envelope.Source.Incarnation = "legacy-unqualified-" + state.Records[i].Envelope.Source.Provider
			state.Records[i].Envelope.Target.Incarnation = "legacy-unqualified-" + state.Records[i].Envelope.Target.Provider
		}
		state.Version = storeVersion
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

func (s *Store) writeLocked(state diskState, history []historyRecord) error {
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
	// The history log is durable before the store commits, so a crash in this
	// window can duplicate a reclaimed record but cannot lose one.
	if err := s.appendHistoryLocked(history); err != nil {
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
	return syncDir(dir)
}

// syncDir makes a new or renamed directory entry durable. A filesystem that
// refuses the fsync is reported as a success because the entry is not this
// process's to repair.
func syncDir(dir string) error {
	directory, err := os.Open(dir) // #nosec G304 -- withLock validated this exact parent as a private directory.
	if err != nil {
		return nil
	}
	if syncErr := directory.Sync(); syncErr != nil && !errors.Is(syncErr, fs.ErrInvalid) && !errors.Is(syncErr, fs.ErrPermission) {
		_ = directory.Close()
		return syncErr
	}
	_ = directory.Close()
	return nil
}

// pruneRecords returns the records the store keeps and the ones it reclaimed.
// Reclaiming is not deleting: every returned record is written to the history
// log before the caller's store write commits.
// pinned names refs the caller must keep whatever their delivery state: the
// reply path pins the original it is answering, so making room for a first
// attempt never reclaims the correlation that attempt depends on.
func pruneRecords(records []Record, now time.Time, pinned ...string) ([]Record, []reclaimedRecord) {
	// A live original and its attempts form one durable idempotency boundary.
	// Evicting a delivered/unknown reply while retaining the original would
	// make a later fresh ref look like the first attempt after store reload.
	protected := make(map[string]bool)
	for _, ref := range pinned {
		if ref != "" {
			protected[ref] = true
		}
	}
	for _, record := range records {
		if record.Envelope.Target.Provider == "claude" && record.Envelope.Deadline.After(now) && record.Delivery.State == coremessage.StateDelivered {
			protected[record.Envelope.MessageRef] = true
		}
	}
	for _, record := range records {
		if protected[record.Envelope.ReplyTo] {
			protected[record.Envelope.MessageRef] = true
		}
	}
	cutoff := now.Add(-terminalRetention)
	var reclaimed []reclaimedRecord
	// out aliases records, so each reclaimed record is copied out before the
	// kept records are compacted over its slot.
	out := records[:0]
	for _, record := range records {
		if !protected[record.Envelope.MessageRef] && record.Delivery.State.Terminal() && !record.Delivery.TerminalAt.IsZero() && record.Delivery.TerminalAt.Before(cutoff) {
			reclaimed = append(reclaimed, reclaimedRecord{Record: record, Reason: reclaimRetention})
			continue
		}
		out = append(out, record)
	}
	if len(out) < maxRecords {
		return out, reclaimed
	}
	// At capacity, reclaim the oldest terminal records first. Non-terminal
	// records are never silently evicted.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Envelope.AcceptedAt.Before(out[j].Envelope.AcceptedAt) })
	for len(out) >= maxRecords {
		index := -1
		for i := range out {
			if out[i].Delivery.State.Terminal() && !protected[out[i].Envelope.MessageRef] {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		reclaimed = append(reclaimed, reclaimedRecord{Record: out[index], Reason: reclaimCapacity})
		out = append(out[:index], out[index+1:]...)
	}
	return out, reclaimed
}
