package metadata

import (
	"strings"
	"time"
)

// Agent runtime linkage is what connects a live tmux pane that is running an AI
// agent to the Agent resource whose runtime it is.
//
// Three separate observations forced this to exist as one unit.
//
//  1. The pane import path mints a *shell* Pane for every orphan live pane,
//     whatever it is running, and no Agent at all. So a machine full of running
//     agents reported `Role: shell` panes and an empty AGENT column.
//  2. The Agent resources that did exist carried no status.paneRef, because
//     nothing ever wrote one for a pane it had not itself launched. So
//     `get agents` showed conversations that had finished and hid the ones that
//     were running.
//  3. Agent status was inherited from the owning Window, so every Agent under a
//     live Window read live regardless of its own runtime.
//
// Splitting them is not possible in a meaningful way: observing an Agent's own
// runtime with no paneRef to observe reports every Agent offline, and filling
// paneRef for Agents that were never created reaches only the minority of
// running agents that happen to have one.
//
// # What counts as evidence that a live pane is an agent
//
// `pane_current_command == claude` is NOT the evidence, and nothing here reads
// it. The evidence is `@projmux_ai_agent`, the pane-scoped option the AI routes
// write when *projmux itself* launches an agent into a pane. That is a projmux
// authorship marker, not a content heuristic about what a process is called,
// which is why bindLegacyPaneTx already trusts exactly this option to mint an
// Agent on its create path. This file makes the adopt and rebind paths agree
// with the create path that was already shipped; it does not introduce a new
// kind of judgment.
//
// A pane where the operator simply typed `claude` in a shell carries no such
// option and gets no Agent. That is Phase 1's refuse rule applied unchanged:
// when the evidence is absent, nothing is attached.
//
// # Which Agent
//
// In order:
//
//  1. The Pane is already owned by an Agent. That Agent is the answer and the
//     only work left is repairing status.paneRef, which is the reverse pointer.
//  2. An Agent in the same Window already records the *same provider
//     conversation* in status.sessionRef as the live pane carries in its
//     `@projmux_ai_session_id` / `@projmux_ai_thread_id` options. That is an
//     exact identifier equality on a value both sides got from the provider --
//     not a name, a title, a command, or an ordinal -- so attaching there
//     re-identifies nothing and reuses the Agent that already exists.
//  3. Otherwise a new Agent is minted. Minting allocates a fresh uid beside
//     whatever else is in the registry and changes no existing one, which is the
//     same reading AdoptionForeign already gets on the Pane layer.
//
// Ambiguity refuses to attach and mints instead. Two Agents recording one
// conversation is legal registry state (see AgentStatus.SessionRef), so an
// ambiguous conversation match cannot be resolved to "the first one"; taking a
// binding that might belong to the other Agent is the one mistake a later pass
// cannot undo, while an extra Agent is inert and visible.
//
// # What is never done here
//
// No uid is changed, merged, or reassigned. No resource is deleted or pruned.
// No name is rewritten -- a promoted Pane keeps the name it already had, only
// its reservation scope follows its new owner. status.sessionRef is not written
// either: the pane option is a live routing index and the durable conversation
// pointer belongs to hook ingest, which reaches the Agent on its own the moment
// the Pane is Agent-owned.

// AgentLinkKind is the outcome of linking one live agent pane to an Agent.
type AgentLinkKind string

const (
	// AgentLinkNone means the pane carries no agent authorship marker, or the
	// registry state refused the link. Nothing was written.
	AgentLinkNone AgentLinkKind = "none"
	// AgentLinkRebound means the Pane was already owned by an Agent. Only the
	// reverse pointer, status.paneRef, was repaired.
	AgentLinkRebound AgentLinkKind = "rebound"
	// AgentLinkAttached means an existing Agent recording the same provider
	// conversation took the Pane.
	AgentLinkAttached AgentLinkKind = "attached"
	// AgentLinkMinted means no Agent existed for this conversation and one was
	// created.
	AgentLinkMinted AgentLinkKind = "minted"
)

// AgentLinkage reports one linkage decision.
type AgentLinkage struct {
	Kind AgentLinkKind
	// AgentUID is set for every kind except AgentLinkNone.
	AgentUID string
	// Promoted reports whether the Pane moved from Window-owned shell to
	// Agent-owned managed.
	Promoted bool
}

// Linked reports whether the decision named an Agent.
func (l AgentLinkage) Linked() bool { return l.Kind != AgentLinkNone && l.AgentUID != "" }

// LinkAgentPane connects one already-bound registry Pane to the Agent resource
// whose runtime the live tmux pane is.
//
// It is idempotent by construction. The second pass over the same machine finds
// the Pane already Agent-owned and does nothing but reassert the reverse
// pointer, so a reconciler that runs on every mutation route converges instead
// of accumulating Agents.
//
// windowUID is the Window the Pane was bound inside. It is the whole scope: the
// candidate Agents are that Window's Agents and no others, so an Agent can never
// be pulled across a Project boundary. The caller reached this Window by pairing
// it against a live tmux window inside one resolved Project, so the boundary is
// structural here rather than checked.
func (m Mutator) LinkAgentPane(reg *Registry, windowUID, paneUID string, observed LegacyPane, binder *BindingMatcher, operationID string) (AgentLinkage, error) {
	const op = "link agent pane"

	if reg == nil {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}
	if _, ok := reg.Window(windowUID); !ok {
		return AgentLinkage{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	if _, ok := reg.Pane(paneUID); !ok {
		return AgentLinkage{}, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	if agentNameBaseFor(observed) == "" {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	linkage, err := m.linkAgentPaneTx(txn, reg, op, windowUID, paneUID, observed, binder, now)
	if err != nil {
		txn.Rollback()
		return AgentLinkage{}, err
	}
	txn.Commit()
	if linkage.Linked() {
		reg.UpdatedAt = now
	}
	return linkage, nil
}

// linkAgentPaneTx is the transaction-scoped body both write paths share: the
// legacy import walk, which already holds a transaction, and the binding-repair
// walk, which opens one per pane exactly as ImportOrphanPane does.
func (m Mutator) linkAgentPaneTx(txn *Transaction, reg *Registry, op, windowUID, paneUID string, observed LegacyPane, binder *BindingMatcher, now time.Time) (AgentLinkage, error) {
	nameBase := agentNameBaseFor(observed)
	if nameBase == "" {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}
	pane, ok := reg.Pane(paneUID)
	if !ok {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}

	// Already Agent-owned: the topology is right and only the reverse pointer
	// can have drifted. Repairing it is what makes `describe agent` agree with
	// `get panes` for a Pane that was linked by an earlier pass.
	if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == KindAgent {
		agentUID := pane.Metadata.OwnerRef.UID
		agent, ok := reg.Agent(agentUID)
		if !ok {
			return AgentLinkage{Kind: AgentLinkNone}, nil
		}
		binder.Claim(agentUID)
		m.bindAgentToPane(agent, paneUID, now)
		return AgentLinkage{Kind: AgentLinkRebound, AgentUID: agentUID}, nil
	}

	// A Pane bound inside some other Window is not this Window's to re-parent.
	// The caller cannot reach that state, but the guard keeps the invariant
	// local to the function that depends on it.
	if pane.Metadata.OwnerUID() != windowUID {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}

	// The candidate is settled before anything is written, so a mint can never
	// be left behind by a promotion that then refused. An existing Agent that
	// already holds this Pane's name in its own scope cannot take the Pane
	// without a rename, and renaming is not on the table -- so it stops being a
	// candidate and the pane gets a fresh Agent, whose scope is empty by
	// construction.
	kind := AgentLinkAttached
	agentUID, ok := reg.agentForConversation(windowUID, observed, binder, now)
	if ok && !reg.canHoldPaneName(agentUID, pane.Metadata.Name, paneUID) {
		ok = false
	}
	if !ok {
		kind = AgentLinkMinted
		minted, err := m.mintLinkedAgentTx(txn, reg, op, windowUID, observed, nameBase, now)
		if err != nil {
			return AgentLinkage{}, err
		}
		agentUID = minted
	}

	if !reg.promotePaneToAgent(paneUID, windowUID, agentUID) {
		return AgentLinkage{}, stateErr(op, ErrNameConflict, "pane name %q is not free in agent scope %q", pane.Metadata.Name, agentUID)
	}
	binder.Claim(agentUID)
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return AgentLinkage{Kind: AgentLinkNone}, nil
	}
	m.bindAgentToPane(agent, paneUID, now)
	return AgentLinkage{Kind: kind, AgentUID: agentUID, Promoted: true}, nil
}

// bindAgentToPane points an Agent at its managed Pane.
//
// The phase move is Pending/Offline/Failed -> Running through the same closed
// transition table AttachAgentPane uses; this file owns no transition rule of
// its own and adds none. An Agent already Running with this exact paneRef is
// left byte-identical, including lastTransitionAt, so a reconcile that observes
// no change writes no change.
func (m Mutator) bindAgentToPane(agent *Agent, paneUID string, now time.Time) {
	if agent == nil {
		return
	}
	if agent.Status.Phase == PhaseRunning && agent.Status.PaneRef == paneUID {
		return
	}
	if !CanTransitionAgent(agent.Status.Phase, PhaseRunning) {
		// Unreachable for the closed table as it stands -- every phase can reach
		// Running -- but a table change must not silently produce an Agent that
		// is Running by assignment rather than by transition.
		return
	}
	agent.Status.Phase = PhaseRunning
	agent.Status.PaneRef = paneUID
	agent.Status.Progress = AgentProgress{}
	agent.Status.Reason = ""
	agent.Status.LastTransitionAt = now
}

// mintLinkedAgentTx creates the Agent a live agent pane has never had.
//
// It is the Agent-layer twin of ImportOrphanPane and follows the same rules: the
// name comes from the registry's own allocator over the provider name base, the
// topic goes to a non-identifying annotation rather than to a name, and nothing
// that already exists is touched. The Agent is created Pending and the caller
// transitions it to Running once the Pane is actually attached, which is the
// order CreateAgent + AttachAgentPane already establish.
func (m Mutator) mintLinkedAgentTx(txn *Transaction, reg *Registry, op, windowUID string, observed LegacyPane, nameBase string, now time.Time) (string, error) {
	agentUID, err := m.mintUID(KindAgent)
	if err != nil {
		return "", err
	}
	name, err := reg.allocateName(op, windowUID, KindAgent, nameBase, agentUID)
	if err != nil {
		return "", err
	}
	agent := Agent{
		APIVersion: APIVersion,
		Kind:       KindAgent,
		Metadata: ObjectMeta{
			UID:       agentUID,
			Name:      name,
			OwnerRef:  &OwnerRef{Kind: KindWindow, UID: windowUID},
			CreatedAt: now,
		},
		Spec:   AgentSpec{Provider: NormalizeProvider(observed.Provider)},
		Status: AgentStatus{Phase: PhasePending, LastTransitionAt: now},
	}
	if topic := strings.TrimSpace(observed.Topic); topic != "" {
		agent.Metadata.Annotations = map[string]string{AnnotationAgentTopic: topic}
	}
	reg.Agents = append(reg.Agents, agent)
	txn.record(KindAgent, agentUID)
	return agentUID, nil
}

// promotePaneToAgent re-parents one shell Pane under an Agent of the same
// Window and marks it managed.
//
// This is the one mutation in this file that rewrites an existing resource, so
// its boundaries are worth stating exactly. The uid does not change. The name
// does not change. The Project and Window the Pane sits under do not change --
// the Agent is owned by the very Window that owned the Pane, so the ancestry is
// identical and only one hop longer. What changes is the two fields that were
// wrong about a pane the operator has been staring at all along: spec.role,
// which said shell for a pane running an agent, and the ownerRef, which is the
// single edge every other reader resolves Agent<->Pane through -- the AGENT
// column, hook session-ref attribution, and cascading delete all read it, so
// leaving it pointing at the Window and expressing the link only in
// status.paneRef would create a second, disagreeing source of truth.
//
// It reports false and writes nothing when the name cannot move scope, so a
// caller never ends up with a resource and its reservation in different scopes.
func (r *Registry) promotePaneToAgent(paneUID, windowUID, agentUID string) bool {
	pane, ok := r.Pane(paneUID)
	if !ok {
		return false
	}
	if !r.rescopeName(windowUID, agentUID, KindPane, pane.Metadata.Name, paneUID) {
		return false
	}
	pane.Metadata.OwnerRef = &OwnerRef{Kind: KindAgent, UID: agentUID}
	pane.Spec.Role = PaneRoleAgent
	return true
}

// canHoldPaneName reports whether a Pane could keep its current name inside an
// Agent's name scope.
func (r *Registry) canHoldPaneName(agentUID, name, paneUID string) bool {
	owner, taken := r.nameOwner(agentUID, KindPane, name)
	return !taken || owner == paneUID
}

// agentForConversation returns the Agent of windowUID that already records the
// provider conversation the live pane carries.
//
// It is the whole of the "attach to an existing Agent" rule and it is an exact
// identifier equality, never a similarity. Both sides of the comparison are the
// same value as the provider reported it: status.sessionRef was folded from a
// provider hook, and the pane options were written by the route that launched
// the agent. Provider is compared too, because a Codex thread id and a Claude
// session id are different namespaces that must never be equated.
//
// Candidates already claimed by this pass, and Agents whose paneRef is some
// other Pane, are skipped -- one Agent is the runtime owner of at most one live
// pane. More than one surviving candidate is ambiguous and reports false, which
// mints instead of guessing.
func (r *Registry) agentForConversation(windowUID string, observed LegacyPane, binder *BindingMatcher, now time.Time) (string, bool) {
	ref, ok := NewAgentSessionRef(AgentSessionObservation{
		Provider:  observed.Provider,
		SessionID: observed.SessionID,
		ThreadID:  observed.ThreadID,
	}, now)
	if !ok {
		return "", false
	}
	conversation := ref.ConversationID()
	if conversation == "" {
		return "", false
	}

	var found string
	for _, agent := range r.AgentsOf(windowUID) {
		stored := agent.Status.SessionRef
		if stored.Empty() || stored.Provider != ref.Provider || stored.ConversationID() != conversation {
			continue
		}
		if binder.Claimed(agent.Metadata.UID) {
			continue
		}
		if agent.Status.PaneRef != "" {
			continue
		}
		if found != "" {
			return "", false
		}
		found = agent.Metadata.UID
	}
	return found, found != ""
}

// agentNameBaseFor returns the Agent name base a live pane's authorship marker
// implies, and "" when the pane carries no marker at all.
//
// The unknown-spelling fallback matches bindLegacyPaneTx exactly: a pane that
// declares *some* provider projmux does not recognize is still a projmux-managed
// agent pane, so it gets the generic "agent" name base rather than being
// silently demoted to a shell.
func agentNameBaseFor(observed LegacyPane) string {
	if strings.TrimSpace(observed.Provider) == "" {
		return ""
	}
	if provider := NormalizeProvider(observed.Provider); provider != "" {
		return provider
	}
	return FallbackAgentNameBase
}
