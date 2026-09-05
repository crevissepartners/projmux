package claude

import (
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Process reads kernel birth identity. No command line, environment, transcript,
// or provider registration store is inspected.
func Process(pid int) (metadata.ProcessIdentity, int, error) {
	return localipc.Process(pid)
}
