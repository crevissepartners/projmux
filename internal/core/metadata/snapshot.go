package metadata

import (
	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

// MatchSource records how a snapshot element was bound back to a resource.
type MatchSource string

const (
	// MatchUID is an exact metadata.uid match.
	MatchUID MatchSource = "uid"
	// MatchSession matched a Project through its status.session projection.
	MatchSession MatchSource = "session"
	// MatchRoot matched a Project through its spec.root.
	MatchRoot MatchSource = "root"
	// MatchPositional is the deterministic legacy fallback for snapshots
	// written before resource metadata existed.
	MatchPositional MatchSource = "positional"
	// MatchNone means no resource could be bound.
	MatchNone MatchSource = "none"
)

// SnapshotBinding is one reconciled snapshot element.
type SnapshotBinding struct {
	WindowIndex int
	// PaneIndex is -1 for a window-level binding.
	PaneIndex int
	Kind      Kind
	UID       string
	Name      string
	Match     MatchSource
}

// Reconciliation is the full snapshot-to-resource binding result.
type Reconciliation struct {
	ProjectUID   string
	ProjectName  string
	ProjectMatch MatchSource
	Windows      []SnapshotBinding
	Panes        []SnapshotBinding
}

// AttachSnapshotMetadata stamps resource metadata onto a session snapshot so a
// later restore refers to the same logical resources.
//
// The snapshot's top-level block carries the owning Project; the tmux session
// itself is only a runtime projection and gets no identity of its own. Window
// and Pane blocks are filled from the Project topology in registry insertion
// order, which is the same order the topology was declared in.
func AttachSnapshotMetadata(reg *Registry, projectUID string, snap *sessionstate.Snapshot) error {
	const op = "attach snapshot metadata"

	project, ok := reg.Project(projectUID)
	if !ok {
		return stateErr(op, ErrNotFound, "project %q does not exist", projectUID)
	}
	snap.Metadata = &sessionstate.ResourceMetadata{
		UID:    project.Metadata.UID,
		Name:   project.Metadata.Name,
		Labels: cloneStringMap(project.Metadata.Labels),
	}

	windows := reg.WindowsOf(projectUID)
	for wi := range snap.Windows {
		if wi >= len(windows) {
			break
		}
		window := windows[wi]
		snap.Windows[wi].Metadata = &sessionstate.ResourceMetadata{
			UID:       window.Metadata.UID,
			Name:      window.Metadata.Name,
			Labels:    cloneStringMap(window.Metadata.Labels),
			OwnerKind: string(KindProject),
			OwnerUID:  projectUID,
		}
		panes := reg.snapshotPanesOf(window.Metadata.UID)
		for pi := range snap.Windows[wi].Panes {
			if pi >= len(panes) {
				break
			}
			pane := panes[pi]
			snap.Windows[wi].Panes[pi].Metadata = &sessionstate.ResourceMetadata{
				UID:       pane.Metadata.UID,
				Name:      pane.Metadata.Name,
				Labels:    cloneStringMap(pane.Metadata.Labels),
				OwnerKind: string(pane.Metadata.OwnerRef.Kind),
				OwnerUID:  pane.Metadata.OwnerUID(),
			}
		}
	}
	return nil
}

// snapshotPanesOf returns the Panes a Window contributes to a snapshot: its
// own shell Panes followed by the managed Panes of its Agents, both in
// registry insertion order.
func (r *Registry) snapshotPanesOf(windowUID string) []Pane {
	panes := r.PanesOf(windowUID)
	for _, agent := range r.AgentsOf(windowUID) {
		panes = append(panes, r.PanesOf(agent.Metadata.UID)...)
	}
	return panes
}

// ReconcileSnapshot binds a loaded snapshot back onto registry resources.
//
// Bindings prefer the stored metadata.uid. A snapshot written before resource
// metadata existed reconciles deterministically: the Project is matched by
// session projection and then by root, and Windows and Panes are matched
// positionally against the registry topology in insertion order. No resource
// is created, renamed, or re-identified here.
func ReconcileSnapshot(reg *Registry, snap sessionstate.Snapshot) Reconciliation {
	out := Reconciliation{ProjectMatch: MatchNone}

	project, match := resolveSnapshotProject(reg, snap)
	if project != nil {
		out.ProjectUID = project.Metadata.UID
		out.ProjectName = project.Metadata.Name
		out.ProjectMatch = match
	}

	var windows []Window
	if project != nil {
		windows = reg.WindowsOf(project.Metadata.UID)
	}

	for wi, snapWindow := range snap.Windows {
		binding := SnapshotBinding{WindowIndex: wi, PaneIndex: -1, Kind: KindWindow, Match: MatchNone}
		var resolved *Window
		if snapWindow.Metadata != nil {
			if window, ok := reg.Window(snapWindow.Metadata.UID); ok {
				resolved = window
				binding.Match = MatchUID
			}
		}
		if resolved == nil && wi < len(windows) {
			resolved = &windows[wi]
			binding.Match = MatchPositional
		}
		if resolved != nil {
			binding.UID = resolved.Metadata.UID
			binding.Name = resolved.Metadata.Name
		}
		out.Windows = append(out.Windows, binding)

		var panes []Pane
		if resolved != nil {
			panes = reg.snapshotPanesOf(resolved.Metadata.UID)
		}
		for pi, snapPane := range snapWindow.Panes {
			paneBinding := SnapshotBinding{WindowIndex: wi, PaneIndex: pi, Kind: KindPane, Match: MatchNone}
			var resolvedPane *Pane
			if snapPane.Metadata != nil {
				if pane, ok := reg.Pane(snapPane.Metadata.UID); ok {
					resolvedPane = pane
					paneBinding.Match = MatchUID
				}
			}
			if resolvedPane == nil && pi < len(panes) {
				resolvedPane = &panes[pi]
				paneBinding.Match = MatchPositional
			}
			if resolvedPane != nil {
				paneBinding.UID = resolvedPane.Metadata.UID
				paneBinding.Name = resolvedPane.Metadata.Name
			}
			out.Panes = append(out.Panes, paneBinding)
		}
	}
	return out
}

func resolveSnapshotProject(reg *Registry, snap sessionstate.Snapshot) (*Project, MatchSource) {
	if snap.Metadata != nil {
		if project, ok := reg.Project(snap.Metadata.UID); ok {
			return project, MatchUID
		}
	}
	if snap.Session != "" {
		for i := range reg.Projects {
			session := reg.Projects[i].Status.Session
			if session != nil && session.Name == snap.Session {
				return &reg.Projects[i], MatchSession
			}
		}
	}
	if snap.DefaultCWD != "" {
		if project, ok := reg.ProjectByRoot(snap.DefaultCWD); ok {
			return project, MatchRoot
		}
	}
	return nil, MatchNone
}
