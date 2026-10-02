package processhost

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func prepareReaper() error { return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) }

func reapGroup(group int, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-group, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("owned group %d did not reap before deadline", group)
		}
		if pid == 0 {
			time.Sleep(time.Millisecond)
		}
	}
}

// observeChildExit observes only this helper's direct child, leaving it waitable
// so the PID and process-group identity cannot be recycled before cleanup.
func observeChildExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
