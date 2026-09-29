package metadata

import (
	"io"
	"os"
	"testing"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

// lockHolderChildEnv turns this test binary into a stand-in Registry lock
// holder: a process whose argv is whatever the test chose, which writes one
// ready byte to stdout once it runs, blocks until its stdin closes, and then
// exits. It runs before the guard because it runs no test and touches nothing
// -- it only has to exist with that argv.
const lockHolderChildEnv = "METADATA_TEST_LOCK_HOLDER_CHILD"

// TestMain runs the package behind liveguard: its test binary links code that
// can reach the live Registry, tmux server, or provider CLIs.
func TestMain(m *testing.M) {
	if os.Getenv(lockHolderChildEnv) == "1" {
		_, _ = os.Stdout.Write([]byte{'\n'})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(liveguard.RunTests(m))
}

// TestLiveMachineGuardHolds pins that TestMain runs this package behind the
// guard.
func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }
