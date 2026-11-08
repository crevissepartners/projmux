package projmux

import (
	"os"

	"github.com/crevissepartners/projmux/internal/core/metadata"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ProcessChainSkip is the stable reason token of a process-chain judgment that
// refused its evidence. The empty value is no skip. The set is closed and its
// spelling is public; the sentence that explains a token is the caller's.
type ProcessChainSkip string

const (
	// ProcessChainSkipUnobservable: the ancestry was partial, empty or over
	// ProcessAncestryMaxSteps while a chain member was a recorded provider child.
	ProcessChainSkipUnobservable ProcessChainSkip = ProcessChainSkip(metadata.ProcessChainSkipUnobservable)
	// ProcessChainSkipIdentityMismatch: a chain member carried a recorded
	// provider child pid with a different or foreign identity.
	ProcessChainSkipIdentityMismatch ProcessChainSkip = ProcessChainSkip(metadata.ProcessChainSkipIdentityMismatch)
	// ProcessChainSkipBindingMismatch: the recorded binding is not the current,
	// running, matching Agent and Pane.
	ProcessChainSkipBindingMismatch ProcessChainSkip = ProcessChainSkip(metadata.ProcessChainSkipBindingMismatch)
	// ProcessChainSkipAmbiguous: more than one Pane matched the same chain member.
	ProcessChainSkipAmbiguous ProcessChainSkip = ProcessChainSkip(metadata.ProcessChainSkipAmbiguous)
)

// ProcessChainAgent is the Agent and Pane a process ancestry is bound to.
type ProcessChainAgent struct {
	AgentUID string
	PaneUID  string
}

// ResolveProcessChainAgent finds the single process Agent whose provider child
// is the nearest valid matching provider child in chain, using this prepared
// Registry. A nearer recorded child that fails the identity or binding check
// does not stop the search; that failure becomes a skip only when no farther
// valid child is found.
// chain is the caller's ancestry, nearest first, as ReadProcessAncestry returns
// it, and chainErr is the error of that same call, passed through unchanged. The
// caller's user id is read by the SDK (os.Getuid).
//
// The result is one of three, distinguished so a caller never confuses them:
//   - found: ProcessChainAgent.AgentUID and PaneUID are set and the skip is empty;
//   - skipped: the Agent is empty and the skip is one of the ProcessChainSkip
//     tokens, evidence that was refused (a partial walk never proves an actor);
//   - none: the Agent and the skip are both empty, there is nothing to observe,
//     which is also the answer for a handle that was not prepared.
//
// The judgment is the one the CLI uses to record a creator. The handle is a
// snapshot: to confirm the Agent's Pane afterwards, read Snapshot and check
// that the Agent's PaneRef names PaneUID.
func (p *PreparedRegistrySnapshot) ResolveProcessChainAgent(chain []core.ProcessIdentity, chainErr error) (ProcessChainAgent, ProcessChainSkip) {
	if !p.valid() {
		return ProcessChainAgent{}, ""
	}
	internal := make([]metadata.ProcessIdentity, len(chain))
	for i := range chain {
		internal[i] = metadata.ProcessIdentity(chain[i])
	}
	agentUID, paneUID, skip := p.registry.ResolveProcessChainActor(internal, chainErr, os.Getuid())
	if skip != "" {
		return ProcessChainAgent{}, ProcessChainSkip(skip)
	}
	return ProcessChainAgent{AgentUID: agentUID, PaneUID: paneUID}, ""
}
