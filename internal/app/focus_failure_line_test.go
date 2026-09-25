package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// focusSubprocessExitError returns a real *exec.ExitError with exit code 3.
func focusSubprocessExitError(t *testing.T) *exec.ExitError {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 3").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("subprocess error = %v, want *exec.ExitError with code 3", err)
	}
	return exitErr
}

// entrypointFailureLine is a local copy of cmd/projmux executeCLI's stderr
// rule: it returns what the entrypoint itself prints for the error a command
// returned.
func entrypointFailureLine(err error) string {
	if err == nil {
		return ""
	}
	var coded interface {
		error
		ExitCode() int
	}
	if errors.As(err, &coded) {
		if exitErr, ok := coded.(*exec.ExitError); ok && error(exitErr) != err {
			return err.Error() + "\n"
		}
		return ""
	}
	return err.Error() + "\n"
}

// TestCLIPrintsFailureMirrorsExecuteCLIRule pins the dispatch predicate to the
// entrypoint's print rule over every error kind focus can return, so a drift
// in either side is caught here.
func TestCLIPrintsFailureMirrorsExecuteCLIRule(t *testing.T) {
	t.Parallel()
	exitErr := focusSubprocessExitError(t)
	wrapped := fmt.Errorf("focus: list-sessions: %w", exitErr)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "plain error", err: errors.New("boom"), want: true},
		{name: "usage error", err: usageError("bad usage"), want: true},
		{name: "wrapped exit error", err: wrapped, want: true},
		{name: "doubly wrapped exit error", err: fmt.Errorf("outer: %w", wrapped), want: true},
		{name: "bare exit error", err: exitErr, want: false},
		{name: "focus exit error", err: focusExitError{code: focusExitNotResolved, err: errors.New("gone")}, want: false},
		{name: "focus exit error wrapping exit error", err: focusExitError{code: focusExitNotResolved, err: wrapped}, want: false},
		{name: "wrapped focus exit error", err: fmt.Errorf("outer: %w", focusExitError{code: focusExitNotResolved, err: wrapped}), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cliPrintsFailure(tt.err)
			if got != tt.want {
				t.Fatalf("cliPrintsFailure = %v, want %v", got, tt.want)
			}
			if entrypoint := entrypointFailureLine(tt.err) != ""; entrypoint != got {
				t.Fatalf("cliPrintsFailure = %v, but the executeCLI rule prints = %v", got, entrypoint)
			}
		})
	}
}

// TestFocusDispatchFailureReachesStderrOnce drives each focus failure kind
// through Run and adds what the entrypoint prints for the returned error: the
// total stderr is exactly one line with the error's own text, and the returned
// error keeps its exit code class.
func TestFocusDispatchFailureReachesStderrOnce(t *testing.T) {
	t.Parallel()
	exitErr := focusSubprocessExitError(t)
	const (
		codeNone = -1 // no exit coder: the entrypoint exits 1
	)
	tests := []struct {
		name     string
		args     []string
		respond  func(args []string) ([]byte, error)
		registry func() (coremetadata.Registry, error)
		wantLine string
		wantCode int
		// wantNotResolved is true when the returned error is focusExitError.
		wantNotResolved bool
	}{
		{
			name: "execute plain error",
			args: []string{"--target", "workspace"},
			respond: func(args []string) ([]byte, error) {
				return nil, errors.New("boom")
			},
			wantLine: "focus: list-sessions: boom",
			wantCode: codeNone,
		},
		{
			name: "execute wrapped exit error",
			args: []string{"--target", "workspace"},
			respond: func(args []string) ([]byte, error) {
				return nil, exitErr
			},
			wantLine: "focus: list-sessions: exit status 3",
			wantCode: 3,
		},
		{
			name:            "execute unresolved session",
			args:            []string{"--target", "workspace"},
			respond:         func(args []string) ([]byte, error) { return nil, nil },
			wantLine:        `focus: session "workspace" not found and no fallback matched`,
			wantCode:        focusExitNotResolved,
			wantNotResolved: true,
		},
		{
			name: "execute unresolved window id wrapping exit error",
			args: []string{"--target", "workspace:@5"},
			respond: func(args []string) ([]byte, error) {
				switch {
				case containsArg(args, "list-sessions"):
					return []byte("100" + focusFieldSeparator + "workspace" + focusFieldSeparator + "1\n"), nil
				case containsArg(args, "list-clients"):
					return []byte("/dev/pts/0" + focusFieldSeparator + "workspace\n"), nil
				case containsArg(args, "select-window"):
					return nil, exitErr
				}
				return nil, nil
			},
			wantLine:        `focus: select-window "workspace:@5": exit status 3`,
			wantCode:        focusExitNotResolved,
			wantNotResolved: true,
		},
		{
			name:     "resolve plain error",
			args:     []string{"project", "uid:proj-1"},
			wantLine: "focus: resource registry loader is not configured",
			wantCode: codeNone,
		},
		{
			name:     "resolve wrapped exit error",
			args:     []string{"project", "uid:proj-1"},
			registry: func() (coremetadata.Registry, error) { return coremetadata.Registry{}, exitErr },
			wantLine: "focus: read resource registry: exit status 3",
			wantCode: 3,
		},
		{
			name: "resolve unresolved window",
			args: []string{"window", "main", "-p", "workspace"},
			respond: func(args []string) ([]byte, error) {
				return nil, errors.New("gone")
			},
			wantCode:        focusExitNotResolved,
			wantNotResolved: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := newFocusTestCommand(&focusFakeRunner{respond: tt.respond}, nil, nil)
			cmd.loadRegistry = tt.registry
			var stdout, stderr bytes.Buffer
			err := cmd.Run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("Run returned nil, want failure (stdout=%q stderr=%q)", stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}

			total := stderr.String() + entrypointFailureLine(err)
			if strings.Count(total, "\n") != 1 || total != err.Error()+"\n" {
				t.Fatalf("total stderr = %q (dispatch %q), want exactly one line %q", total, stderr.String(), err.Error()+"\n")
			}
			if tt.wantLine != "" && total != tt.wantLine+"\n" {
				t.Fatalf("total stderr = %q, want %q", total, tt.wantLine+"\n")
			}

			var focusExit focusExitError
			if got := errors.As(err, &focusExit); got != tt.wantNotResolved {
				t.Fatalf("errors.As(focusExitError) = %v, want %v (err=%v)", got, tt.wantNotResolved, err)
			}
			var coded interface{ ExitCode() int }
			switch {
			case tt.wantCode == codeNone:
				if errors.As(err, &coded) {
					t.Fatalf("returned error has exit coder with code %d, want none", coded.ExitCode())
				}
			case !errors.As(err, &coded) || coded.ExitCode() != tt.wantCode:
				t.Fatalf("returned error exit code class wrong: err=%v, want code %d", err, tt.wantCode)
			}
		})
	}
}

// TestFocusDispatchJSONFailureKeepsStderrQuiet pins the JSON execute path:
// the failure travels as ok:false on stdout, dispatch writes nothing to
// stderr, and the returned error is unchanged.
func TestFocusDispatchJSONFailureKeepsStderrQuiet(t *testing.T) {
	t.Parallel()
	exitErr := focusSubprocessExitError(t)
	tests := []struct {
		name            string
		respond         func(args []string) ([]byte, error)
		wantReason      string
		wantNote        string
		wantNotResolved bool
	}{
		{
			name:       "plain error",
			respond:    func(args []string) ([]byte, error) { return nil, errors.New("boom") },
			wantReason: "dispatch-failed",
			wantNote:   "focus: list-sessions: boom",
		},
		{
			name:       "wrapped exit error",
			respond:    func(args []string) ([]byte, error) { return nil, exitErr },
			wantReason: "dispatch-failed",
			wantNote:   "focus: list-sessions: exit status 3",
		},
		{
			name:            "unresolved session",
			respond:         func(args []string) ([]byte, error) { return nil, nil },
			wantReason:      "session-unresolved",
			wantNote:        `focus: session "workspace" not found and no fallback matched`,
			wantNotResolved: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := newFocusTestCommand(&focusFakeRunner{respond: tt.respond}, nil, nil)
			var stdout, stderr bytes.Buffer
			err := cmd.Run([]string{"--target", "workspace", "--json"}, &stdout, &stderr)
			if err == nil || err.Error() != tt.wantNote {
				t.Fatalf("Run err = %v, want %q", err, tt.wantNote)
			}
			if stderr.Len() != 0 {
				t.Fatalf("dispatch stderr = %q, want empty in JSON mode", stderr.String())
			}
			var res focusResult
			if decodeErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); decodeErr != nil {
				t.Fatalf("decode JSON: %v (raw=%q)", decodeErr, stdout.String())
			}
			if res.OK || res.Reason != tt.wantReason || res.Note != tt.wantNote {
				t.Fatalf("result = %#v, want ok=false reason=%q note=%q", res, tt.wantReason, tt.wantNote)
			}
			var focusExit focusExitError
			if got := errors.As(err, &focusExit); got != tt.wantNotResolved {
				t.Fatalf("errors.As(focusExitError) = %v, want %v", got, tt.wantNotResolved)
			}
		})
	}
}

// TestFocusDispatchJSONResolveFailureKeepsHistoricalLine pins the JSON resolve
// path to its historical output: dispatch always writes the failure line to
// stderr, stdout stays empty, and the returned error is unchanged. A plain
// error therefore still reaches stderr twice in total; JSON output is out of
// scope for the single-line rule.
func TestFocusDispatchJSONResolveFailureKeepsHistoricalLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		args            []string
		respond         func(args []string) ([]byte, error)
		wantLine        string
		wantNotResolved bool
	}{
		{
			name:     "plain error",
			args:     []string{"project", "uid:proj-1", "--json"},
			wantLine: "focus: resource registry loader is not configured",
		},
		{
			name: "unresolved window",
			args: []string{"window", "main", "-p", "workspace", "--json"},
			respond: func(args []string) ([]byte, error) {
				return nil, errors.New("gone")
			},
			wantNotResolved: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := newFocusTestCommand(&focusFakeRunner{respond: tt.respond}, nil, nil)
			var stdout, stderr bytes.Buffer
			err := cmd.Run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("Run returned nil, want failure")
			}
			if tt.wantLine != "" && err.Error() != tt.wantLine {
				t.Fatalf("Run err = %q, want %q", err.Error(), tt.wantLine)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != err.Error()+"\n" {
				t.Fatalf("dispatch stderr = %q, want the historical line %q", stderr.String(), err.Error()+"\n")
			}
			var focusExit focusExitError
			if got := errors.As(err, &focusExit); got != tt.wantNotResolved {
				t.Fatalf("errors.As(focusExitError) = %v, want %v (err=%v)", got, tt.wantNotResolved, err)
			}
			var coded interface{ ExitCode() int }
			if tt.wantNotResolved {
				if !errors.As(err, &coded) || coded.ExitCode() != focusExitNotResolved {
					t.Fatalf("returned error exit code class wrong: err=%v, want code %d", err, focusExitNotResolved)
				}
			} else if errors.As(err, &coded) {
				t.Fatalf("returned error has exit coder with code %d, want none", coded.ExitCode())
			}
		})
	}
}
