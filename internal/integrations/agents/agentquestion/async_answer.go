package agentquestion

import (
	"encoding/json"
	"fmt"
	"reflect"
)

const (
	MaxAsyncQuestionTextBytes       = 4096
	MaxAsyncQuestionSelectionsBytes = 16384
)

// ValidateAsyncSelections validates a complete explicit set before any claim.
// The code is stable; callers must not classify failures by their message.
func ValidateAsyncSelections(questions []Question, selections map[int]Selection) (string, error) {
	for _, q := range questions {
		if q.IsSecret {
			return "question-secret-native-only", fmt.Errorf("secret question sets require the native surface")
		}
	}
	for _, s := range selections {
		if s.HasText && len(s.Text) > MaxAsyncQuestionTextBytes {
			return "question-answer-too-large", fmt.Errorf("async text exceeds %d UTF-8 bytes", MaxAsyncQuestionTextBytes)
		}
	}
	raw, err := json.Marshal(selections)
	if err != nil || len(raw) > MaxAsyncQuestionSelectionsBytes {
		return "question-answer-too-large", fmt.Errorf("async selections exceed %d JSON bytes", MaxAsyncQuestionSelectionsBytes)
	}
	if _, err = BuildCodexAnswers(questions, selections); err != nil {
		return "question-invalid-answer", err
	}
	return "", nil
}

// BeginAsync claims only the exact waiting async record. The owning handle must
// already retain a terminal tombstone, including if this durable write fails.
// Existing closed/disposition metadata keeps strict T1 readers compatible.
func (s *Store) BeginAsync(expected Record) (Record, error) {
	var receipt Record
	err := s.withLock(patientLockWait, func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		i := findRecord(state.Records, expected.ID, expected.AgentUID)
		if i < 0 {
			return ErrNotFound
		}
		r := state.Records[i]
		now := s.clock()
		if !s.asyncNamespace || r.Delivery != "async" {
			return ErrInvalidRecord
		}
		if r.State != StateWaiting {
			return ErrNotPending
		}
		if !now.Before(r.Deadline) {
			return ErrExpired
		}
		if !reflect.DeepEqual(r, expected) {
			return ErrNotPending
		}
		r.State, r.Disposition, r.UpdatedAt = StateClosed, "async-unknown", now
		state.Records[i] = r
		receipt = r // Also identify a rename committed before a directory-sync error.
		if err = s.writeLocked(state); err != nil {
			return err
		}
		return nil
	})
	return receipt, err
}

// FinishAsync settles the exact claim, never a replacement or pruned record.
// No answer body is persisted. Losing settlement can only yield uncertainty.
func (s *Store) FinishAsync(receipt Record, outcome string) error {
	if outcome != "submitted" && outcome != "unknown" && outcome != "not-submitted" {
		return ErrInvalidRecord
	}
	return s.withLock(lockWait, func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		i := findRecord(state.Records, receipt.ID, receipt.AgentUID)
		if i < 0 {
			return ErrNotFound
		}
		if !s.asyncNamespace || receipt.Delivery != "async" || receipt.State != StateClosed || receipt.Disposition != "async-unknown" || !reflect.DeepEqual(state.Records[i], receipt) {
			return ErrNotPending
		}
		r := receipt
		r.Disposition, r.UpdatedAt = "async-"+outcome, s.clock()
		state.Records[i] = r
		return s.writeLocked(state)
	})
}
