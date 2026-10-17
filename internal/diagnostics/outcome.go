package diagnostics

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
)

// RecordOutcome appends the single top-level outcome selected by policy.
// Storage is deliberately best-effort: callers must ignore its return value.
func RecordOutcome(store *Store, args []string, runID, version, muxBackend string, started time.Time, commandErr error, usageError, lifecycleRecorded bool, reasons ...*CommandOutcomeReasons) error {
	if lifecycleRecorded {
		return nil
	}
	if JournalForbidden(args) {
		return nil
	}
	class := Classify(args)
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
		// The kind follows the entrypoint's own verdict, so a failure the
		// entrypoint prints is never journaled as a silent coded exit.
		switch cli.ClassifyFailure(commandErr, usageError).Kind {
		case cli.FailureUsage:
			event.Kind = "usage"
			event.Message = "invalid command usage"
		case cli.FailureExit:
			event.Kind = "exit"
			event.Message = "command completed with a non-success status"
		default:
			event.Kind = "runtime"
			event.Message = "command failed"
			var registry *CommandOutcomeReasons
			if len(reasons) > 0 {
				registry = reasons[0]
			}
			if reason, ok := outcomeReason(class, commandErr, registry); ok {
				event.Message += ": " + string(reason)
			}
		}
	}
	return store.Append(event)
}

// OutcomeReason is a content-free token declared by a command error.
// Only tokens explicitly registered for the classified command can be recorded.
type OutcomeReason string

type outcomeClassKey struct{ command, subcommand string }

// CommandOutcomeReasons holds caller-owned closed reason sets per command class.
// The zero value is empty and ready for concurrent registration and lookup.
// Register copies each set; a class can be registered only once.
type CommandOutcomeReasons struct {
	mu      sync.RWMutex
	classes map[outcomeClassKey]map[OutcomeReason]struct{}
}

var outcomeToken = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

// Register declares a complete set of content-free reasons for a class.
// StateChanging does not participate in class identity. Invalid or conflicting
// registrations fail without changing any existing set. There are no defaults.
func (r *CommandOutcomeReasons) Register(class CommandClass, reasons []OutcomeReason) error {
	validToken := func(token string) bool { return len(token) <= 96 && outcomeToken.MatchString(token) }
	if r == nil || !validToken(class.Command) || (class.Subcommand != "" && !validToken(class.Subcommand)) || len(reasons) == 0 {
		return fmt.Errorf("invalid command outcome reason registration")
	}
	allowed := make(map[OutcomeReason]struct{}, len(reasons))
	for _, reason := range reasons {
		if !validToken(string(reason)) {
			return fmt.Errorf("invalid command outcome reason token")
		}
		if _, exists := allowed[reason]; exists {
			return fmt.Errorf("duplicate command outcome reason token")
		}
		allowed[reason] = struct{}{}
	}
	key := outcomeClassKey{class.Command, class.Subcommand}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.classes[key]; exists {
		return fmt.Errorf("command outcome reasons already registered")
	}
	if r.classes == nil {
		r.classes = make(map[outcomeClassKey]map[OutcomeReason]struct{})
	}
	r.classes[key] = allowed
	return nil
}

type outcomeReasoner interface {
	error
	OutcomeReason() OutcomeReason
}

func outcomeReason(class CommandClass, err error, registry *CommandOutcomeReasons) (OutcomeReason, bool) {
	if registry == nil {
		return "", false
	}
	var reasoner outcomeReasoner
	if !errors.As(err, &reasoner) {
		return "", false
	}
	reason := reasoner.OutcomeReason()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, ok := registry.classes[outcomeClassKey{class.Command, class.Subcommand}][reason]
	if !ok {
		return "", false
	}
	return reason, true
}

// JournalForbidden reports an invocation that must never append to the
// operational journal, whatever it does or however it ends. It owns every such
// boundary, so the top-level outcome and every in-process recorder installed
// for the whole invocation (the Registry lock observer) honor the same set:
//
//   - Rejected mixed-root compatibility argv and fully removed roots are a
//     strict zero-side-effect boundary. In particular, reporting their expected
//     exit-2/exit-1 result must not create the diagnostics journal they were
//     forbidden to touch during dispatch.
//   - Doctor and the support report own a strict no-write contract, including
//     invalid flag invocations. Their read-only work never becomes a journal
//     write.
//   - Global guidance read/validate are strict no-write editing operations,
//     including candidate, filesystem and usage failures.
func JournalForbidden(args []string) bool {
	if len(args) >= 3 && args[0] == "agent" && args[1] == "guidance" && (args[2] == "read" || args[2] == "validate") {
		return true
	}
	if retiredCLINoWrite(args) {
		return true
	}
	class := Classify(args)
	return class.Command == "doctor" || (class.Command == "diagnostics" && class.Subcommand == "report")
}

func retiredCLINoWrite(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "ai":
		return true
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
		return len(args) < 2 || args[1] != "project"
	default:
		return false
	}
}
