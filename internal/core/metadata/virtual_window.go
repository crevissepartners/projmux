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

// setWindowAnchor also retires the tmux projection when its last anchor leaves.
func (r *Registry) setWindowAnchor(windowUID, paneUID string) {
	window, ok := r.Window(windowUID)
	if !ok {
		return
	}
	window.Spec.AnchorPaneRef = paneUID
	if r.IsVirtualWindow(windowUID) {
		window.Spec.DefaultShellPaneRef = ""
		window.Status.RuntimeSessionID = ""
		window.Status.RuntimeID = ""
		clearCondition(&window.Status.Conditions, ConditionMissingRuntime)
	}
}

// VirtualWindowDeleteCascade predicts the Window removed with its last Pane.
// It counts retained Pane rows too; deleting an unrelated Offline Agent cannot
// remove another Agent's process Pane or an ineligible retained Pane.
func (r Registry) VirtualWindowDeleteCascade(kind Kind, uid string) string {
	var windowUID string
	switch kind {
	case KindPane:
		pane, ok := r.Pane(uid)
		if !ok {
			return ""
		}
		windowUID, _ = paneWindowOwnerUID(r, *pane)
	case KindAgent:
		agent, ok := r.Agent(uid)
		if !ok {
			return ""
		}
		windowUID = agent.Metadata.OwnerUID()
	default:
		return ""
	}
	if !r.IsVirtualWindow(windowUID) {
		return ""
	}
	removed := false
	for _, pane := range r.Panes {
		owner, ok := paneWindowOwnerUID(r, pane)
		if !ok || owner != windowUID {
			continue
		}
		if kind == KindPane && pane.Metadata.UID == uid || kind == KindAgent && pane.Metadata.OwnerUID() == uid {
			removed = true
			continue
		}
		return ""
	}
	if removed {
		return windowUID
	}
	return ""
}

// ReturnAbsentTmuxWindowsToVirtual consumes a complete, successful inventory.
// Only a previously bound tmux Window with a remaining eligible process Pane
// can be retired. Live siblings and never-materialized topology stay intact.
func (m Mutator) ReturnAbsentTmuxWindowsToVirtual(reg *Registry, observed RuntimeObservation) error {
	next := reg.Clone()
	for _, window := range reg.Windows {
		uid := window.Metadata.UID
		if window.Status.RuntimeID == "" || observed.BoundWindow(uid) {
			continue
		}
		process, live := false, false
		var absent []string
		for _, pane := range reg.Panes {
			owner, ok := paneWindowOwnerUID(*reg, pane)
			if !ok || owner != uid {
				continue
			}
			if pane.Spec.Runtime.EffectiveKind() == RuntimeProcess {
				process = process || windowAnchorEligibility(*reg, uid, pane) == windowAnchorEligible
			} else {
				live = live || observed.BoundPane(pane.Metadata.UID)
				absent = append(absent, pane.Metadata.UID)
			}
		}
		if !process || live {
			continue
		}
		for _, paneUID := range absent {
			if err := m.DeletePane(&next, paneUID); err != nil {
				return err
			}
		}
		next.setWindowAnchor(uid, next.firstWindowAnchorPaneUID(uid))
	}
	*reg = next
	return nil
}
