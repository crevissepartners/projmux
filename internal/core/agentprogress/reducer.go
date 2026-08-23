// Package agentprogress reduces provider event discriminators and bounded
// scalar counts into the one current-turn Agent progress projection. It never
// accepts wire objects or content-bearing strings.
package agentprogress

import (
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const MinWriteInterval = 250 * time.Millisecond

type EventKind uint8

const (
	EventTurnStarted EventKind = iota + 1
	EventTurnTerminal
	EventPlanUpdated
	EventDiffUpdated
	EventItemStarted
	EventItemCompleted
)

// Event is the complete provider-neutral reducer input. ItemRef is an opaque
// dedupe token and is never persisted or rendered. No field can carry prompt,
// reasoning, step, command, tool, path, output, or diff content.
type Event struct {
	Kind             EventKind
	TurnRef          string
	ItemRef          string
	Activity         coremetadata.AgentProgressActivity
	PlanCompleted    uint8
	PlanInProgress   uint8
	PlanTotal        uint8
	PlanTruncated    bool
	ChangedFiles     uint16
	FilesTruncated   bool
	StartedAt        time.Time
	ObservedAt       time.Time
	UnknownIncrement uint32
}

type Diagnostics struct {
	Dropped  uint32
	Unknown  uint32
	Overflow uint32
}

type itemState struct {
	activity coremetadata.AgentProgressActivity
	sequence uint64
	active   bool
}

// Reducer retains at most 32 opaque item ids for the current turn and one
// pending scalar projection. Emitted history is not retained beyond the value
// needed to suppress identical writes.
type Reducer struct {
	progress     coremetadata.AgentProgress
	items        map[string]itemState
	sequence     uint64
	overflowed   bool
	diagnostics  Diagnostics
	pending      bool
	clearPending bool
	lastEmitted  coremetadata.AgentProgress
	lastWriteAt  time.Time
}

func (r *Reducer) Begin(turnRef string, startedAt, observedAt time.Time) {
	r.resetTurn(turnRef, startedAt, observedAt)
	if turnRef != "" {
		r.pending = true
	}
}

func (r *Reducer) Observe(event Event) bool {
	if event.UnknownIncrement > 0 {
		r.diagnostics.Unknown += event.UnknownIncrement
	}
	if event.TurnRef == "" {
		return false
	}
	if event.Kind == EventTurnStarted {
		if event.TurnRef == r.progress.TurnRef {
			return false
		}
		r.resetTurn(event.TurnRef, event.StartedAt, event.ObservedAt)
		r.pending = true
		return true
	}
	if event.TurnRef != r.progress.TurnRef || r.progress.TurnRef == "" {
		r.diagnostics.Dropped++
		return false
	}
	if !event.ObservedAt.IsZero() && !r.progress.ObservedAt.IsZero() && event.ObservedAt.Before(r.progress.ObservedAt) {
		r.diagnostics.Dropped++
		return false
	}
	if event.Kind == EventTurnTerminal {
		r.progress = coremetadata.AgentProgress{}
		r.items = nil
		r.pending = false
		r.clearPending = !r.lastEmitted.IsZero()
		return r.clearPending
	}

	before := r.progress
	switch event.Kind {
	case EventPlanUpdated:
		r.progress.PlanCompleted = event.PlanCompleted
		r.progress.PlanInProgress = event.PlanInProgress
		r.progress.PlanTotal = event.PlanTotal
		r.progress.PlanTruncated = event.PlanTruncated
	case EventDiffUpdated:
		r.progress.ChangedFiles = event.ChangedFiles
		r.progress.FilesTruncated = event.FilesTruncated
	case EventItemStarted:
		r.observeItemStarted(event)
	case EventItemCompleted:
		r.observeItemCompleted(event)
	default:
		return false
	}
	if sameSemanticProgress(before, r.progress) {
		return false
	}
	if !event.ObservedAt.IsZero() {
		r.progress.ObservedAt = event.ObservedAt.UTC()
	}
	r.pending = true
	return true
}

func (r *Reducer) Invalidate() bool {
	hadDurableValue := !r.lastEmitted.IsZero()
	r.progress = coremetadata.AgentProgress{}
	r.items = nil
	r.pending = false
	r.clearPending = hadDurableValue
	return hadDurableValue
}

// Flush emits at most one durable mutation. A terminal/invalidation clear is
// never rate limited; non-terminal semantic changes are separated by 250ms.
func (r *Reducer) Flush(now time.Time) (coremetadata.AgentProgress, bool) {
	if r.clearPending {
		r.clearPending = false
		r.lastEmitted = coremetadata.AgentProgress{}
		return coremetadata.AgentProgress{}, true
	}
	if !r.pending {
		return coremetadata.AgentProgress{}, false
	}
	if !r.lastWriteAt.IsZero() && now.Sub(r.lastWriteAt) < MinWriteInterval {
		return coremetadata.AgentProgress{}, false
	}
	r.pending = false
	r.lastWriteAt = now
	r.lastEmitted = r.progress
	return r.progress, true
}

func (r *Reducer) NextFlushAt() time.Time {
	if r.clearPending {
		return time.Time{}
	}
	if !r.pending {
		return time.Time{}
	}
	if r.lastWriteAt.IsZero() {
		return time.Time{}
	}
	return r.lastWriteAt.Add(MinWriteInterval)
}

func (r *Reducer) Diagnostics() Diagnostics { return r.diagnostics }

func (r *Reducer) Current() coremetadata.AgentProgress { return r.progress }

func (r *Reducer) resetTurn(turnRef string, startedAt, observedAt time.Time) {
	r.progress = coremetadata.AgentProgress{
		TurnRef: turnRef, StartedAt: startedAt.UTC(), ObservedAt: observedAt.UTC(),
		Source: coremetadata.AgentProgressSource,
	}
	r.items = map[string]itemState{}
	r.sequence = 0
	r.overflowed = false
	r.pending = false
	r.clearPending = false
}

func (r *Reducer) observeItemStarted(event Event) {
	if event.ItemRef == "" || !coremetadata.ValidAgentProgressActivity(event.Activity) || event.Activity == "" {
		r.diagnostics.Dropped++
		return
	}
	if _, seen := r.items[event.ItemRef]; seen {
		return
	}
	if len(r.items) >= coremetadata.AgentProgressItemsCap {
		r.overflowed = true
		r.diagnostics.Dropped++
		r.diagnostics.Overflow++
		r.progress.Activity = coremetadata.ProgressOther
		return
	}
	r.sequence++
	r.items[event.ItemRef] = itemState{activity: event.Activity, sequence: r.sequence, active: true}
	r.refreshActivity()
}

func (r *Reducer) observeItemCompleted(event Event) {
	if event.ItemRef == "" {
		r.diagnostics.Dropped++
		return
	}
	item, seen := r.items[event.ItemRef]
	if !seen {
		if len(r.items) >= coremetadata.AgentProgressItemsCap {
			r.overflowed = true
			r.diagnostics.Dropped++
			r.diagnostics.Overflow++
			r.progress.Activity = coremetadata.ProgressOther
			return
		}
		r.items[event.ItemRef] = itemState{}
		return
	}
	if !item.active {
		return
	}
	item.active = false
	r.items[event.ItemRef] = item
	r.refreshActivity()
}

func (r *Reducer) refreshActivity() {
	activeCount := 0
	var latest itemState
	for _, item := range r.items {
		if !item.active {
			continue
		}
		activeCount++
		if item.sequence > latest.sequence {
			latest = item
		}
	}
	r.progress.ActiveItemCount = uint8(activeCount)
	if r.overflowed {
		r.progress.Activity = coremetadata.ProgressOther
	} else {
		r.progress.Activity = latest.activity
	}
}

func sameSemanticProgress(a, b coremetadata.AgentProgress) bool {
	a.ObservedAt = time.Time{}
	b.ObservedAt = time.Time{}
	return a == b
}
