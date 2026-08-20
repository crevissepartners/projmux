//go:build !windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestRunSupervisedChildRetainsRelayedHUPEvidenceWhenChildExits129(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}

	ready := filepath.Join(t.TempDir(), "ready")
	signals := make(chan os.Signal, 1)
	type childResult struct {
		outcome processOutcome
		err     error
	}
	result := make(chan childResult, 1)
	go func() {
		outcome, runErr := runSupervisedChildWithSignalSource([]string{
			shell, "-c",
			`trap 'exit 129' HUP; : > "$1"; while :; do sleep 10; done`,
			"supervised-hup-fixture", ready,
		}, "", signals)
		result <- childResult{outcome: outcome, err: runErr}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("stat child readiness: %v", statErr)
		}
		select {
		case got := <-result:
			t.Fatalf("child finished before HUP: outcome=%#v err=%v", got.outcome, got.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child HUP trap")
		}
		time.Sleep(10 * time.Millisecond)
	}

	signals <- syscall.SIGHUP
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("runSupervisedChildWithSignalSource error = %v", got.err)
		}
		want := processOutcome{Signal: "HUP", SignalNumber: int(syscall.SIGHUP)}
		if got.outcome != want {
			t.Fatalf("outcome = %#v, want relayed HUP %#v", got.outcome, want)
		}
		if classification := coremetadata.ClassifyProcessExit(got.outcome.ExitCode, got.outcome.Signal); classification != coremetadata.TerminationKilled {
			t.Fatalf("classification = %q, want %q", classification, coremetadata.TerminationKilled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for child to translate relayed HUP")
	}
}

func TestRunSupervisedChildDoesNotInferHUPFromExit129(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell: %v", err)
	}

	outcome, err := runSupervisedChildWithSignalSource(
		[]string{shell, "-c", "exit 129"},
		"",
		make(chan os.Signal),
	)
	if err != nil {
		t.Fatalf("runSupervisedChildWithSignalSource error = %v", err)
	}
	want := processOutcome{ExitCode: 129}
	if outcome != want {
		t.Fatalf("outcome = %#v, want plain exit %#v", outcome, want)
	}
	if classification := coremetadata.ClassifyProcessExit(outcome.ExitCode, outcome.Signal); classification != coremetadata.TerminationAbnormal {
		t.Fatalf("classification = %q, want %q", classification, coremetadata.TerminationAbnormal)
	}
}
