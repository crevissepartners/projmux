package mutations

import (
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"os"
	"testing"
)

func TestMain(m *testing.M)             { os.Exit(liveguard.RunTests(m)) }
func TestLiveMachineGuard(t *testing.T) { liveguard.RequireActive(t) }
