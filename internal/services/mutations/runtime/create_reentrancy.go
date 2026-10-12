package runtime

import (
	"strings"
)

// CreateOperationEnvironment is a session-scoped lease carried by canonical
// create mutations. Generated tmux lifecycle hooks inspect it before opening
// the registry: a hook caused by the create that already owns the registry lock
// returns immediately, while an ordinary hook keeps its synchronous contract.
const CreateOperationEnvironment = "__projmux_create_operation"

func SessionEnvironmentValue(output, name string) string {
	prefix := strings.TrimSpace(name) + "="
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return after
		}
	}
	return ""
}
