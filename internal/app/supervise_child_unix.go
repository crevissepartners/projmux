//go:build !windows

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// forwardedSupervisorSignals are the signals the supervisor relays to the
// child's process group.
//
// The tty already delivers SIGINT, SIGQUIT, SIGTSTP, and SIGWINCH straight to
// the foreground process group, which is the child. What it does not deliver is
// a signal aimed at the *pane process*: tmux tears a pane down with
// killpg(pane pid, SIGHUP), and the supervisor is that pid. Relaying keeps the
// child's teardown identical to the unsupervised case, where the pane pid and
// the child pid were the same process.
var forwardedSupervisorSignals = []os.Signal{
	syscall.SIGHUP,
	syscall.SIGTERM,
	syscall.SIGINT,
	syscall.SIGQUIT,
	syscall.SIGUSR1,
	syscall.SIGUSR2,
}

// runSupervisedChild starts argv on this pane's terminal and returns its
// reaped wait status.
//
// Parity is the whole design constraint. The child gets this process's exact
// stdin/stdout/stderr file descriptors -- the pty tmux allocated, not a pipe --
// so it is interactive in exactly the way it was before, and it is placed in
// its own process group that is made the terminal's foreground group, so job
// control works and `#{pane_current_command}` keeps naming the child rather
// than the supervisor. Nothing rewrites argv or cwd; the activation-aware
// production entry point adds only the two private hook-evidence variables.
func runSupervisedChild(argv []string, argv0 string) (processOutcome, error) {
	return runSupervisedChildWithSignalSource(argv, argv0, nil)
}

func runSupervisedChildWithActivation(argv []string, argv0 string, spec superviseSpec) (processOutcome, error) {
	if spec.AgentUID == "" {
		return runSupervisedChildWithEnvironment(argv, argv0, activationEnvironment(spec), nil)
	}
	if spec.OperationID == "" {
		return processOutcome{}, errors.New("agent activation requires an operation id")
	}
	if err := exactActivationRegistryPath(spec.RegistryPath); err != nil {
		return processOutcome{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return processOutcome{}, fmt.Errorf("resolve activation gate executable: %w", err)
	}
	return runSupervisedActivationGate(activationExecArgv(binary, spec, argv0, 3, argv))
}

// runSupervisedChildWithSignalSource is the test seam for signals delivered to
// the pane supervisor. A nil source installs the process-wide signal relay used
// in production; tests can supply a private channel without signalling the Go
// test process itself.
func runSupervisedChildWithSignalSource(argv []string, argv0 string, suppliedSignals <-chan os.Signal) (processOutcome, error) {
	return runSupervisedChildWithEnvironment(argv, argv0, nil, suppliedSignals)
}

func runSupervisedChildWithEnvironment(argv []string, argv0 string, environment []string, suppliedSignals <-chan os.Signal) (processOutcome, error) {
	if len(argv) == 0 {
		return processOutcome{}, errors.New("no command to supervise")
	}
	// The foreground handoff is attempted first and retried without it. There
	// is no portable way to ask "is fd 0 my controlling terminal" without an
	// ioctl, and asking the kernel by doing the thing is both cheaper and more
	// honest than a probe that could disagree with the real attempt. A failed
	// start forks no surviving child: the runtime reaps it before returning.
	cmd, err := startSupervisedChild(argv, argv0, environment, nil, true)
	if err != nil {
		cmd, err = startSupervisedChild(argv, argv0, environment, nil, false)
	}
	if err != nil {
		return processOutcome{}, err
	}
	return waitSupervisedChild(cmd, suppliedSignals)
}

// runSupervisedActivationGate starts the commit gate as the immediately
// supervised child. fd 3 is a private, CLOEXEC failure channel: admission or
// provider-exec failure writes a constant marker, while successful exec closes
// it. A signal-killed gate writes no marker and remains exact killed evidence;
// crucially, the provider was never started in that case.
func runSupervisedActivationGate(argv []string) (processOutcome, error) {
	return runSupervisedActivationGateWithSignalSource(argv, nil)
}

func runSupervisedActivationGateWithSignalSource(argv []string, suppliedSignals <-chan os.Signal) (processOutcome, error) {
	readFailure, writeFailure, err := os.Pipe()
	if err != nil {
		return processOutcome{}, fmt.Errorf("create activation failure channel: %w", err)
	}
	defer readFailure.Close()

	cmd, err := startSupervisedChild(argv, "", nil, []*os.File{writeFailure}, true)
	if err != nil {
		cmd, err = startSupervisedChild(argv, "", nil, []*os.File{writeFailure}, false)
	}
	_ = writeFailure.Close()
	if err != nil {
		return processOutcome{}, err
	}
	outcome, waitErr := waitSupervisedChild(cmd, suppliedSignals)
	failure, readErr := io.ReadAll(io.LimitReader(readFailure, 64))
	if readErr != nil {
		return processOutcome{}, fmt.Errorf("read activation failure channel: %w", readErr)
	}
	if len(failure) != 0 {
		return processOutcome{}, errors.New("activation gate refused provider launch")
	}
	return outcome, waitErr
}

func waitSupervisedChild(cmd *exec.Cmd, suppliedSignals <-chan os.Signal) (processOutcome, error) {

	signals := suppliedSignals
	var ownedSignals chan os.Signal
	if signals == nil {
		ownedSignals = make(chan os.Signal, 8)
		signals = ownedSignals
		signal.Notify(ownedSignals, forwardedSupervisorSignals...)
	}
	done := make(chan struct{})
	relayDone := make(chan struct{})
	relayedHUP := false
	go func() {
		defer close(relayDone)
		for {
			select {
			case received, open := <-signals:
				if !open {
					return
				}
				sig, ok := received.(syscall.Signal)
				if !ok || cmd.Process == nil {
					continue
				}
				// Negative pid addresses the child's process group, which is
				// where a job-control-aware child put its own children.
				if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil && sig == syscall.SIGHUP {
					relayedHUP = true
				}
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	if ownedSignals != nil {
		signal.Stop(ownedSignals)
	}
	close(done)
	<-relayDone

	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return processOutcome{}, waitErr
		}
	}
	if cmd.ProcessState == nil {
		return processOutcome{}, fmt.Errorf("supervised process %s produced no wait status", cmd.Args[0])
	}
	// Some interactive providers catch the relayed HUP and translate it into a
	// conventional 128+SIGHUP exit code. The wait status alone then looks like
	// a voluntary exit 129. Preserve the stronger evidence that this supervisor
	// actually delivered HUP; never infer HUP from the numeric exit code itself.
	if relayedHUP {
		return processOutcome{Signal: signalName(syscall.SIGHUP), SignalNumber: int(syscall.SIGHUP)}, nil
	}
	return outcomeFromWaitStatus(cmd.ProcessState.Sys()), nil
}

// startSupervisedChild builds and starts one child, optionally handing it the
// terminal's foreground process group.
func startSupervisedChild(argv []string, argv0 string, environment []string, extraFiles []*os.File, foreground bool) (*exec.Cmd, error) {
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
	cmd.ExtraFiles = extraFiles
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if foreground {
		// Ctty is a descriptor number in the *child*, and the child's fd 0 is
		// the pty this pane owns.
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = 0
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// outcomeFromWaitStatus reads the platform wait status into the portable
// outcome. A signalled child is reported by signal even when the platform also
// exposes a code alongside it.
func outcomeFromWaitStatus(raw any) processOutcome {
	status, ok := raw.(syscall.WaitStatus)
	if !ok {
		return processOutcome{}
	}
	if status.Signaled() {
		sig := status.Signal()
		return processOutcome{Signal: signalName(sig), SignalNumber: int(sig)}
	}
	return processOutcome{ExitCode: status.ExitStatus()}
}

// signalName renders a signal without the "SIG" prefix, which is the spelling
// `kill -l` already uses.
//
// syscall.Signal.String renders a human sentence ("hangup", "terminated"), not
// a token, so it is deliberately not used: a receipt field is machine-read
// evidence and must not carry prose. An unlisted signal is recorded by number.
func signalName(sig syscall.Signal) string {
	if known, ok := signalNames[sig]; ok {
		return known
	}
	return fmt.Sprintf("%d", int(sig))
}

// signalNames is the closed set of signal spellings a receipt may carry.
// Anything outside it is recorded by number, so an exotic platform signal is
// still durable evidence without inventing a vocabulary entry for it.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "HUP",
	syscall.SIGINT:  "INT",
	syscall.SIGQUIT: "QUIT",
	syscall.SIGILL:  "ILL",
	syscall.SIGABRT: "ABRT",
	syscall.SIGFPE:  "FPE",
	syscall.SIGKILL: "KILL",
	syscall.SIGSEGV: "SEGV",
	syscall.SIGPIPE: "PIPE",
	syscall.SIGALRM: "ALRM",
	syscall.SIGTERM: "TERM",
	syscall.SIGUSR1: "USR1",
	syscall.SIGUSR2: "USR2",
	syscall.SIGBUS:  "BUS",
	syscall.SIGTRAP: "TRAP",
}
