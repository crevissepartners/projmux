package diagnostics

import (
	"errors"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
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
	// Phase 2 retirement tombstones and removed pre-namespace aliases are a
	// strict zero-side-effect boundary. In particular, reporting their expected
	// exit-2/exit-1 result must not create the diagnostics journal they were
	// forbidden to touch during dispatch.
	if retiredCLINoWrite(args) {
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

func retiredCLINoWrite(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "ai":
		return !cli.IsLegacyAIProducerArgv(args[1:])
	case "current", "kill", "notify", "sessions", "session-state", "tag", "upgrade", "usage",
		"key-broker", "popup-wait-key", "preview", "session-popup", "status", "statusbar", "tmux":
		return true
	case "attach":
		return len(args) < 2 || args[1] != "project"
	case "focus":
		return len(args) < 2 || (args[1] != "project" && args[1] != "window" && args[1] != "pane")
	case "pin":
		return len(args) < 2 || args[1] != "project"
	case "prune":
		return len(args) < 2 || (args[1] != "project" && args[1] != "snapshot")
	default:
		return false
	}
}
