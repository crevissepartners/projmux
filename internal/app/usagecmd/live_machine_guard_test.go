package usagecmd

import (
	"os"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// TestMain runs the package behind liveguard. A Command without journalFn
// resolves diagnostics.DefaultPath under the guard's private XDG_STATE_HOME,
// so the shared guard's audit reports a forgotten journalFn as a default
// per-user write and fails the run.
func TestMain(m *testing.M) {
	os.Exit(liveguard.RunTests(m))
}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }
