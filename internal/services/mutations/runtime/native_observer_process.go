package runtime

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const NativeObserverTimeout = 3 * time.Second
const nativeObserverSettle = 75 * time.Millisecond
const nativeObserverStartupPrefix = "projmux-codex-observer-v1"

// LaunchNativeObserverProcess starts only the observation child.
func LaunchNativeObserverProcess(cmd *exec.Cmd, timeout time.Duration, requireReady bool) NativeObserverStartup {
	configureNativeObserverProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return NativeObserverStartup{Status: "fallback", Reason: "observer-start-failed"}
	}
	if err := cmd.Start(); err != nil {
		return NativeObserverStartup{Status: "fallback", Reason: "observer-start-failed"}
	}
	if timeout <= 0 {
		timeout = NativeObserverTimeout
	}
	line := make(chan string, 2)
	go func() {
		reader := bufio.NewReader(io.LimitReader(stdout, 512))
		value, _ := reader.ReadString('\n')
		line <- value
		value, _ = reader.ReadString('\n')
		line <- value
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case value := <-line:
		result, ok := ParseNativeObserverStartupLine(value)
		if !ok {
			terminateNativeObserverProcess(cmd)
			return NativeObserverStartup{Status: "fallback", Reason: "observer-exited"}
		}
		if result.Status == "ready" || result.Status == "retrying" {
			settled := time.NewTimer(nativeObserverSettle)
			defer settled.Stop()
			select {
			case <-line:
				terminateNativeObserverProcess(cmd)
				return NativeObserverStartup{Status: "fallback", Reason: "observer-exited"}
			case <-settled.C:
				_ = stdout.Close()
				if err := cmd.Process.Release(); err != nil {
					terminateNativeObserverProcess(cmd)
					return NativeObserverStartup{Status: "fallback", Reason: "observer-start-failed"}
				}
				return result
			}
		}
		terminateNativeObserverProcess(cmd)
		return result
	case <-timer.C:
		if requireReady {
			// A transfer completes only against a ready observer, and the grant
			// must not outlive the creator that holds it.
			terminateNativeObserverProcess(cmd)
			return NativeObserverStartup{Status: "fallback", Reason: "observer-timeout"}
		}
		// A slow start is not a failed one. Under a loaded tmux server every
		// query the observer makes before its first report can take most of a
		// second, so killing it here left the Agent with no observer at all and
		// nothing that would ever start one. The observer owns every later
		// transition itself; the creator only stops waiting for it.
		_ = stdout.Close()
		if err := cmd.Process.Release(); err != nil {
			terminateNativeObserverProcess(cmd)
			return NativeObserverStartup{Status: "fallback", Reason: "observer-start-failed"}
		}
		return NativeObserverStartup{Status: "retrying", Reason: "observer-timeout"}
	}
}
func ParseNativeObserverStartupLine(line string) (NativeObserverStartup, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || fields[0] != nativeObserverStartupPrefix {
		return NativeObserverStartup{}, false
	}
	switch fields[1] {
	case "ready":
		if len(fields) != 3 || fields[2] == "" {
			return NativeObserverStartup{}, false
		}
		return NativeObserverStartup{Status: "ready", Epoch: fields[2], Committed: true}, true
	case "fallback":
		if len(fields) != 3 || fields[2] == "" {
			return NativeObserverStartup{}, false
		}
		return NativeObserverStartup{Status: "fallback", Reason: fields[2], Committed: true}, true
	case "retrying":
		if len(fields) != 3 || fields[2] == "" {
			return NativeObserverStartup{}, false
		}
		return NativeObserverStartup{Status: "retrying", Reason: fields[2], Committed: true}, true
	case "stale":
		if len(fields) != 2 {
			return NativeObserverStartup{}, false
		}
		return NativeObserverStartup{Status: "stale", Committed: true}, true
	default:
		return NativeObserverStartup{}, false
	}
}

func configureNativeObserverProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminateNativeObserverProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func ValidateNativeObserverExecutable(executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(executable)), ".exe")
	if executable == "" || strings.HasSuffix(name, ".test") {
		return "", errors.New("codex native lifecycle observer executable is unavailable")
	}
	if !filepath.IsAbs(executable) {
		return "", errors.New("codex native lifecycle observer executable must be absolute")
	}
	executable = filepath.Clean(executable)
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("stat Codex native lifecycle observer executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("codex native lifecycle observer executable must be a regular executable file")
	}
	return executable, nil
}
