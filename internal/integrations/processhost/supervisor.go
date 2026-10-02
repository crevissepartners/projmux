// Package processhost owns dedicated provider processes. It has no registry,
// tmux, or public command dependencies; consumers supply binding transactions
// and an executable that dispatches ServeSupervisor in a dedicated child.
package processhost

import (
	"encoding/json"
	"errors"
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
	died := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, lifetime); close(died) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	waited := make(chan Exit, 1)
	go func() { _ = cmd.Wait(); waited <- exitOf(cmd.ProcessState) }()
	if err := out.Encode(processStatus{PID: group}); err != nil {
		_ = syscall.Kill(-group, syscall.SIGKILL)
		<-waited
		return err
	}
	var exit Exit
	select {
	case exit = <-waited:
	case <-died:
		exit = stopGroup(group, launch.Grace, waited)
	case <-signals:
		exit = stopGroup(group, launch.Grace, waited)
	}
	// A provider may finish while its tool children still hold stdout/stderr.
	// Bound their lifetime too, before publishing completion.
	_ = syscall.Kill(-group, syscall.SIGKILL)
	if err := reapGroup(group, launch.Grace); err != nil {
		return err
	}
	return out.Encode(processStatus{Exit: &exit})
}

func stopGroup(group int, grace time.Duration, waited <-chan Exit) Exit {
	_ = syscall.Kill(-group, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case exit := <-waited:
		return exit
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
