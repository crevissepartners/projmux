//go:build windows

package app

import (
	"errors"
	"os"
	"os/exec"
)

func markActivationFailureCloseOnExec(int) {}

func execCommittedActivation(argv []string, argv0 string, spec superviseSpec) error {
	if len(argv) == 0 {
		return errors.New("no provider command to exec")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if argv0 != "" {
		cmd.Args[0] = argv0
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), activationEnvironment(spec)...)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return superviseExitError{code: exitErr.ExitCode()}
	}
	return err
}
