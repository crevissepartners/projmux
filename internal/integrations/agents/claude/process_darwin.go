package claude

import (
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Process uses the kernel's absolute process birth time on both Darwin targets.
func Process(pid int) (metadata.ProcessIdentity, int, error) {
	return localipc.Process(pid)
}
