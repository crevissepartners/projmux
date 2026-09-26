// Package agentapproval persists the permission requests ("Do you want to
// proceed?") a projmux Claude Agent raises while its operator chose to capture
// them, and the one allow or deny answer each may take.
//
// A request record is written by the PermissionRequest hook that holds the
// request open and read back by that same hook, which hands an allow or deny
// decision to Claude Code only when this store settled the record that way
// under its lock. The answer is written by `projmux agent approval answer`.
// Claude Code's own prompt stays usable while the hook waits; a PostToolUse
// for the same tool call closes the record as answered in the terminal.
//
// Every transition is also appended to a private audit log in the same
// directory. The full tool input lives only in the record and is pruned with
// it; the audit line carries a bounded one-line summary.
package agentapproval

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	storeVersion  = 1
	storeDirName  = "agent-approvals"
	storeFileName = "requests.json"
	// maxRecords bounds the store. A waiting record is never evicted to make
	// room; the oldest terminal ones are.
	maxRecords    = 64
	maxStoreBytes = 8 << 20
	// MaxToolInputBytes bounds the tool input one record stores. A larger
	// request is not captured: the hook leaves it to Claude Code's prompt.
	MaxToolInputBytes = 64 << 10
	// terminalRetention keeps settled records listable for a day.
	terminalRetention = 24 * time.Hour
	// lockWait bounds how long a writer queues behind another holder.
	lockWait          = 2 * time.Second
	lockRetryInterval = 2 * time.Millisecond
)

// State is the closed lifecycle of one permission request record.
type State string

const (
	// StateWaiting: the hook holds the request open for an answer.
	StateWaiting State = "waiting"
	// StateAllowed: an allow answer was accepted. Terminal.
	StateAllowed State = "allowed"
	// StateDenied: a deny answer was accepted. Terminal.
	StateDenied State = "denied"
	// StateExpired: the answer window ended first. Terminal.
	StateExpired State = "expired"
	// StateClosed: the hook was canceled, failed, or the request was answered
	// in Claude Code's own prompt, before a projmux answer arrived. Terminal.
	StateClosed State = "closed"
)

// Terminal reports whether no further transition is possible.
func (s State) Terminal() bool { return s != StateWaiting }

// Close reasons the audit log records.
const (
	CloseReasonCanceled           = "canceled"
	CloseReasonFailed             = "hook-failed"
	CloseReasonAnsweredInTerminal = "answered-in-terminal"
)

// Via values an answer reports about itself. They are self-reported by the
// caller and never verified.
const (
	ViaCLI   = "cli"
	ViaPopup = "popup"
	ViaWeb   = "web"
)

// ValidVia reports whether via is one of the self-reported answer channels.
func ValidVia(via string) bool { return via == ViaCLI || via == ViaPopup || via == ViaWeb }

var (
	ErrNotFound       = errors.New("permission request not found")
	ErrNotPending     = errors.New("permission request is already answered")
	ErrExpired        = errors.New("permission request expired")
	ErrClosed         = errors.New("permission request closed")
	ErrCapacity       = errors.New("agent approval store is at capacity")
	ErrMalformedStore = errors.New("malformed agent approval store")
	ErrBusy           = errors.New("agent approval store is busy")
	ErrInvalidRecord  = errors.New("invalid agent approval record")
)

var idPattern = regexp.MustCompile(`^permission-[0-9a-f]{16}$`)

// NewID mints a request id. Claude Code's PermissionRequest payload carries no
// tool_use_id, so the hook names each request itself.
func NewID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "permission-" + hex.EncodeToString(raw[:]), nil
}

// ValidID reports whether id is a request id this package mints.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Record is one permission request and its outcome.
type Record struct {
	ID        string          `json:"id"`
	AgentUID  string          `json:"agentUID"`
	PaneUID   string          `json:"paneUID,omitempty"`
	SessionID string          `json:"sessionID,omitempty"`
	AgentType string          `json:"agentType,omitempty"`
	ToolName  string          `json:"toolName"`
	ToolInput json.RawMessage `json:"toolInput"`
	CreatedAt time.Time       `json:"createdAt"`
	Deadline  time.Time       `json:"deadline"`
	State     State           `json:"state"`
	// Via is the self-reported channel of the answer that settled the record.
	Via       string    `json:"via,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Effective is the record as a reader must see it at now: a waiting record past
// its deadline is expired even when the hook that held it never lived to say so.
func (r Record) Effective(now time.Time) Record {
	if r.State == StateWaiting && !now.Before(r.Deadline) {
		r.State = StateExpired
		r.UpdatedAt = r.Deadline
	}
	return r
}

type diskState struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

// Store is the private, flock-guarded request file of one state directory and
// its audit log.
type Store struct {
	path       string
	auditPath  string
	auditLimit int64
	now        func() time.Time
}

// NewStore opens the store under stateDir. Nothing is touched until the first
// call.
func NewStore(stateDir string) *Store {
	return NewStoreAt(filepath.Join(stateDir, storeDirName, storeFileName))
}

// NewStoreAt opens the store at an exact file path; the audit log is its
// sibling.
func NewStoreAt(path string) *Store {
	return &Store{path: path, auditPath: filepath.Join(filepath.Dir(path), auditFileName), auditLimit: auditMaxBytes, now: time.Now}
}

// WithClock returns the same store reading time from now.
func (s *Store) WithClock(now func() time.Time) *Store {
	out := *s
	out.now = now
	return &out
}

// WithAuditLimit returns the same store rotating its audit log at limit bytes.
func (s *Store) WithAuditLimit(limit int64) *Store {
	out := *s
	out.auditLimit = limit
	return &out
}

// Path is the store file.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// AuditPath is the audit log file.
func (s *Store) AuditPath() string {
	if s == nil {
		return ""
	}
	return s.auditPath
}

func (s *Store) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// Create stores one new waiting record and audits it as requested. ID,
// AgentUID, ToolName, ToolInput, and Deadline are the caller's; CreatedAt
// defaults to now.
func (s *Store) Create(record Record) (Record, error) {
	now := s.clock()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.Deadline = record.Deadline.UTC()
	record.State = StateWaiting
	record.Via = ""
	record.UpdatedAt = record.CreatedAt
	compact, ok := compactInput(record.ToolInput)
	if !ok {
		return Record{}, ErrInvalidRecord
	}
	record.ToolInput = compact
	if !validRecord(record) || !record.Deadline.After(record.CreatedAt) {
		return Record{}, ErrInvalidRecord
	}
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, existing := range state.Records {
			if existing.ID == record.ID {
				return ErrInvalidRecord
			}
		}
		records, err := pruneRecords(state.Records, now, 1)
		if err != nil {
			return err
		}
		state.Records = append(records, record)
		if err := s.writeLocked(state); err != nil {
			return err
		}
		s.auditLocked(auditLine(AuditRequested, record, now))
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// compactInput stores a tool input object without insignificant whitespace.
func compactInput(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxToolInputBytes || raw[0] != '{' {
		return nil, false
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// Get reads one record as a reader sees it now. It takes no lock: every write
// replaces the file atomically, so a read sees one whole generation.
func (s *Store) Get(id string) (Record, bool, error) {
	state, err := s.read()
	if err != nil {
		return Record{}, false, err
	}
	now := s.clock()
	for _, record := range state.Records {
		if record.ID == id {
			return record.Effective(now), true, nil
		}
	}
	return Record{}, false, nil
}

// List reads the records of one Agent, oldest first, as a reader sees them now.
func (s *Store) List(agentUID string) ([]Record, error) {
	state, err := s.read()
	if err != nil {
		return nil, err
	}
	now := s.clock()
	var out []Record
	for _, record := range state.Records {
		if record.AgentUID == agentUID {
			out = append(out, record.Effective(now))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Answer settles one waiting record of agentUID as allowed or denied. The
// deadline is judged under the lock with the store clock, so an answer racing
// the hook's own expiry, a second answer, or a terminal close lands on exactly
// one side: the first transition wins. A refusal changes no record and is
// audited as refused.
func (s *Store) Answer(id, agentUID string, allow bool, via string) (Record, error) {
	var out Record
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		now := s.clock()
		index := findRecord(state.Records, id, agentUID)
		if index < 0 {
			s.auditLocked(AuditLine{Event: AuditRefused, RequestID: boundedLine(id, 64), AgentUID: agentUID, Via: via, Reason: "not-found", At: now})
			return ErrNotFound
		}
		record := state.Records[index]
		if err := refusalFor(record.Effective(now).State); err != nil {
			line := auditLine(AuditRefused, record, now)
			line.Via, line.Reason = via, string(record.Effective(now).State)
			s.auditLocked(line)
			return err
		}
		record.State = StateDenied
		event := AuditDenied
		if allow {
			record.State, event = StateAllowed, AuditAllowed
		}
		record.Via = via
		record.UpdatedAt = now
		state.Records[index] = record
		records, err := pruneRecords(state.Records, now, 0)
		if err != nil {
			return err
		}
		state.Records = records
		if err := s.writeLocked(state); err != nil {
			return err
		}
		s.auditLocked(auditLine(event, record, now))
		out = record
		return nil
	})
	return out, err
}

// refusalFor maps a record that cannot take an answer to its error.
func refusalFor(state State) error {
	switch state {
	case StateWaiting:
		return nil
	case StateAllowed, StateDenied:
		return ErrNotPending
	case StateExpired:
		return ErrExpired
	default:
		return ErrClosed
	}
}

// Settle is the hook's deadline step: a waiting record at or past its deadline
// becomes expired. It returns the record as it now stands under the lock, so a
// caller that lost the race to an answer receives the settled record.
func (s *Store) Settle(id string) (Record, error) {
	return s.transition(id, "", func(record Record, now time.Time) (State, bool) {
		if record.State == StateWaiting && !now.Before(record.Deadline) {
			return StateExpired, true
		}
		return record.State, false
	})
}

// Close ends one waiting record without an answer, for reason. A record
// already past its deadline expires instead. It returns the record as it now
// stands.
func (s *Store) Close(id, reason string) (Record, error) {
	return s.transition(id, reason, func(record Record, now time.Time) (State, bool) {
		if record.State != StateWaiting {
			return record.State, false
		}
		if !now.Before(record.Deadline) {
			return StateExpired, true
		}
		return StateClosed, true
	})
}

func (s *Store) transition(id, reason string, next func(Record, time.Time) (State, bool)) (Record, error) {
	var out Record
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		now := s.clock()
		index := findRecord(state.Records, id, "")
		if index < 0 {
			return ErrNotFound
		}
		record := state.Records[index]
		target, changed := next(record, now)
		if !changed {
			out = record.Effective(now)
			return nil
		}
		record.State = target
		record.UpdatedAt = now
		if target == StateExpired {
			record.UpdatedAt = record.Deadline
		}
		state.Records[index] = record
		records, err := pruneRecords(state.Records, now, 0)
		if err != nil {
			return err
		}
		state.Records = records
		if err := s.writeLocked(state); err != nil {
			return err
		}
		line := auditLine(AuditClosed, record, now)
		if target == StateExpired {
			line = auditLine(AuditExpired, record, now)
		} else {
			line.Reason = reason
		}
		s.auditLocked(line)
		out = record
		return nil
	})
	return out, err
}

// CloseAnsweredInTerminal closes the one waiting record the operator already
// answered in Claude Code's own prompt: the record of sessionID and toolName
// whose tool input is canonically equal to toolInput. It closes only when
// exactly one waiting record matches; none or several close nothing, and the
// window expiry stays the backstop. It reports whether it closed one.
//
// PostToolUse runs it for every tool call, so it is cheap when there is
// nothing to do: a missing store file, and an unlocked read that finds no
// single candidate, return before the lock or any directory is created.
func (s *Store) CloseAnsweredInTerminal(sessionID, toolName string, toolInput json.RawMessage) (bool, error) {
	if s == nil || s.path == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(toolName) == "" {
		return false, nil
	}
	want, ok := CanonicalInput(toolInput)
	if !ok {
		return false, nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	state, err := s.read()
	if err != nil {
		return false, err
	}
	if _, count := matchWaiting(state.Records, s.clock(), sessionID, toolName, want); count != 1 {
		return false, nil
	}
	closed := false
	err = s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		now := s.clock()
		index, count := matchWaiting(state.Records, now, sessionID, toolName, want)
		if count != 1 {
			return nil
		}
		record := state.Records[index]
		record.State = StateClosed
		record.UpdatedAt = now
		state.Records[index] = record
		records, err := pruneRecords(state.Records, now, 0)
		if err != nil {
			return err
		}
		state.Records = records
		if err := s.writeLocked(state); err != nil {
			return err
		}
		line := auditLine(AuditClosed, record, now)
		line.Reason = CloseReasonAnsweredInTerminal
		s.auditLocked(line)
		closed = true
		return nil
	})
	return closed, err
}

// matchWaiting finds the waiting records of sessionID and toolName whose
// canonical tool input is want, and returns the index of the last one and how
// many there are.
func matchWaiting(records []Record, now time.Time, sessionID, toolName, want string) (int, int) {
	index, count := -1, 0
	for i, record := range records {
		if record.Effective(now).State != StateWaiting || record.SessionID != sessionID || record.ToolName != toolName {
			continue
		}
		if got, ok := CanonicalInput(record.ToolInput); ok && got == want {
			index, count = i, count+1
		}
	}
	return index, count
}

// CanonicalInput spells a tool input so two spellings of the same JSON value
// compare equal: object keys sorted, insignificant whitespace dropped, numbers
// kept as written.
func CanonicalInput(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", false
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return "", false
	}
	return strings.TrimSuffix(out.String(), "\n"), true
}

func findRecord(records []Record, id, agentUID string) int {
	for i := range records {
		if records[i].ID == id && (agentUID == "" || records[i].AgentUID == agentUID) {
			return i
		}
	}
	return -1
}

// pruneRecords materializes the expiry of every waiting record past its
// deadline, drops terminal records settled longer than the retention ago, and
// then makes room for incoming new records by dropping the oldest terminal
// ones. Waiting records are never dropped: a full store of them refuses.
func pruneRecords(records []Record, now time.Time, incoming int) ([]Record, error) {
	cutoff := now.Add(-terminalRetention)
	out := make([]Record, 0, len(records)+incoming)
	for _, record := range records {
		record = record.Effective(now)
		if record.State.Terminal() && record.UpdatedAt.Before(cutoff) {
			continue
		}
		out = append(out, record)
	}
	for len(out)+incoming > maxRecords {
		oldest := -1
		for i := range out {
			if out[i].State.Terminal() && (oldest < 0 || out[i].UpdatedAt.Before(out[oldest].UpdatedAt)) {
				oldest = i
			}
		}
		if oldest < 0 {
			return nil, ErrCapacity
		}
		out = append(out[:oldest], out[oldest+1:]...)
	}
	return out, nil
}

func validRecord(record Record) bool {
	if !ValidID(record.ID) || strings.TrimSpace(record.AgentUID) == "" || strings.TrimSpace(record.ToolName) == "" ||
		record.CreatedAt.IsZero() || record.Deadline.IsZero() || record.UpdatedAt.IsZero() {
		return false
	}
	if _, ok := compactInput(record.ToolInput); !ok {
		return false
	}
	if record.Via != "" && !ValidVia(record.Via) {
		return false
	}
	switch record.State {
	case StateWaiting, StateAllowed, StateDenied, StateExpired, StateClosed:
		return true
	default:
		return false
	}
}

// read decodes the current file without the lock.
func (s *Store) read() (diskState, error) {
	if s == nil || s.path == "" {
		return diskState{}, errors.New("agent approval store path is empty")
	}
	return s.loadLocked()
}

func (s *Store) withLock(fn func() error) error {
	if s == nil || s.path == "" {
		return errors.New("agent approval store path is empty")
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
	fd := int(lock.Fd())
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	deadline := time.Now().Add(lockWait)
	for lockBusy(err) && time.Now().Before(deadline) {
		time.Sleep(lockRetryInterval)
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		if lockBusy(err) {
			return ErrBusy
		}
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN) //nolint:errcheck -- releasing an owned advisory lock.
	return fn()
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
	return decodeState(data)
}

func decodeState(data []byte) (diskState, error) {
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
		if !validRecord(record) || seen[record.ID] {
			return diskState{}, ErrMalformedStore
		}
		seen[record.ID] = true
	}
	return state, nil
}

func (s *Store) writeLocked(state diskState) error {
	state.Version = storeVersion
	if state.Records == nil {
		state.Records = []Record{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxStoreBytes || len(state.Records) > maxRecords {
		return ErrCapacity
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".requests.tmp-*")
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
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	committed = true
	localstate.RepairPrivateFile(s.path)
	return syncDir(dir)
}

// syncDir makes a renamed directory entry durable. A filesystem that refuses
// the fsync is reported as a success because the entry is not this process's to
// repair.
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
