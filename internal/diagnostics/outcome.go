package diagnostics

import (
	"errors"
	"time"
)

type exitCoder interface {
	error
	ExitCode() int
}

// RecordOutcome appends the single top-level outcome selected by policy.
// Storage is deliberately best-effort: callers must ignore its return value.
func RecordOutcome(store *Store, args []string, runID, version, muxBackend string, started time.Time, commandErr error, usageError, lifecycleRecorded bool) error {
	if lifecycleRecorded {
		return nil
	}
	class := Classify(args)
	// Doctor owns a strict no-write contract, including invalid flag
	// invocations. Do not turn its read-only result into a journal write.
	if class.Command == "doctor" || (class.Command == "diagnostics" && class.Subcommand == "report") {
		return nil
	}
	if commandErr == nil && !class.StateChanging {
		return nil
	}
	event := Event{
		At:         time.Now().UTC().Format(time.RFC3339Nano),
		Level:      "info",
		Component:  "cli",
		Event:      "command.outcome",
		Result:     "success",
		DurationMS: max(time.Since(started).Milliseconds(), 0),
		RunID:      runID,
		Version:    version,
		MuxBackend: muxBackend,
		Command:    class.Command,
		Subcommand: class.Subcommand,
	}
	if commandErr != nil {
		event.Level = "error"
		event.Result = "error"
		event.Kind = "runtime"
		if usageError {
			event.Kind = "usage"
			event.Message = "invalid command usage"
		} else {
			var coded exitCoder
			if errors.As(commandErr, &coded) {
				event.Kind = "exit"
				event.Message = "command completed with a non-success status"
			} else {
				event.Message = "command failed"
			}
		}
	}
	return store.Append(event)
}
