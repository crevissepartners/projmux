package osfocus

import (
	"os"
	"os/exec"
)

// WindowsTerminalWSLAdapter raises the Windows Terminal window when projmux
// is running inside WSL and Windows Terminal is the host (the most common
// projmux setup for Windows users).
//
// Detection follows the spike doc's "Notes for adapter authors": Windows
// Terminal cannot be detected via TERM_PROGRAM inside tmux because tmux
// rewrites that variable to "tmux"; instead `WT_SESSION` is forwarded into
// WSL via `WSLENV`. The combination of `WT_SESSION` and `WSL_INTEROP`
// identifies "Windows Terminal hosting a WSL session" specifically and lets
// us safely shell out to `wt.exe` via WSL interop.
//
// Focus uses `wt.exe -w 0 focus-tab -t 0`. Test 1 in the spike measured
// this as the no-side-effect raise call: it focuses the active WT window
// without spawning a new tab (which is what the bare `wt.exe -w 0` form
// does — see Test 3) and without un-maximizing the window (which the
// `SetForegroundWindow` + `ShowWindow(SW_RESTORE)` combo can do — see
// Test 2). The Target argument is ignored at tier-1 because `-w 0` means
// "the currently active WT window," which matches the pane the user just
// clicked toward.
type WindowsTerminalWSLAdapter struct {
	// LookupEnv is injected for tests. When nil it falls back to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Run is injected for tests. When nil it spawns wt.exe in the background
	// with stdout/stderr discarded so the caller is never blocked. The
	// adapter does not wait on the spawned process.
	Run func(name string, args ...string) error
}

// Name returns the adapter's identifier used for logging/telemetry.
func (a WindowsTerminalWSLAdapter) Name() string { return "windows-terminal-wsl" }

// Detect returns true only when we are inside a WSL distro hosted by Windows
// Terminal. Both env vars must be non-empty:
//
//   - WT_SESSION  → Windows Terminal forwarded its session GUID into WSL
//     (the user has WSLENV configured the standard way, or WT inherits it).
//   - WSL_INTEROP → we can spawn Windows-side executables via the WSL
//     interop bridge (which is how we reach wt.exe).
func (a WindowsTerminalWSLAdapter) Detect() bool {
	lookup := a.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	wt, _ := lookup("WT_SESSION")
	if wt == "" {
		return false
	}
	interop, _ := lookup("WSL_INTEROP")
	return interop != ""
}

// Focus spawns `wt.exe -w 0 focus-tab -t 0` in a goroutine and returns
// immediately. The call is best-effort: if the spawn fails (wt.exe missing
// from $PATH, interop misconfigured, etc.) the error is dropped because the
// chain documents silent fallback as the failure policy. The Target argument
// is accepted for forward compatibility with tier-2 adapters but not
// consumed here.
func (a WindowsTerminalWSLAdapter) Focus(_ Target) error {
	runner := a.Run
	if runner == nil {
		runner = defaultWTRun
	}
	// Background dispatch keeps Focus non-blocking — the caller is on the
	// focus hot path and must not wait on an external process. We discard
	// the runner's error intentionally; the chain's contract is silent
	// fallback on adapter failure.
	go func() {
		_ = runner("wt.exe", "-w", "0", "focus-tab", "-t", "0")
	}()
	return nil
}

// defaultWTRun spawns the command without waiting on it. stdout/stderr are
// discarded so the spawn cannot leak handles into the projmux process.
func defaultWTRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child in a goroutine so we don't leave a zombie behind. We
	// don't surface Wait's error because Focus already returned to the
	// caller — silent fallback policy.
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
