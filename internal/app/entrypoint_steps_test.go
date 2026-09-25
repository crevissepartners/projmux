package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// realTmuxExitFailure returns the production typed tmux carrier around a real
// *exec.ExitError with exit status 1. It runs sh through inttmux.ExecRunner,
// so stderr becomes the carrier's CommandFailure projection exactly as a
// failed tmux command's would.
func realTmuxExitFailure(t *testing.T, stderr string) error {
	t.Helper()
	_, err := inttmux.ExecRunner{}.Run(context.Background(), "sh", "-c", `printf '%s\n' "$1" >&2; exit 1`, "sh", stderr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("sh exit 1 = %v, want a typed carrier around *exec.ExitError with code 1", err)
	}
	return err
}

// entrypointSteps is what cmd/projmux leaves behind for one returned error.
type entrypointSteps struct {
	stderr   string
	exitCode int
	// topLevel holds the top-level command outcomes RecordOutcome journaled.
	topLevel []diagnostics.Event
}

// runEntrypointSteps pushes a command's returned error through the steps
// executeCLI takes after invoke: the top-level diagnostics outcome (skipped
// when lifecycle already recorded one), then the shared verdict's stderr line
// and exit code. lifecycle may be nil for a command that owns no lifecycle.
func runEntrypointSteps(t *testing.T, args []string, err error, lifecycle *diagnostics.LifecycleRecorder) entrypointSteps {
	t.Helper()
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "diagnostics", "events.jsonl"))
	usage := IsUsageError(err)
	if recordErr := diagnostics.RecordOutcome(store, args, "run-entrypoint", "0.0.0-test", "tmux", time.Now(), err, usage, lifecycle.RecordedOutcome()); recordErr != nil {
		t.Fatalf("RecordOutcome: %v", recordErr)
	}
	var stderr bytes.Buffer
	verdict := cli.ClassifyFailure(err, usage)
	if verdict.Print {
		fmt.Fprintln(&stderr, err)
	}
	events, readErr := store.Read()
	if readErr != nil {
		t.Fatalf("read journal: %v", readErr)
	}
	return entrypointSteps{stderr: stderr.String(), exitCode: verdict.ExitCode, topLevel: events}
}

// assertPrintedRuntimeSubprocessFailure pins the entrypoint result for a
// wrapped subprocess failure outside a lifecycle-owned command: the cause is
// reachable, stderr is the error once, the exit is the child's 1, and the
// journal holds one top-level runtime outcome.
func assertPrintedRuntimeSubprocessFailure(t *testing.T, args []string, err error) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error lost its subprocess cause: %T %v", err, err)
	}
	got := runEntrypointSteps(t, args, err, nil)
	if got.stderr != err.Error()+"\n" {
		t.Fatalf("entrypoint stderr = %q, want exactly one line %q", got.stderr, err.Error()+"\n")
	}
	if got.exitCode != 1 {
		t.Fatalf("entrypoint exit = %d, want the child's 1", got.exitCode)
	}
	if len(got.topLevel) != 1 || got.topLevel[0].Event != "command.outcome" || got.topLevel[0].Kind != "runtime" || got.topLevel[0].Result != "error" {
		t.Fatalf("journal = %#v, want one command.outcome kind runtime", got.topLevel)
	}
}
