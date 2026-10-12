package runtime

import (
	"errors"
	"os"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestResumeSettingsPreservesLazyPathFailure(t *testing.T) {
	failure := errors.New("config paths unavailable")
	calls := 0
	paths := func() (config.Paths, error) { calls++; return config.Paths{}, failure }
	if _, err := ResolveAgentSettingsWithPaths(paths, "antigravity", nil, AgentSettingsRequest{}); err != nil || calls != 0 {
		t.Fatalf("unlayered provider read paths: calls=%d err=%v", calls, err)
	}
	launch, err := ResolveAgentSettingsWithPaths(paths, "claude", map[string]string{metadata.AnnotationAgentPersona: "reviewer"}, AgentSettingsRequest{})
	if err != nil || !errors.Is(launch.InstructionsErr, failure) || calls != 1 {
		t.Fatalf("instructions fallback changed: %+v calls=%d err=%v", launch, calls, err)
	}
	_, err = ResolveAgentSettingsWithPaths(paths, "claude", map[string]string{metadata.AnnotationAgentProfile: "review"}, AgentSettingsRequest{})
	var profile *ProfileResumeError
	if !errors.As(err, &profile) || profile.Reason != "profile-not-found" {
		t.Fatalf("profile path error lost refusal: %v", err)
	}
}

func TestMain(m *testing.M)                  { os.Exit(liveguard.RunTests(m)) }
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }
