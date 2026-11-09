package projmux

import runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

// RelaunchCurrentHost returns the host a plain relaunch of the Agent agentUID
// launches on, exactly as the CLI relaunch decides it: "process" when exactly
// one Pane the Agent owns is a process Pane, else "tmux" (no process Pane, or
// more than one). It returns "" for a handle that was not prepared and for an
// agentUID with no Agent row; the internal rule does not check for the Agent
// row. There is no error, and the handle's rows are read and not changed.
func (p *PreparedRegistrySnapshot) RelaunchCurrentHost(agentUID string) string {
	if !p.valid() {
		return ""
	}
	if _, ok := p.registry.Agent(agentUID); !ok {
		return ""
	}
	return runtime.RelaunchCurrentHost(p.registry, agentUID)
}
