package metadata

// IsVirtualWindow derives virtual state from the exact eligible anchor. Host
// liveness is irrelevant: an Offline process Agent still owns its Pane.
func (r Registry) IsVirtualWindow(windowUID string) bool {
	anchor, ok := r.WindowAnchor(windowUID)
	return ok && anchor.Spec.Runtime.EffectiveKind() == RuntimeProcess
}

// CreateProcessWindow creates the Window, Agent, and reserved process Pane as
// one atomic mutation. No shell or tmux runtime binding is allocated.
func (m Mutator) CreateProcessWindow(reg *Registry, projectUID string, declared BootstrapWindow, opts CreateAgentOptions, binding ProcessBinding) (Window, Agent, Pane, error) {
	const op = "create process window"
	if _, ok := reg.Project(projectUID); !ok {
		return Window{}, Agent{}, Pane{}, stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	next := reg.Clone()
	uid, name, err := m.mintAndReserveName(&next, op, projectUID, KindWindow, declared.Name)
	if err != nil {
		return Window{}, Agent{}, Pane{}, err
	}
	window := Window{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{
		UID: uid, Name: name, Labels: cloneStringMap(declared.Labels),
		OwnerRef: &OwnerRef{Kind: KindProject, UID: projectUID}, CreatedAt: m.clock()().UTC(),
	}}
	next.Windows = append(next.Windows, window)
	next.adoptProjectPrimaryWindow(KindProject, projectUID, uid)
	agent, err := m.CreateAgent(&next, uid, opts)
	if err != nil {
		return Window{}, Agent{}, Pane{}, err
	}
	paneName := agent.Metadata.Name + "-pane"
	if ValidateName(paneName) != nil {
		paneName = ""
	}
	pane, err := m.AttachAgentPane(&next, agent.Metadata.UID, BootstrapPane{Name: paneName, CWD: opts.Workspace.CWD, Labels: opts.Labels}, opts.OperationID)
	if err != nil {
		return Window{}, Agent{}, Pane{}, err
	}
	w, _ := next.Window(uid)
	w.Spec.AnchorPaneRef = pane.Metadata.UID
	binding.ProjectUID, binding.WindowUID, binding.AgentUID, binding.PaneUID = projectUID, uid, agent.Metadata.UID, pane.Metadata.UID
	if err := m.ReserveProcessBinding(&next, binding); err != nil {
		return Window{}, Agent{}, Pane{}, err
	}
	*reg = next
	w, _ = reg.Window(uid)
	a, _ := reg.Agent(agent.Metadata.UID)
	p, _ := reg.Pane(pane.Metadata.UID)
	return w.Clone(), a.Clone(), p.Clone(), nil
}
