package picker

import (
	"os"
	"testing"
)

// TestMain pins a deterministic default UI locale for the picker test package,
// mirroring internal/app. Several native-interactive render tests assert
// English chrome (search header, titlebar) without resolving a locale of their
// own, so on a host whose ambient LANG is ko_KR they would render Korean and
// diverge from CI (which runs under en-US). Setting the lowest-priority rung
// (LANG=en_US.UTF-8) and clearing the higher rungs keeps the suite
// deterministic while letting any test opt into another locale via t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("PROJMUX_LOCALE")
	os.Unsetenv("LC_ALL")
	os.Unsetenv("LC_MESSAGES")
	os.Setenv("LANG", "en_US.UTF-8")
	os.Exit(runWithIsolatedXDGConfigHome(m.Run))
}

// runWithIsolatedXDGConfigHome isolates run from the developer machine's real
// global projmux config (e.g. locale=ko-KR), which outranks the LANG rung, and
// removes the temporary XDG_CONFIG_HOME afterwards. It returns run's exit code
// instead of exiting so the removal happens (os.Exit skips defers).
func runWithIsolatedXDGConfigHome(run func() int) int {
	dir, err := os.MkdirTemp("", "projmux-test-xdg")
	if err != nil {
		return run()
	}
	defer os.RemoveAll(dir)
	os.Setenv("XDG_CONFIG_HOME", dir)
	return run()
}

// TestRunWithIsolatedXDGConfigHomeRemovesDir guards against TestMain leaving a
// projmux-test-xdg* dir in TMPDIR on every package run.
func TestRunWithIsolatedXDGConfigHomeRemovesDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_CONFIG_HOME", os.Getenv("XDG_CONFIG_HOME"))

	code := runWithIsolatedXDGConfigHome(func() int {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("XDG_CONFIG_HOME %q Stat() = (%v, %v), want an existing dir during run", dir, info, err)
		}
		return 7
	})
	if code != 7 {
		t.Fatalf("runWithIsolatedXDGConfigHome() = %d, want run's exit code 7", code)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", tmp, err)
	}
	if len(entries) != 0 {
		t.Fatalf("TMPDIR entries after run = %v, want none", entries)
	}
}
