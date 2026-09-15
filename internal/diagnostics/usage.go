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
	UsageFailureRowsSkipped           UsageFailure = "rows-skipped"
	UsageFailureAppServerUnavailable  UsageFailure = "app-server-unavailable"
	UsageFailureAppServerUnsupported  UsageFailure = "app-server-unsupported"
	UsageFailureAccountUnsupported    UsageFailure = "account-unsupported"
	UsageFailureAppServerTimeout      UsageFailure = "app-server-timeout"
	UsageFailureAppServerProtocol     UsageFailure = "app-server-protocol-error"
	UsageFailureAppServerDisconnected UsageFailure = "app-server-disconnected"

	// Closed classes of a whole Claude collection failure. Each one replaces
	// collect-failed for Claude when the adapter named why nothing refreshed;
	// a Claude failure without a class still records collect-failed.
	//
	// UsageFailureCredentialsUnavailable covers an unresolved credentials
	// path and a credentials file that is missing, unreadable, or unparseable.
	UsageFailureCredentialsUnavailable UsageFailure = "credentials-unavailable"
	// UsageFailureCredentialsTokenEmpty is a credentials file without an
	// access token.
	UsageFailureCredentialsTokenEmpty UsageFailure = "credentials-token-empty"
	// UsageFailureAuthRejected is a 401 the stored refresh token could not
	// recover from, including a 401 for the refreshed token.
	UsageFailureAuthRejected UsageFailure = "auth-rejected"
	// UsageFailureRateLimited is a 429 that started or extended a backoff.
	UsageFailureRateLimited UsageFailure = "rate-limited"
	// UsageFailureHTTPStatus is any other non-200 usage response.
	UsageFailureHTTPStatus UsageFailure = "http-status"
	// UsageFailureNetwork is a usage request that could not be built or sent,
	// or whose body could not be read.
	UsageFailureNetwork UsageFailure = "network-error"
	// UsageFailureResponseInvalid is a 200 usage response that did not parse.
	UsageFailureResponseInvalid UsageFailure = "response-invalid"
)

// usageWholeCollectFailure reports whether failure means the adapter refreshed
// nothing at all: the generic collect-failed or one of its closed classes.
func usageWholeCollectFailure(failure UsageFailure) bool {
	switch failure {
	case UsageFailureCollect, UsageFailureCredentialsUnavailable, UsageFailureCredentialsTokenEmpty,
		UsageFailureAuthRejected, UsageFailureRateLimited, UsageFailureHTTPStatus,
		UsageFailureNetwork, UsageFailureResponseInvalid:
		return true
	}
	return false
}

type UsageSource string

const (
	UsageSourceAppServer     UsageSource = "app-server"
	UsageSourceRollout       UsageSource = "rollout"
	UsageSourceLastKnownGood UsageSource = "last-known-good"
)

type usageEventKey struct {
	provider Provider
	source   UsageSource
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
	r.RecordCollectOutcome(provider, "", failure, started)
}

// RecordCollectOutcome records a source-aware usage anomaly. Healthy native
// collections remain silent; callers use this for row skips, rollout fallback,
// and last-known-good retention only.
func (r *UsageRecorder) RecordCollectOutcome(provider Provider, source UsageSource, failure UsageFailure, started time.Time) {
	if r == nil {
		return
	}
	key := usageEventKey{provider: provider, source: source, failure: failure}
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
		MuxBackend: r.muxBackend, Provider: string(provider), Source: string(source), Failure: string(failure),
	}
	if usageWholeCollectFailure(failure) || source == UsageSourceLastKnownGood {
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
	case UsageFailureCredentialsUnavailable, UsageFailureCredentialsTokenEmpty,
		UsageFailureAuthRejected, UsageFailureRateLimited, UsageFailureHTTPStatus,
		UsageFailureNetwork, UsageFailureResponseInvalid:
		// A classified whole collection failure is the same runtime error as
		// collect-failed. It only exists on the sourceless path: retained or
		// fallback data records its own closed reason instead.
		return event.Source == "" && event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
	case UsageFailureRowsSkipped:
		// A partial collection still refreshed the healthy rows, so it stays
		// an informational anomaly rather than a command-level error.
		return event.Level == "info" && event.Result == "success" && event.Kind == ""
	case UsageFailureAppServerUnavailable, UsageFailureAppServerUnsupported,
		UsageFailureAccountUnsupported, UsageFailureAppServerTimeout,
		UsageFailureAppServerProtocol, UsageFailureAppServerDisconnected:
		switch UsageSource(event.Source) {
		case UsageSourceRollout:
			return event.Level == "info" && event.Result == "success" && event.Kind == ""
		case UsageSourceLastKnownGood:
			return event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
		}
	}
	return false
}
