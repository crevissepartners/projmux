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
type Failure struct {
	Print    bool
	ExitCode int
	Kind     FailureKind
}

// exitCoder lets a command request a non-default exit code while still
// returning an error.
type exitCoder interface {
	error
	ExitCode() int
}

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
	return f
}
