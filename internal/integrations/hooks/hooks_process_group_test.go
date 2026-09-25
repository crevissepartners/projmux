package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// blockingHookWithChildren leaves a background child and a foreground child
// (a grandchild of projmux) running, records both pids in the hook's cwd, and
// blocks. The trailing `exit 0` keeps sh from exec'ing the foreground child in
// place of itself, so both pids are real descendants of the hook's sh.
const blockingHookWithChildren = `sleep 300 & echo $! > bg.pid; sh -c 'echo $$ > fg.pid; exec sleep 301'; exit 0`

const signalHelperEnv = "PROJMUX_HOOKS_SIGNAL_HELPER"
const signalHelperDirEnv = "PROJMUX_HOOKS_SIGNAL_HELPER_DIR"

func TestRunnerTimeoutKillsHookProcessGroup(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	cleanupHookPidFiles(t, cwd, "bg.pid", "fg.pid")
	runner, logger := processGroupRunner(t, blockingHookWithChildren, time.Second)

	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(logger.String(), "timed out after 1s") {
		t.Fatalf("expected timeout warning, got:\n%s", logger.String())
	}

	bg, fg := readHookPids(t, cwd)
	waitHookPidsGone(t, bg, fg)
}

func TestRunnerParentCancelKillsHookProcessGroup(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	cleanupHookPidFiles(t, cwd, "bg.pid", "fg.pid")
	runner, _ := processGroupRunner(t, blockingHookWithChildren, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		ready <- waitForFiles(filepath.Join(cwd, "bg.pid"), filepath.Join(cwd, "fg.pid"))
		cancel()
	}()

	returned := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, EventPostCreate, Context{CWD: cwd})
		returned <- err
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return after the parent context was canceled")
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}

	bg, fg := readHookPids(t, cwd)
	waitHookPidsGone(t, bg, fg)
}

func TestRunnerOnTimeHookKeepsBackgroundChild(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	cleanupHookPidFiles(t, cwd, "bg.pid")
	runner, logger := processGroupRunner(t, `(sleep 300 & echo $! > bg.pid) >/dev/null 2>&1; exit 0`, 10*time.Second)

	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}

	bg := readPidFile(t, filepath.Join(cwd, "bg.pid"))
	if !pidAlive(bg) {
		t.Fatalf("background child %d of an on-time hook was killed", bg)
	}
}

func TestRunnerHookRunsInOwnProcessGroup(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	cleanupHookPidFiles(t, cwd, "bg.pid")
	runner, _ := processGroupRunner(t, `(sleep 300 & echo $! > bg.pid) >/dev/null 2>&1; exit 0`, 10*time.Second)

	if _, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	bg := readPidFile(t, filepath.Join(cwd, "bg.pid"))

	pgid, err := syscall.Getpgid(bg)
	if err != nil {
		t.Fatalf("Getpgid(%d) error = %v", bg, err)
	}
	if own := syscall.Getpgrp(); pgid == own {
		t.Fatalf("hook child %d runs in the caller's process group %d", bg, own)
	}
}

// TestRunnerHookSignalHelperProcess is not a test on its own. It is the child
// process TestRunnerForwardsTerminationSignalToHookGroup re-executes: it runs a
// blocking hook that only a signal to this process can end.
func TestRunnerHookSignalHelperProcess(t *testing.T) {
	if os.Getenv(signalHelperEnv) != "1" {
		return
	}
	cwd := os.Getenv(signalHelperDirEnv)
	globalConfigPath := filepath.Join(cwd, "global", "config.toml")
	writeFileEnsuringDir(t, globalConfigPath, "[hooks.post-create]\nrun = "+strconv.Quote(blockingHookWithChildren))
	runner := &Runner{GlobalConfigPath: globalConfigPath, Logger: os.Stderr, Timeout: time.Minute}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	fmt.Fprintf(os.Stderr, "helper: Run returned without a signal: %v\n", err)
	os.Exit(3)
}

func TestRunnerForwardsTerminationSignalToHookGroup(t *testing.T) {
	t.Parallel()

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			if signal.Ignored(sig) {
				t.Skipf("%v is ignored by this process; the helper would inherit that", sig)
			}

			cwd := t.TempDir()
			cleanupHookPidFiles(t, cwd, "bg.pid", "fg.pid")
			var output bytes.Buffer
			helper := exec.Command(os.Args[0], "-test.run=^TestRunnerHookSignalHelperProcess$", "-test.count=1")
			helper.Env = append(os.Environ(), signalHelperEnv+"=1", signalHelperDirEnv+"="+cwd)
			helper.Stdout = &output
			helper.Stderr = &output
			if err := helper.Start(); err != nil {
				t.Fatalf("start helper: %v", err)
			}
			waited := make(chan error, 1)
			go func() { waited <- helper.Wait() }()
			exited := false
			t.Cleanup(func() {
				if !exited {
					_ = helper.Process.Kill()
					<-waited
				}
			})

			if err := waitForFiles(filepath.Join(cwd, "bg.pid"), filepath.Join(cwd, "fg.pid")); err != nil {
				t.Fatalf("%v\nhelper output:\n%s", err, output.String())
			}
			bg, fg := readHookPids(t, cwd)

			if err := helper.Process.Signal(sig); err != nil {
				t.Fatalf("signal helper: %v", err)
			}
			select {
			case <-waited:
				exited = true
			case <-time.After(15 * time.Second):
				t.Fatalf("helper did not exit after %v\nhelper output:\n%s", sig, output.String())
			}
			status, ok := helper.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != sig {
				t.Fatalf("helper exit = %v, want death by %v\nhelper output:\n%s", helper.ProcessState, sig, output.String())
			}
			waitHookPidsGone(t, bg, fg)
		})
	}
}

// TestRunnerReturnsWhenCallerCatchesTheSignal proves a caller that catches a
// termination signal with its own Notify channel survives the re-raise: the
// hook's group dies, the caller's channel receives the signal, and Run
// returns. It signals the test process itself, so it must not run in parallel.
func TestRunnerReturnsWhenCallerCatchesTheSignal(t *testing.T) {
	if signal.Ignored(syscall.SIGTERM) {
		t.Skipf("%v is ignored by this process; Notify would un-ignore it", syscall.SIGTERM)
	}

	other := make(chan os.Signal, 2)
	signal.Notify(other, syscall.SIGTERM)

	cwd := t.TempDir()
	cleanupHookPidFiles(t, cwd, "bg.pid", "fg.pid")
	runner, _ := processGroupRunner(t, blockingHookWithChildren, time.Minute)

	returned := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
		returned <- err
	}()
	if err := waitForFiles(filepath.Join(cwd, "bg.pid"), filepath.Join(cwd, "fg.pid")); err != nil {
		t.Fatal(err)
	}
	bg, fg := readHookPids(t, cwd)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal test process: %v", err)
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return after a SIGTERM the caller catches")
	}
	select {
	case sig := <-other:
		if sig != syscall.SIGTERM {
			t.Fatalf("caller received %v, want %v", sig, syscall.SIGTERM)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller's Notify channel did not receive SIGTERM within 5s")
	}
	waitHookPidsGone(t, bg, fg)
	signal.Stop(other)
}

func processGroupRunner(t *testing.T, run string, timeout time.Duration) (*Runner, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process groups require POSIX")
	}
	globalConfigPath := filepath.Join(t.TempDir(), "global", "config.toml")
	writeFileEnsuringDir(t, globalConfigPath, "[hooks.post-create]\nrun = "+strconv.Quote(run))
	logger := &bytes.Buffer{}
	return &Runner{GlobalConfigPath: globalConfigPath, Logger: logger, Timeout: timeout}, logger
}

// readHookPids reads the pid files blockingHookWithChildren writes. A missing
// file fails the test: without the pids the kill assertion would pass
// vacuously.
func readHookPids(t *testing.T, cwd string) (bg, fg int) {
	t.Helper()
	return readPidFile(t, filepath.Join(cwd, "bg.pid")), readPidFile(t, filepath.Join(cwd, "fg.pid"))
}

func readPidFile(t *testing.T, path string) int {
	t.Helper()
	pid, err := parsePidFile(path)
	if err != nil {
		t.Fatalf("hook pid file %s not written before the hook ended: %v", path, err)
	}
	return pid
}

func parsePidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pid file %s holds %q", path, data)
	}
	return pid, nil
}

// waitForFiles polls until every path holds a pid, bounded to 10s.
func waitForFiles(paths ...string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		missing := ""
		for _, path := range paths {
			if _, err := parsePidFile(path); err != nil {
				missing = path
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hook pid file %s was not written within 10s", missing)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitHookPidsGone fails unless every pid is gone within 5s.
func waitHookPidsGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var alive []int
		for _, pid := range pids {
			if pidAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook descendants %v still running 5s after the hook ended", alive)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pidAlive reports whether pid is a running process. A zombie awaiting its
// reaper counts as gone.
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		// The state follows the parenthesized command name.
		if idx := bytes.LastIndexByte(data, ')'); idx >= 0 && idx+2 < len(data) && data[idx+2] == 'Z' {
			return false
		}
	}
	return true
}

// cleanupHookPidFiles SIGKILLs, by exact pid, every process recorded in the
// named pid files under dir that is still running when the test ends, so a
// failing run leaks nothing.
func cleanupHookPidFiles(t *testing.T, dir string, names ...string) {
	t.Cleanup(func() {
		for _, name := range names {
			pid, err := parsePidFile(filepath.Join(dir, name))
			if err == nil && pidAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}
