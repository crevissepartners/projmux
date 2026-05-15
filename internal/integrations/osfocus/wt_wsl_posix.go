//go:build unix

package osfocus

import (
	"os/exec"
	"syscall"
)

// defaultWTRun spawns the command without waiting on it. stdout/stderr are
// discarded so the spawn cannot leak handles into the projmux process.
//
// WSL interop quirk: a direct Go exec of `wt.exe` (the binfmt_misc shim
// routes us into Windows) spawns the resulting Windows process WITHOUT
// foreground-window rights. wt.exe runs but cannot raise its window.
// Wrapping through `bash -c` lets bash set up the process (controlling
// tty / session) such that foreground rights propagate and the raise
// actually fires. Verified empirically vs three alternatives during the
// tier-1 ship.
//
// Setsid rationale: `projmux ai notify` is invoked from tmux hooks as a
// one-shot subprocess; after Notify returns the projmux main goroutine
// exits and the kernel sends SIGHUP to every member of the parent's
// process group. cmd.Start("bash", "-c", "wt.exe ...") returns after the
// fork(2) succeeds but BEFORE bash has exec'd wt.exe -- so without
// detaching, bash dies in the parent's pgroup before the exec can happen
// and wt.exe never spawns. SysProcAttr{Setsid: true} makes bash a session
// leader with its own pgroup, so the SIGHUP-on-parent-exit chain does not
// reach it and the raise fires correctly.
func defaultWTRun(name string, args ...string) error {
	quoted := shellQuoteArgs(append([]string{name}, args...))
	cmd := exec.Command("bash", "-c", quoted)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child in a goroutine so we don't leave a zombie behind. We
	// don't surface Wait's error because Focus already returned to the
	// caller -- silent fallback policy. If projmux exits first the goroutine
	// dies too, but the bash child is now in its own session and continues
	// running detached, so wt.exe still spawns.
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
