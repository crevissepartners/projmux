package agentmessages

import (
	"os"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

func TestMain(m *testing.M) {
	os.Exit(liveguard.RunTests(m))
}

func TestLiveMachineGuardHolds(t *testing.T) {
	liveguard.RequireActive(t)
}
