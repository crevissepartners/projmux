package codexinstalled

import (
	"os"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

// TestMain preserves the production self-exec contract when an installed
// conformance test starts a durable generation from the go test process. The
// child inherits the exact operation guard on FD 3; all path, guard-identity,
// and session-leader validation remains in the production supervisor entrypoint.
func TestMain(m *testing.M) {
	if supervisorArgs, ok := durableLaunchSupervisorArgs(os.Args[1:]); ok {
		if err := codexgenerationhost.RunDurableLaunchSupervisor(supervisorArgs); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func durableLaunchSupervisorArgs(args []string) ([]string, bool) {
	if len(args) != 6 || args[0] != "internal" || args[1] != "codex-generation-launch" ||
		args[2] != "--intent" || args[4] != "--guard" {
		return nil, false
	}
	return args[2:], true
}

func TestDurableLaunchSupervisorArgsRecognizesOnlyExactHiddenRoute(t *testing.T) {
	want := []string{"--intent", "/private/intent", "--guard", "/private/intent.guard"}
	got, ok := durableLaunchSupervisorArgs(append([]string{"internal", "codex-generation-launch"}, want...))
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("exact hidden route = (%q, %t), want (%q, true)", got, ok, want)
	}
	for _, args := range [][]string{
		nil,
		{"codex-generation-launch", "--intent", "/private/intent", "--guard", "/private/intent.guard"},
		{"internal", "other", "--intent", "/private/intent", "--guard", "/private/intent.guard"},
		{"internal", "codex-generation-launch", "--guard", "/private/intent.guard", "--intent", "/private/intent"},
		{"internal", "codex-generation-launch", "--intent", "/private/intent", "--guard", "/private/intent.guard", "extra"},
	} {
		if got, ok := durableLaunchSupervisorArgs(args); ok || got != nil {
			t.Fatalf("non-exact argv %q = (%q, %t), want (nil, false)", args, got, ok)
		}
	}
}
