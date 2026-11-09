package projmux

import (
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ProcessAgentPane returns a copy of the one process Pane the Agent agentUID
// owns, found by the internal rule the CLI uses for process Agents: ok is true
// when the Agent owns exactly one Pane whose runtime is process. ambiguous is
// true, with ok false and the zero Pane, when it owns more than one. Both are
// false, with the zero Pane, when it owns none, when the copy fails, for an
// agentUID with no Agent row (the internal rule does not check for the Agent
// row), and for a handle that was not prepared. There is no error, the
// handle's rows are read and not changed, and the caller owns the result.
func (p *PreparedRegistrySnapshot) ProcessAgentPane(agentUID string) (pane core.Pane, ambiguous bool, ok bool) {
	if !p.valid() {
		return core.Pane{}, false, false
	}
	if _, found := p.registry.Agent(agentUID); !found {
		return core.Pane{}, false, false
	}
	found, amb := runtime.ProcessAgentPane(p.registry, agentUID)
	if amb {
		return core.Pane{}, true, false
	}
	out, ok := publicPaneCopy(found, found != nil)
	return out, false, ok
}
