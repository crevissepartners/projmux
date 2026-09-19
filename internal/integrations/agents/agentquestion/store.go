package agentquestion

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
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
	storeDirName  = "agent-questions"
	storeFileName = "questions.json"
	// maxRecords bounds the store. A waiting record is never evicted to make
	// room; the oldest terminal ones are.
	maxRecords    = 64
	maxStoreBytes = 2 << 20
	// terminalRetention keeps answered, expired, and closed records listable
	// for a day after they settled.
	terminalRetention = 24 * time.Hour
	// lockWait bounds how long a writer queues behind another holder. Holders
	// keep the lock for one read and at most one fsync'd write.
	lockWait          = 2 * time.Second
	lockRetryInterval = 2 * time.Millisecond
)

// State is the closed lifecycle of one question record.
type State string

const (
	// StateWaiting: the hook holds the tool call open for an answer.
	StateWaiting State = "waiting"
	// StateAnswered: an answer was accepted. Terminal.
	StateAnswered State = "answered"
	// StateExpired: the answer window ended first. Terminal.
	StateExpired State = "expired"
	// StateClosed: the hook was canceled, or the channel was turned off,
	// before an answer arrived. Terminal.
	StateClosed State = "closed"
)

// Terminal reports whether no further transition is possible.
func (s State) Terminal() bool { return s != StateWaiting }

var (
	ErrNotFound       = errors.New("question not found")
	ErrNotPending     = errors.New("question is already answered")
	ErrExpired        = errors.New("question expired")
	ErrClosed         = errors.New("question closed")
	ErrCapacity       = errors.New("agent question store is at capacity")
	ErrMalformedStore = errors.New("malformed agent question store")
	ErrBusy           = errors.New("agent question store is busy")
	ErrInvalidRecord  = errors.New("invalid agent question record")
)

var idPattern = regexp.MustCompile(`^question-[0-9a-f]{16}$`)

// NewID mints a question id.
func NewID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "question-" + hex.EncodeToString(raw[:]), nil
}

// ValidID reports whether id is a question id this package mints.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Record is one question set and its outcome.
type Record struct {
	ID        string            `json:"id"`
	AgentUID  string            `json:"agentUID"`
	PaneUID   string            `json:"paneUID,omitempty"`
	SessionID string            `json:"sessionID,omitempty"`
	ToolUseID string            `json:"toolUseID,omitempty"`
	Questions json.RawMessage   `json:"questions"`
	CreatedAt time.Time         `json:"createdAt"`
	Deadline  time.Time         `json:"deadline"`
	State     State             `json:"state"`
	Answers   map[string]string `json:"answers,omitempty"`
	UpdatedAt time.Time         `json:"updatedAt"`
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

// ParsedQuestions decodes the stored question set.
func (r Record) ParsedQuestions() ([]Question, error) { return ParseQuestions(r.Questions) }

type diskState struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

// Store is the private, flock-guarded question file of one state directory.
type Store struct {
	path string
	now  func() time.Time
}

// NewStore opens the store under stateDir. Nothing is touched until the first
// call.
func NewStore(stateDir string) *Store {
	return NewStoreAt(filepath.Join(stateDir, storeDirName, storeFileName))
}

// NewStoreAt opens the store at an exact file path.
func NewStoreAt(path string) *Store { return &Store{path: path, now: time.Now} }

// WithClock returns the same store reading time from now.
func (s *Store) WithClock(now func() time.Time) *Store {
	out := *s
	out.now = now
	return &out
}

// Path is the store file.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// Create stores one new waiting record. ID, AgentUID, Questions, and Deadline
// are the caller's; CreatedAt defaults to now.
func (s *Store) Create(record Record) (Record, error) {
	now := s.clock()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.Deadline = record.Deadline.UTC()
	record.State = StateWaiting
	record.Answers = nil
	record.UpdatedAt = record.CreatedAt
	record.Questions = compactQuestions(record.Questions)
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
		return s.writeLocked(state)
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// compactQuestions stores the question set without insignificant whitespace.
// The hook answers Claude Code with the bytes it received, not these.
func compactQuestions(raw json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
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

// Answer settles one waiting record of agentUID with answers. The deadline is
// judged under the lock with the store clock, so an answer racing the hook's
// own expiry lands on exactly one side. A refusal changes nothing.
func (s *Store) Answer(id, agentUID string, answers map[string]string) (Record, error) {
	var out Record
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		now := s.clock()
		index := findRecord(state.Records, id, agentUID)
		if index < 0 {
			return ErrNotFound
		}
		record := state.Records[index]
		if err := refusalFor(record.Effective(now).State); err != nil {
			return err
		}
		questions, err := record.ParsedQuestions()
		if err != nil {
			return ErrMalformedStore
		}
		if err := ValidateAnswers(questions, answers); err != nil {
			return err
		}
		record.State = StateAnswered
		record.Answers = cloneAnswers(answers)
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
	case StateAnswered:
		return ErrNotPending
	case StateExpired:
		return ErrExpired
	default:
		return ErrClosed
	}
}

// Settle is the hook's deadline step: a waiting record at or past its deadline
// becomes expired. It returns the record as it now stands, so a caller that
// lost the race to an answer receives the answered record.
func (s *Store) Settle(id string) (Record, error) {
	return s.transition(id, func(record Record, now time.Time) (State, bool) {
		if record.State == StateWaiting && !now.Before(record.Deadline) {
			return StateExpired, true
		}
		return record.State, false
	})
}

// Close ends one waiting record without an answer: the hook was canceled. A
// record already past its deadline expires instead. It returns the record as it
// now stands.
func (s *Store) Close(id string) (Record, error) {
	return s.transition(id, func(record Record, now time.Time) (State, bool) {
		if record.State != StateWaiting {
			return record.State, false
		}
		if !now.Before(record.Deadline) {
			return StateExpired, true
		}
		return StateClosed, true
	})
}

func (s *Store) transition(id string, next func(Record, time.Time) (State, bool)) (Record, error) {
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
		out = record
		return nil
	})
	return out, err
}

// CloseAgent closes every record of agentUID still waiting, and reports how
// many it closed. Turning an Agent's question channel off calls it, so each
// hook holding one of them returns the question to Claude Code's own prompt.
func (s *Store) CloseAgent(agentUID string) (int, error) {
	closed := 0
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		now := s.clock()
		for i := range state.Records {
			record := state.Records[i]
			if record.AgentUID != agentUID || record.State != StateWaiting || !now.Before(record.Deadline) {
				continue
			}
			record.State = StateClosed
			record.UpdatedAt = now
			state.Records[i] = record
			closed++
		}
		if closed == 0 {
			return nil
		}
		records, err := pruneRecords(state.Records, now, 0)
		if err != nil {
			return err
		}
		state.Records = records
		return s.writeLocked(state)
	})
	return closed, err
}

func findRecord(records []Record, id, agentUID string) int {
	for i := range records {
		if records[i].ID == id && (agentUID == "" || records[i].AgentUID == agentUID) {
			return i
		}
	}
	return -1
}

func cloneAnswers(answers map[string]string) map[string]string {
	out := make(map[string]string, len(answers))
	maps.Copy(out, answers)
	return out
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
	if !ValidID(record.ID) || strings.TrimSpace(record.AgentUID) == "" || record.CreatedAt.IsZero() ||
		record.Deadline.IsZero() || record.UpdatedAt.IsZero() {
		return false
	}
	if _, err := ParseQuestions(record.Questions); err != nil {
		return false
	}
	switch record.State {
	case StateAnswered:
		return len(record.Answers) > 0
	case StateWaiting, StateExpired, StateClosed:
		return len(record.Answers) == 0
	default:
		return false
	}
}

// read decodes the current file without the lock.
func (s *Store) read() (diskState, error) {
	if s == nil || s.path == "" {
		return diskState{}, errors.New("agent question store path is empty")
	}
	return s.loadLocked()
}

func (s *Store) withLock(fn func() error) error {
	if s == nil || s.path == "" {
		return errors.New("agent question store path is empty")
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
	tmp, err := os.CreateTemp(dir, ".questions.tmp-*")
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
