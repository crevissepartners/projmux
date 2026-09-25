package tmux

import (
	"os"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// TestMain runs the package behind liveguard: its test binary links code that
// can reach the live Registry, tmux server, or provider CLIs.
func TestMain(m *testing.M) {
	os.Exit(liveguard.RunTests(m))
}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }
