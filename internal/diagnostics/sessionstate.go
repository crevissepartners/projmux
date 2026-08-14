package diagnostics

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	OperationSessionStateSave     Operation = "session-state.save"
	OperationSessionStateAutosave Operation = "session-state.autosave"
	OperationSessionStateRestore  Operation = "session-state.restore"
	OperationSessionStateDelete   Operation = "session-state.delete"

	CodeSessionStateSaveFailed     Code = "session-state.save.failed"
	CodeSessionStateAutosaveFailed Code = "session-state.autosave.failed"
	CodeSessionStateRestoreFailed  Code = "session-state.restore.failed"
	CodeSessionStateDeleteFailed   Code = "session-state.delete.failed"
)

type SessionStateSource string

const (
	SessionStateSourceManual         SessionStateSource = "manual"
	SessionStateSourceSettingsLatest SessionStateSource = "settings-latest"
	SessionStateSourceSettingsNamed  SessionStateSource = "settings-named"
	SessionStateSourceAutosave       SessionStateSource = "autosave"
	SessionStateSourceStartupLatest  SessionStateSource = "startup-latest"
	SessionStateSourceStartupNamed   SessionStateSource = "startup-named"
	SessionStateSourcePrune          SessionStateSource = "prune"
)

// SessionStateCounts is the closed aggregate projection for a successful
// snapshot mutation. It deliberately cannot carry paths, commands, IDs, or
// arbitrary metadata.
type SessionStateCounts struct {
	WindowCount        int
	PaneCount          int
	ShellRecipeCount   int
	AgentRecipeCount   int
	StartupRecipeCount int
	ItemCount          int
}

// SessionStateRecorder writes one terminal outcome per selected mutation.
// Attempts are synchronous, but the mutex also keeps concurrent interactive
// actions from interleaving ownership and append bookkeeping.
type SessionStateRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time
	outcomes   *atomic.Uint64
	mu         sync.Mutex
}

func (r *SessionStateRecorder) Record(operation Operation, source SessionStateSource, started time.Time, counts SessionStateCounts, operationErr error) {
	if r == nil || operation == OperationSessionStateAutosave && operationErr == nil {
		return
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event := Event{
		At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "session-state",
		Event: "session-state.outcome", Result: "success", DurationMS: max(now.Sub(started).Milliseconds(), 0),
		RunID: r.runID, Version: r.version, MuxBackend: r.muxBackend,
		Operation: string(operation), Source: string(source),
	}
	if operationErr != nil {
		event.Level, event.Result, event.Kind = "error", "error", "runtime"
		event.Code = string(failureCode(operation))
	} else if operation == OperationSessionStateDelete {
		event.ItemCount = intPointer(counts.ItemCount)
	} else {
		event.WindowCount = intPointer(counts.WindowCount)
		event.PaneCount = intPointer(counts.PaneCount)
		event.ShellRecipeCount = intPointer(counts.ShellRecipeCount)
		event.AgentRecipeCount = intPointer(counts.AgentRecipeCount)
		event.StartupRecipeCount = intPointer(counts.StartupRecipeCount)
	}
	r.mu.Lock()
	if r.outcomes != nil {
		// Ownership is logical and precedes the best-effort append.
		r.outcomes.Add(1)
	}
	if r.writer != nil {
		_ = r.writer.Append(event)
	}
	r.mu.Unlock()
}

func intPointer(value int) *int { return &value }

func sessionStateOperation(operation Operation) bool {
	switch operation {
	case OperationSessionStateSave, OperationSessionStateAutosave, OperationSessionStateRestore, OperationSessionStateDelete:
		return true
	default:
		return false
	}
}
