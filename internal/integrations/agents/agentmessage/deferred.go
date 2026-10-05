package agentmessage

import (
	"errors"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// DeferredFor preserves store acceptance order, including timestamp ties.
// Operator-dialog holds belong to the existing release consumer alone.
func (s *Store) DeferredFor(uid, reason string) ([]Record, error) {
	var records []Record
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.Envelope.Target.AgentUID == uid && record.Delivery.State == coremessage.StateHeld && record.Delivery.Reason == reason {
				records = append(records, record)
			}
		}
		return nil
	})
	return records, err
}

// ReaddressDeferred is the sole narrow exception to immutable target routes.
// The caller proves the retired binding and same provider conversation against
// its claimed resume result before calling. Only unsubmitted deferred holds can
// move, and only generation changes; every other envelope byte stays fixed.
func (s *Store) ReaddressDeferred(ref string, old, next coremessage.Route, reason string, now time.Time) (Record, error) {
	var result Record
	if !old.Valid() || !next.Valid() || old.AgentUID != next.AgentUID || old.PaneUID != next.PaneUID || old.Provider != next.Provider || old.Incarnation != next.Incarnation || reason != "target-awaiting-resume" {
		return result, coremessage.ErrRetryMismatch
	}
	err := s.withLock(func() error {
		state, err := s.loadLocked()
		if err != nil {
			return err
		}
		for i := range state.Records {
			record := &state.Records[i]
			if record.Envelope.MessageRef != ref {
				continue
			}
			if !record.Envelope.Target.Same(old) || record.Delivery.State != coremessage.StateHeld || record.Delivery.Reason != reason || record.HandoffObserved {
				return coremessage.ErrRetryMismatch
			}
			if !record.Envelope.Deadline.After(now) {
				return errors.New("deferred message deadline expired")
			}
			record.Envelope.Target = next
			result = *record
			return s.writeLocked(state, nil)
		}
		return ErrNotFound
	})
	return result, err
}
