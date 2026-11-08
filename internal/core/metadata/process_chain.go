package metadata

// ProcessChainMaxSteps bounds a caller's process ancestry: a chain longer than
// this is not evidence of who acted. It is the one definition the ancestry
// walk, the CLI creator provenance and the public SDK share.
const ProcessChainMaxSteps = 64

// ProcessChainSkip is the stable reason token of a process-chain judgment that
// refused its evidence. The empty value is no skip.
type ProcessChainSkip string

// The process-chain skip vocabulary is closed. The spelling is public.
const (
	// ProcessChainSkipUnobservable: the walk was partial, empty or over the
	// bound while a chain member was a recorded provider child.
	ProcessChainSkipUnobservable ProcessChainSkip = "process-chain-unobservable"
	// ProcessChainSkipIdentityMismatch: a chain member carried a recorded
	// provider child pid with a different or foreign identity.
	ProcessChainSkipIdentityMismatch ProcessChainSkip = "process-child-identity-mismatch"
	// ProcessChainSkipBindingMismatch: the recorded binding is not the current,
	// running, matching Agent and Pane.
	ProcessChainSkipBindingMismatch ProcessChainSkip = "process-binding-mismatch"
	// ProcessChainSkipAmbiguous: more than one Pane matched the same member.
	ProcessChainSkipAmbiguous ProcessChainSkip = "process-child-ambiguous"
)

// ResolveProcessChainActor identifies the nearest live provider child in the
// caller's process ancestry, bound to exactly one current Agent and Pane.
// chain is the caller's ancestry nearest first, chainErr the error of the walk
// that read it, and uid the caller's user id. A skip token describes refused
// evidence; a partial walk never proves an actor. The result is one of: found
// (agentUID and paneUID set, skip empty), skipped (both empty, skip set) or
// nothing to observe (all empty).
func (r *Registry) ResolveProcessChainActor(chain []ProcessIdentity, chainErr error, uid int) (agentUID, paneUID string, skip ProcessChainSkip) {
	if r == nil {
		return "", "", ""
	}
	working := r
	// A partial walk is not evidence: no guessed ancestry on read failure.
	if chainErr != nil || len(chain) == 0 || len(chain) > ProcessChainMaxSteps {
		// A partially observed provider candidate deserves a stable diagnostic;
		// an unrelated shell still has no creator claim to report.
		for _, identity := range chain {
			for _, pane := range working.Panes {
				if activation := pane.Status.Activation.Process; activation != nil && activation.Child.PID == identity.PID {
					return "", "", ProcessChainSkipUnobservable
				}
			}
		}
		return "", "", ""
	}
	var rejected ProcessChainSkip
	for _, identity := range chain {
		type match struct{ agentUID, paneUID string }
		var matches []match
		for _, pane := range working.Panes {
			activation := pane.Status.Activation.Process
			if activation == nil || activation.Child.PID != identity.PID {
				continue
			}
			if !identity.Valid() || int64(identity.OwnerUID) != int64(uid) || activation.Child != identity {
				rejected = ProcessChainSkipIdentityMismatch
				continue
			}
			binding := activation.Binding
			current, _, ok := working.CurrentProcessActivation(binding)
			agent, found := working.Agent(binding.AgentUID)
			_, missing := pane.HasCondition(ConditionMissingRuntime)
			if !ok || !found || missing || agent.Status.Phase != PhaseRunning ||
				binding.PaneUID != pane.Metadata.UID || current != *activation ||
				pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != binding ||
				pane.Status.ProcessSession.Provider != agent.Spec.Provider {
				rejected = ProcessChainSkipBindingMismatch
				continue
			}
			matches = append(matches, match{binding.AgentUID, binding.PaneUID})
		}
		if len(matches) > 1 {
			return "", "", ProcessChainSkipAmbiguous
		}
		if len(matches) == 1 {
			return matches[0].agentUID, matches[0].paneUID, ""
		}
	}
	return "", "", rejected
}
