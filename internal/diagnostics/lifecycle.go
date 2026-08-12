package diagnostics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Operation is the closed set of state-changing runtime lifecycles.
type Operation string

const (
	OperationSessionCreate Operation = "session.create"
	OperationSessionAttach Operation = "session.attach"
	OperationSessionSwitch Operation = "session.switch"
	OperationSessionKill   Operation = "session.kill"
	OperationTmuxApply     Operation = "tmux.apply"
)

// Code is the closed, content-free runtime outcome classification consumed by
// read-only diagnostics. It must never contain a tmux target, path, argv, or
// generated configuration value.
type Code string

const (
	CodeSessionCreateFailed        Code = "session.create.failed"
	CodeSessionAttachFailed        Code = "session.attach.failed"
	CodeSessionSwitchFailed        Code = "session.switch.failed"
	CodeSessionKillFailed          Code = "session.kill.failed"
	CodeTmuxApplyFailed            Code = "tmux.apply.failed"
	CodeTmuxApplySocketUnreachable Code = "tmux.apply.socket-unreachable"
	CodeTmuxApplyReloadFailed      Code = "tmux.apply.reload-failed"
	CodeTmuxApplyReloadSkipped     Code = "tmux.apply.reload-skipped"
)

// LifecycleResult selects the terminal state for one started lifecycle.
type LifecycleResult string

const (
	LifecycleSuccess LifecycleResult = "success"
	LifecycleError   LifecycleResult = "error"
)

// EventWriter is the narrow append seam used for best-effort lifecycle
// records. Store implements it; tests can inject a failing writer.
type EventWriter interface {
	Append(Event) error
}

type commandLifecycle struct {
	operation Operation
	started   time.Time
	result    LifecycleResult
	code      Code
	finished  bool
}

// LifecycleRecorder coalesces all nested tmux steps from one explicit CLI
// invocation into one start and one terminal outcome. The first mutating step
// selects the safe operation. A create that is immediately followed by initial
// attach/switch remains one outer create lifecycle: activation is part of that
// provisioning attempt, so an activation error terminates it as create.failed
// instead of emitting a second lifecycle outcome.
// Appends are best-effort and never flow back into command control paths.
type LifecycleRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time

	mu       sync.Mutex
	command  *commandLifecycle
	outcomes atomic.Uint64
}

// NewLifecycleRecorder binds one process run ID to the existing event store.
func NewLifecycleRecorder(writer EventWriter, runID, version, muxBackend string) *LifecycleRecorder {
	return &LifecycleRecorder{writer: writer, runID: runID, version: version, muxBackend: muxBackend, now: time.Now}
}

// BeginCommand starts an in-memory command scope. It deliberately does not
// write until Mark observes the first real lifecycle mutation.
func (r *LifecycleRecorder) BeginCommand() func(error) {
	if r == nil {
		return func(error) {}
	}
	r.mu.Lock()
	scope := &commandLifecycle{}
	r.command = scope
	r.mu.Unlock()

	var once sync.Once
	return func(commandErr error) {
		once.Do(func() { r.finishCommand(scope, commandErr) })
	}
}

// Mark selects the first lifecycle operation in the active explicit command.
// Later nested operations are coalesced into the same outer outcome.
func (r *LifecycleRecorder) Mark(operation Operation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	scope := r.command
	if scope == nil || scope.finished || scope.operation != "" {
		r.mu.Unlock()
		return
	}
	scope.operation = operation
	scope.started = r.now()
	event := r.event(scope.started, "info", "lifecycle.start", "started", scope, "", "")
	r.append(event)
	r.mu.Unlock()
}

// Hint records a safe terminal classification discovered inside a command
// whose historical CLI return semantics intentionally remain successful.
func (r *LifecycleRecorder) Hint(result LifecycleResult, code Code) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if scope := r.command; scope != nil && !scope.finished && scope.operation != "" {
		scope.result = result
		scope.code = code
	}
}

func (r *LifecycleRecorder) finishCommand(scope *commandLifecycle, commandErr error) {
	r.mu.Lock()
	if scope == nil || scope.finished || scope.operation == "" {
		if r.command == scope {
			r.command = nil
		}
		r.mu.Unlock()
		return
	}
	scope.finished = true
	result, code := scope.result, scope.code
	if commandErr != nil {
		result = LifecycleError
		if code == "" {
			code = failureCode(scope.operation)
		}
	}
	if result == "" {
		result = LifecycleSuccess
	}
	level, kind := "info", ""
	if result == LifecycleError {
		level, kind = "error", "runtime"
		if code == "" {
			code = failureCode(scope.operation)
		}
	}
	now := r.now()
	event := r.event(now, level, "lifecycle.outcome", string(result), scope, code, kind)
	if r.command == scope {
		r.command = nil
	}
	r.append(event)
	// Logical ownership, not storage success, suppresses the duplicate top-level
	// outcome. A failing journal must not change the original command result.
	r.outcomes.Add(1)
	r.mu.Unlock()
}

func (r *LifecycleRecorder) event(at time.Time, level, name, result string, scope *commandLifecycle, code Code, kind string) Event {
	return Event{
		At:         at.UTC().Format(time.RFC3339Nano),
		Level:      level,
		Component:  "runtime",
		Event:      name,
		Result:     result,
		DurationMS: max(at.Sub(scope.started).Milliseconds(), 0),
		RunID:      r.runID,
		Version:    r.version,
		MuxBackend: r.muxBackend,
		Kind:       kind,
		Operation:  string(scope.operation),
		Code:       string(code),
	}
}

func failureCode(operation Operation) Code {
	switch operation {
	case OperationSessionCreate:
		return CodeSessionCreateFailed
	case OperationSessionAttach:
		return CodeSessionAttachFailed
	case OperationSessionSwitch:
		return CodeSessionSwitchFailed
	case OperationSessionKill:
		return CodeSessionKillFailed
	case OperationTmuxApply:
		return CodeTmuxApplyFailed
	default:
		return ""
	}
}

func (r *LifecycleRecorder) append(event Event) {
	if r != nil && r.writer != nil {
		_ = r.writer.Append(event)
	}
}

// RecordedOutcome reports logical lifecycle ownership even when storage
// failed, so the CLI boundary never attempts a duplicate fallback outcome.
func (r *LifecycleRecorder) RecordedOutcome() bool {
	return r != nil && r.outcomes.Load() > 0
}
