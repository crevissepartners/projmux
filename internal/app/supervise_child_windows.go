//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// runSupervisedChild runs the child with this process's stdio on Windows,
// which has no POSIX process groups.
//
// There is no terminal foreground group to hand over here, so the parity story
// is narrower: argv, cwd, environment, and the inherited handles are exact, and
// the exit status is still observed and reported. tmux's own pane bookkeeping
// on Windows never depended on the process-group behavior the POSIX build
// preserves.
func runSupervisedChild(argv []string, argv0 string) (processOutcome, error) {
	if len(argv) == 0 {
		return processOutcome{}, errors.New("no command to supervise")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if argv0 != "" {
		cmd.Args[0] = argv0
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return processOutcome{}, err
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return processOutcome{}, waitErr
		}
	}
	if cmd.ProcessState == nil {
		return processOutcome{}, fmt.Errorf("supervised process %s produced no wait status", argv[0])
	}
	return processOutcome{ExitCode: cmd.ProcessState.ExitCode()}, nil
}
