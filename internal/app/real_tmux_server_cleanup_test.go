package app

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The isolated real-tmux tests start a server that daemonizes away from the
// test process: it is reparented to init, nothing reaps it, and once the test
// root is removed its socket is unreachable. So a cleanup that never runs its
// kill-server leaves an orphan behind on every run. These helpers give those
// tests the one shape that cannot leak — a kill on a context the test body
// cannot cancel, and a guard that the recorded server PID is really gone.

// realTmuxServerPID reads the #{pid} a server reported at start. A PID that
// cannot be read is reported here, not fatally, so the caller still registers
// the cleanup that kills the server it just started.
func realTmuxServerPID(t *testing.T, field string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || pid <= 0 {
		t.Errorf("isolated tmux server pid %q: %v", field, err)
		return 0
	}
	return pid
}

// assertRealTmuxServerGone is the regression guard: after cleanup killed the
// server, its PID must disappear. The server is not our child, so waiting on it
// is impossible; signal 0 is the liveness probe both supported hosts share. A
// PID that was never recorded has already been reported by realTmuxServerPID.
func assertRealTmuxServerGone(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Errorf("isolated tmux server %d survived cleanup", pid)
			return
		}
	}
}

// killRealTmuxServerOnCleanup registers the exact-socket kill the isolated
// real-tmux tests share. Callers register it after the cleanup that removes the
// test root, so LIFO kills the server while its socket still exists. The test
// body's own context is canceled by the time cleanups run and
// exec.CommandContext never starts a process on a canceled context, hence the
// fresh deadline here.
func killRealTmuxServerOnCleanup(t *testing.T, environment []string, socket string, pid int) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "tmux", "-S", socket, "-f", "/dev/null", "kill-server")
		command.Env = environment
		if out, err := command.CombinedOutput(); err != nil {
			t.Logf("kill isolated tmux %s: %v: %s", socket, err, strings.TrimSpace(string(out)))
		}
		assertRealTmuxServerGone(t, pid)
	})
}
