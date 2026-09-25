package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

type testExitCoder struct {
	code  int
	cause error
}

func (e testExitCoder) Error() string { return "already displayed" }
func (e testExitCoder) ExitCode() int { return e.code }
func (e testExitCoder) Unwrap() error { return e.cause }

// subprocessExitError returns a real bare *exec.ExitError with exit code 3, so
// a test can tell its code apart from the default exit code 1.
func subprocessExitError(t *testing.T) *exec.ExitError {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found: %v", err)
	}
	runErr := exec.Command(sh, "-c", "exit 3").Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || runErr != error(exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("sh -c 'exit 3' = %v, want a bare *exec.ExitError with code 3", runErr)
	}
	return exitErr
}

// TestClassifyFailureDecidesPrintExitCodeAndKind pins the one failure verdict
// the entrypoint, focus, and the diagnostics journal share. Every field of
// every row is compared, so flipping any branch of the rule fails a row.
func TestClassifyFailureDecidesPrintExitCodeAndKind(t *testing.T) {
	t.Parallel()
	exitErr := subprocessExitError(t)
	wrapped := fmt.Errorf("switch tmux session %q: %w", "demo", exitErr)
	tests := []struct {
		name  string
		err   error
		usage bool
		want  Failure
	}{
		{name: "nil", want: Failure{}},
		{name: "plain error", err: errors.New("boom"), want: Failure{Print: true, ExitCode: 1, Kind: FailureRuntime}},
		{name: "usage error", err: errors.New("bad usage"), usage: true, want: Failure{Print: true, ExitCode: 2, Kind: FailureUsage}},
		{name: "wrapped exit error", err: wrapped, want: Failure{Print: true, ExitCode: 3, Kind: FailureRuntime}},
		{name: "doubly wrapped exit error", err: fmt.Errorf("attach: %w", wrapped), want: Failure{Print: true, ExitCode: 3, Kind: FailureRuntime}},
		{name: "bare exit error", err: exitErr, want: Failure{ExitCode: 3, Kind: FailureExit}},
		{name: "app coder", err: testExitCoder{code: 4}, want: Failure{ExitCode: 4, Kind: FailureExit}},
		{name: "wrapped app coder", err: fmt.Errorf("outer: %w", testExitCoder{code: 4}), want: Failure{ExitCode: 4, Kind: FailureExit}},
		{name: "app coder wrapping exit error", err: testExitCoder{code: 5, cause: wrapped}, want: Failure{ExitCode: 5, Kind: FailureExit}},
		// A coder's own code wins over the usage exit code, while the journal
		// still names the usage; both halves predate the shared rule.
		{name: "usage error with app coder", err: testExitCoder{code: 6}, usage: true, want: Failure{ExitCode: 6, Kind: FailureUsage}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyFailure(tt.err, tt.usage); got != tt.want {
				t.Fatalf("ClassifyFailure(%v, %v) = %+v, want %+v", tt.err, tt.usage, got, tt.want)
			}
		})
	}
}
