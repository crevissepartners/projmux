package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

func TestProcessResumeErrorCodeClassifiesEachRefusal(t *testing.T) {
	local := errors.New("local profile")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"stale", fmt.Errorf("%w: prepared source changed", ErrResumePreparationStale), ResumePreparationStale},
		{"failure", &mutations.Failure{Code: "some-code", Cause: errors.New("c")}, "some-code"},
		{"agent refusal", &AgentCreateRefusal{Reason: "agent-reason", Message: "m"}, "agent-reason"},
		{"profile", &ProfileResumeError{Name: "p"}, "profile-resume-unavailable"},
		{"local profile", fmt.Errorf("wrap: %w", local), "profile-resume-unavailable"},
		{"usage", &createUsageError{message: "bad"}, "resume-request-invalid"},
		{"stale host", fmt.Errorf("x: %w", processhost.ErrStale), "process-resume-refused"},
		{"refused", fmt.Errorf("x: %w", processhost.ErrResumeRefused), "process-resume-refused"},
		{"other", errors.New("boom"), "resume-execution-failed"},
	}
	for _, tc := range cases {
		got := ProcessResumeErrorCode(tc.err, func(err error) bool { return errors.Is(err, local) })
		if got != tc.want {
			t.Errorf("%s: code = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResumePreparationStaleErrorKeepsItsClassification(t *testing.T) {
	err := ResumePreparationStaleError("prepared files changed")
	if !errors.Is(err, ErrResumePreparationStale) || err.Error() != ResumePreparationStale+": prepared files changed" {
		t.Fatalf("unexpected stale error: %v", err)
	}
}
