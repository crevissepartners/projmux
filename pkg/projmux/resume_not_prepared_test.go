package projmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// A not-prepared prepare is told apart by errors.Is while the code and the
// error text stay what a caller saw before the sentinel existed.
func TestPrepareAgentResumeRefusalTellsNotPreparedApart(t *testing.T) {
	const text = "process-resume-not-prepared"
	// The public sentinel, the producer's sentinel and the token the web reads
	// today are one text.
	if ErrResumeNotPrepared.Error() != text || mutationruntime.ErrResumeNotPrepared.Error() != text {
		t.Fatalf("sentinel texts %q %q", ErrResumeNotPrepared.Error(), mutationruntime.ErrResumeNotPrepared.Error())
	}
	failure := func(code, message string) error {
		return &mutationruntime.ProcessResumeFailure{Code: code, Cause: errors.New(message), Owner: mutationruntime.ProcessDetachedReply{}.Owner}
	}
	marker := mutationruntime.ResumeNotPreparedDetail(mutationruntime.ErrResumeNotPrepared)
	for name, tc := range map[string]struct {
		err    error
		detail json.RawMessage
		is     bool
	}{
		"marked":            {failure("resume-execution-failed", text), marker, true},
		"no marker":         {failure("resume-execution-failed", text), nil, false},
		"other failure":     {failure("resume-execution-failed", "launch failed"), nil, false},
		"other code marked": {failure("process-resume-refused", "refused"), marker, true},
		"damaged marker":    {failure("resume-execution-failed", text), json.RawMessage(`{"Code":`), false},
	} {
		t.Run(name, func(t *testing.T) {
			got := preparationRefusal(tc.err, "agent-01", tc.detail)
			var refusal *Refusal
			if !errors.As(got, &refusal) {
				t.Fatalf("not a Refusal: %v", got)
			}
			if errors.Is(got, ErrResumeNotPrepared) != tc.is || errors.Is(fmt.Errorf("outer: %w", got), ErrResumeNotPrepared) != tc.is {
				t.Fatalf("errors.Is = %v, want %v", !tc.is, tc.is)
			}
			// Code, text and the process error are what they were without the marker.
			plain := preparationRefusal(tc.err, "agent-01", nil)
			var want, have *ResumeProcessError
			if !errors.As(plain, &want) || !errors.As(got, &have) {
				t.Fatalf("no ResumeProcessError: %v %v", plain, got)
			}
			if got.Error() != plain.Error() || refusal.Code != Code(tc.err.(*mutationruntime.ProcessResumeFailure).Code) || have.Error() != want.Error() || have.Code != want.Code || have.Owner != want.Owner || have.Binding != want.Binding {
				t.Fatalf("marker changed the refusal: %q / %q", got.Error(), plain.Error())
			}
		})
	}
	// The refusal keeps the failure's code and, being its message, the failure's text.
	got := preparationRefusal(failure("resume-execution-failed", text), "agent-01", marker)
	var refusal *Refusal
	if !errors.As(got, &refusal) || refusal.Code != CodeResumeExecutionFailed {
		t.Fatalf("refusal %v code %v", got, refusal)
	}
	// The general Resume reader leaves an error with the marker unchanged.
	base := failure("resume-execution-failed", text)
	if restoreRecipeDetail(base, marker) != base {
		t.Fatal("restoreRecipeDetail changed an error for the marker")
	}
	var process *ResumeProcessError
	if !errors.As(got, &process) || process.Code != CodeResumeExecutionFailed {
		t.Fatalf("errors.As ResumeProcessError: %v", process)
	}
}
