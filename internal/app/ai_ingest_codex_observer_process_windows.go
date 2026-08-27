//go:build windows

package app

import "os/exec"

func configureCodexObserverProcess(*exec.Cmd) {}

func terminateCodexObserverProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
