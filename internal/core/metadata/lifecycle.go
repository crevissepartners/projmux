package metadata

import (
	"strings"
	"time"
)

// The lifecycle projection: turning one piece of termination evidence and one
// observed runtime absence into durable Agent and Pane state.
//
// Everything in this file is a *consumer*. Nothing here mints a generation,
// launches a process, kills a runtime object, or decides that anything should be
// started; the whole file runs against a registry and a set of pane uids that a
// caller has already observed to be absent on one exact host. That is the
// property the phase boundary rests on: an observation is not an activation
// authority, so a pass that reconciles a hundred dead panes still starts
// nothing.
//
// The four classifications are not collapsed into two phases and then forgotten.
// The phase answers "is this Agent resumable", which has two useful values, but
// the reason an operator needs in order to act -- an intentional close, a clean
// exit, a crash, and an unexplained disappearance -- has four. Both are stored:
// the phase on status.phase, the evidence on status.lastTermination, and a
// distinct status.reason clause tying them together.

// Termination reason clauses. Each classification gets its own, because
// status.reason is the one line an operator reads next to the phase, and two
// classifications sharing a clause would make the phase the only thing
// separating them -- which is exactly the information the phase collapses.
const (
	// TerminationReasonIntentional is a canonical control action's own record.
	TerminationReasonIntentional = "managed pane was ended by a control action"
	// TerminationReasonNormal is a supervised exit status 0.
	TerminationReasonNormal = "managed process exited with status 0"
	// TerminationReasonKilled is an externally requested hangup with no paired
	// canonical control-action receipt.
	TerminationReasonKilled = "managed process was killed externally"
	// TerminationReasonAbnormal is a supervised non-zero exit or a signal.
	TerminationReasonAbnormal = "managed process exited abnormally"
	// TerminationReasonUnknown is an absence no receipt explains.
	//
	// It is the pre-existing sweep clause, kept verbatim. The sweep applied it
	// to every disappearance including the ones that are now proven; narrowing
	// it to the case it was always literally true of needs no new words.
	TerminationReasonUnknown = "managed pane is no longer live"
)

// TerminationDisposition is the lifecycle decision one classification implies.
//
// It is a total function of the classification and of nothing else. Not of the
// role, not of whether an Agent owns the Pane, not of which host answered: those
// decide *what gets written*, never *what the evidence means*.
type TerminationDisposition struct {
	// Exit is the Agent exit classification, meaningful only for an
	// Agent-owned Pane.
	Exit AgentExit
	// Reason is the status.reason clause for this classification.
	Reason string
}

// DispositionFor maps one classification onto its lifecycle decision.
//
// An unrecognized classification is treated as unknown rather than refused. A
// registry written by a build with a wider vocabulary must still converge here,
// and "this build cannot interpret the evidence" is precisely what unknown
// means.
func DispositionFor(classification TerminationClassification) TerminationDisposition {
	switch classification {
	case TerminationIntentional:
		return TerminationDisposition{Exit: AgentExitDeleted, Reason: TerminationReasonIntentional}
	case TerminationNormal:
		return TerminationDisposition{Exit: AgentExitNormal, Reason: TerminationReasonNormal}
	case TerminationKilled:
		return TerminationDisposition{Exit: AgentExitUnknown, Reason: TerminationReasonKilled}
	case TerminationAbnormal:
		return TerminationDisposition{Exit: AgentExitAbnormal, Reason: TerminationReasonAbnormal}
	default:
		return TerminationDisposition{Exit: AgentExitUnknown, Reason: TerminationReasonUnknown}
	}
}

// TerminationProjectionInput names one absent Pane for projection.
type TerminationProjectionInput struct {
	// PaneUID is the Pane the caller observed to have no runtime object.
	PaneUID string
	// Generation is an optional guard. When it is set it must equal the Pane's
	// current activation generation, so an event describing a materialization
	// the registry has already replaced projects nothing.
	Generation string
	// ObservedAt is when the absence was observed. A zero value means now.
	ObservedAt time.Time
}

// TerminationProjection is what one projection did, or why it did nothing.
type TerminationProjection struct {
	// PaneUID is the projected Pane.
	PaneUID string
	// AgentUID is the Agent released, empty for a shell Pane and for a Pane its
	// Agent no longer binds.
	AgentUID string
	// Classification is the evidence the projection acted on.
	Classification TerminationClassification
	// Phase is the phase the Agent landed in, empty when no Agent moved.
	Phase AgentPhase
	// PaneRetained reports whether the logical Pane resource survived. A shell
	// Pane always survives a runtime disappearance; an Agent's current managed
	// Pane is released with the Agent.
	PaneRetained bool
	// Changed reports whether the registry now differs.
	Changed bool
	// Reason is the diagnostic for a projection that changed nothing.
	Reason string
}

// ProjectTermination is the one lifecycle transition of the exit reconciler.
//
// It consumes the receipt the Pane already stores, or records an unknown one
// when it stores none, and applies the single transition that evidence implies:
//
//   - a shell Pane keeps its logical existence and gains the evidence. A runtime
//     object going away is not a statement about desired topology, and deleting
//     the resource here would make a crashed shell indistinguishable from one
//     the operator asked to remove.
//   - an Agent's *current* managed Pane is unbound by status only: the Agent
//     moves to the phase the evidence implies, drops status.paneRef, and keeps
//     both status.sessionRef and the Pane row. Runtime observation is evidence
//     of disappearance, not canonical delete authority.
//   - an Agent-owned Pane the Agent no longer binds is evidence only. The Agent
//     has since been relaunched onto another Pane, and touching it here would
//     apply a dead process's exit to a live one.
//
// It is idempotent by construction. The unknown receipt it writes is the thing
// that makes it so: a second pass over the same disappearance finds that
// document already stored, RecordTermination reports it a duplicate, and nothing
// is written. An absence with no stored value would be re-projected forever.
//
// It performs no runtime call of any kind and can start nothing.
func (m Mutator) ProjectTermination(reg *Registry, in TerminationProjectionInput) (TerminationProjection, error) {
	const op = "project termination"

	paneUID := strings.TrimSpace(in.PaneUID)
	if paneUID == "" {
		return TerminationProjection{}, inputErr(op, ErrInvalidRegistry, "termination projection must name a pane")
	}
	out := TerminationProjection{PaneUID: paneUID}
	pane, ok := reg.Pane(paneUID)
	if !ok {
		// A Pane the registry no longer holds has already been reconciled, or
		// was removed by a canonical delete. Either way there is nothing left to
		// project onto and nothing was lost.
		out.Reason = "pane " + paneUID + " is not in the registry"
		return out, nil
	}
	current := pane.Status.Activation.Generation
	if guard := strings.TrimSpace(in.Generation); guard != "" && guard != current {
		// The event names a materialization this Pane no longer holds. The
		// current binding is left exactly as it is.
		out.Reason = "event generation " + guard + " is not pane " + paneUID + " current generation " + current
		return out, nil
	}

	observedAt := in.ObservedAt
	if observedAt.IsZero() {
		observedAt = m.clock()().UTC()
	} else {
		observedAt = observedAt.UTC()
	}

	classification, changed, err := m.resolveTerminationEvidence(reg, pane, observedAt)
	if err != nil {
		return TerminationProjection{}, err
	}
	out.Classification = classification
	out.Changed = changed

	// The reason a runtime object went away is recorded on the resource itself,
	// with the same condition and the same message the reconciler's inventory
	// diff writes, so the two writers can never disagree about the wording of a
	// row an operator reads.
	if m.refreshRuntimeCondition(&pane.Status.Conditions, false,
		"no live tmux pane mirrors pane uid "+paneUID, observedAt) {
		out.Changed = true
	}

	disposition := DispositionFor(classification)
	agent, bound := boundAgentFor(reg, *pane)
	if !bound {
		out.PaneRetained = true
		if released, refinable := releasedAgentForTerminationRefinement(reg, *pane); refinable {
			phase, _ := disposition.Exit.Phase()
			if released.Status.Phase != phase || released.Status.Reason != disposition.Reason {
				if !CanTransitionAgent(released.Status.Phase, phase) {
					out.Reason = "agent " + released.Metadata.UID + " cannot refine from " +
						string(released.Status.Phase) + " to " + string(phase)
					if out.Changed {
						reg.UpdatedAt = observedAt
					}
					return out, nil
				}
				transitioned, err := m.TransitionAgent(reg, released.Metadata.UID, phase, disposition.Reason)
				if err != nil {
					return TerminationProjection{}, err
				}
				out.AgentUID = transitioned.Metadata.UID
				out.Phase = transitioned.Status.Phase
				out.Changed = true
			}
		}
		if out.Changed {
			reg.UpdatedAt = observedAt
		}
		return out, nil
	}
	phase, _ := disposition.Exit.Phase()
	if !CanTransitionAgent(agent.Status.Phase, phase) {
		// The closed transition table is never widened by an observation. An
		// Agent that may not reach this phase keeps its phase, its paneRef, and
		// its managed Pane; only the evidence was recorded.
		out.PaneRetained = true
		out.Reason = "agent " + agent.Metadata.UID + " cannot move from " +
			string(agent.Status.Phase) + " to " + string(phase)
		if out.Changed {
			reg.UpdatedAt = observedAt
		}
		return out, nil
	}
	released, err := m.TransitionAgent(reg, agent.Metadata.UID, phase, disposition.Reason)
	if err != nil {
		return TerminationProjection{}, err
	}
	out.AgentUID = released.Metadata.UID
	out.Phase = released.Status.Phase
	out.PaneRetained = true
	out.Changed = true
	return out, nil
}

// resolveTerminationEvidence returns the classification this Pane's stored
// receipt asserts, recording an unknown receipt first when it stores none.
//
// A receipt whose generation is not the Pane's current one is read as no
// evidence at all. RecordTermination cannot write such a receipt and
// RecordPaneActivation clears the old one, so this is unreachable through the
// mutators -- but a hand-edited or downgraded registry is not a reason to apply
// a previous materialization's exit status to the current one.
func (m Mutator) resolveTerminationEvidence(reg *Registry, pane *Pane, observedAt time.Time) (TerminationClassification, bool, error) {
	stored := pane.Status.LastTermination
	if stored != nil && stored.Generation == pane.Status.Activation.Generation &&
		ValidTerminationClassification(stored.Classification) {
		return stored.Classification, false, nil
	}
	receipt := TerminationEvidence{
		Source:         TerminationSourceReconcile,
		Classification: TerminationUnknown,
		ObservedAt:     observedAt,
		PaneUID:        pane.Metadata.UID,
		Generation:     pane.Status.Activation.Generation,
	}
	// The Agent uid is set only when the Agent still binds this Pane. The field
	// is what makes a receipt mirror onto the Agent, and mirroring a superseded
	// Pane's disappearance onto an Agent that has since been relaunched would
	// report a live Agent's evidence as this dead process's. A receipt naming
	// only the Pane is the honest record of a Pane that outlived its binding.
	if agent, bound := boundAgentFor(reg, *pane); bound {
		receipt.AgentUID = agent.Metadata.UID
	}
	outcome, err := m.RecordTermination(reg, receipt)
	if err != nil {
		return "", false, err
	}
	// The classification is read back out of the registry rather than assumed to
	// be the one just offered. A refused offer means something the registry
	// already stores won -- the sticky-intent rule, for one -- and reporting
	// `unknown` while the document says `intentional` would make the projection
	// disagree with the evidence it is projecting.
	if stored := pane.Status.LastTermination; stored != nil &&
		ValidTerminationClassification(stored.Classification) {
		return stored.Classification, outcome.Applied, nil
	}
	return TerminationUnknown, outcome.Applied, nil
}

// boundAgentFor returns the Agent whose *current* managed Pane is pane.
//
// Ownership alone is not enough. An Agent that has been relaunched still owns
// the Pane resources of its previous materializations until they are released,
// and only the one status.paneRef names is the binding a termination can end.
func boundAgentFor(reg *Registry, pane Pane) (*Agent, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil || owner.Kind != KindAgent {
		return nil, false
	}
	agent, ok := reg.Agent(owner.UID)
	if !ok || agent.Status.PaneRef != pane.Metadata.UID {
		return nil, false
	}
	return agent, true
}

// releasedAgentForTerminationRefinement recognizes only the terminal half of
// the fast-exit ordering: the Agent has no current Pane binding, and both the
// Pane and Agent carry the same supervisor evidence for this exact activation.
// A resumed Agent has a non-empty PaneRef and is therefore never returned.
func releasedAgentForTerminationRefinement(reg *Registry, pane Pane) (*Agent, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil || owner.Kind != KindAgent {
		return nil, false
	}
	agent, ok := reg.Agent(owner.UID)
	if !ok || agent.Status.PaneRef != "" {
		return nil, false
	}
	paneReceipt := pane.Status.LastTermination
	agentReceipt := agent.Status.LastTermination
	if paneReceipt == nil || agentReceipt == nil || paneReceipt.Source != TerminationSourceSupervisor ||
		paneReceipt.Generation != pane.Status.Activation.Generation || !sameEvidence(paneReceipt, agentReceipt) {
		return nil, false
	}
	return agent, true
}

// NeedsTerminationProjection reports whether one absent Pane still has work
// left for the projection.
//
// It is the pre-transaction dirty check, and its job is to make a repeat pass
// cost zero write transactions rather than one that happens to change nothing.
// There are exactly two kinds of outstanding work: evidence that has not been
// recorded, and an Agent still bound to the dead Pane that can still move. A
// Pane with its evidence stored and no live binding left is finished.
func NeedsTerminationProjection(reg Registry, paneUID string) bool {
	pane, ok := reg.Pane(paneUID)
	if !ok {
		return false
	}
	stored := pane.Status.LastTermination
	if stored == nil || stored.Generation != pane.Status.Activation.Generation ||
		!ValidTerminationClassification(stored.Classification) {
		return true
	}
	if !hasCondition(pane.Status.Conditions, ConditionMissingRuntime) {
		return true
	}
	agent, bound := boundAgentFor(&reg, *pane)
	if !bound {
		released, refinable := releasedAgentForTerminationRefinement(&reg, *pane)
		if !refinable {
			return false
		}
		disposition := DispositionFor(stored.Classification)
		phase, _ := disposition.Exit.Phase()
		return (released.Status.Phase != phase || released.Status.Reason != disposition.Reason) &&
			CanTransitionAgent(released.Status.Phase, phase)
	}
	phase, _ := DispositionFor(stored.Classification).Exit.Phase()
	return CanTransitionAgent(agent.Status.Phase, phase)
}

// hasCondition reports whether conditions already record conditionType.
func hasCondition(conditions []Condition, conditionType string) bool {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return true
		}
	}
	return false
}
