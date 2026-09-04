package metadata

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

const maxSnapshotUIDAllocationAttempts = 100

// StampProjectSnapshot records the current Registry identity and naming
// provenance on every resource represented by a captured Project snapshot.
// Capture order and display fields are never identity evidence: transient
// exact tmux runtime IDs are joined to Window/Pane status, and drift is
// refused rather than producing partially or incorrectly stamped metadata.
func StampProjectSnapshot(registry Registry, projectUID string, snap sessionstate.Snapshot) (sessionstate.Snapshot, error) {
	const op = "stamp project snapshot metadata"
	if err := registry.Validate(); err != nil {
		return sessionstate.Snapshot{}, fmt.Errorf("%s: %w", op, err)
	}
	project, ok := registry.Project(strings.TrimSpace(projectUID))
	if !ok {
		return sessionstate.Snapshot{}, stateErr(op, ErrNotFound, "Project %q does not exist", projectUID)
	}
	windowsByRuntime := make(map[string]Window)
	panesByRuntime := make(map[string]Pane)
	for _, window := range registry.WindowsOf(project.Metadata.UID) {
		if runtimeID := strings.TrimSpace(window.Status.RuntimeID); runtimeID != "" {
			if existing, duplicate := windowsByRuntime[runtimeID]; duplicate {
				return sessionstate.Snapshot{}, stateErr(op, ErrInvalidRegistry,
					"Registry Windows %q and %q share runtime id %q", existing.Metadata.UID, window.Metadata.UID, runtimeID)
			}
			windowsByRuntime[runtimeID] = window
		}
		for _, pane := range registry.snapshotPanesOf(window.Metadata.UID) {
			if runtimeID := strings.TrimSpace(pane.Status.Activation.RuntimeID); runtimeID != "" {
				if existing, duplicate := panesByRuntime[runtimeID]; duplicate {
					return sessionstate.Snapshot{}, stateErr(op, ErrInvalidRegistry,
						"Registry Panes %q and %q share runtime id %q in Project %q", existing.Metadata.UID, pane.Metadata.UID, runtimeID, project.Metadata.UID)
				}
				panesByRuntime[runtimeID] = pane
			}
		}
	}
	out := snap
	out.Metadata = snapshotResourceMetadata(project.Metadata, "", "")
	out.Windows = append([]sessionstate.Window(nil), snap.Windows...)
	seenWindows := make(map[string]bool, len(snap.Windows))
	seenPanes := make(map[string]bool)
	for wi := range snap.Windows {
		runtimeID := strings.TrimSpace(snap.Windows[wi].RuntimeID)
		window, found := windowsByRuntime[runtimeID]
		if runtimeID == "" || !found || seenWindows[runtimeID] || strings.TrimSpace(snap.Windows[wi].RegistryUID) != window.Metadata.UID {
			return sessionstate.Snapshot{}, stateErr(op, ErrInvalidRegistry,
				"captured Window %d runtime id %q and mirrored uid %q do not identify one unique Window in Project %q", wi, runtimeID, snap.Windows[wi].RegistryUID, project.Metadata.UID)
		}
		seenWindows[runtimeID] = true
		out.Windows[wi].Metadata = snapshotResourceMetadata(window.Metadata, string(KindProject), project.Metadata.UID)
		windowPanesByRuntime := make(map[string]Pane)
		for _, pane := range registry.snapshotPanesOf(window.Metadata.UID) {
			if paneRuntimeID := strings.TrimSpace(pane.Status.Activation.RuntimeID); paneRuntimeID != "" {
				windowPanesByRuntime[paneRuntimeID] = pane
			}
		}
		out.Windows[wi].Panes = append([]sessionstate.Pane(nil), snap.Windows[wi].Panes...)
		for pi := range snap.Windows[wi].Panes {
			paneRuntimeID := strings.TrimSpace(snap.Windows[wi].Panes[pi].RuntimeID)
			pane, found := windowPanesByRuntime[paneRuntimeID]
			if paneRuntimeID == "" || !found || seenPanes[paneRuntimeID] || strings.TrimSpace(snap.Windows[wi].Panes[pi].RegistryUID) != pane.Metadata.UID {
				return sessionstate.Snapshot{}, stateErr(op, ErrInvalidRegistry,
					"captured Pane %d in Window runtime %q has runtime id %q and mirrored uid %q outside the exact Registry owner graph", pi, runtimeID, paneRuntimeID, snap.Windows[wi].Panes[pi].RegistryUID)
			}
			seenPanes[paneRuntimeID] = true
			ownerKind, ownerUID := "", ""
			if pane.Metadata.OwnerRef != nil {
				ownerKind, ownerUID = string(pane.Metadata.OwnerRef.Kind), pane.Metadata.OwnerRef.UID
			}
			out.Windows[wi].Panes[pi].Metadata = snapshotResourceMetadata(pane.Metadata, ownerKind, ownerUID)
			if pane.Spec.Role == PaneRoleAgent {
				agent, ok := registry.Agent(pane.Metadata.OwnerUID())
				if !ok || agent.Metadata.OwnerUID() != window.Metadata.UID || agent.Status.PaneRef != pane.Metadata.UID {
					return sessionstate.Snapshot{}, stateErr(op, ErrInvalidRegistry,
						"Pane %q runtime %q does not have one exact Agent owner chain", pane.Metadata.UID, paneRuntimeID)
				}
				out.Windows[wi].Panes[pi].AgentMetadata = snapshotResourceMetadata(agent.Metadata, string(KindWindow), window.Metadata.UID)
			} else {
				out.Windows[wi].Panes[pi].AgentMetadata = nil
			}
		}
	}
	if err := out.Validate(); err != nil {
		return sessionstate.Snapshot{}, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

func snapshotResourceMetadata(meta ObjectMeta, ownerKind, ownerUID string) *sessionstate.ResourceMetadata {
	return &sessionstate.ResourceMetadata{
		UID: meta.UID, Name: meta.Name, Labels: cloneStringMap(meta.Labels),
		OwnerKind: ownerKind, OwnerUID: ownerUID, RegistrySchemaVersion: SchemaVersion,
	}
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
	// Migration is the deterministic internal receipt for legacy Registry-name
	// canonicalization performed while importing this snapshot. It is not a
	// public command receipt and contains no removed presentation value.
	Migration MigrationReport
}

// PlanSnapshotProjection translates a v1 session snapshot into the desired
// Registry subtree of targetProjectUID. It performs no I/O and never mutates
// registry or snapshot.
func PlanSnapshotProjection(registry Registry, targetProjectUID string, snap sessionstate.Snapshot, now time.Time, newUID func(Kind) (string, error)) (SnapshotProjectionPlan, error) {
	const op = "project snapshot projection"
	if err := snap.Validate(); err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s: %w", op, err)
	}
	provenance, metadataBearing, err := snap.RegistrySchemaProvenance()
	if err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s: %w", op, err)
	}
	if metadataBearing {
		switch provenance {
		case 0, 3, SchemaVersion:
		case 1, 2:
			return SnapshotProjectionPlan{}, inputErr(op, ErrSchemaUnsupported,
				"snapshot registry schema provenance v%d is not an importable naming schema", provenance)
		default:
			if provenance > SchemaVersion {
				return SnapshotProjectionPlan{}, inputErr(op, ErrSchemaTooNew,
					"snapshot registry schema provenance v%d is newer than supported v%d", provenance, SchemaVersion)
			}
			return SnapshotProjectionPlan{}, inputErr(op, ErrSchemaUnsupported,
				"snapshot registry schema provenance v%d is invalid", provenance)
		}
	}
	if metadataBearing && provenance == SchemaVersion {
		if snap.Metadata == nil {
			return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry,
				"current-v%d snapshot is missing authoritative Project metadata", provenance)
		}
		for wi, window := range snap.Windows {
			if window.Metadata == nil {
				return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry,
					"current-v%d snapshot Window %d is missing authoritative Window metadata", provenance, wi)
			}
			for pi, pane := range window.Panes {
				if pane.Metadata == nil {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry,
						"current-v%d snapshot Window %d Pane %d is missing authoritative Pane metadata", provenance, wi, pi)
				}
				if pane.Recipe.Kind == sessionstate.RecipeKindAgent && pane.AgentMetadata == nil {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry,
						"current-v%d snapshot Window %d Pane %d is missing authoritative Agent metadata", provenance, wi, pi)
				}
			}
		}
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
	projectedNames, err := projectedNameIndex(desired)
	if err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s: index retained names: %w", op, err)
	}
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
	mint := func(kind Kind, ownerUID, preferred string) (string, error) {
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
			if err := ValidateName(candidate); err != nil {
				continue
			}
			scope, err := desired.scopeFor(kind, ownerUID)
			if err != nil {
				return "", err
			}
			if holder, taken := projectedNames[nameSlot{scope: scope, kind: kind, name: candidate}]; taken && holder != candidate {
				continue
			}
			used[candidate] = true
			return candidate, nil
		}
		return "", stateErr(op, ErrInvalidRegistry, "could not allocate a unique %s uid after %d attempts", kind, maxSnapshotUIDAllocationAttempts)
	}
	stamp := now.UTC()
	plan := SnapshotProjectionPlan{ProjectUID: target.Metadata.UID, Desired: desired, ReplacedWindows: len(oldWindows)}
	if metadataBearing {
		plan.Migration = MigrationReport{FromVersion: provenance, ToVersion: SchemaVersion}
	}
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
		uid, err := mint(KindWindow, target.Metadata.UID, preferred)
		if err != nil {
			return SnapshotProjectionPlan{}, err
		}
		meta := projectedMeta(KindWindow, uid, target.Metadata.UID, sw.Name, nil, stamp, oldWindowMeta(oldWindow), sw.Metadata)
		if err := reserveProjectedName(&desired, op, target.Metadata.UID, KindWindow, &meta, oldWindowMeta(oldWindow), sw.Metadata); err != nil {
			return SnapshotProjectionPlan{}, err
		}
		recordProjectedName(projectedNames, target.Metadata.UID, KindWindow, meta)
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
				paneUID, err := mint(KindPane, uid, preferredPane)
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
				recordProjectedName(projectedNames, target.Metadata.UID, KindPane, paneMeta)
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
				if sp.AgentMetadata != nil {
					preferredAgent = sp.AgentMetadata.UID
				}
				agentUID, err := mint(KindAgent, uid, preferredAgent)
				if err != nil {
					return SnapshotProjectionPlan{}, err
				}
				if sp.Metadata != nil && (sp.Metadata.OwnerKind != string(KindAgent) || sp.Metadata.OwnerUID != agentUID) {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Agent Pane uid %q owner %s/%s does not match Agent %q", sp.Metadata.UID, sp.Metadata.OwnerKind, sp.Metadata.OwnerUID, agentUID)
				}
				if sp.AgentMetadata != nil && (sp.AgentMetadata.UID != agentUID || sp.AgentMetadata.OwnerKind != string(KindWindow) || sp.AgentMetadata.OwnerUID != uid) {
					return SnapshotProjectionPlan{}, inputErr(op, ErrInvalidRegistry, "snapshot Agent uid %q owner %s/%s does not match Window %q", sp.AgentMetadata.UID, sp.AgentMetadata.OwnerKind, sp.AgentMetadata.OwnerUID, uid)
				}
				paneUID, err := mint(KindPane, agentUID, preferredPane)
				if err != nil {
					return SnapshotProjectionPlan{}, err
				}
				agentMeta := projectedMeta(KindAgent, agentUID, uid, sp.Recipe.Agent, map[string]string{"projmux.io/topic": sp.Recipe.Topic}, stamp, oldAgentMeta(oldAgent), sp.AgentMetadata)
				if err := reserveProjectedName(&desired, op, uid, KindAgent, &agentMeta, oldAgentMeta(oldAgent), sp.AgentMetadata); err != nil {
					return SnapshotProjectionPlan{}, err
				}
				recordProjectedName(projectedNames, target.Metadata.UID, KindAgent, agentMeta)
				agent := Agent{APIVersion: APIVersion, Kind: KindAgent, Metadata: agentMeta, Spec: AgentSpec{Provider: NormalizeProvider(sp.Recipe.Agent), Workspace: AgentWorkspace{CWD: sp.CWD}}, Status: AgentStatus{Phase: PhaseOffline, Interaction: AgentInteraction{Kind: InteractionUnknown}, Activation: AgentActivation{State: ActivationNotRequested}, LastTransitionAt: snap.SavedAt.UTC()}}
				if sp.Recipe.ResumeID != "" {
					agent.Status.SessionRef, _ = NewAgentSessionRef(AgentSessionObservation{Provider: sp.Recipe.Agent, SessionID: sp.Recipe.ResumeID, ThreadID: sp.Recipe.ResumeID}, snap.SavedAt.UTC())
				}
				paneMeta := projectedMeta(KindPane, paneUID, agentUID, sp.Label, nil, stamp, oldPaneMeta(oldPane), sp.Metadata)
				paneMeta.OwnerRef = &OwnerRef{Kind: KindAgent, UID: agentUID}
				if err := reserveProjectedName(&desired, op, agentUID, KindPane, &paneMeta, oldPaneMeta(oldPane), sp.Metadata); err != nil {
					return SnapshotProjectionPlan{}, err
				}
				recordProjectedName(projectedNames, target.Metadata.UID, KindPane, paneMeta)
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
			paneUID, err := mint(KindPane, uid, preferred)
			if err != nil {
				return SnapshotProjectionPlan{}, err
			}
			paneMeta := projectedMeta(KindPane, paneUID, uid, "shell", nil, stamp, oldPaneMeta(oldPane), nil)
			if err := reserveProjectedName(&desired, op, uid, KindPane, &paneMeta, oldPaneMeta(oldPane), nil); err != nil {
				return SnapshotProjectionPlan{}, err
			}
			recordProjectedName(projectedNames, target.Metadata.UID, KindPane, paneMeta)
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
	nameRepairs, err := desired.canonicalizeRootConflictsWithReport(target.Metadata.UID)
	if err != nil {
		return SnapshotProjectionPlan{}, fmt.Errorf("%s canonicalize legacy snapshot names: %w", op, err)
	}
	if len(nameRepairs) != 0 && metadataBearing && provenance == SchemaVersion {
		return SnapshotProjectionPlan{}, inputErr(op, ErrNameConflict,
			"current-v%d snapshot contains a root-wide same-kind name collision; nothing was changed", provenance)
	}
	plan.Migration.NameRepairs = nameRepairs
	rebuildProjectedReservations(before, &desired)
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

type nameSlot struct {
	scope string
	kind  Kind
	name  string
}

func projectedNameIndex(registry Registry) (map[nameSlot]string, error) {
	resources, err := migrationResources(&registry)
	if err != nil {
		return nil, err
	}
	index := make(map[nameSlot]string, len(resources))
	for _, resource := range resources {
		key := nameSlot{scope: resource.root, kind: resource.kind, name: *resource.name}
		if _, exists := index[key]; !exists {
			index[key] = resource.uid
		}
	}
	return index, nil
}

func recordProjectedName(index map[nameSlot]string, root string, kind Kind, meta ObjectMeta) {
	key := nameSlot{scope: root, kind: kind, name: meta.Name}
	if _, exists := index[key]; !exists {
		index[key] = meta.UID
	}
}

// rebuildProjectedReservations reconstructs the authoritative v4 table while
// preserving the byte-significant order of every reservation that survives a
// projection. New resources are appended in canonical order, so a legacy
// snapshot is deterministic and a current snapshot can remain an exact no-op.
func rebuildProjectedReservations(before Registry, desired *Registry) {
	resources, err := migrationResources(desired)
	if err != nil {
		return
	}
	want := make(map[string]NameReservation, len(resources))
	for _, resource := range resources {
		want[resource.uid] = NameReservation{
			Scope: resource.root, Kind: resource.kind, Name: *resource.name, UID: resource.uid,
		}
	}
	seen := make(map[string]bool, len(want))
	reservations := make([]NameReservation, 0, len(want))
	for _, existing := range before.NameReservations {
		reservation, ok := want[existing.UID]
		if !ok || seen[existing.UID] {
			continue
		}
		reservations = append(reservations, reservation)
		seen[existing.UID] = true
	}
	var added []NameReservation
	for uid, reservation := range want {
		if !seen[uid] {
			added = append(added, reservation)
		}
	}
	sortNameReservations(added)
	desired.NameReservations = append(reservations, added...)
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
		created := Pane{
			APIVersion: APIVersion, Kind: KindPane,
			Metadata: ObjectMeta{UID: paneUID, Name: paneUID, OwnerRef: &OwnerRef{Kind: KindWindow, UID: w.Metadata.UID}, CreatedAt: createdAt.UTC()},
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
func projectedMeta(kind Kind, uid, owner, _ string, annotations map[string]string, now time.Time, old *ObjectMeta, snap *sessionstate.ResourceMetadata) ObjectMeta {
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
	name := uid
	labels := map[string]string(nil)
	if snap != nil {
		if strings.TrimSpace(snap.Name) != "" {
			name = snap.Name
		}
		labels = cloneStringMap(snap.Labels)
	}
	return ObjectMeta{UID: uid, Name: name, Labels: labels, Annotations: cloneStringMap(annotations), OwnerRef: &OwnerRef{Kind: ownerKindFor(kind), UID: owner}, CreatedAt: now}
}

func reserveProjectedName(registry *Registry, op, scope string, kind Kind, meta *ObjectMeta, old *ObjectMeta, snap *sessionstate.ResourceMetadata) error {
	if meta == nil {
		return inputErr(op, ErrInvalidRegistry, "%s projection metadata is missing", kind)
	}
	if old == nil && (snap == nil || strings.TrimSpace(snap.Name) == "") {
		meta.Name = meta.UID
	}
	if err := ValidateName(meta.Name); err != nil {
		return err
	}
	return nil
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
