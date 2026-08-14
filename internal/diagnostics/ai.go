package diagnostics

import (
	"sync"
	"sync/atomic"
	"time"
)

// AIKind is the closed, content-free classification of watcher and hook
// ingress. Provider event names are projected here; raw names are never stored.
type AIKind string

const (
	AIKindWatcher      AIKind = "watcher"
	AIKindPayload      AIKind = "payload"
	AIKindPrompt       AIKind = "prompt"
	AIKindPermission   AIKind = "permission"
	AIKindStop         AIKind = "stop"
	AIKindNotification AIKind = "notification"
	AIKindTool         AIKind = "tool"
	AIKindSession      AIKind = "session"
	AIKindCompact      AIKind = "compact"
	AIKindSubagent     AIKind = "subagent"
	AIKindTeammate     AIKind = "teammate"
	AIKindStatusline   AIKind = "statusline"
	AIKindInvocation   AIKind = "invocation"
	AIKindLifecycle    AIKind = "lifecycle"
	AIKindBell         AIKind = "bell"
	AIKindUnknown      AIKind = "unknown"
)

// AIResult is the closed watcher/ingest terminal classification.
type AIResult string

const (
	AIResultStarted    AIResult = "started"
	AIResultPaneGone   AIResult = "pane-gone"
	AIResultHookActive AIResult = "hook-active"
	AIResultIgnored    AIResult = "ignored"
	AIResultFailed     AIResult = "failed"
)

// AIFailure identifies only the safe stage that failed. It never contains an
// error string, hook payload, target, path, or provider identifier.
type AIFailure string

const (
	AIFailurePayloadInvalid   AIFailure = "payload-invalid"
	AIFailurePayloadRead      AIFailure = "payload-read"
	AIFailurePayloadOversized AIFailure = "payload-oversized"
	AIFailureTargetInvalid    AIFailure = "target-invalid"
	AIFailureTargetUnmatched  AIFailure = "target-unmatched"
	AIFailureUnsupportedEvent AIFailure = "unsupported-event"
	AIFailureRoute            AIFailure = "route-failed"
	AIFailureWatcherLaunch    AIFailure = "watcher-launch-failed"
	AIFailureWatcherState     AIFailure = "watcher-state-failed"
)

const ProviderTmuxBell Provider = "tmux-bell"

type aiEventKey struct {
	name     string
	provider Provider
	kind     AIKind
	result   AIResult
	failure  AIFailure
}

// AIRecorder emits only watcher lifecycle transitions and anomalous hook
// outcomes. Identical safe tuples are coalesced per process run. Successful
// state/notify/quiet/dedupe hook traffic remains zero-volume here because its
// mutation/delivery owners already live in the notify and focus surfaces.
type AIRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time
	outcomes   *atomic.Uint64

	mu   sync.Mutex
	seen map[aiEventKey]bool
}

func (r *AIRecorder) RecordWatcher(result AIResult, failure AIFailure, started time.Time, ownsTopLevel bool) {
	r.record("ai.watcher.transition", ProviderAI, AIKindWatcher, result, failure, started, ownsTopLevel)
}

func (r *AIRecorder) RecordIngest(provider Provider, kind AIKind, result AIResult, failure AIFailure, started time.Time, ownsTopLevel bool) {
	r.record("ai.ingest.outcome", provider, kind, result, failure, started, ownsTopLevel)
}

func (r *AIRecorder) record(name string, provider Provider, kind AIKind, result AIResult, failure AIFailure, started time.Time, ownsTopLevel bool) {
	if r == nil {
		return
	}
	key := aiEventKey{name: name, provider: provider, kind: kind, result: result, failure: failure}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = make(map[aiEventKey]bool)
	}
	if owned, ok := r.seen[key]; ok {
		if ownsTopLevel && !owned && r.outcomes != nil {
			r.outcomes.Add(1)
			r.seen[key] = true
		}
		return
	}
	r.seen[key] = ownsTopLevel
	if ownsTopLevel && r.outcomes != nil {
		// Logical ownership precedes best-effort append so storage failure can
		// never resurrect a generic top-level duplicate.
		r.outcomes.Add(1)
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event := Event{
		At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "ai", Event: name,
		Result: "success", DurationMS: max(now.Sub(started).Milliseconds(), 0), RunID: r.runID,
		Version: r.version, MuxBackend: r.muxBackend, Provider: string(provider), AIKind: string(kind),
		AIResult: string(result), Failure: string(failure),
	}
	if result == AIResultStarted {
		event.Result = "started"
	}
	if result == AIResultFailed {
		event.Level, event.Result, event.Kind = "error", "error", "runtime"
	}
	if r.writer != nil {
		_ = r.writer.Append(event)
	}
}

func aiTupleMatches(event Event) bool {
	result, failure := AIResult(event.AIResult), AIFailure(event.Failure)
	switch event.Event {
	case "ai.watcher.transition":
		if event.Provider != string(ProviderAI) || event.AIKind != string(AIKindWatcher) {
			return false
		}
		switch result {
		case AIResultStarted:
			return event.Level == "info" && event.Result == "started" && event.Kind == "" && failure == ""
		case AIResultPaneGone, AIResultHookActive:
			return event.Level == "info" && event.Result == "success" && event.Kind == "" && failure == ""
		case AIResultFailed:
			return event.Level == "error" && event.Result == "error" && event.Kind == "runtime" && (failure == AIFailureWatcherLaunch || failure == AIFailureWatcherState)
		}
	case "ai.ingest.outcome":
		if event.Provider == string(ProviderAI) || event.AIKind == string(AIKindWatcher) {
			return false
		}
		if result == AIResultIgnored {
			if event.Level != "info" || event.Result != "success" || event.Kind != "" {
				return false
			}
			switch failure {
			case AIFailureTargetInvalid:
				return event.Provider == string(ProviderTmuxBell) && event.AIKind == string(AIKindBell)
			case AIFailureTargetUnmatched:
				return true
			case AIFailureUnsupportedEvent:
				return event.AIKind == string(AIKindUnknown)
			}
		}
		if result == AIResultFailed {
			switch failure {
			case AIFailurePayloadInvalid, AIFailurePayloadRead, AIFailurePayloadOversized:
				return event.AIKind == string(AIKindPayload) && event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
			case AIFailureRoute:
				return event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
			}
		}
	}
	return false
}

// AI returns the Phase 5 recorder bound to the same run and logical outcome
// ownership counter as lifecycle, Session State, notify, and focus events.
func (r *LifecycleRecorder) AI() *AIRecorder {
	if r == nil {
		return nil
	}
	return &AIRecorder{
		writer: r.writer, runID: r.runID, version: r.version,
		muxBackend: r.muxBackend, now: time.Now, outcomes: &r.outcomes,
	}
}
