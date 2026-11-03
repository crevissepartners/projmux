package runtime

import (
	"errors"
	"fmt"
	"testing"
)

func TestProcessCreateErrorCodeNamesFailedPostCreateHook(t *testing.T) {
	hook := &ProcessPostCreateHookFailure{Cause: errors.New("exit status 7")}
	if got, want := hook.Error(), "process-post-create-hook-failed: exit status 7"; got != want {
		t.Fatalf("message %q, want %q", got, want)
	}
	refusal := &AgentCreateRefusal{Reason: "reservation-stale", Message: "stale"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"hook", hook, "process-post-create-hook-failed"},
		{"wrapped hook", fmt.Errorf("rollback: %w", errors.Join(hook, errors.New("deadline"))), "process-post-create-hook-failed"},
		{"typed refusal", refusal, "reservation-stale"},
		{"refusal over hook", errors.Join(refusal, hook), "reservation-stale"},
		{"other failure", errors.New("provider failed"), "create-execution-failed"},
		// The code comes from the type, never from the sentence.
		{"hook-like text", errors.New("process-post-create-hook-failed: looks alike"), "create-execution-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProcessCreateErrorCode(tc.err); got != tc.want {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
		})
	}
	if !errors.Is(hook, hook.Cause) {
		t.Fatal("the hook cause must stay reachable through Unwrap")
	}
}
