package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func markActivationFailureCloseOnExec(fd int) {
	syscall.CloseOnExec(fd)
}

func execCommittedActivation(argv []string, argv0 string, spec superviseSpec) error {
	if len(argv) == 0 {
		return errors.New("no provider command to exec")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	execArgv := append([]string(nil), argv...)
	if argv0 != "" {
		execArgv[0] = argv0
	}
	// #nosec G204 -- this dynamic executable is the existing managed Pane/provider
	// contract input, resolved by exec.LookPath above and passed with its exact
	// argv directly to Exec without a shell.
	return syscall.Exec(path, execArgv, append(os.Environ(), activationEnvironment(spec)...))
}
