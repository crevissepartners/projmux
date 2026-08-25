package metadata

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

const maxSnapshotUIDAllocationAttempts = 1024

// SnapshotProjectionPlan is the pure, scoped replacement of one Project's
// descendants. Desired is safe to commit as one Registry transaction.
type SnapshotProjectionPlan struct {
	ProjectUID      string
	Desired         Registry
	Changed         bool
	ReplacedWindows int
	ReplacedPanes   int
	ReplacedAgents  int
	PreservedUIDs   int
	DeletedWindows  int
	DeletedPanes    int
	DeletedAgents   int
	LostSessionRefs int
}

// PlanSnapshotProjection translates a v1 session snapshot into the desired
// Registry subtree of targetProjectUID. It performs no I/O and never mutates
// registry or snapshot.
func PlanSnapshotProjection(registry Registry, targetProjectUID string, snap sessionstate.Snapshot, now time.Time, newUID func(Kind) (string, error)) (SnapshotProjectionPlan, error) {
	const op = "project snapshot projection"
	if err := snap.Validate(); err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s: %w", op, err)
	}
	if now.IsZero() {
		return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "projection timestamp is required")
	}
	target, ok := registry.Project(strings.TrimSpace(targetProjectUID))
	if !ok {
		return SnapshotProjectionPlan{}, stateErr(op, ErrNotFound, "target Project %q does not exist", targetProjectUID)
	}
	if snap.Metadata != nil && snap.Metadata.UID != target.Metadata.UID {
		return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Project uid %q does not match target %q", snap.Metadata.UID, target.Metadata.UID)
	}

	before := registry.Clone()
	desired := registry.Clone()
	oldWindows := registry.WindowsOf(target.Metadata.UID)
	oldPaneByUID := make(map[string]Pane)
	oldAgentByUID := make(map[string]Agent)
	oldWindowByUID := make(map[string]Window)
	for _, window := range oldWindows {
		oldWindowByUID[window.Metadata.UID] = window
		for _, pane := range registry.snapshotPanesOf(window.Metadata.UID) {
			oldPaneByUID[pane.Metadata.UID] = pane
		}
		for _, agent := range registry.AgentsOf(window.Metadata.UID) {
			oldAgentByUID[agent.Metadata.UID] = agent
		}
	}

	owned := targetDescendantUIDs(registry, target.Metadata.UID)
	ownedKinds := make(map[string]Kind, len(owned.windows)+len(owned.panes)+len(owned.agents))
	for uid := range owned.windows {
		ownedKinds[uid] = KindWindow
	}
	for uid := range owned.panes {
		ownedKinds[uid] = KindPane
	}
	for uid := range owned.agents {
		ownedKinds[uid] = KindAgent
	}
	stripTargetDescendants(&desired, target.Metadata.UID, owned)
	used := map[string]bool{target.Metadata.UID: true}
	for _, project := range desired.Projects {
		used[project.Metadata.UID] = true
	}
	for _, control := range desired.ControlSessions {
		used[control.Metadata.UID] = true
	}
	for _, window := range desired.Windows {
		used[window.Metadata.UID] = true
	}
	for _, pane := range desired.Panes {
		used[pane.Metadata.UID] = true
	}
	for _, agent := range desired.Agents {
		used[agent.Metadata.UID] = true
	}
	mint := func(kind Kind, preferred string) (string, error) {
		uid := strings.TrimSpace(preferred)
		if uid != "" {
			if used[uid] {
				return "", inputErr(op, ErrInvalidRegistry, "%s uid %q collides outside the target Project projection or is duplicated by the snapshot", kind, uid)
			}
			if originalKind, existed := ownedKinds[uid]; existed && originalKind != kind {
				return "", inputErr(op, ErrInvalidRegistry, "snapshot reuses target %s uid %q as %s", originalKind, uid, kind)
			}
			used[uid] = true
			return uid, nil
		}
		if newUID == nil {
			return "", fmt.Errorf("%s: uid source is not configured", op)
		}
		for range maxSnapshotUIDAllocationAttempts {
			candidate, err := newUID(kind)
			if err != nil {
				return "", err
			}
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				return "", fmt.Errorf("%s: uid source returned an empty %s uid", op, kind)
			}
			if used[candidate] {
				continue
			}
			// Generated identities must not consume any old target descendant uid.
			// A later positional legacy match may still need that exact identity;
			// reserving the whole old set keeps allocation independent of traversal
			// order and preserves every reusable uid.
			if _, existed := ownedKinds[candidate]; existed {
				continue
			}
			used[candidate] = true
			return candidate, nil
		}
		return "", stateErr(op, ErrInvalidRegistry, "could not allocate a unique %s uid after %d attempts", kind, maxSnapshotUIDAllocationAttempts)
	}
	stamp := now.UTC()
	plan := SnapshotProjectionPlan{ProjectUID: target.Metadata.UID, Desired: desired, ReplacedWindows: len(oldWindows)}
	plan.ReplacedPanes = len(owned.panes)
	plan.ReplacedAgents = len(owned.agents)
	preservedWindows, preservedPanes, preservedAgents := 0, 0, 0
	for _, agent := range oldAgentByUID {
		if agent.Status.SessionRef != nil {
			plan.LostSessionRefs++
		}
	}

	var primaryWindowUID string
	for wi, sw := range snap.Windows {
		if sw.Metadata != nil && (sw.Metadata.OwnerKind != string(KindProject) || sw.Metadata.OwnerUID != target.Metadata.UID) {
			return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Window uid %q owner %s/%s does not match target Project %q", sw.Metadata.UID, sw.Metadata.OwnerKind, sw.Metadata.OwnerUID, target.Metadata.UID)
		}
		var oldWindow *Window
		preferred := ""
		if sw.Metadata != nil {
			preferred = sw.Metadata.UID
			if candidate, ok := oldWindowByUID[preferred]; ok {
				copy := candidate
				oldWindow = &copy
				preferred = candidate.Metadata.UID
			}
		} else if wi < len(oldWindows) {
			copy := oldWindows[wi]
			oldWindow = &copy
			preferred = copy.Metadata.UID
		}
		uid, err := mint(KindWindow, preferred)
		if err != nil {
			return SnapshotProjectionPlan{}, err
		}
		meta := projectedMeta(KindWindow, uid, target.Metadata.UID, sw.Name, nil, stamp, oldWindowMeta(oldWindow), sw.Metadata)
		if err := reserveProjectedName(&desired, op, target.Metadata.UID, KindWindow, &meta, oldWindowMeta(oldWindow), sw.Metadata); err != nil {
			return SnapshotProjectionPlan{}, err
		}
		window := Window{APIVersion: APIVersion, Kind: KindWindow, Metadata: meta}
		desired.Windows = append(desired.Windows, window)
		if oldWindow != nil {
			plan.PreservedUIDs++
			preservedWindows++
		}
		if uid == target.Spec.PrimaryWindowRef {
			primaryWindowUID = uid
		}

		oldShells, oldAgents := projectionCandidates(registry, oldWindow)
		shellPos, agentPos := 0, 0
		projectedPanes := make(map[string]bool)
		directShells := make(map[string]bool)
		var firstPane string
		var firstShell string
		for _, sp := range sw.Panes {
			switch sp.Recipe.Kind {
			case sessionstate.RecipeKindShell, sessionstate.RecipeKindStartup:
				if sp.Metadata != nil && (sp.Metadata.OwnerKind != string(KindWindow) || sp.Metadata.OwnerUID != uid) {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot shell Pane uid %q owner %s/%s does not match Window %q", sp.Metadata.UID, sp.Metadata.OwnerKind, sp.Metadata.OwnerUID, uid)
				}
				var oldPane *Pane
				preferredPane := ""
				if sp.Metadata != nil {
					preferredPane = sp.Metadata.UID
					if candidate, ok := oldPaneByUID[preferredPane]; ok {
						if candidate.Spec.Role != PaneRoleShell || candidate.Metadata.OwnerUID() != uid {
							return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Pane uid %q conflicts with its Window owner", preferredPane)
						}
						copy := candidate
						oldPane = &copy
					}
				} else if shellPos < len(oldShells) {
					copy := oldShells[shellPos]
					oldPane = &copy
					preferredPane = copy.Metadata.UID
					shellPos++
				}
				paneUID, err := mint(KindPane, preferredPane)
				if err != nil {
					return SnapshotProjectionPlan{}, err
				}
				command := ""
				if sp.Recipe.Kind == sessionstate.RecipeKindStartup {
					command = strings.TrimSpace(sp.Recipe.Command)
				}
				paneMeta := projectedMeta(KindPane, paneUID, uid, sp.Label, nil, stamp, oldPaneMeta(oldPane), sp.Metadata)
				if err := reserveProjectedName(&desired, op, uid, KindPane, &paneMeta, oldPaneMeta(oldPane), sp.Metadata); err != nil {
					return SnapshotProjectionPlan{}, err
				}
				pane := Pane{APIVersion: APIVersion, Kind: KindPane, Metadata: paneMeta, Spec: PaneSpec{Role: PaneRoleShell, CWD: sp.CWD, Command: command}}
				desired.Panes = append(desired.Panes, pane)
				projectedPanes[paneUID] = true
				directShells[paneUID] = true
				if firstPane == "" {
					firstPane = paneUID
				}
				if firstShell == "" {
					firstShell = paneUID
				}
				if oldPane != nil {
					plan.PreservedUIDs++
					preservedPanes++
				}
			case sessionstate.RecipeKindAgent:
				var oldAgent *Agent
				var oldPane *Pane
				preferredPane, preferredAgent := "", ""
				if sp.Metadata != nil {
					preferredPane = sp.Metadata.UID
					if candidate, ok := oldPaneByUID[preferredPane]; ok {
						if candidate.Spec.Role != PaneRoleAgent {
							return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Agent Pane uid %q has shell role", preferredPane)
						}
						copy := candidate
						oldPane = &copy
						preferredAgent = candidate.Metadata.OwnerUID()
						if candidateAgent, ok := oldAgentByUID[preferredAgent]; ok {
							ac := candidateAgent
							oldAgent = &ac
						}
					} else if sp.Metadata.OwnerKind == string(KindAgent) {
						preferredAgent = sp.Metadata.OwnerUID
					}
					if candidateAgent, ok := oldAgentByUID[preferredAgent]; ok {
						if candidateAgent.Metadata.OwnerUID() != uid {
							return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Agent uid %q belongs to Window %q, not containing Window %q", preferredAgent, candidateAgent.Metadata.OwnerUID(), uid)
						}
						ac := candidateAgent
						oldAgent = &ac
					}
				} else if agentPos < len(oldAgents) {
					ac := oldAgents[agentPos]
					oldAgent = &ac
					preferredAgent = ac.Metadata.UID
					if ac.Status.PaneRef != "" {
						if pc, ok := oldPaneByUID[ac.Status.PaneRef]; ok {
							oldPane = &pc
							preferredPane = pc.Metadata.UID
						}
					}
					agentPos++
				}
				agentUID, err := mint(KindAgent, preferredAgent)
				if err != nil {
					return SnapshotProjectionPlan{}, err
				}
				if sp.Metadata != nil && (sp.Metadata.OwnerKind != string(KindAgent) || sp.Metadata.OwnerUID != agentUID) {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Agent Pane uid %q owner %s/%s does not match Agent %q", sp.Metadata.UID, sp.Metadata.OwnerKind, sp.Metadata.OwnerUID, agentUID)
				}
				paneUID, err := mint(KindPane, preferredPane)
				if err != nil {
					return SnapshotProjectionPlan{}, err
				}
				agentMeta := projectedMeta(KindAgent, agentUID, uid, sp.Recipe.Agent, map[string]string{"projmux.io/topic": sp.Recipe.Topic}, stamp, oldAgentMeta(oldAgent), nil)
				if err := reserveProjectedName(&desired, op, uid, KindAgent, &agentMeta, oldAgentMeta(oldAgent), nil); err != nil {
					return SnapshotProjectionPlan{}, err
				}
				agent := Agent{APIVersion: APIVersion, Kind: KindAgent, Metadata: agentMeta, Spec: AgentSpec{Provider: NormalizeProvider(sp.Recipe.Agent), Workspace: AgentWorkspace{CWD: sp.CWD}}, Status: AgentStatus{Phase: PhaseOffline, Interaction: AgentInteraction{Kind: InteractionUnknown}, Activation: AgentActivation{State: ActivationNotRequested}, LastTransitionAt: snap.SavedAt.UTC()}}
				if sp.Recipe.ResumeID != "" {
					agent.Status.SessionRef, _ = NewAgentSessionRef(AgentSessionObservation{Provider: sp.Recipe.Agent, SessionID: sp.Recipe.ResumeID, ThreadID: sp.Recipe.ResumeID}, snap.SavedAt.UTC())
				}
				paneMeta := projectedMeta(KindPane, paneUID, agentUID, sp.Label, nil, stamp, oldPaneMeta(oldPane), sp.Metadata)
				paneMeta.OwnerRef = &OwnerRef{Kind: KindAgent, UID: agentUID}
				if err := reserveProjectedName(&desired, op, agentUID, KindPane, &paneMeta, oldPaneMeta(oldPane), sp.Metadata); err != nil {
					return SnapshotProjectionPlan{}, err
				}
				pane := Pane{APIVersion: APIVersion, Kind: KindPane, Metadata: paneMeta, Spec: PaneSpec{Role: PaneRoleAgent, CWD: sp.CWD}}
				agent.Status.PaneRef = paneUID
				desired.Agents = append(desired.Agents, agent)
				desired.Panes = append(desired.Panes, pane)
				projectedPanes[paneUID] = true
				if firstPane == "" {
					firstPane = paneUID
				}
				if oldAgent != nil {
					plan.PreservedUIDs++
					preservedAgents++
					if sameProjectedConversation(oldAgent.Status.SessionRef, agent.Status.SessionRef) {
						plan.LostSessionRefs--
					}
				}
				if oldPane != nil {
					plan.PreservedUIDs++
					preservedPanes++
				}
			}
		}
		if firstPane == "" {
			var oldPane *Pane
			preferred := ""
			if oldWindow != nil {
				if candidate, ok := oldPaneByUID[oldWindow.Spec.DefaultShellPaneRef]; ok &&
					candidate.Metadata.OwnerUID() == oldWindow.Metadata.UID && candidate.Spec.Role == PaneRoleShell {
					copy := candidate
					oldPane = &copy
					preferred = copy.Metadata.UID
				}
			}
			paneUID, err := mint(KindPane, preferred)
			if err != nil {
				return SnapshotProjectionPlan{}, err
			}
			paneMeta := projectedMeta(KindPane, paneUID, uid, "shell", nil, stamp, oldPaneMeta(oldPane), nil)
			if err := reserveProjectedName(&desired, op, uid, KindPane, &paneMeta, oldPaneMeta(oldPane), nil); err != nil {
				return SnapshotProjectionPlan{}, err
			}
			pane := Pane{APIVersion: APIVersion, Kind: KindPane, Metadata: paneMeta, Spec: PaneSpec{Role: PaneRoleShell, CWD: target.Spec.Root}}
			desired.Panes = append(desired.Panes, pane)
			projectedPanes[paneUID] = true
			directShells[paneUID] = true
			firstPane = paneUID
			firstShell = paneUID
			if oldPane != nil {
				plan.PreservedUIDs++
				preservedPanes++
			}
		}
		stored, _ := desired.Window(uid)
		stored.Spec.AnchorPaneRef = firstPane
		stored.Spec.DefaultShellPaneRef = firstShell
		// Metadata-bearing snapshots preserve final-v2 Window ref authority when
		// the referenced Pane is present in the projected subtree. Snapshot v1
		// deliberately carries no Window-spec fields, so a new/unmatched Window
		// and a legacy snapshot fall back to snapshot order: first valid Pane is
		// the role-agnostic anchor and the first direct shell is the optional
		// default. Missing referenced descendants are never invented.
		if sw.Metadata != nil && oldWindow != nil {
			if projectedPanes[oldWindow.Spec.AnchorPaneRef] {
				stored.Spec.AnchorPaneRef = oldWindow.Spec.AnchorPaneRef
			}
			if directShells[oldWindow.Spec.DefaultShellPaneRef] {
				stored.Spec.DefaultShellPaneRef = oldWindow.Spec.DefaultShellPaneRef
			}
		}
	}
	if len(snap.Windows) == 0 {
		anchorWindow, anchorPane, reusedPane, err := canonicalProjectShell(registry, target.Metadata.UID, stamp, newUID)
		if err != nil {
			return SnapshotProjectionPlan{}, err
		}
		desired.Windows = append(desired.Windows, anchorWindow)
		desired.Panes = append(desired.Panes, anchorPane)
		desired.putReservation(target.Metadata.UID, KindWindow, anchorWindow.Metadata.Name, anchorWindow.Metadata.UID)
		desired.putReservation(anchorWindow.Metadata.UID, KindPane, anchorPane.Metadata.Name, anchorPane.Metadata.UID)
		primaryWindowUID = anchorWindow.Metadata.UID
		plan.PreservedUIDs++
		if reusedPane {
			plan.PreservedUIDs++
		}
		preservedWindows++
		if reusedPane {
			preservedPanes++
		}
	}
	if primaryWindowUID == "" {
		primaryWindowUID = desired.WindowsOf(target.Metadata.UID)[0].Metadata.UID
	}
	storedProject, _ := desired.Project(target.Metadata.UID)
	storedProject.Spec.PrimaryWindowRef = primaryWindowUID
	storedProject.Status.Session = cloneSessionProjection(target.Status.Session)
	plan.DeletedWindows = plan.ReplacedWindows - preservedWindows
	plan.DeletedPanes = plan.ReplacedPanes - preservedPanes
	plan.DeletedAgents = plan.ReplacedAgents - preservedAgents
	if err := desired.Validate(); err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s produced invalid desired Registry: %w", op, err)
	}
	plan.Desired = desired
	plan.Changed = !reflect.DeepEqual(before, desired)
	if plan.Changed {
		plan.Desired.UpdatedAt = stamp
	}
	return plan, nil
}

func sameProjectedConversation(old, projected *AgentSessionRef) bool {
	return old != nil && projected != nil && old.Provider == projected.Provider && old.ConversationID() != "" && old.ConversationID() == projected.ConversationID()
}

type descendantSet struct{ windows, panes, agents map[string]bool }

func targetDescendantUIDs(r Registry, projectUID string) descendantSet {
	d := descendantSet{map[string]bool{}, map[string]bool{}, map[string]bool{}}
	for _, w := range r.WindowsOf(projectUID) {
		d.windows[w.Metadata.UID] = true
		for _, p := range r.PanesOf(w.Metadata.UID) {
			d.panes[p.Metadata.UID] = true
		}
		for _, a := range r.AgentsOf(w.Metadata.UID) {
			d.agents[a.Metadata.UID] = true
			for _, p := range r.PanesOf(a.Metadata.UID) {
				d.panes[p.Metadata.UID] = true
			}
		}
	}
	return d
}
func stripTargetDescendants(r *Registry, projectUID string, d descendantSet) {
	r.Windows = filter(r.Windows, func(v Window) bool { return !d.windows[v.Metadata.UID] })
	r.Panes = filter(r.Panes, func(v Pane) bool { return !d.panes[v.Metadata.UID] })
	r.Agents = filter(r.Agents, func(v Agent) bool { return !d.agents[v.Metadata.UID] })
	r.NameReservations = filter(r.NameReservations, func(v NameReservation) bool {
		return !d.windows[v.UID] && !d.panes[v.UID] && !d.agents[v.UID] && !d.windows[v.Scope] && !d.agents[v.Scope]
	})
}
func filter[T any](in []T, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
func canonicalProjectShell(r Registry, projectUID string, createdAt time.Time, newUID func(Kind) (string, error)) (Window, Pane, bool, error) {
	p, ok := r.Project(projectUID)
	if !ok {
		return Window{}, Pane{}, false, stateErr("canonical project shell projection", ErrNotFound, "Project %q does not exist", projectUID)
	}
	w, ok := r.Window(p.Spec.PrimaryWindowRef)
	if !ok || w.Metadata.OwnerRef == nil || w.Metadata.OwnerRef.Kind != KindProject || w.Metadata.OwnerUID() != projectUID {
		return Window{}, Pane{}, false, inputErr("canonical project shell projection", ErrInvalidRegistry, "Project %q canonical Window anchor is invalid; run Phase 3 registry repair", p.Metadata.Name)
	}
	var pane *Pane
	if candidate, found := r.WindowDefaultShell(w.Metadata.UID); found {
		pane = candidate
	} else if candidate, found := r.WindowAnchor(w.Metadata.UID); found && candidate.Spec.Role == PaneRoleShell && candidate.Metadata.OwnerUID() == w.Metadata.UID {
		pane = candidate
	} else {
		for _, candidate := range r.PanesOf(w.Metadata.UID) {
			if candidate.Spec.Role == PaneRoleShell && candidate.Metadata.OwnerUID() == w.Metadata.UID {
				copy := candidate
				pane = &copy
				break
			}
		}
	}
	wc := w.Clone()
	wc.Status = WindowStatus{}
	if pane == nil {
		if newUID == nil {
			return Window{}, Pane{}, false, fmt.Errorf("canonical project shell projection: uid source is not configured")
		}
		used := make(map[string]bool)
		for _, project := range r.Projects {
			used[project.Metadata.UID] = true
		}
		for _, control := range r.ControlSessions {
			used[control.Metadata.UID] = true
		}
		for _, window := range r.Windows {
			used[window.Metadata.UID] = true
		}
		for _, existingPane := range r.Panes {
			used[existingPane.Metadata.UID] = true
		}
		for _, agent := range r.Agents {
			used[agent.Metadata.UID] = true
		}
		paneUID := ""
		for range maxSnapshotUIDAllocationAttempts {
			candidate, err := newUID(KindPane)
			if err != nil {
				return Window{}, Pane{}, false, err
			}
			candidate = strings.TrimSpace(candidate)
			if candidate == "" || used[candidate] {
				continue
			}
			paneUID = candidate
			break
		}
		if paneUID == "" {
			return Window{}, Pane{}, false, stateErr("canonical project shell projection", ErrInvalidRegistry, "could not allocate a unique Pane uid after %d attempts", maxSnapshotUIDAllocationAttempts)
		}
		nameRegistry := r.Clone()
		name, err := nameRegistry.allocateName("canonical project shell projection", w.Metadata.UID, KindPane, "shell", paneUID)
		if err != nil {
			return Window{}, Pane{}, false, err
		}
		created := Pane{
			APIVersion: APIVersion, Kind: KindPane,
			Metadata: ObjectMeta{UID: paneUID, Name: name, OwnerRef: &OwnerRef{Kind: KindWindow, UID: w.Metadata.UID}, CreatedAt: createdAt.UTC()},
			Spec:     PaneSpec{Role: PaneRoleShell, CWD: p.Spec.Root},
		}
		pane = &created
		wc.Spec.AnchorPaneRef = paneUID
		wc.Spec.DefaultShellPaneRef = paneUID
		return wc, created, false, nil
	}
	pc := pane.Clone()
	pc.Status = PaneStatus{}
	wc.Spec.AnchorPaneRef = pc.Metadata.UID
	wc.Spec.DefaultShellPaneRef = pc.Metadata.UID
	return wc, pc, true, nil
}
func projectionCandidates(r Registry, w *Window) ([]Pane, []Agent) {
	if w == nil {
		return nil, nil
	}
	shells := r.PanesOf(w.Metadata.UID)
	agents := r.AgentsOf(w.Metadata.UID)
	return shells, agents
}
func oldWindowMeta(v *Window) *ObjectMeta {
	if v == nil {
		return nil
	}
	m := v.Metadata.Clone()
	return &m
}
func oldPaneMeta(v *Pane) *ObjectMeta {
	if v == nil {
		return nil
	}
	m := v.Metadata.Clone()
	return &m
}
func oldAgentMeta(v *Agent) *ObjectMeta {
	if v == nil {
		return nil
	}
	m := v.Metadata.Clone()
	return &m
}
func projectedMeta(kind Kind, uid, owner, fallback string, annotations map[string]string, now time.Time, old *ObjectMeta, snap *sessionstate.ResourceMetadata) ObjectMeta {
	if old != nil {
		m := old.Clone()
		m.OwnerRef = &OwnerRef{Kind: ownerKindFor(kind), UID: owner}
		if snap != nil {
			if name := strings.TrimSpace(snap.Name); name != "" {
				m.Name = name
			}
			m.Labels = cloneStringMap(snap.Labels)
		}
		if annotations != nil {
			m.Annotations = cloneStringMap(annotations)
		}
		return m
	}
	name := strings.TrimSpace(fallback)
	labels := map[string]string(nil)
	if snap != nil {
		if strings.TrimSpace(snap.Name) != "" {
			name = snap.Name
		}
		labels = cloneStringMap(snap.Labels)
	}
	if name == "" {
		name = strings.ToLower(string(kind))
	}
	name = SanitizeNameBase(name)
	return ObjectMeta{UID: uid, Name: name, Labels: labels, Annotations: cloneStringMap(annotations), OwnerRef: &OwnerRef{Kind: ownerKindFor(kind), UID: owner}, CreatedAt: now}
}

func reserveProjectedName(registry *Registry, op, scope string, kind Kind, meta *ObjectMeta, old *ObjectMeta, snap *sessionstate.ResourceMetadata) error {
	if meta == nil {
		return inputErr(op, ErrInvalidRegistry, "%s projection metadata is missing", kind)
	}
	if old == nil && (snap == nil || strings.TrimSpace(snap.Name) == "") {
		name, err := registry.allocateName(op, scope, kind, meta.Name, meta.UID)
		if err != nil {
			return err
		}
		meta.Name = name
		return nil
	}
	return registry.reserveExplicitName(op, scope, kind, meta.Name, meta.UID)
}
func ownerKindFor(kind Kind) Kind {
	if kind == KindWindow {
		return KindProject
	}
	if kind == KindAgent {
		return KindWindow
	}
	return KindWindow
}
func cloneSessionProjection(in *SessionProjection) *SessionProjection {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
