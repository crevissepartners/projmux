package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/version"
)

// exitCoder lets specific commands request a non-default exit code while
// still flowing through the app.Run error channel. A command that returns an
// app-defined coder or a bare subprocess *exec.ExitError is expected to have
// already written any user-facing diagnostic (pass-through child output), so
// main suppresses the default stderr print for these. A subprocess
// *exec.ExitError wrapped with outer context is printed once, so the non-zero
// exit carries its reason, and keeps the ExitError's code.
type exitCoder interface {
	error
	ExitCode() int
}

func main() {
	started := time.Now()
	runID := diagnostics.NewRunID()
	var store *diagnostics.Store
	if path, err := diagnostics.DefaultPath(os.Getenv, os.UserHomeDir); err == nil {
		store = diagnostics.NewStore(path)
	}
	var lifecycle *diagnostics.LifecycleRecorder
	if store != nil {
		lifecycle = diagnostics.NewLifecycleRecorder(store, runID, version.String(), diagnostics.MuxBackend())
	}
	code := executeCLI(
		func() error { return app.RunWithLifecycleDiagnostics(os.Args[1:], os.Stdout, os.Stderr, lifecycle) },
		func(err error) {
			if store != nil {
				_ = diagnostics.RecordOutcome(store, os.Args[1:], runID, version.String(), diagnostics.MuxBackend(), started, err, app.IsUsageError(err), lifecycle.RecordedOutcome())
			}
		},
		os.Stderr,
	)
	if code != 0 {
		os.Exit(code)
	}
}

func executeCLI(invoke func() error, record func(error), stderr io.Writer) int {
	err := invoke()
	record(err)
	if err == nil {
		return 0
	}

	var coded exitCoder
	if errors.As(err, &coded) {
		// Only the wrapping context is new to the user; a bare ExitError's
		// child already spoke for itself.
		if exitErr, ok := coded.(*exec.ExitError); ok && error(exitErr) != err {
			fmt.Fprintln(stderr, err)
		}
		return coded.ExitCode()
	}

	fmt.Fprintln(stderr, err)
	if app.IsUsageError(err) {
		return 2
	}
	return 1
}
