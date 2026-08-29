package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodexObserverChildStartupMatrixAndExactRoute(t *testing.T) {
	target := codexLifecycleObserverTarget{
		Identity: codexLifecycleIdentity{
			AgentUID: "agent-exact", PaneUID: "pane-exact", RuntimeID: "%71",
			Generation: "generation-exact", ThreadID: "thread-exact",
		},
		Route: tmuxTransport{Kind: tmuxSocketName, Value: "route-exact", Source: tmuxSocketNameSource},
	}
	for _, test := range []struct {
		name    string
		body    string
		timeout time.Duration
		want    codexObserverStartupResult
	}{
		{name: "early exit", body: "exit 17", want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-exited"}},
		{name: "handshake timeout", body: "sleep 30", timeout: 25 * time.Millisecond, want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-timeout"}},
		{name: "typed fallback", body: fmt.Sprintf("printf '%s fallback control-unavailable\\n'; sleep 30", codexObserverStartupPrefix), want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "control-unavailable", committed: true}},
		{name: "ready then exit", body: fmt.Sprintf("printf '%s ready epoch-early\\n'", codexObserverStartupPrefix), want: codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-exited"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "pid")
			t.Setenv("PROJMUX_OBSERVER_TEST_PID", pidPath)
			executable := writeCodexObserverProcessFixture(t, "printf '%s' \"$$\" > \"$PROJMUX_OBSERVER_TEST_PID\"\n"+test.body)
			got := startCodexLifecycleObserverProcess(executable, target, test.timeout)
			if got != test.want {
				t.Fatalf("startup result = %+v, want %+v", got, test.want)
			}
			pid := readCodexObserverFixturePID(t, pidPath)
			waitForCodexObserverProcessGone(t, pid)
		})
	}

	t.Run("ready survives settle", func(t *testing.T) {
		root := t.TempDir()
		pidPath := filepath.Join(root, "pid")
		argvPath := filepath.Join(root, "argv")
		envPath := filepath.Join(root, "env")
		t.Setenv("PROJMUX_OBSERVER_TEST_PID", pidPath)
		t.Setenv("PROJMUX_OBSERVER_TEST_ARGV", argvPath)
		t.Setenv("PROJMUX_OBSERVER_TEST_ENV", envPath)
		t.Setenv("TMUX", "/tmp/ambient,1,0")
		t.Setenv("TMUX_PANE", "%999")
		t.Setenv(runtimeMutationAnchorPaneEnv, "%998")
		t.Setenv(codexObserverStartupEnvironment, "stale")
		executable := writeCodexObserverProcessFixture(t, fmt.Sprintf(`
printf '%%s' "$$" > "$PROJMUX_OBSERVER_TEST_PID"
printf '%%s\n' "$*" > "$PROJMUX_OBSERVER_TEST_ARGV"
env > "$PROJMUX_OBSERVER_TEST_ENV"
printf '%s ready epoch-ready\n'
sleep 30
`, codexObserverStartupPrefix))
		got := startCodexLifecycleObserverProcess(executable, target, time.Second)
		if got != (codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: "epoch-ready", committed: true}) {
			t.Fatalf("ready result = %+v", got)
		}
		pid := readCodexObserverFixturePID(t, pidPath)
		t.Cleanup(func() {
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			waitForCodexObserverProcessGone(t, pid)
		})
		argvBytes, err := os.ReadFile(argvPath)
		if err != nil {
			t.Fatal(err)
		}
		argv := string(argvBytes)
		for _, exact := range []string{
			"--agent-uid agent-exact", "--pane-uid pane-exact", "--pane %71",
			"--generation generation-exact", "--thread thread-exact", "--tmux-socket-name route-exact",
		} {
			if !strings.Contains(argv, exact) {
				t.Errorf("observer argv %q lacks %q", argv, exact)
			}
		}
		envBytes, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		env := string(envBytes)
		startupEntries := 0
		for line := range strings.SplitSeq(strings.TrimSpace(env), "\n") {
			if strings.HasPrefix(line, codexObserverStartupEnvironment+"=") {
				startupEntries++
				if line != codexObserverStartupEnvironment+"=1" {
					t.Fatalf("observer inherited stale startup environment: %q", line)
				}
			}
		}
		if startupEntries != 1 || strings.Contains(env, "TMUX=") || strings.Contains(env, "TMUX_PANE=") || strings.Contains(env, runtimeMutationAnchorPaneEnv+"=") {
			t.Fatalf("observer environment escaped startup/exact-route contract: %q", env)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		got := startCodexLifecycleObserverProcess(missing, target, time.Second)
		want := codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
		if got != want {
			t.Fatalf("start failure = %+v, want %+v", got, want)
		}
	})
}

func writeCodexObserverProcessFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projmux-observer-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readCodexObserverFixturePID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("fixture pid %q: %v", data, err)
	}
	return pid
}

func waitForCodexObserverProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if stat, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); readErr == nil {
			fields := strings.Fields(string(stat))
			if len(fields) > 2 && fields[2] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("observer process %d survived bounded cleanup", pid)
}
