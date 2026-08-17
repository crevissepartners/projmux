package metadata

import (
	"slices"
	"strings"
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
)

// Phase maps an exit classification onto the resulting Agent phase.
func (e AgentExit) Phase() (AgentPhase, bool) {
	switch e {
	case AgentExitNormal, AgentExitDeleted:
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
	// Provider is the raw provider spelling; it is normalized to codex,
	// claude, or antigravity, and falls back to the "agent" name base when
	// unknown.
	Provider    string
	DisplayName string
	Labels      map[string]string
	Annotations map[string]string
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

	agentUID, err := m.mintUID(KindAgent)
	if err != nil {
		txn.Rollback()
		return Agent{}, err
	}
	var name string
	if explicit := strings.TrimSpace(opts.Name); explicit != "" {
		if err := reg.reserveExplicitName(op, windowUID, KindAgent, explicit, agentUID); err != nil {
			txn.Rollback()
			return Agent{}, err
		}
		name = explicit
	} else {
		name, err = reg.allocateName(op, windowUID, KindAgent, AgentNameBase("", opts.Provider), agentUID)
		if err != nil {
			txn.Rollback()
			return Agent{}, err
		}
	}

	agent := Agent{
		APIVersion: APIVersion,
		Kind:       KindAgent,
		Metadata: ObjectMeta{
			UID:         agentUID,
			Name:        name,
			DisplayName: strings.TrimSpace(opts.DisplayName),
			Labels:      cloneStringMap(opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
			OwnerRef:    &OwnerRef{Kind: KindWindow, UID: windowUID},
			CreatedAt:   now,
		},
		Spec:   AgentSpec{Provider: NormalizeProvider(opts.Provider)},
		Status: AgentStatus{Phase: PhasePending, LastTransitionAt: now},
	}
	reg.Agents = append(reg.Agents, agent)
	txn.record(KindAgent, agentUID)
	txn.Commit()
	reg.UpdatedAt = now
	return agent, nil
}

// RenameAgent sets only an Agent's stable metadata.name inside its owning
// Window scope. Provider, topic annotations, lifecycle state, and managed Pane
// metadata are independent and remain unchanged.
func (m Mutator) RenameAgent(reg *Registry, agentUID, name string) (Agent, error) {
	const op = "rename agent"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	name = strings.TrimSpace(name)
	if err := reg.reserveExplicitName(op, agent.Metadata.OwnerUID(), KindAgent, name, agentUID); err != nil {
		return Agent{}, err
	}
	agent.Metadata.Name = name
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// AttachAgentPane creates the managed Pane owned by an Agent and moves the
// Agent to Running. The Pane name uses the "<agent-name>-pane" base inside the
// Agent's owner scope.
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
	pane, err := m.addPaneTx(txn, reg, op, agentUID, KindAgent, PaneRoleAgent, declared.Name, ManagedPaneNameBase(agent.Metadata.Name), declared.Command, declared.CWD, declared.Labels, now)
	if err != nil {
		txn.Rollback()
		return Pane{}, err
	}
	txn.Commit()

	agent = mustAgent(reg, agentUID)
	agent.Status.Phase = PhaseRunning
	agent.Status.PaneRef = pane.Metadata.UID
	agent.Status.Reason = ""
	agent.Status.LastTransitionAt = now
	reg.UpdatedAt = now
	return pane, nil
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
	if paneUID := agent.Status.PaneRef; paneUID != "" {
		reg.deletePane(paneUID)
	}
	agent = mustAgent(reg, agentUID)
	agent.Status.Phase = phase
	agent.Status.PaneRef = ""
	agent.Status.Reason = strings.TrimSpace(reason)
	agent.Status.LastTransitionAt = now
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
	}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

// firstWindowPaneUID returns the first surviving Pane transitively owned by a
// Window in registry insertion order, or "" when the Window has no Panes left.
func (r *Registry) firstWindowPaneUID(windowUID string) string {
	owned := r.windowPaneUIDs(windowUID)
	for _, pane := range r.Panes {
		if owned[pane.Metadata.UID] {
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
			if r.Windows[j].Spec.PrimaryPaneRef != uid {
				continue
			}
			// Promote the next surviving pane so the Window keeps a valid
			// primaryPaneRef instead of dangling at a deleted uid.
			r.Windows[j].Spec.PrimaryPaneRef = r.firstWindowPaneUID(r.Windows[j].Metadata.UID)
		}
		for j := range r.Agents {
			if r.Agents[j].Status.PaneRef == uid {
				r.Agents[j].Status.PaneRef = ""
			}
		}
		return true
	}
	return false
}

// DeletePane removes one Pane resource. Deleting the managed Pane of a running
// Agent moves that Agent to Offline; the Agent resource survives.
func (m Mutator) DeletePane(reg *Registry, paneUID string) error {
	const op = "delete pane"

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	ownerUID := pane.Metadata.OwnerUID()
	ownerKind := KindWindow
	if pane.Metadata.OwnerRef != nil {
		ownerKind = pane.Metadata.OwnerRef.Kind
	}
	now := m.clock()().UTC()
	reg.deletePane(paneUID)
	if ownerKind == KindAgent {
		if agent, ok := reg.Agent(ownerUID); ok && agent.Status.Phase == PhaseRunning {
			agent.Status.Phase = PhaseOffline
			agent.Status.Reason = string(AgentExitDeleted)
			agent.Status.LastTransitionAt = now
		}
	}
	reg.UpdatedAt = now
	return nil
}

// DeleteAgent removes an Agent and its managed Panes.
func (m Mutator) DeleteAgent(reg *Registry, agentUID string) error {
	const op = "delete agent"

	if _, ok := reg.Agent(agentUID); !ok {
		return stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
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
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}

// DeleteWindow removes a Window and every descendant Pane and Agent.
func (m Mutator) DeleteWindow(reg *Registry, windowUID string) error {
	const op = "delete window"

	if _, ok := reg.Window(windowUID); !ok {
		return stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	for _, agent := range reg.AgentsOf(windowUID) {
		if err := m.DeleteAgent(reg, agent.Metadata.UID); err != nil {
			return err
		}
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
	reg.UpdatedAt = m.clock()().UTC()
	return nil
}

// DeleteProject removes a Project and every descendant resource.
func (m Mutator) DeleteProject(reg *Registry, projectUID string) error {
	const op = "delete project"

	if _, ok := reg.Project(projectUID); !ok {
		return stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	for _, window := range reg.WindowsOf(projectUID) {
		if err := m.DeleteWindow(reg, window.Metadata.UID); err != nil {
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
