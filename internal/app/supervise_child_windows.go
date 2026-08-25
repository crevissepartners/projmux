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
	return runSupervisedChildWithEnvironment(argv, argv0, nil)
}

func runSupervisedChildWithActivation(argv []string, argv0 string, spec superviseSpec) (processOutcome, error) {
	if spec.AgentUID == "" {
		return runSupervisedChildWithEnvironment(argv, argv0, activationEnvironment(spec))
	}
	if spec.OperationID == "" {
		return processOutcome{}, errors.New("agent activation requires an operation id")
	}
	if err := exactActivationRegistryPath(spec.RegistryPath); err != nil {
		return processOutcome{}, err
	}
	// Native Windows has no tmux Pane process group or POSIX HUP boundary. Run
	// the same exact zero-write admission before starting the provider; a refusal
	// is returned as a launch failure and therefore cannot fabricate a receipt.
	// WSL uses the Unix build and its CLOEXEC handshake.
	gate := newActivationExecCommand()
	if err := gate.awaitCommittedActivation(spec); err != nil {
		return processOutcome{}, fmt.Errorf("activation admission: %w", err)
	}
	return runSupervisedChildWithEnvironment(argv, argv0, activationEnvironment(spec))
}

func runSupervisedChildWithEnvironment(argv []string, argv0 string, environment []string) (processOutcome, error) {
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
	if len(environment) > 0 {
		cmd.Env = append(os.Environ(), environment...)
	}
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
