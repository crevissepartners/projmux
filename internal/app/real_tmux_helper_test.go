package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// realTmuxStrictEnv turns a missing tmux into a failure for every real-tmux
// test in this package instead of a skip. Only the value "1" enables it.
const realTmuxStrictEnv = "PROJMUX_REAL_TMUX_STRICT"

// requireRealTmux is the one place a real-tmux test discovers tmux. It returns
// the binary on PATH. Without one the test skips, unless realTmuxStrictEnv or
// one of the test's own opt-in variables in strictEnvs is "1": then the test
// fails, so a runner that is meant to exercise real tmux cannot pass by
// skipping. TestRealTmuxSkipIsOwnedByOneHelper keeps every other skip out.
func requireRealTmux(t testing.TB, strictEnvs ...string) string {
	t.Helper()
	binary, err := exec.LookPath("tmux")
	if err == nil {
		return binary
	}
	for _, name := range strictEnvs {
		if os.Getenv(name) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", name, err)
		}
	}
	if os.Getenv(realTmuxStrictEnv) == "1" {
		t.Fatalf("tmux is not installed and %s=1 requires real-tmux tests to run: %v", realTmuxStrictEnv, err)
	}
	t.Skip("tmux is not installed")
	return ""
}

// realTmuxHelperRecorder stands in for the test handed to requireRealTmux so
// the self-tests can watch a skip or a failure without taking either
// themselves. Skip and Fatalf end the goroutine the way testing does.
type realTmuxHelperRecorder struct {
	testing.TB
	skipped bool
	fatal   string
}

func (r *realTmuxHelperRecorder) Helper() {}

func (r *realTmuxHelperRecorder) Skip(args ...any) {
	r.skipped = true
	runtime.Goexit()
}

func (r *realTmuxHelperRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func runRealTmuxHelper(strictEnvs ...string) (*realTmuxHelperRecorder, string) {
	recorder := &realTmuxHelperRecorder{}
	var binary string
	done := make(chan struct{})
	go func() {
		defer close(done)
		binary = requireRealTmux(recorder, strictEnvs...)
	}()
	<-done
	return recorder, binary
}

// TestRequireRealTmuxSkipsOrFailsWithoutTmux pins both answers to a missing
// tmux: a skip by default, a failure naming the variable under strict mode.
func TestRequireRealTmuxSkipsOrFailsWithoutTmux(t *testing.T) {
	const testOptIn = "PMX_TEST_REQUIRE_REAL_TMUX_SELF_TEST"
	t.Setenv("PATH", t.TempDir())
	t.Setenv(testOptIn, "")
	if _, err := exec.LookPath("tmux"); err == nil {
		t.Fatal("tmux is still reachable on the emptied PATH")
	}

	for _, value := range []string{"", "0", "true"} {
		t.Setenv(realTmuxStrictEnv, value)
		recorder, _ := runRealTmuxHelper(testOptIn)
		if !recorder.skipped || recorder.fatal != "" {
			t.Fatalf("%s=%q: skipped=%v fatal=%q, want a skip", realTmuxStrictEnv, value, recorder.skipped, recorder.fatal)
		}
	}

	t.Setenv(realTmuxStrictEnv, "1")
	recorder, _ := runRealTmuxHelper()
	if recorder.skipped || !strings.Contains(recorder.fatal, realTmuxStrictEnv+"=1") {
		t.Fatalf("strict: skipped=%v fatal=%q, want a failure naming %s", recorder.skipped, recorder.fatal, realTmuxStrictEnv)
	}

	// A test's own opt-in still fails on its own, with its own name.
	t.Setenv(realTmuxStrictEnv, "")
	t.Setenv(testOptIn, "1")
	recorder, _ = runRealTmuxHelper(testOptIn)
	if recorder.skipped || !strings.HasPrefix(recorder.fatal, testOptIn+"=1 requires tmux") {
		t.Fatalf("test opt-in: skipped=%v fatal=%q, want a failure naming %s", recorder.skipped, recorder.fatal, testOptIn)
	}
}

// TestRequireRealTmuxReturnsTheBinaryOnPath proves strict mode changes nothing
// when tmux is present: the helper hands back the binary and neither skips nor
// fails.
func TestRequireRealTmuxReturnsTheBinaryOnPath(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, value := range []string{"", "1"} {
		t.Setenv(realTmuxStrictEnv, value)
		recorder, binary := runRealTmuxHelper()
		if recorder.skipped || recorder.fatal != "" || binary != fake {
			t.Fatalf("%s=%q: binary=%q skipped=%v fatal=%q, want %q", realTmuxStrictEnv, value, binary, recorder.skipped, recorder.fatal, fake)
		}
	}
}
