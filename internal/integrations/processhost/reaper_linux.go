package processhost

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

// orphanReapInterval backs up SIGCHLD; a missed or coalesced signal waits at
// most this long.
const orphanReapInterval = 5 * time.Second

// reapOrphans reaps exited descendants the subreaper adopts while the provider
// lives, including those outside the provider group. It never waits for the
// provider itself, so observeChildExit and finishOwnedChild keep its exit
// status and PID/PGID reservation. The returned stop ends reaping.
func reapOrphans(provider int) (stop func()) {
	sigchld := make(chan os.Signal, 1)
	signal.Notify(sigchld, syscall.SIGCHLD)
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		defer signal.Stop(sigchld)
		ticker := time.NewTicker(orphanReapInterval)
		defer ticker.Stop()
		for {
			reapExitedOrphans(provider)
			select {
			case <-done:
				return
			case <-sigchld:
			case <-ticker.C:
			}
		}
	}()
	return func() { close(done); <-finished }
}

// reapExitedOrphans waits only for zombie children other than provider, one
// exact PID at a time. A zombie's PID cannot be recycled until it is reaped,
// and waitid(P_PID) cannot touch a process that is not this helper's child.
func reapExitedOrphans(provider int) {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == provider {
			continue
		}
		if parent, zombie := procChildState(pid); parent != self || !zombie {
			continue
		}
		var info unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG, nil)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
	}
}

func procChildState(pid int) (parent int, zombie bool) {
	// #nosec G304 -- pid is a checked decimal integer in a fixed procfs path.
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 2 {
		return 0, false
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return parent, fields[0] == "Z"
}
