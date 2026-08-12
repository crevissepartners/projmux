package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/version"
)

// exitCoder lets specific commands request a non-default exit code while
// still flowing through the app.Run error channel. The command is expected to
// have already written any user-facing diagnostic, so main suppresses the
// default stderr print for these.
type exitCoder interface {
	error
	ExitCode() int
}

func main() {
	started := time.Now()
	runID := diagnostics.NewRunID()
	code := executeCLI(
		func() error { return app.Run(os.Args[1:], os.Stdout, os.Stderr) },
		func(err error) {
			if path, pathErr := diagnostics.DefaultPath(os.Getenv, os.UserHomeDir); pathErr == nil {
				_ = diagnostics.RecordOutcome(diagnostics.NewStore(path), os.Args[1:], runID, version.String(), diagnostics.MuxBackend(os.Getenv, ""), started, err, app.IsUsageError(err))
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
		return coded.ExitCode()
	}

	fmt.Fprintln(stderr, err)
	if app.IsUsageError(err) {
		return 2
	}
	return 1
}
