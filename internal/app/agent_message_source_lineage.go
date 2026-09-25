package app

import (
	"fmt"
	"io"
	"os"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// claudeSourceSessionEnv is read only to describe the caller in the warning.
// A resumed session shares the registered session id, so it never decides
// anything.
const claudeSourceSessionEnv = "CLAUDE_CODE_SESSION_ID"

// warnForeignClaudeSource reports an accepted send that names a Claude source
// Agent while its caller does not descend from that Agent's registered Claude
// process: typically a `claude --resume` in another terminal or a background
// session left after `/exit`. The envelope is filled from the Registry, so
// this ancestry is the only thing that tells the two senders apart. It prints
// one stderr line and records one journal event, and changes nothing about the
// send. A lineage that cannot be read warns nothing.
func (c *agentCommand) warnForeignClaudeSource(stderr io.Writer, registry *coremetadata.Registry,
	source coremetadata.Agent, route coremetadata.AgentRouteRef,
) {
	if source.Spec.Provider != string(aiprovider.Claude) || registry == nil {
		return
	}
	pane, ok := registry.Pane(route.PaneUID)
	if !ok || pane.Status.Activation.Claude == nil || !pane.Status.Activation.Claude.Process.Valid() {
		return
	}
	binding := pane.Status.Activation.Claude
	callerPID, foreign := c.claudeSourceCallerForeign(binding.Process)
	if !foreign {
		return
	}
	session := binding.RegistrationSessionID
	if session == "" {
		session = "-"
	}
	env := "unset"
	if c.lookupEnv != nil {
		if value := c.lookupEnv(claudeSourceSessionEnv); value != "" {
			env = fmt.Sprintf("%q", value)
		}
	}
	_, _ = fmt.Fprintf(stderr, "agent message send: warning: source Agent uid:%s is registered to Claude session %s, "+
		"but this caller (pid %d, %s=%s) does not descend from that session's Claude process; the send proceeds unchanged. "+
		"Another Claude session, such as a `claude --resume` in another terminal or a background session left by /exit \"Move to background\", is sending as this Agent; "+
		"end that session, or send from the registered session\n",
		source.Metadata.UID, session, callerPID, claudeSourceSessionEnv, env)
	c.messageDiagnostics.RecordForeignSource(source.Metadata.UID, route.PaneUID)
}

// claudeSourceCallerForeign reports whether this process provably does not
// descend from the registered provider. Our own identity, the provider still
// being the recorded birth, and every link of the walk must all be readable;
// otherwise the answer is unknown and reported as not foreign.
func (c *agentCommand) claudeSourceCallerForeign(provider coremetadata.ProcessIdentity) (int, bool) {
	read := c.messageProcess
	if read == nil {
		read = localipc.Process
	}
	pid := os.Getpid()
	if c.messageCallerPID != nil {
		pid = c.messageCallerPID()
	}
	caller, _, err := read(pid)
	if err != nil {
		return pid, false
	}
	if actual, _, err := read(provider.PID); err != nil || actual != provider {
		return pid, false
	}
	descendant, err := claudeProviderLineage(read, caller, provider)
	return pid, err == nil && !descendant
}
