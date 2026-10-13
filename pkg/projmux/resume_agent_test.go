package projmux

import (
	"errors"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"

	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func TestResumeRefusalReasons(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  Code
	}{{&mutations.Failure{Code: string(CodeResumePhaseNotResumable), Cause: errors.New("phase")}, CodeResumePhaseNotResumable}, {&mutations.Failure{Code: string(CodeResumeRequestInvalid), Cause: errors.New("input")}, CodeResumeRequestInvalid}, {&mutationruntime.ProfileResumeError{Name: "gone", Reason: "profile-not-found"}, CodeProfileResumeUnavailable}, {&mutationruntime.AgentCreateRefusal{Reason: string(CodeReplyOnlyLaunchFixed)}, CodeReplyOnlyLaunchFixed}, {errors.New("split failed"), CodeResumeExecutionFailed}} {
		err := resumeRefusal(tc.cause)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code || !errors.Is(err, tc.cause) {
			t.Fatalf("cause=%v err=%v code=%v", tc.cause, err, tc.code)
		}
	}
}

func TestResumeInputFieldsNameOnlyExistingRequestFields(t *testing.T) {
	a := &metadata.Agent{Spec: metadata.AgentSpec{Provider: "antigravity"}, Status: metadata.AgentStatus{Phase: metadata.PhaseOffline}}
	err := resumeRefusal(mutationruntime.ValidateResumeAgent("agent resume", a, "sonnet", "", false, mutationruntime.AgentInputFields{Prefix: "ResumeAgentRequest"}))
	if err == nil || !strings.Contains(err.Error(), "ResumeAgentRequest.Model") || strings.Contains(err.Error(), "ResumeAgentRequest.Provider") || strings.Contains(err.Error(), "--provider") {
		t.Fatal(err)
	}
}
