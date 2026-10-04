package app

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The owner supplies three private pipe descriptors. This is a single-child
// internal entry, without a listener, resource selector or adoption operation.
type processHostSupervisorCommand struct{}

func (*processHostSupervisorCommand) Run(args []string, _, _ io.Writer) error {
	if len(args) != 0 {
		return usageError("internal: process supervisor accepts no arguments")
	}
	for fd := 3; fd <= 5; fd++ {
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return fmt.Errorf("process supervisor requires inherited pipe descriptor %d", fd)
		}
	}
	return processhost.ServeSupervisor(os.NewFile(3, "owner-lifetime"), os.NewFile(4, "launch-specification"), os.NewFile(5, "exit-status"))
}
