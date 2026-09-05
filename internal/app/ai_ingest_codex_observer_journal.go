package app

import (
	"slices"
	"sync"
	"time"
)

// aiIngestCodexObserverSource is the ai-ingest.log source column for records
// written by a managed Codex lifecycle observer.
//
// It is deliberately a sibling of the hook sources rather than a new file.
// Operators already read this log, and a pane option cannot hold history: it
// carries one value that the next transition overwrites, so a flap between two
// samples is unobservable there by construction.
const aiIngestCodexObserverSource = "codex-observer"

// codexObserverTransition names one observable step in an observer's life. The
// four together are what separates a flapping observer (repeated connected and
// disconnected pairs) from a frozen one (a single connected record and nothing
// after it) from a stopped one (a disconnect with no reconnect behind it).
type codexObserverTransition string

const (
	codexObserverTransitionConnected    codexObserverTransition = "observer.connected"
	codexObserverTransitionDisconnected codexObserverTransition = "observer.disconnected"
	codexObserverTransitionReconnecting codexObserverTransition = "observer.reconnecting"
	codexObserverTransitionFallback     codexObserverTransition = "observer.fallback"
)

// codexObserverTransitions is the closed set. A record writer validates
// against it for the same reason the reason column is validated.
var codexObserverTransitions = []codexObserverTransition{
	codexObserverTransitionConnected,
	codexObserverTransitionDisconnected,
	codexObserverTransitionReconnecting,
	codexObserverTransitionFallback,
}

// codexObserverJournalWindow bounds how often one (transition, reason) pair
// may occupy a line of the log.
//
// A flapping observer reconnects about once a second, and this sink is shared
// with every provider hook, so an unbounded writer would evict hook history
// within the hour. Coalescing keeps the evidence instead of dropping it: a
// suppressed transition is counted and the count rides the next record of that
// same pair, so the flap rate is still readable afterwards.
const codexObserverJournalWindow = 5 * time.Second

// codexObserverJournal is the observer's append-only history sink.
type codexObserverJournal interface {
	RecordObserverTransition(codexLifecycleIdentity, codexObserverTransition, string, codexObserverReason)
}

// codexObserverLogJournal writes observer transitions into ai-ingest.log under
// the coalescing window above.
type codexObserverLogJournal struct {
	appendEntry func(aiIngestLogEntry)
	now         func() time.Time
	window      time.Duration

	mu         sync.Mutex
	lastAt     map[string]time.Time
	suppressed map[string]int
}

func newCodexObserverLogJournal(appendEntry func(aiIngestLogEntry), now func() time.Time) *codexObserverLogJournal {
	return &codexObserverLogJournal{appendEntry: appendEntry, now: now}
}

// RecordObserverTransition appends one transition record, or folds it into the
// pending count of an identical transition already recorded inside the window.
//
// Only content-free routing identity is written: the tmux pane id, the opaque
// thread id the hook sources already carry, the epoch label, and one
// vocabulary token. No provider conversation, no rollout content, no path.
func (j *codexObserverLogJournal) RecordObserverTransition(
	identity codexLifecycleIdentity,
	kind codexObserverTransition,
	epochLabel string,
	reason codexObserverReason,
) {
	if j == nil || j.appendEntry == nil {
		return
	}
	if !codexObserverTransitionValid(kind) {
		return
	}
	if codexObserverReasonFor(string(reason)) == "" {
		// An unnamed reason is exactly what this record exists to end. Naming
		// it unrecorded keeps the transition visible without inventing a cause.
		reason = codexObserverReasonUnrecorded
	}
	repeat, emit := j.admit(kind, reason)
	if !emit {
		return
	}
	j.appendEntry(aiIngestLogEntry{
		Source:   aiIngestCodexObserverSource,
		Event:    string(kind),
		Result:   codexObserverTransitionResult(kind),
		Reason:   string(reason),
		Pane:     identity.RuntimeID,
		ThreadID: identity.ThreadID,
		Epoch:    epochLabel,
		Repeat:   repeat,
	})
}

// admit applies the coalescing window per (transition, reason) pair and
// returns how many identical transitions were folded into this one.
func (j *codexObserverLogJournal) admit(kind codexObserverTransition, reason codexObserverReason) (int, bool) {
	window := j.window
	if window <= 0 {
		window = codexObserverJournalWindow
	}
	now := time.Now().UTC()
	if j.now != nil {
		now = j.now().UTC()
	}
	key := string(kind) + "\x00" + string(reason)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lastAt == nil {
		j.lastAt, j.suppressed = map[string]time.Time{}, map[string]int{}
	}
	last, seen := j.lastAt[key]
	if seen && now.Sub(last) < window {
		j.suppressed[key]++
		return 0, false
	}
	repeat := j.suppressed[key]
	delete(j.suppressed, key)
	j.lastAt[key] = now
	return repeat, true
}

func codexObserverTransitionValid(kind codexObserverTransition) bool {
	return slices.Contains(codexObserverTransitions, kind)
}

// codexObserverTransitionResult names the authority the Pane holds after this
// transition. A disconnect followed by a fallback record is the fallback
// strategy; a disconnect with no fallback behind it is the hold strategy.
func codexObserverTransitionResult(kind codexObserverTransition) string {
	switch kind {
	case codexObserverTransitionConnected:
		return codexAuthorityControlPlane
	case codexObserverTransitionDisconnected:
		return codexAuthorityInvalidating
	case codexObserverTransitionFallback:
		return codexAuthorityHook
	default:
		return "retry"
	}
}
