package hooks

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// hookTerminationSignals are the signals that end projmux while a hook runs.
// The hook sits in its own process group, so a terminal SIGINT/SIGHUP aimed at
// projmux's group no longer reaches it; forwardTerminationSignals covers that.
var hookTerminationSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// startInOwnProcessGroup makes cmd the leader of a new process group and
// makes context cancellation (timeout or parent cancel) SIGKILL that whole
// group, so children and grandchildren the hook started stop with it.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killProcessGroup(cmd.Process)
	}
}

// killProcessGroup SIGKILLs the group led by p. When the group is already gone
// it falls back to p.Kill, which keeps the default Cmd.Cancel result
// (os.ErrProcessDone for a finished process) and therefore Wait's outcome.
func killProcessGroup(p *os.Process) error {
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return p.Kill()
	}
	return err
}

// forwardTerminationSignals catches SIGINT, SIGTERM, and SIGHUP while the hook
// p runs. On a signal it kills the hook's group, stops catching, and re-raises
// the signal on projmux. On Linux the re-raise targets the calling thread, so
// a caller with the default disposition dies by it before release returns. On
// macOS it is process-directed and not deterministic, so the caller may still
// pass Run before it dies. A caller that catches the signal with its own
// Notify channel receives it once more, and Run returns. A signal the caller
// ignores is left alone, since Notify would un-ignore it. The returned release
// must be called once Wait has returned; a signal that arrives after that is
// re-raised without killing the finished hook.
//
// Concurrent hooks each register their own channel. Every channel receives the
// signal, each kills its own group before Stop, and a re-raise reaches the
// channels still registered, so the default action only runs once every
// running hook's group is dead. Process-level handlers elsewhere (e.g.
// signal.NotifyContext) may see the signal a second time, which they tolerate.
func forwardTerminationSignals(p *os.Process) (release func()) {
	var sigs []os.Signal
	for _, sig := range hookTerminationSignals {
		if !signal.Ignored(sig) {
			sigs = append(sigs, sig)
		}
	}
	if len(sigs) == 0 {
		return func() {}
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	var (
		mu       sync.Mutex
		finished bool
	)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case sig := <-ch:
			mu.Lock()
			if !finished {
				_ = killProcessGroup(p)
			}
			mu.Unlock()
			signal.Stop(ch)
			reraiseSignal(sig)
		case <-done:
		}
	}()

	return func() {
		mu.Lock()
		finished = true
		mu.Unlock()
		close(done)
		<-exited
		signal.Stop(ch)
		select {
		case sig := <-ch:
			reraiseSignal(sig)
		default:
		}
	}
}
