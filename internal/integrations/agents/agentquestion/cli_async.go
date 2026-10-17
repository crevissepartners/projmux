package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	localstate "github.com/crevissepartners/projmux/internal/state"
	"golang.org/x/sys/unix"
)

// MaxCLIReceiptBytes bounds the retained delivery summary; canonical broker
// receipts remain in the message store. Pending records reserve this space.
const MaxCLIReceiptBytes = 2 << 10

var errCLIAsyncUnchanged = errors.New("unchanged CLI question")

// CLIAsyncStore is independent of both strict legacy readers. Its question
// lifetime belongs to an Agent UID, never a provider generation or thread.
type CLIAsyncStore struct{ base *Store }
type CLIAsyncRecord struct {
	Record
	ResultDeadline time.Time      `json:"resultDeadline,omitzero"`
	ResultReason   string         `json:"resultReason,omitempty"`
	Outcome        string         `json:"outcome,omitempty"`
	Parts          []CLIAsyncPart `json:"parts,omitempty"`
}
type CLIAsyncPart struct {
	MessageRef  string          `json:"messageRef"`
	Attempt     uint32          `json:"attempt,omitempty"`
	NextAttempt time.Time       `json:"nextAttempt,omitzero"`
	State       string          `json:"state"`
	Receipt     json.RawMessage `json:"receipt,omitempty"`
}
type cliAsyncDisk struct {
	Version int              `json:"version"`
	Records []CLIAsyncRecord `json:"records"`
}

func (s *Store) CLIAsyncStore() *CLIAsyncStore {
	p := filepath.Dir(s.path)
	if s.asyncNamespace {
		p = filepath.Dir(p)
	}
	return &CLIAsyncStore{base: NewStoreAt(filepath.Join(p, ".cli-async", storeFileName)).WithClock(s.now)}
}
func (s *CLIAsyncStore) Path() string { return s.base.Path() }
func (s *CLIAsyncStore) read() (cliAsyncDisk, error) {
	b, e := os.ReadFile(s.Path()) // #nosec G304 -- private question namespace.
	if errors.Is(e, os.ErrNotExist) {
		return cliAsyncDisk{Version: 1}, nil
	}
	if e != nil {
		return cliAsyncDisk{}, e
	}
	if len(b) > maxStoreBytes {
		return cliAsyncDisk{}, ErrMalformedStore
	}
	var d cliAsyncDisk
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || d.Version != 1 || len(d.Records) > maxRecords {
		return d, ErrMalformedStore
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return d, ErrMalformedStore
	}
	seen := map[string]bool{}
	for _, r := range d.Records {
		if !ValidID(r.ID) || seen[r.ID] || r.Delivery != "async-message" || r.AgentUID == "" || r.CreatedAt.IsZero() || !r.Deadline.After(r.CreatedAt) || r.UpdatedAt.IsZero() {
			return d, ErrMalformedStore
		}
		if _, e := ParseQuestions(r.Questions); e != nil {
			return d, ErrMalformedStore
		}
		if r.State != StateWaiting && r.State != StateAnswered && r.State != StateExpired && r.State != StateClosed {
			return d, ErrMalformedStore
		}

		if r.State == StateWaiting {
			if r.Outcome != "" || len(r.Parts) != 0 || len(r.Answers) != 0 || !r.ResultDeadline.IsZero() {
				return d, ErrMalformedStore
			}
		} else if r.Outcome == "target-deleted" && r.State == StateClosed && r.Disposition == "target-deleted" && r.ResultReason == "target-deleted" {
			if len(r.Parts) != 0 || len(r.Answers) != 0 || !r.ResultDeadline.IsZero() {
				return d, ErrMalformedStore
			}
		} else {
			if r.ResultDeadline.IsZero() || r.Outcome == "" {
				return d, ErrMalformedStore
			}
			switch r.Outcome {
			case "pending", "delivered", "unknown", "failed", "target-deleted", "result-expired":
			default:
				return d, ErrMalformedStore
			}
			if r.State == StateAnswered {
				qs, _ := ParseQuestions(r.Questions)
				if ValidateAnswers(qs, r.Answers) != nil || r.ResultReason != "" {
					return d, ErrMalformedStore
				}
			} else if len(r.Answers) != 0 || (r.ResultReason != "expired" && r.ResultReason != "declined") {
				return d, ErrMalformedStore
			}
			bodies, e := r.Messages()
			if e != nil || len(bodies) != len(r.Parts) {
				return d, ErrMalformedStore
			}
			for n, p := range r.Parts {
				if len(p.Receipt) > MaxCLIReceiptBytes {
					return d, ErrMalformedStore
				}
				if p.MessageRef != r.PartRef(n, p.Attempt) {
					return d, ErrMalformedStore
				}
				switch p.State {
				case "pending", "sending", "accepted", "delivered", "unknown", "failed":
				default:
					return d, ErrMalformedStore
				}
			}
		}
		seen[r.ID] = true
	}
	return d, nil
}
func (s *CLIAsyncStore) update(fn func(*cliAsyncDisk, time.Time) error) error {
	return s.base.withLock(patientLockWait, func() error {
		d, e := s.read()
		if e != nil {
			return e
		}
		if e = fn(&d, s.base.clock()); e != nil {
			if errors.Is(e, errCLIAsyncUnchanged) {
				return nil
			}
			return e
		}
		b, e := json.Marshal(d)
		if e != nil {
			return e
		}
		if len(b)+1 > maxStoreBytes || len(d.Records) > maxRecords {
			return ErrCapacity
		}
		budget := len(b) + 1
		for _, r := range d.Records {
			for _, part := range r.Parts {
				if len(part.Receipt) > MaxCLIReceiptBytes {
					return ErrCapacity
				}
			}
			if !r.Pending() {
				continue
			}
			actual, e := json.Marshal(r)
			if e != nil {
				return e
			}
			future, e := cliAsyncFutureSize(r)
			if e != nil {
				return e
			}
			budget += max(0, future-len(actual))
			if budget > maxStoreBytes {
				return ErrCapacity
			}
		}
		return s.writeLocked(append(b, '\n'))
	})
}
func (s *CLIAsyncStore) List(uid string) ([]CLIAsyncRecord, error) {
	d, e := s.read()
	if e != nil {
		return nil, e
	}
	out := []CLIAsyncRecord{}
	for _, r := range d.Records {
		if uid == "" || r.AgentUID == uid {
			r.Record = r.Effective(s.base.clock())
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (s *CLIAsyncStore) Get(id string) (CLIAsyncRecord, bool, error) {
	rs, e := s.List("")
	for _, r := range rs {
		if r.ID == id {
			return r, true, e
		}
	}
	return CLIAsyncRecord{}, false, e
}
func (r CLIAsyncRecord) Pending() bool {
	if r.Outcome == "" || r.Outcome == "pending" {
		return true
	}
	if r.Outcome == "unknown" {
		for _, p := range r.Parts {
			if p.State == "pending" || p.State == "sending" || p.State == "accepted" {
				return true
			}
		}
	}
	return false
}
func (s *CLIAsyncStore) Create(uid string, raw json.RawMessage, duration time.Duration) (CLIAsyncRecord, error) {
	if _, e := ParseQuestions(raw); e != nil {
		return CLIAsyncRecord{}, e
	}
	if uid == "" || duration <= 0 || duration > 7*24*time.Hour {
		return CLIAsyncRecord{}, ErrInvalidRecord
	}
	id, e := NewID()
	if e != nil {
		return CLIAsyncRecord{}, e
	}
	var out CLIAsyncRecord
	e = s.update(func(d *cliAsyncDisk, now time.Time) error {
		kept := d.Records[:0]
		for _, r := range d.Records {
			if r.Pending() || !r.UpdatedAt.Before(now.Add(-terminalRetention)) {
				kept = append(kept, r)
			}
		}
		d.Records = kept
		for len(d.Records) >= maxRecords {
			oldest := -1
			for i, r := range d.Records {
				if !r.Pending() && (oldest < 0 || r.UpdatedAt.Before(d.Records[oldest].UpdatedAt)) {
					oldest = i
				}
			}
			if oldest < 0 {
				return ErrCapacity
			}
			d.Records = append(d.Records[:oldest], d.Records[oldest+1:]...)
		}
		out = CLIAsyncRecord{Record: Record{ID: id, AgentUID: uid, Delivery: "async-message", Questions: compactQuestions(raw), CreatedAt: now, UpdatedAt: now, Deadline: now.Add(duration), State: StateWaiting}}
		d.Records = append(d.Records, out)
		return nil
	})
	return out, e
}

// Resolve commits the answer/non-answer and outbox atomically. Expiry wins at
// the deadline; simultaneous answers, decline and timer have one winner.
func (s *CLIAsyncStore) Resolve(id, uid string, answers map[string]string, reason string) (CLIAsyncRecord, error) {
	var out CLIAsyncRecord
	e := s.update(func(d *cliAsyncDisk, now time.Time) error {
		for i := range d.Records {
			r := &d.Records[i]
			if r.ID != id || r.AgentUID != uid {
				continue
			}
			if r.State != StateWaiting {
				return ErrNotPending
			}
			if !now.Before(r.Deadline) {
				reason = "expired"
				answers = nil
			}
			if reason != "" && reason != "expired" && reason != "declined" {
				return ErrInvalidRecord
			}
			if reason == "" {
				qs, _ := ParseQuestions(r.Questions)
				if e := ValidateAnswers(qs, answers); e != nil {
					return e
				}
				r.State = StateAnswered
				r.Answers = cloneAnswers(answers)
			} else if reason == "expired" {
				r.State = StateExpired
			} else {
				r.State = StateClosed
				r.Disposition = "declined"
			}
			r.ResultReason = reason
			r.ResultDeadline = now.Add(24 * time.Hour)
			r.UpdatedAt = now
			r.Outcome = "pending"
			parts, e := r.Messages()
			if e != nil {
				return e
			}
			for n := range parts {
				r.Parts = append(r.Parts, CLIAsyncPart{MessageRef: r.PartRef(n, 0), State: "pending"})
			}
			out = *r
			return nil
		}
		return ErrNotFound
	})
	return out, e
}

// MutateResult serializes dispatcher claims with durable receipt settlement.
func (s *CLIAsyncStore) MutateResult(id string, fn func(*CLIAsyncRecord) error) error {
	return s.update(func(d *cliAsyncDisk, now time.Time) error {
		for i := range d.Records {
			if d.Records[i].ID == id {
				before, err := json.Marshal(d.Records[i])
				if err != nil {
					return err
				}
				if e := fn(&d.Records[i]); e != nil {
					return e
				}
				after, err := json.Marshal(d.Records[i])
				if err != nil {
					return err
				}
				if bytes.Equal(before, after) {
					return errCLIAsyncUnchanged
				}
				d.Records[i].UpdatedAt = now
				return nil
			}
		}
		return ErrNotFound
	})
}

// Messages preserves the complete result JSON. Large JSON is reconstructed by
// concatenating UTF-8 data fields in part order; each encoded body is <=2KiB.
func (r CLIAsyncRecord) Messages() ([]string, error) {
	status := "answered"
	if r.ResultReason != "" {
		status = "no-answer"
	}
	payload := struct {
		Kind       string            `json:"kind"`
		QuestionID string            `json:"questionId"`
		Questions  json.RawMessage   `json:"questions"`
		Status     string            `json:"status"`
		Answers    map[string]string `json:"answers"`
		Reason     string            `json:"reason,omitempty"`
	}{"projmux-question-result", r.ID, r.Questions, status, r.Answers, r.ResultReason}
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	if len(raw) <= 2048 {
		return []string{string(raw)}, nil
	}
	chunks := []string{}
	for len(raw) > 0 {
		n := min(256, len(raw))
		for n > 0 && !utf8.Valid(raw[:n]) {
			n--
		}
		if n == 0 {
			return nil, ErrInvalidRecord
		}
		chunks = append(chunks, string(raw[:n]))
		raw = raw[n:]
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		b, e := json.Marshal(struct {
			Kind       string `json:"kind"`
			QuestionID string `json:"questionId"`
			Part       int    `json:"part"`
			Total      int    `json:"total"`
			Data       string `json:"data"`
		}{"projmux-question-result", r.ID, i + 1, len(chunks), c})
		if e != nil {
			return nil, e
		}
		out[i] = string(b)
	}
	return out, nil
}

// LockOwner elects one expiry/outbox process per namespace. The kernel releases
// ownership on exit; the lock inode is retained across all generations.
func (s *CLIAsyncStore) LockOwner() (func(), bool, error) {
	if e := localstate.EnsurePrivateDir(filepath.Dir(s.Path())); e != nil {
		return nil, false, e
	}
	f, e := os.OpenFile(s.Path()+".owner", os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- private owner lock.
	if e != nil {
		return nil, false, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		if lockBusy(e) {
			return nil, false, nil
		}
		return nil, false, e
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, true, nil
}

// ReleaseIfIdle checks and unlocks in the writer critical section so a creator
// cannot race a dying owner and mistake it for an owner of its new question.
func (s *CLIAsyncStore) ReleaseIfIdle(release func()) (bool, error) {
	idle := false
	e := s.base.withLock(patientLockWait, func() error {
		d, e := s.read()
		if e != nil {
			return e
		}
		for _, r := range d.Records {
			if r.Pending() {
				return nil
			}
		}
		release()
		idle = true
		return nil
	})
	return idle, e
}
func (s *CLIAsyncStore) RemoveUnstarted(id string) error {
	return s.update(func(d *cliAsyncDisk, _ time.Time) error {
		for i, r := range d.Records {
			if r.ID == id && r.State == StateWaiting && r.Outcome == "" {
				d.Records = append(d.Records[:i], d.Records[i+1:]...)
				return nil
			}
		}
		return ErrNotPending
	})
}

// The result and its delivery claim must survive a machine restart, not merely
// a process exit. Fsync both the temporary file and its renamed directory entry.
func (s *CLIAsyncStore) writeLocked(data []byte) error {
	dir := filepath.Dir(s.Path())
	localstate.RemoveLockedTemps(dir, tempPrefix, nil)
	f, e := os.CreateTemp(dir, tempPrefix+"*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = f.Close(); _ = os.Remove(name) }()
	if e = f.Chmod(localstate.PrivateFileMode); e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, s.Path()); e != nil {
		return e
	}
	return syncDir(dir)
}

// PartRef is stable across crashes for one bounded dispatch attempt. A new
// attempt is allowed only after a receipt proves the previous write failed.
func (r CLIAsyncRecord) PartRef(index int, attempt uint32) string {
	ref := fmt.Sprintf("message-%s-%d", r.ID, index+1)
	if attempt != 0 {
		ref += fmt.Sprintf("-retry-%d", attempt)
	}
	return ref
}

// TargetDeleted closes a still-open card without inventing an operator answer.
// A settled result retains its receipt/unknown evidence while becoming terminal.
func (s *CLIAsyncStore) TargetDeleted(id string) error {
	return s.MutateResult(id, func(r *CLIAsyncRecord) error {
		if r.State == StateWaiting {
			r.State = StateClosed
			r.Disposition = "target-deleted"
			r.ResultReason = "target-deleted"
		}
		r.Outcome = "target-deleted"
		return nil
	})
}

// cliAsyncFutureSize reserves the bounded receipt/retry bookkeeping before a
// question or answer is accepted. A waiting question reserves its decline/expiry
// outbox; a larger answer must obtain its own reservation atomically at Resolve.
// Settled parts need no further reservation. No pending record is evicted.
func cliAsyncFutureSize(r CLIAsyncRecord) (int, error) {
	future := r
	longestTime := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	future.UpdatedAt = longestTime
	if r.State == StateWaiting {
		future.State = StateClosed
		future.Disposition = "declined"
		future.ResultReason = "declined"
		future.ResultDeadline = longestTime
		bodies, e := future.Messages()
		if e != nil {
			return 0, e
		}
		future.Parts = make([]CLIAsyncPart, len(bodies))
	} else {
		future.Parts = append([]CLIAsyncPart(nil), r.Parts...)
	}
	future.Outcome = "target-deleted"
	padding := json.RawMessage(`"` + strings.Repeat("x", MaxCLIReceiptBytes-2) + `"`)
	for i, p := range future.Parts {
		if p.State == "delivered" || p.State == "unknown" || p.State == "failed" {
			continue
		}
		future.Parts[i] = CLIAsyncPart{MessageRef: future.PartRef(i, ^uint32(0)), Attempt: ^uint32(0), NextAttempt: longestTime, State: "delivered", Receipt: padding}
	}
	b, e := json.Marshal(future)
	return len(b), e
}
