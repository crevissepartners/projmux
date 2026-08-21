package app

import (
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/controller"
)

// TestControlTargetConvergenceUsesNoSessionNameOrCWDInference is the Phase 11
// negative audit. The plan receives a literal declaration and exact mirrors;
// it has no workspace/name/display/command field from which it could infer a
// ControlSession identity.
func TestControlTargetConvergenceUsesNoSessionNameOrCWDInference(t *testing.T) {
	state := controller.ControlTargetState{}
	_ = state // compile-time inventory: adding evidence fields requires review here.
	data, err := os.ReadFile("../core/controller/control_target.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"CWD", "DisplayName", "Command", "ProjectByRoot"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("control-target planner contains forbidden inference evidence %q", forbidden)
		}
	}
}
