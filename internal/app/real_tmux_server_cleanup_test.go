package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
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
// server, its PID must exit. The server is not our child, so waiting on it is
// impossible, and realTmuxServerExited is the one judgment of whether it did. A
// PID that was never recorded has already been reported by realTmuxServerPID.
func assertRealTmuxServerGone(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if realTmuxServerExited(pid) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Errorf("isolated tmux server %d survived cleanup", pid)
			return
		}
	}
}

// realTmuxServerExited reports whether pid has exited. Signal 0 failing with
// ESRCH is the probe both supported hosts share. It is not enough on Linux:
// the server is reparented to PID 1, and in a container whose PID 1 never
// reaps (`tail -f /dev/null`) the killed server stays a zombie that signal 0
// still reaches. A zombie has exited, so a Z or X state counts as gone there.
func realTmuxServerExited(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	state, err := linuxProcessState(pid)
	return err == nil && (state == "Z" || state == "X")
}

// linuxProcessState reads the state field of /proc/<pid>/stat. The command
// name before it may hold spaces and parentheses, so the state is the first
// field after the last ')'.
func linuxProcessState(pid int) (string, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	end := bytes.LastIndexByte(stat, ')')
	fields := strings.Fields(string(stat[end+1:]))
	if end < 0 || len(fields) == 0 {
		return "", errors.New("malformed /proc stat for pid " + strconv.Itoa(pid))
	}
	return fields[0], nil
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

// TestRealTmuxServerExitedCountsAnUnreapedZombie pins the guard's judgment
// without tmux or its 3s deadline: a live child has not exited, and a killed
// child nobody has waited on yet is a zombie that has.
func TestRealTmuxServerExitedCountsAnUnreapedZombie(t *testing.T) {
	start := func(t *testing.T) *exec.Cmd {
		t.Helper()
		child := exec.Command("sleep", "600")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		return child
	}
	t.Run("live", func(t *testing.T) {
		child := start(t)
		if realTmuxServerExited(child.Process.Pid) {
			t.Fatalf("live child %d judged exited", child.Process.Pid)
		}
	})
	t.Run("unreaped zombie", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("only Linux judges a zombie through /proc; this host relies on ESRCH alone")
		}
		child := start(t)
		pid := child.Process.Pid
		if err := child.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			state, err := linuxProcessState(pid)
			if err != nil {
				t.Fatalf("read state of unreaped child %d: %v", pid, err)
			}
			if state == "Z" {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("killed child %d never became a zombie; state %q", pid, state)
			}
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("signal 0 on the zombie = %v; the case this guards needs it to succeed", err)
		}
		if !realTmuxServerExited(pid) {
			t.Fatalf("unreaped zombie %d judged alive", pid)
		}
	})
}
