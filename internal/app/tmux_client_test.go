package app

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

func TestDefaultPostCreateRunnerAttachesForProjectLocalOptInWithoutGlobalHook(t *testing.T) {
	t.Parallel()

	runner := defaultPostCreateRunner("", true)
	if runner == nil {
		t.Fatal("defaultPostCreateRunner returned nil, want project-local runner")
	}
	if runner.HookPath != "" {
		t.Fatalf("HookPath = %q, want empty", runner.HookPath)
	}
	if !runner.ProjectHooksEnabled {
		t.Fatal("ProjectHooksEnabled = false, want true")
	}
	if runner.Timeout != hooks.DefaultPostCreateTimeout {
		t.Fatalf("Timeout = %s, want %s", runner.Timeout, hooks.DefaultPostCreateTimeout)
	}
}

func TestDefaultPostCreateRunnerSkipsWhenNoHookSources(t *testing.T) {
	t.Parallel()

	if runner := defaultPostCreateRunner("", false); runner != nil {
		t.Fatalf("defaultPostCreateRunner = %#v, want nil", runner)
	}
}
