package diagnostics

import (
	"errors"
	"os"
	"strings"
	"time"
)

type exitCoder interface {
	error
	ExitCode() int
}

// RecordOutcome appends the single top-level outcome selected by policy.
// Storage is deliberately best-effort: callers must ignore its return value.
func RecordOutcome(store *Store, args []string, runID, version, muxBackend string, started time.Time, commandErr error, usageError bool) error {
	class := Classify(args)
	if commandErr == nil && !class.StateChanging {
		return nil
	}
	home, _ := os.UserHomeDir()
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
		} else {
			var coded exitCoder
			if errors.As(commandErr, &coded) {
				event.Kind = "exit"
			}
		}
		event.Message = sanitizeOutcomeMessage(commandErr.Error(), args, os.Environ(), home)
	}
	return store.Append(event)
}

func sanitizeOutcomeMessage(message string, args, environ []string, home string) string {
	message = SanitizeMessage(message, home)
	class := Classify(args)
	for index, arg := range args {
		if arg == "" || (index == 0 && arg == class.Command) || (index == 1 && arg == class.Subcommand) {
			continue
		}
		message = redactLiteral(message, arg)
		if strings.HasPrefix(arg, "-") {
			message = redactLiteral(message, "-"+strings.TrimLeft(arg, "-"))
		}
	}
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "HOME" || value == "" {
			continue
		}
		message = redactLiteral(message, value)
	}
	return SanitizeMessage(message, "")
}

func redactLiteral(message, value string) string {
	if value == "" {
		return message
	}
	return strings.ReplaceAll(message, value, "<redacted>")
}
