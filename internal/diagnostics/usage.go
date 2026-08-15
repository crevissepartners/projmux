package diagnostics

import (
	"sync"
	"time"
)

// UsageFailure is the closed, content-free classification of one AI-usage
// collection failure. It is a stage enum, never an error string, an upstream
// percentage, a reset timestamp, a quota bucket identity, or a credential.
type UsageFailure string

const (
	// UsageFailureCollect is a whole-adapter failure: no rows were produced,
	// so the user is looking at last-known-good values (or nothing).
	UsageFailureCollect UsageFailure = "collect-failed"
	// UsageFailureRowsSkipped is a partial failure: the adapter produced
	// usable rows but dropped the ones that failed field validation.
	UsageFailureRowsSkipped UsageFailure = "rows-skipped"
)

type usageEventKey struct {
	provider Provider
	failure  UsageFailure
}

// UsageRecorder appends AI-usage collection failures to the operations
// journal. A successful collection writes nothing at all: the journal records
// anomalies, not the healthy steady state.
//
// Identical (provider, failure) tuples are emitted at most once per run — the
// same suppression NotifyFocusRecorder applies — so a status-line refresh loop
// or a repeated `usage` invocation inside one process cannot fill the bounded
// journal with records that carry no additional diagnostic information.
//
// Appends are best-effort and have no control-flow effect: a failing journal
// write must not change the result of the command that was being observed.
type UsageRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time

	mu   sync.Mutex
	seen map[usageEventKey]bool
}

// NewUsageRecorder binds one process run ID to an event store. Usage
// collection runs from command surfaces that are not part of the runtime
// lifecycle graph, so this recorder is constructed directly rather than
// derived from LifecycleRecorder.
func NewUsageRecorder(writer EventWriter, runID, version, muxBackend string) *UsageRecorder {
	return &UsageRecorder{writer: writer, runID: runID, version: version, muxBackend: muxBackend, now: time.Now}
}

// RecordCollectFailure appends one usage-collection failure for provider.
// Unknown adapter names must be projected to ProviderOther by the caller.
func (r *UsageRecorder) RecordCollectFailure(provider Provider, failure UsageFailure, started time.Time) {
	if r == nil {
		return
	}
	key := usageEventKey{provider: provider, failure: failure}
	r.mu.Lock()
	if r.seen == nil {
		r.seen = make(map[usageEventKey]bool)
	}
	if r.seen[key] {
		r.mu.Unlock()
		return
	}
	r.seen[key] = true
	r.mu.Unlock()

	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event := Event{
		At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "usage", Event: "usage.collect.outcome",
		Result: "success", DurationMS: max(now.Sub(started).Milliseconds(), 0), RunID: r.runID, Version: r.version,
		MuxBackend: r.muxBackend, Provider: string(provider), Failure: string(failure),
	}
	if failure == UsageFailureCollect {
		event.Level, event.Result, event.Kind = "error", "error", "runtime"
	}
	if r.writer != nil {
		_ = r.writer.Append(event)
	}
}

func usageTupleMatches(event Event) bool {
	switch UsageFailure(event.Failure) {
	case UsageFailureCollect:
		// A dropped collection is a runtime error: nothing refreshed.
		return event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
	case UsageFailureRowsSkipped:
		// A partial collection still refreshed the healthy rows, so it stays
		// an informational anomaly rather than a command-level error.
		return event.Level == "info" && event.Result == "success" && event.Kind == ""
	default:
		return false
	}
}
