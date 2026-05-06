package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/app"
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
	err := app.Run(os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}

	var coded exitCoder
	if errors.As(err, &coded) {
		os.Exit(coded.ExitCode())
	}

	fmt.Fprintln(os.Stderr, err)
	if app.IsUsageError(err) {
		os.Exit(2)
	}
	os.Exit(1)
}
