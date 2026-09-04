package metadata

import (
	"slices"
	"strings"
	"time"
)

// AgentExit classifies why an Agent stopped owning its managed Pane.
type AgentExit string

const (
	// AgentExitNormal is a normal managed-Pane exit. It resolves to Offline.
	AgentExitNormal AgentExit = "normal"
	// AgentExitDeleted is an explicit pane deletion. It resolves to Offline.
	AgentExitDeleted AgentExit = "deleted"
	// AgentExitAbnormal is an abnormal managed-Pane exit. It resolves to Failed.
	AgentExitAbnormal AgentExit = "abnormal"
	// AgentExitLaunchFailure is a failure to launch. It resolves to Failed.
	AgentExitLaunchFailure AgentExit = "launch-failure"
	// AgentExitUnknown is a managed-Pane disappearance no receipt explains. It
	// resolves to Offline.
	//
	// Offline rather than Failed is a deliberate asymmetry. The phase is what an
	// operator reads to decide whether to resume, and an unproven Failed is
	// worse for that decision than an honest Offline: the evidence that the
	// answer is unproven is carried by status.lastTermination, where it can be
	// read without being mistaken for a diagnosis.
	AgentExitUnknown AgentExit = "unknown"
)

// Phase maps an exit classification onto the resulting Agent phase.
func (e AgentExit) Phase() (AgentPhase, bool) {
	switch e {
	case AgentExitNormal, AgentExitDeleted, AgentExitUnknown:
		return PhaseOffline, true
	case AgentExitAbnormal, AgentExitLaunchFailure:
		return PhaseFailed, true
	default:
		return "", false
	}
}

// agentTransitions is the closed Agent lifecycle transition table. Offline and
// Failed both remain resumable, which is why they can return to Pending or
// Running.
var agentTransitions = map[AgentPhase][]AgentPhase{
	PhasePending: {PhasePending, PhaseRunning, PhaseOffline, PhaseFailed},
	PhaseRunning: {PhaseRunning, PhaseOffline, PhaseFailed},
	PhaseOffline: {PhaseOffline, PhasePending, PhaseRunning, PhaseFailed},
	PhaseFailed:  {PhaseFailed, PhasePending, PhaseRunning, PhaseOffline},
}

// ValidAgentPhase reports whether phase is in the closed phase set.
func ValidAgentPhase(phase AgentPhase) bool {
	return slices.Contains(AgentPhases(), phase)
}

// CanTransitionAgent reports whether from -> to is a permitted transition.
func CanTransitionAgent(from, to AgentPhase) bool {
	allowed, ok := agentTransitions[from]
	if !ok {
		return false
	}
	return slices.Contains(allowed, to)
}

// CreateAgentOptions is the input to offline Agent creation.
type CreateAgentOptions struct {
	// Name is an explicit --name. A collision fails with ErrNameConflict.
	Name string
	// Provider is the raw provider spelling normalized into Agent spec. It does
	// not participate in automatic naming.
	Provider    string
	Labels      map[string]string
	Annotations map[string]string
	Workspace   AgentWorkspace
	Activation  AgentActivationState
	OperationID string
}

// CreateAgent creates an Agent owned by windowUID in the Pending phase. The
// managed Pane is attached separately.
func (m Mutator) CreateAgent(reg *Registry, windowUID string, opts CreateAgentOptions) (Agent, error) {
	const op = "create agent"

	if _, ok := reg.Window(windowUID); !ok {
		return Agent{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	now := m.clock()().UTC()
	txn := m.Begin(reg, opts.OperationID)

	agentUID, name, err := m.mintAndReserveName(reg, op, windowUID, KindAgent, opts.Name)
	if err != nil {
		txn.Rollback()
		return Agent{}, err
	}

	agent := Agent{
		APIVersion: APIVersion,
		Kind:       KindAgent,
		Metadata: ObjectMeta{
			UID:         agentUID,
			Name:        name,
			Labels:      cloneStringMap(opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
			OwnerRef:    &OwnerRef{Kind: KindWindow, UID: windowUID},
			CreatedAt:   now,
		},
		Spec: AgentSpec{Provider: NormalizeProvider(opts.Provider), Workspace: AgentWorkspace{
			CWD:                     strings.TrimSpace(opts.Workspace.CWD),
			AdditionalWritableRoots: slices.Clone(opts.Workspace.AdditionalWritableRoots),
		}},
		Status: AgentStatus{
			Phase:            PhasePending,
			Interaction:      AgentInteraction{Kind: InteractionUnknown},
			Activation:       AgentActivation{State: opts.Activation},
			LastTransitionAt: now,
		},
	}
	if agent.Status.Activation.State == "" {
		agent.Status.Activation.State = ActivationNotRequested
	}
	reg.Agents = append(reg.Agents, agent)
	txn.record(KindAgent, agentUID)
	txn.Commit()
	reg.UpdatedAt = now
	return agent, nil
}

// RenameAgent sets only an Agent's stable metadata.name inside its root-wide
// Agent scope. Provider, topic annotations, lifecycle state, and managed Pane
// metadata are independent and remain unchanged.
func (m Mutator) RenameAgent(reg *Registry, agentUID, name string) (Agent, error) {
	const op = "rename agent"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if err := reg.reserveExplicitName(op, agent.Metadata.OwnerUID(), KindAgent, name, agentUID); err != nil {
		return Agent{}, err
	}
	agent.Metadata.Name = name
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// AttachAgentPane creates the managed Pane owned by an Agent and moves the
// Agent to Running. An automatic Pane name is its exact minted UID in the
// Agent's root-wide Pane namespace.
func (m Mutator) AttachAgentPane(reg *Registry, agentUID string, declared BootstrapPane, operationID string) (Pane, error) {
	const op = "attach agent pane"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if !CanTransitionAgent(agent.Status.Phase, PhaseRunning) {
		return Pane{}, stateErr(op, ErrInvalidPhase, "agent %s cannot move from %s to %s", agent.Metadata.Name, agent.Status.Phase, PhaseRunning)
	}
	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	pane, err := m.addPaneTx(txn, reg, op, agentUID, KindAgent, PaneRoleAgent, declared.Name, declared.Command, declared.CWD, declared.Labels, now)
	if err != nil {
		txn.Rollback()
		return Pane{}, err
	}
	txn.Commit()

	agent = mustAgent(reg, agentUID)
	agent.Status.Phase = PhaseRunning
	agent.Status.PaneRef = pane.Metadata.UID
	agent.Status.Progress = AgentProgress{}
	agent.Status.Reason = ""
	agent.Status.LastTransitionAt = now
	reg.UpdatedAt = now
	return pane, nil
}

// RebindAgentPane makes a retained Agent-owned Pane the Agent's current managed
// Pane again without changing either UID. Runtime disappearance retains Pane
// rows as evidence; materialization may therefore reuse the exact row instead
// of deleting the Window anchor and allocating a replacement identity.
func (m Mutator) RebindAgentPane(reg *Registry, agentUID, paneUID string) (Pane, error) {
	const op = "rebind agent pane"
	agent, ok := reg.Agent(strings.TrimSpace(agentUID))
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if !CanTransitionAgent(agent.Status.Phase, PhaseRunning) {
		return Pane{}, stateErr(op, ErrInvalidPhase, "agent %s cannot move from %s to %s", agent.Metadata.Name, agent.Status.Phase, PhaseRunning)
	}
	if current := strings.TrimSpace(agent.Status.PaneRef); current != "" && current != strings.TrimSpace(paneUID) {
		return Pane{}, stateErr(op, ErrInvalidRegistry, "agent %s is already bound to pane %q", agent.Metadata.Name, current)
	}
	pane, ok := reg.Pane(strings.TrimSpace(paneUID))
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent ||
		pane.Metadata.OwnerRef.UID != agent.Metadata.UID || pane.Spec.Role != PaneRoleAgent {
		return Pane{}, stateErr(op, ErrInvalidRegistry, "pane %q is not an Agent-owned managed Pane of agent %s", paneUID, agent.Metadata.Name)
	}
	now := m.clock()().UTC()
	agent.Status.Phase = PhaseRunning
	agent.Status.PaneRef = pane.Metadata.UID
	agent.Status.Progress = AgentProgress{}
	agent.Status.Reason = ""
	agent.Status.LastTransitionAt = now
	reg.UpdatedAt = now
	return pane.Clone(), nil
}

// ReleaseAgentPane removes the managed Pane and moves the Agent to the phase
// implied by exit. The Agent itself survives as an offline/resumable resource.
func (m Mutator) ReleaseAgentPane(reg *Registry, agentUID string, exit AgentExit, reason string) (Agent, error) {
	const op = "release agent pane"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	phase, ok := exit.Phase()
	if !ok {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported agent exit %q", string(exit))
	}
	if !CanTransitionAgent(agent.Status.Phase, phase) {
		return Agent{}, stateErr(op, ErrInvalidPhase, "agent %s cannot move from %s to %s", agent.Metadata.Name, agent.Status.Phase, phase)
	}

	now := m.clock()().UTC()
	before := reg.Clone()
	windowUID := agent.Metadata.OwnerUID()
	if paneUID := agent.Status.PaneRef; paneUID != "" {
		reg.deletePane(paneUID)
	}
	agent = mustAgent(reg, agentUID)
	agent.Status.Phase = phase
	agent.Status.PaneRef = ""
	agent.Status.Interaction = AgentInteraction{Kind: InteractionUnknown, ObservedAt: now, Source: "lifecycle"}
	agent.Status.Progress = AgentProgress{}
	agent.Status.Reason = strings.TrimSpace(reason)
	agent.Status.LastTransitionAt = now
	if err := m.repairRetainedWindow(reg, windowUID, "replace-released-agent-anchor", now); err != nil {
		*reg = before
		return Agent{}, err
	}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

// TransitionAgent applies an explicit phase transition.
func (m Mutator) TransitionAgent(reg *Registry, agentUID string, phase AgentPhase, reason string) (Agent, error) {
	const op = "transition agent"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if !ValidAgentPhase(phase) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported agent phase %q", string(phase))
	}
	if !CanTransitionAgent(agent.Status.Phase, phase) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "agent %s cannot move from %s to %s", agent.Metadata.Name, agent.Status.Phase, phase)
	}
	now := m.clock()().UTC()
	agent.Status.Phase = phase
	agent.Status.Reason = strings.TrimSpace(reason)
	agent.Status.LastTransitionAt = now
	if phase != PhaseRunning {
		agent.Status.PaneRef = ""
		agent.Status.Interaction = AgentInteraction{Kind: InteractionUnknown, ObservedAt: now, Source: "lifecycle"}
		agent.Status.Progress = AgentProgress{}
	}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

// firstWindowShellPaneUID returns the first surviving direct Window-owned shell
// Pane in Registry insertion order. Registry order is the stable tie-breaker
// used by every lifecycle transition; runtime pane order is never authority.
func (r *Registry) firstWindowShellPaneUID(windowUID string) string {
	for _, pane := range r.Panes {
		if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == KindWindow &&
			pane.Metadata.OwnerRef.UID == windowUID && pane.Spec.Role == PaneRoleShell {
			return pane.Metadata.UID
		}
	}
	return ""
}

// firstWindowAnchorPaneUID returns the first Pane whose exact Registry ancestry
// reaches windowUID. Both direct shell Panes and managed Agent Panes are valid
// final-v2 anchors.
func (r *Registry) firstWindowAnchorPaneUID(windowUID string) string {
	for _, pane := range r.Panes {
		if owner, ok := paneWindowOwnerUID(*r, pane); ok && owner == windowUID {
			return pane.Metadata.UID
		}
	}
	return ""
}

func mustAgent(reg *Registry, uid string) *Agent {
	agent, _ := reg.Agent(uid)
	return agent
}

// deletePane removes one Pane and releases its name reservation.
func (r *Registry) deletePane(uid string) bool {
	for i := range r.Panes {
		if r.Panes[i].Metadata.UID != uid {
			continue
		}
		r.Panes = slices.Delete(r.Panes, i, i+1)
		r.releaseNames(uid)
		for j := range r.Windows {
			if r.Windows[j].Spec.AnchorPaneRef == uid {
				r.Windows[j].Spec.AnchorPaneRef = r.firstWindowAnchorPaneUID(r.Windows[j].Metadata.UID)
			}
			if r.Windows[j].Spec.DefaultShellPaneRef == uid {
				r.Windows[j].Spec.DefaultShellPaneRef = r.firstWindowShellPaneUID(r.Windows[j].Metadata.UID)
			}
		}
		for j := range r.Agents {
			if r.Agents[j].Status.PaneRef == uid {
				r.Agents[j].Status.PaneRef = ""
				r.Agents[j].Status.Progress = AgentProgress{}
			}
		}
		return true
	}
	return false
}

// repairRetainedWindow closes the final-v2 lifecycle invariant after one or
// more descendants have been removed. It preserves an existing valid sibling
// in deterministic Registry order and allocates a shell only when the retained
// Window would otherwise have no descendant Pane at all.
func (m Mutator) repairRetainedWindow(reg *Registry, windowUID, operationID string, now time.Time) error {
	window, ok := reg.Window(windowUID)
	if !ok {
		return nil
	}
	if anchor := reg.firstWindowAnchorPaneUID(windowUID); anchor != "" {
		window.Spec.AnchorPaneRef = anchor
		if shell := reg.firstWindowShellPaneUID(windowUID); shell != "" {
			window.Spec.DefaultShellPaneRef = shell
		} else {
			window.Spec.DefaultShellPaneRef = ""
		}
		return nil
	}
	if _, _, err := m.EnsureWindowDefaultShell(reg, windowUID, "", operationID); err != nil {
		return err
	}
	window, _ = reg.Window(windowUID)
	if window.Spec.AnchorPaneRef == "" || reg.firstWindowAnchorPaneUID(windowUID) == "" {
		return stateErr("repair retained window", ErrInvalidRegistry,
			"window %q has no descendant after replacement", windowUID)
	}
	reg.UpdatedAt = now
	return nil
}

// DeletePane removes one Pane resource. Deleting the managed Pane of a running
// Agent moves that Agent to Offline; the Agent resource survives.
func (m Mutator) DeletePane(reg *Registry, paneUID string) error {
	const op = "delete pane"

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	windowUID, ok := paneWindowOwnerUID(*reg, *pane)
	if !ok {
		return stateErr(op, ErrInvalidRegistry, "pane %q has no exact owning Window", paneUID)
	}
	ownerUID := pane.Metadata.OwnerUID()
	ownerKind := pane.Metadata.OwnerRef.Kind
	now := m.clock()().UTC()
	offlineCurrentAgent := false
	if ownerKind == KindAgent {
		agent, ok := reg.Agent(ownerUID)
		offlineCurrentAgent = ok && agent.Status.Phase == PhaseRunning && agent.Status.PaneRef == paneUID
	}
	before := reg.Clone()
	reg.deletePane(paneUID)
	if ownerKind == KindAgent {
		// An Agent may own retained Panes from earlier materializations. Removing
		// one of those MissingRuntime rows must not offline the Agent's current,
		// different Pane generation.
		if agent, ok := reg.Agent(ownerUID); ok && offlineCurrentAgent {
			agent.Status.Phase = PhaseOffline
			agent.Status.Reason = string(AgentExitDeleted)
			agent.Status.LastTransitionAt = now
		}
	}
	if err := m.repairRetainedWindow(reg, windowUID, "replace-deleted-window-anchor", now); err != nil {
		*reg = before
		return err
	}
	reg.UpdatedAt = now
	return nil
}

// DeleteAgent removes an Agent and its managed Panes.
func (m Mutator) DeleteAgent(reg *Registry, agentUID string) error {
	const op = "delete agent"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	windowUID := agent.Metadata.OwnerUID()
	before := reg.Clone()
	now := m.clock()().UTC()
	for _, pane := range reg.PanesOf(agentUID) {
		reg.deletePane(pane.Metadata.UID)
	}
	for i := range reg.Agents {
		if reg.Agents[i].Metadata.UID == agentUID {
			reg.Agents = slices.Delete(reg.Agents, i, i+1)
			break
		}
	}
	reg.releaseNames(agentUID)
	if err := m.repairRetainedWindow(reg, windowUID, "replace-deleted-agent-anchor", now); err != nil {
		*reg = before
		return err
	}
	reg.UpdatedAt = now
	return nil
}

// DeleteWindow is the explicit canonical Window delete. It removes a Window
// and every descendant Pane and Agent while preserving its Project or
// ControlSession root. A Project with no remaining Window keeps its uid, root,
// session name, pin, and snapshot ownership as a valid closed identity.
func (m Mutator) DeleteWindow(reg *Registry, windowUID string) error {
	return m.deleteWindow(reg, windowUID, true)
}

func (m Mutator) deleteWindow(reg *Registry, windowUID string, preserveProjectAnchor bool) error {
	const op = "delete window"

	window, ok := reg.Window(windowUID)
	if !ok {
		return stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	target := window.Clone()
	now := m.clock()().UTC()
	for _, agent := range reg.AgentsOf(windowUID) {
		for _, pane := range reg.PanesOf(agent.Metadata.UID) {
			reg.deletePane(pane.Metadata.UID)
		}
		for i := range reg.Agents {
			if reg.Agents[i].Metadata.UID == agent.Metadata.UID {
				reg.Agents = slices.Delete(reg.Agents, i, i+1)
				break
			}
		}
		reg.releaseNames(agent.Metadata.UID)
	}
	for _, pane := range reg.PanesOf(windowUID) {
		reg.deletePane(pane.Metadata.UID)
	}
	for i := range reg.Windows {
		if reg.Windows[i].Metadata.UID == windowUID {
			reg.Windows = slices.Delete(reg.Windows, i, i+1)
			break
		}
	}
	reg.releaseNames(windowUID)
	if preserveProjectAnchor && target.Metadata.OwnerRef != nil && target.Metadata.OwnerRef.Kind == KindProject {
		if project, ok := reg.Project(target.Metadata.OwnerRef.UID); ok && project.Spec.PrimaryWindowRef == windowUID {
			projectUID := project.Metadata.UID
			project.Spec.PrimaryWindowRef = ""
			for _, candidate := range reg.WindowsOf(projectUID) {
				if validWindowPrimary(reg, candidate) {
					project.Spec.PrimaryWindowRef = candidate.Metadata.UID
					break
				}
			}
			if project.Spec.PrimaryWindowRef == "" && project.Status.Session != nil {
				project.Status.Session.Live = false
			}
		}
	}
	reg.UpdatedAt = now
	return nil
}

// DeleteProject removes a Project and every descendant resource.
func (m Mutator) DeleteProject(reg *Registry, projectUID string) error {
	const op = "delete project"

	if _, ok := reg.Project(projectUID); !ok {
		return stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	for _, window := range reg.WindowsOf(projectUID) {
		if err := m.deleteWindow(reg, window.Metadata.UID, false); err != nil {
			return err
		}
	}
	for i := range reg.Projects {
		if reg.Projects[i].Metadata.UID == projectUID {
			reg.Projects = slices.Delete(reg.Projects, i, i+1)
			break
		}
	}
	reg.releaseNames(projectUID)
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}
