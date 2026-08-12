package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/app"
)

type testExitError struct{ code int }

func (e testExitError) Error() string { return "already displayed" }
func (e testExitError) ExitCode() int { return e.code }

func TestExecuteCLIPreservesOutputAndExitSemanticsAndRecordsOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "success", wantCode: 0},
		{name: "runtime", err: errors.New("runtime failed"), wantCode: 1, wantStderr: "runtime failed\n"},
		{name: "usage", err: &app.UsageError{Message: "bad usage"}, wantCode: 2, wantStderr: "bad usage\n"},
		{name: "exit coder suppresses default stderr", err: testExitError{code: 2}, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			records := 0
			var recorded error
			code := executeCLI(func() error { return tt.err }, func(err error) {
				records++
				recorded = err
			}, &stderr)
			if code != tt.wantCode || stderr.String() != tt.wantStderr || records != 1 || !errors.Is(recorded, tt.err) {
				t.Fatalf("code=%d stderr=%q records=%d recorded=%v", code, stderr.String(), records, recorded)
			}
		})
	}
}
