package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/version"
)

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

	failure := cli.ClassifyFailure(err, app.IsUsageError(err))
	if failure.Print {
		fmt.Fprintln(stderr, err)
	}
	return failure.ExitCode
}
