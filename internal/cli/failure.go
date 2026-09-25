package cli

import (
	"errors"
	"os/exec"
)

// FailureKind names the class a command error falls into for the diagnostics
// journal. The journal owns the kind strings; this package owns the judgement.
type FailureKind int

const (
	// FailureNone is the class of a nil error.
	FailureNone FailureKind = iota
	// FailureUsage is invalid user input.
	FailureUsage
	// FailureRuntime is a failure whose reason the entrypoint prints.
	FailureRuntime
	// FailureExit is a silent coded exit: the command, or the child process
	// it passed through, already spoke for itself.
	FailureExit
)

// Failure is the one verdict on what a command error means to the process:
// whether the entrypoint prints it, which exit code the process returns, and
// which kind the journal records.
//
// Reported tells the two silent verdicts apart. It is true when the error says
// its reason is already on the user's stderr, so nobody may print it again; it
// is false for a silent coded exit, whose command decides for itself whether a
// line of its own is owed.
type Failure struct {
	Print    bool
	ExitCode int
	Kind     FailureKind
	Reported bool
}

// exitCoder lets a command request a non-default exit code while still
// returning an error.
type exitCoder interface {
	error
	ExitCode() int
}

// reportedFailure lets an error say that its reason is already on the user's
// stderr, so the entrypoint must not print it a second time. A FlagSet parse
// failure is the case: the flag package prints the reason before the usage.
type reportedFailure interface {
	error
	FailureReported() bool
}

// FlagParseError is the usage error of a failed FlagSet.Parse when the FlagSet
// writes to the user's stderr. The flag package has already printed the reason
// there, followed by the usage, so the error reports its reason as printed and
// the entrypoint does not print it again. A FlagSet with a discarded output
// returns a plain usage error instead, so the entrypoint prints the reason.
func FlagParseError(err error) error {
	return &flagParseError{cause: err}
}

type flagParseError struct{ cause error }

func (e *flagParseError) Error() string { return e.cause.Error() }

func (e *flagParseError) Unwrap() error { return e.cause }

// MetadataUsageError marks a flag parse failure as a usage error (exit 2)
// through the shared marker protocol of internal/core/metadata.
func (e *flagParseError) MetadataUsageError() bool { return true }

// FailureReported says stderr already has the reason.
func (e *flagParseError) FailureReported() bool { return true }

// FlagParseReported is the error of a failed FlagSet.Parse on a hidden
// `internal ...` route when the FlagSet writes to the user's stderr. Like
// FlagParseError it reports its reason as printed, so the entrypoint does not
// print it again, but it is not a usage error: a hidden route keeps its
// historical exit code 1, which generated tmux config, provider hooks and
// supervisors consume. A FlagSet with a discarded output returns the parse
// error unwrapped instead, so the entrypoint prints the reason.
func FlagParseReported(err error) error {
	return &flagParseReported{cause: err}
}

type flagParseReported struct{ cause error }

func (e *flagParseReported) Error() string { return e.cause.Error() }

func (e *flagParseReported) Unwrap() error { return e.cause }

// FailureReported says stderr already has the reason.
func (e *flagParseReported) FailureReported() bool { return true }

// ClassifyFailure decides the verdict for err. usage reports whether err is a
// usage error; the caller supplies it because the usage predicate lives above
// this package.
//
// An error with no exit coder in its chain is printed and exits 2 for usage, 1
// otherwise. An app-defined coder or a bare subprocess *exec.ExitError is
// expected to have already written any user-facing diagnostic (pass-through
// child output), so it stays silent. A subprocess *exec.ExitError wrapped with
// outer context is printed once, so the non-zero exit carries its reason. Any
// coder keeps its own exit code, and only a silent one is journaled as an exit.
// An error that reports its reason as already printed is never printed again;
// its exit code and kind are decided as if it were printed, and the verdict is
// marked Reported so a command that owes a silent coded exit its own line can
// tell that nothing is owed here.
func ClassifyFailure(err error, usage bool) Failure {
	if err == nil {
		return Failure{}
	}
	f := Failure{Print: true, ExitCode: 1, Kind: FailureRuntime}
	var coded exitCoder
	if errors.As(err, &coded) {
		// Only the wrapping context is new to the user; a bare ExitError's
		// child already spoke for itself.
		exitErr, ok := coded.(*exec.ExitError)
		f.Print = ok && error(exitErr) != err
		f.ExitCode = coded.ExitCode()
		if !f.Print {
			f.Kind = FailureExit
		}
	} else if usage {
		f.ExitCode = 2
	}
	if usage {
		f.Kind = FailureUsage
	}
	var reported reportedFailure
	if errors.As(err, &reported) && reported.FailureReported() {
		f.Print = false
		f.Reported = true
	}
	return f
}
