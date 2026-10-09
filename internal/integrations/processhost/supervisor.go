// Package processhost owns dedicated provider processes. It has no registry,
// tmux, or public command dependencies; consumers supply binding transactions
// and an executable that dispatches ServeSupervisor in a dedicated child.
package processhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// Command is an already resolved launch configuration. Env must be explicit;
// the host never broadens a provider's settings or permission policy.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

type supervisorSpec struct {
	Command Command
	Grace   time.Duration
}

type processStatus struct {
	Prepared bool
	PID      int
	Exit     *Exit
	Error    string
}

// Exit is evidence obtained from an actual child Wait, never from stream EOF.
type Exit struct {
	Code   int
	Signal string
}

// ServeSupervisor is the internal helper entry seam. The consumer supplies
// three inherited descriptors: owner lifetime, launch specification, status.
// Only this command's dedicated provider process group is signalled. This
// helper accepts one launch and exits; it cannot adopt or serve other runtimes.
// Fixtures dispatch this entry; public command wiring belongs to activation.
func ServeSupervisor(lifetime, spec, status *os.File) error {
	defer lifetime.Close()
	defer spec.Close()
	defer status.Close()
	// These descriptors must not pass through to the provider or its children.
	syscall.CloseOnExec(int(lifetime.Fd()))
	syscall.CloseOnExec(int(spec.Fd()))
	syscall.CloseOnExec(int(status.Fd()))
	out := json.NewEncoder(status)
	if err := out.Encode(processStatus{Prepared: true}); err != nil {
		return err
	}
	var launch supervisorSpec
	if err := json.NewDecoder(io.LimitReader(spec, 1<<20)).Decode(&launch); err != nil {
		return err
	}
	if launch.Grace <= 0 || launch.Grace > time.Minute {
		return errors.New("invalid supervisor grace")
	}
	if err := prepareReaper(); err != nil {
		return err
	}
	// #nosec G204 -- the consumer supplies a resolved executable/argv for this dedicated child; no shell interpolation or PID adoption.
	cmd := exec.Command(launch.Command.Path, launch.Command.Args...)
	cmd.Dir, cmd.Env = launch.Command.Dir, launch.Command.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = out.Encode(processStatus{Error: err.Error()})
		return err
	}
	_ = os.Stdout.Close()
	_ = os.Stderr.Close()
	// The group was created by this exact child Start, not found by PID search.
	group := cmd.Process.Pid
	stopOrphans := reapOrphans(group)
	died := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, lifetime); close(died) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	waited := make(chan error, 1)
	go func() { waited <- observeChildExit(group) }()
	if err := out.Encode(processStatus{PID: group}); err != nil {
		stopOrphans()
		_ = syscall.Kill(-group, syscall.SIGKILL)
		_, cleanupErr := finishOwnedChild(cmd, <-waited, launch.Grace)
		return errors.Join(err, cleanupErr)
	}
	var observationErr error
	select {
	case observationErr = <-waited:
	case <-died:
		observationErr = stopGroup(group, launch.Grace, waited)
	case <-signals:
		observationErr = stopGroup(group, launch.Grace, waited)
	}
	stopOrphans()
	exit, err := finishOwnedChild(cmd, observationErr, launch.Grace)
	if err != nil {
		return err
	}
	return out.Encode(processStatus{Exit: &exit})
}

func finishOwnedChild(cmd *exec.Cmd, observationErr error, grace time.Duration) (Exit, error) {
	// Failed observation confers no cleanup authority. In particular, do not
	// recover authority from a saved PID after another reaper consumed the child.
	// The caller reports unknown; unsupported observation is a platform failure.
	if observationErr != nil {
		return Exit{}, observationErr
	}
	group := cmd.Process.Pid
	// Keep the exited group leader waitable until every signal has been sent.
	// Reaping first could release its PID/PGID for an unrelated process.
	// A provider may also finish while tool children retain stdout/stderr.
	_ = syscall.Kill(-group, syscall.SIGKILL)
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return Exit{}, fmt.Errorf("owned child Wait: %w", waitErr)
	}
	if cmd.ProcessState == nil {
		return Exit{}, errors.New("owned child Wait produced no status")
	}
	if err := reapGroup(group, grace); err != nil {
		return Exit{}, fmt.Errorf("owned group cleanup after child Wait: %w", err)
	}
	return exitOf(cmd.ProcessState), nil
}

func stopGroup(group int, grace time.Duration, waited <-chan error) error {
	// A completed observation (including failure) wins over cancellation.
	select {
	case err := <-waited:
		return err
	default:
	}
	// Owner shutdown closes provider stdin first. Give SessionEnd a bounded
	// opportunity to complete before signalling the still-owned group. Owner
	// death follows the same bound even when a descendant retains stdin.
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-waited:
		return err
	case <-timer.C:
	}
	// Observation failure must never grant signal authority at escalation.
	select {
	case err := <-waited:
		return err
	default:
	}
	_ = syscall.Kill(-group, syscall.SIGTERM)
	timer.Reset(grace)
	select {
	case err := <-waited:
		return err
	case <-timer.C:
		_ = syscall.Kill(-group, syscall.SIGKILL)
		return <-waited
	}
}

func exitOf(state *os.ProcessState) Exit {
	result := Exit{Code: state.ExitCode()}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		switch status.Signal() {
		case syscall.SIGHUP:
			result.Signal = "HUP"
		case syscall.SIGTERM:
			result.Signal = "TERM"
		case syscall.SIGKILL:
			result.Signal = "KILL"
		case syscall.SIGINT:
			result.Signal = "INT"
		default:
			result.Signal = status.Signal().String()
		}
	}
	return result
}
