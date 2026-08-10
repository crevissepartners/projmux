//go:build darwin

package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

func TestKeyBrokerRestartsProcessAfterAccessibilityPermissionPrompt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	home := t.TempDir()
	restartErr := errors.New("restart recorder")
	restarts := 0
	source := &keyBrokerSourceRecorder{runErr: platformkeys.ErrPermissionRequired}
	cmd := newKeyBrokerCommand()
	cmd.source = source
	cmd.nativeKeys = func() bool { return true }
	cmd.homeDir = func() (string, error) { return home, nil }
	cmd.lookupEnv = func(string) string { return "" }
	cmd.runner = &keyBrokerRecordingRunner{}
	cmd.pollEvery = time.Millisecond
	cmd.permissionWait = 10 * time.Millisecond
	cmd.restartProcess = func() error {
		restarts++
		return restartErr
	}

	var stderr bytes.Buffer
	err := cmd.Run([]string{"--socket", "permission-restart-test"}, &bytes.Buffer{}, &stderr)
	if !errors.Is(err, restartErr) {
		t.Fatalf("Run() error = %v, want restart error %v", err, restartErr)
	}
	if source.runCalls != 1 || restarts != 1 {
		t.Fatalf("source runs = %d, restarts = %d, want 1 and 1", source.runCalls, restarts)
	}
	if !strings.Contains(stderr.String(), "waiting for macOS Accessibility approval") {
		t.Fatalf("stderr = %q, want Accessibility wait notice", stderr.String())
	}
}

func TestKeyBrokerDoesNotRestartWithoutTmuxServer(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	home := t.TempDir()
	restarts := 0
	cmd := newKeyBrokerCommand()
	cmd.source = &keyBrokerSourceRecorder{runErr: platformkeys.ErrPermissionRequired}
	cmd.nativeKeys = func() bool { return true }
	cmd.homeDir = func() (string, error) { return home, nil }
	cmd.lookupEnv = func(string) string { return "" }
	cmd.runner = &keyBrokerRecordingRunner{err: errors.New("no server running on /tmp/projmux-test")}
	cmd.pollEvery = time.Millisecond
	cmd.permissionWait = time.Millisecond
	cmd.startupWait = 10 * time.Millisecond
	cmd.restartProcess = func() error {
		restarts++
		return nil
	}

	if err := cmd.Run([]string{"--socket", "permission-no-server-test"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if restarts != 0 {
		t.Fatalf("restarts = %d, want 0 without a tmux server", restarts)
	}
}

func TestRestartKeyBrokerProcessReplacesCurrentProcess(t *testing.T) {
	const stateEnv = "PROJMUX_TEST_KEY_BROKER_RESTART_STATE"
	switch os.Getenv(stateEnv) {
	case "":
		cmd := exec.Command(os.Args[0], "-test.run=^TestRestartKeyBrokerProcessReplacesCurrentProcess$")
		cmd.Env = append(os.Environ(), stateEnv+"=before")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restarted test process: %v\n%s", err, output)
		}
	case "before":
		if err := os.Setenv(stateEnv, "after"); err != nil {
			t.Fatal(err)
		}
		if err := restartKeyBrokerProcess(); err != nil {
			t.Fatalf("restartKeyBrokerProcess() error = %v", err)
		}
		t.Fatal("restartKeyBrokerProcess() returned without replacing the process")
	case "after":
		return
	default:
		t.Fatalf("unexpected restart test state %q", os.Getenv(stateEnv))
	}
}
