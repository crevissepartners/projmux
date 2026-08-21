package resourcegraph

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// resolveClaims decides, for every mirrored uid on the machine, whether it binds
// to a Registry resource.
//
// It runs before any node is built because binding is a global decision: a
// second live object mirroring the same uid changes the answer for the first
// one, and that is only knowable after the whole observation has been grouped.
// The kinds are processed outermost first so a conflict is reported against the
// outermost object involved.
func (r *resolver) resolveClaims() {
	registeredControls := map[string]bool{}
	for _, session := range r.inventory.Sessions {
		if !session.isControlSession(r.inventory.HostMode) {
			continue
		}
		control, ok := r.registry.ControlSessionBySession(session.Name)
		if !ok {
			continue
		}
		registeredControls[session.ID] = true
		ref := r.sessionRef(session)
		if strings.TrimSpace(session.ProjectUID) != "" {
			detail := fmt.Sprintf("live control session %s also mirrors Registry identity %s", session.ID, session.ProjectUID)
			r.conflicts = append(r.conflicts, Conflict{
				Kind: ObjectSession, UID: control.Metadata.UID, Reason: ConflictOwnerMismatch,
				Detail: detail, Targets: []string{session.ID},
			})
			r.conflictedUID[control.Metadata.UID] = true
			if _, known := r.kindByUID[session.ProjectUID]; known {
				r.conflictedUID[session.ProjectUID] = true
			}
			r.classify(session.ID, ClassConflict, "", detail)
			continue
		}
		r.bind(control.Metadata.UID, ref)
		r.managedSession[session.ID] = true
	}

	sessionClaims := map[string][]Session{}
	for _, session := range r.inventory.Sessions {
		if session.ProjectUID == "" || registeredControls[session.ID] {
			continue
		}
		sessionClaims[session.ProjectUID] = append(sessionClaims[session.ProjectUID], session)
	}
	windowClaims := map[string][]Window{}
	for _, window := range r.inventory.Windows {
		if window.UID == "" {
			continue
		}
		windowClaims[window.UID] = append(windowClaims[window.UID], window)
	}
	paneClaims := map[string][]Pane{}
	for _, pane := range r.inventory.Panes {
		if pane.UID == "" {
			continue
		}
		paneClaims[pane.UID] = append(paneClaims[pane.UID], pane)
	}

	for _, uid := range sortedKeys(sessionClaims) {
		claimants := sessionClaims[uid]
		refs := make([]RuntimeRef, 0, len(claimants))
		for _, session := range claimants {
			refs = append(refs, r.sessionRef(session))
		}
		if !r.acceptClaim(ObjectSession, coremetadata.KindProject, uid, refs) {
			continue
		}
		// A Project session has nothing above it, so there is no containment
		// left to contradict: the mirrored Project uid is the whole evidence.
		r.bind(uid, refs[0])
		r.managedSession[claimants[0].ID] = true
	}

	for _, uid := range sortedKeys(windowClaims) {
		claimants := windowClaims[uid]
		refs := make([]RuntimeRef, 0, len(claimants))
		for _, window := range claimants {
			refs = append(refs, r.windowRef(window))
		}
		if !r.acceptClaim(ObjectWindow, coremetadata.KindWindow, uid, refs) {
			continue
		}
		window, _ := r.registry.Window(uid)
		if detail, contradicted := r.windowContainmentContradiction(claimants[0], window.Metadata.OwnerUID()); contradicted {
			r.reject(ObjectWindow, uid, ConflictOwnerMismatch, detail, refs)
			continue
		}
		r.bind(uid, refs[0])
		r.managedWindow[claimants[0].ID] = true
	}

	for _, uid := range sortedKeys(paneClaims) {
		claimants := paneClaims[uid]
		refs := make([]RuntimeRef, 0, len(claimants))
		for _, pane := range claimants {
			refs = append(refs, r.paneRef(pane))
		}
		if !r.acceptClaim(ObjectPane, coremetadata.KindPane, uid, refs) {
			continue
		}
		pane, _ := r.registry.Pane(uid)
		windowUID, _, rootUID := r.paneOwnerChain(*pane)
		if detail, contradicted := r.paneContainmentContradiction(claimants[0], windowUID, rootUID); contradicted {
			r.reject(ObjectPane, uid, ConflictOwnerMismatch, detail, refs)
			continue
		}
		r.bind(uid, refs[0])
	}
}

// acceptClaim applies the two checks that do not depend on containment: the
// mirrored uid must belong to this kind, and exactly one live object may claim
// it. It returns false when the claim was already resolved as recoverable or
// rejected as a conflict.
func (r *resolver) acceptClaim(kind ObjectKind, want coremetadata.Kind, uid string, refs []RuntimeRef) bool {
	if got, known := r.kindByUID[uid]; known && got != want {
		r.reject(kind, uid, ConflictKindMismatch,
			fmt.Sprintf("uid %s is a %s in the Registry but is mirrored on a tmux %s", uid, got, kind), refs)
		return false
	}
	if len(refs) > 1 {
		r.reject(kind, uid, ConflictDuplicateClaim,
			fmt.Sprintf("%d live %s objects mirror uid %s", len(refs), kind, uid), refs)
		return false
	}
	if _, known := r.kindByUID[uid]; !known {
		// Legible identity with no resource behind it. It is evidence for an
		// operator-driven recovery and nothing here adopts it: a uid cannot
		// rebuild the owner chain, the name reservation, or the Agent that a
		// registry row carries.
		r.classify(refs[0].ID, ClassRecoverable, "",
			fmt.Sprintf("mirrors uid %s, which this Registry does not contain", uid))
		return false
	}
	return true
}

// bind records the one surviving claim for uid.
func (r *resolver) bind(uid string, ref RuntimeRef) {
	r.bound[uid] = ref
	r.classify(ref.ID, ClassManaged, uid, "")
}

// reject records a contradiction and refuses every claimant.
func (r *resolver) reject(kind ObjectKind, uid string, reason ConflictReason, detail string, refs []RuntimeRef) {
	targets := make([]string, 0, len(refs))
	for _, ref := range refs {
		targets = append(targets, ref.ID)
	}
	slices.Sort(targets)
	r.conflicts = append(r.conflicts, Conflict{
		Kind: kind, UID: uid, Reason: reason, Detail: detail, Targets: targets,
	})
	r.conflictedUID[uid] = true
	for _, ref := range refs {
		r.classify(ref.ID, ClassConflict, "", detail)
	}
}

func (r *resolver) classify(id string, class Class, resourceUID, reason string) {
	r.runtimeClass[id] = class
	if resourceUID != "" {
		r.runtimeResource[id] = resourceUID
	}
	if reason != "" {
		r.runtimeReason[id] = reason
	}
}

// windowContainmentContradiction reports whether the session holding a claiming
// window names a different Project than the Registry does.
//
// Absent evidence is not a contradiction. A session carrying no mirrored Project
// uid -- an option reset, a server restart, a window moved into a plain session
// -- says nothing about ownership, and the window's own uid is still exact
// evidence of its identity. Only a session that names a *different* Project
// refuses the binding, which is what keeps a cross-Project match impossible.
func (r *resolver) windowContainmentContradiction(window Window, rootUID string) (string, bool) {
	session, known := r.sessionByID[window.SessionID]
	if !known {
		return "", false
	}
	if bound := r.runtimeResource[session.ID]; bound != "" {
		if bound == rootUID {
			return "", false
		}
		return fmt.Sprintf("live window %s is in a session bound to root %s but the Registry owns it under root %s",
			window.ID, bound, rootUID), true
	}
	if session.ProjectUID == "" {
		return "", false
	}
	return fmt.Sprintf("live window %s is in a session mirroring root %s but the Registry owns it under root %s",
		window.ID, session.ProjectUID, rootUID), true
}

// paneContainmentContradiction reports whether the window or session holding a
// claiming pane names a different owner than the Registry does. The same
// absent-versus-contradicting rule as windows applies at both levels.
func (r *resolver) paneContainmentContradiction(pane Pane, windowUID, rootUID string) (string, bool) {
	if windowUID == "" {
		// A Pane whose ownerRef chain does not reach a Window cannot be verified
		// against tmux containment at all. Registry validation refuses that shape
		// on write, so reaching it means the row is already damaged, and binding a
		// live pane to a damaged owner chain is how a mutation lands on the wrong
		// object later.
		return fmt.Sprintf("live pane %s mirrors pane %s whose Registry owner chain does not resolve to a Window",
			pane.ID, pane.UID), true
	}
	window, known := r.windowByID[pane.WindowID]
	if !known {
		return "", false
	}
	if window.UID != "" && window.UID != windowUID {
		return fmt.Sprintf("live pane %s is in a window mirroring window %s but the Registry owns it under window %s",
			pane.ID, window.UID, windowUID), true
	}
	session, sessionKnown := r.sessionByID[window.SessionID]
	if sessionKnown {
		bound := r.runtimeResource[session.ID]
		if bound != "" && rootUID != "" && bound != rootUID {
			return fmt.Sprintf("live pane %s is in a session bound to root %s but the Registry owns it under root %s",
				pane.ID, bound, rootUID), true
		}
		if bound == "" && session.ProjectUID != "" && rootUID != "" && session.ProjectUID != rootUID {
			return fmt.Sprintf("live pane %s is in a session mirroring root %s but the Registry owns it under root %s",
				pane.ID, session.ProjectUID, rootUID), true
		}
	}
	return "", false
}

// paneOwnerChain resolves the effective containing Window and Project of a Pane.
//
// A Pane is owned by a Window when it is a shell pane and by an Agent when it is
// managed, and tmux can only testify to Window containment. Resolving the
// Agent's own Window here is what lets an Agent-owned pane be verified against
// the same evidence a shell pane is.
func (r *resolver) paneOwnerChain(pane coremetadata.Pane) (windowUID string, rootKind coremetadata.Kind, rootUID string) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", "", ""
	}
	switch owner.Kind {
	case coremetadata.KindWindow:
		windowUID = owner.UID
	case coremetadata.KindAgent:
		if agent, ok := r.registry.Agent(owner.UID); ok {
			windowUID = agent.Metadata.OwnerUID()
		}
	}
	if windowUID == "" {
		return "", "", ""
	}
	if window, ok := r.registry.Window(windowUID); ok {
		rootUID = window.Metadata.OwnerUID()
		if window.Metadata.OwnerRef != nil {
			rootKind = window.Metadata.OwnerRef.Kind
		}
	}
	return windowUID, rootKind, rootUID
}

func (r *resolver) sessionRef(session Session) RuntimeRef {
	return RuntimeRef{Kind: ObjectSession, ID: session.ID, Target: nonEmpty(session.Name, session.ID), Name: session.Name}
}

func (r *resolver) windowRef(window Window) RuntimeRef {
	target := window.ID
	if session, ok := r.sessionByID[window.SessionID]; ok && session.Name != "" && window.Index != "" {
		target = session.Name + ":" + window.Index
	}
	return RuntimeRef{Kind: ObjectWindow, ID: window.ID, Target: target, Name: window.DisplayName}
}

func (r *resolver) paneRef(pane Pane) RuntimeRef {
	return RuntimeRef{Kind: ObjectPane, ID: pane.ID, Target: pane.ID, Name: pane.Title}
}

// buildRegistryNodes overlays the observation onto every Registry row. Every row
// is emitted, in Registry insertion order, whatever the observation said.
func (r *resolver) buildRegistryNodes() {
	for _, project := range r.registry.Projects {
		uid := project.Metadata.UID
		missingRoot := projectMissingRoot(project)
		ref := r.boundRef(uid)
		r.projects = append(r.projects, ProjectNode{
			Project:     project,
			Class:       r.rowClass(uid),
			Status:      r.status(missingRoot, ref, ScopeSessions),
			MissingRoot: missingRoot,
			Runtime:     ref,
		})
	}
	for _, control := range r.registry.ControlSessions {
		uid := control.Metadata.UID
		ref := r.boundRef(uid)
		r.controlSessions = append(r.controlSessions, ControlSessionNode{
			ControlSession: control,
			Class:          r.rowClass(uid),
			Status:         r.status(false, ref, ScopeSessions),
			Runtime:        ref,
		})
	}
	for _, window := range r.registry.Windows {
		uid := window.Metadata.UID
		rootUID := window.Metadata.OwnerUID()
		rootKind := coremetadata.Kind("")
		if window.Metadata.OwnerRef != nil {
			rootKind = window.Metadata.OwnerRef.Kind
		}
		projectUID := ""
		if rootKind == coremetadata.KindProject {
			projectUID = rootUID
		}
		missingRoot := r.projectMissingRootByUID(projectUID)
		ref := r.boundRef(uid)
		r.windows = append(r.windows, WindowNode{
			Window:      window,
			RootKind:    rootKind,
			RootUID:     rootUID,
			ProjectUID:  projectUID,
			Class:       r.rowClass(uid),
			Status:      r.status(missingRoot, ref, ScopeWindows),
			MissingRoot: missingRoot,
			Runtime:     ref,
		})
	}
	for _, pane := range r.registry.Panes {
		uid := pane.Metadata.UID
		windowUID, rootKind, rootUID := r.paneOwnerChain(pane)
		projectUID := ""
		if rootKind == coremetadata.KindProject {
			projectUID = rootUID
		}
		agentUID := ""
		if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == coremetadata.KindAgent {
			agentUID = owner.UID
		}
		missingRoot := r.projectMissingRootByUID(projectUID)
		ref := r.boundRef(uid)
		r.panes = append(r.panes, PaneNode{
			Pane:        pane,
			AgentUID:    agentUID,
			WindowUID:   windowUID,
			RootKind:    rootKind,
			RootUID:     rootUID,
			ProjectUID:  projectUID,
			Class:       r.rowClass(uid),
			Status:      r.status(missingRoot, ref, ScopePanes),
			MissingRoot: missingRoot,
			Runtime:     ref,
		})
	}
	for _, agent := range r.registry.Agents {
		windowUID := agent.Metadata.OwnerUID()
		rootKind := coremetadata.Kind("")
		rootUID := ""
		projectUID := ""
		if window, ok := r.registry.Window(windowUID); ok {
			rootUID = window.Metadata.OwnerUID()
			if window.Metadata.OwnerRef != nil {
				rootKind = window.Metadata.OwnerRef.Kind
			}
			if rootKind == coremetadata.KindProject {
				projectUID = rootUID
			}
		}
		missingRoot := r.projectMissingRootByUID(projectUID)
		paneUID := strings.TrimSpace(agent.Status.PaneRef)
		var ref *RuntimeRef
		status := StatusOffline
		switch {
		case missingRoot:
			status = StatusMissingRoot
		case paneUID == "":
			// The Registry itself records that this Agent holds no managed
			// Pane, so there is no runtime object to be uncertain about.
			status = StatusOffline
		default:
			ref = r.boundRef(paneUID)
			status = r.status(false, ref, ScopePanes)
		}
		r.agents = append(r.agents, AgentNode{
			Agent:       agent,
			WindowUID:   windowUID,
			RootKind:    rootKind,
			RootUID:     rootUID,
			ProjectUID:  projectUID,
			PaneUID:     paneUID,
			Class:       r.rowClass(agent.Metadata.UID),
			Status:      status,
			MissingRoot: missingRoot,
			Runtime:     ref,
		})
	}
}

// buildRuntimeNodes classifies every observed object, managed ones included.
func (r *resolver) buildRuntimeNodes() {
	host := r.inventory.HostMode
	for _, session := range r.inventory.Sessions {
		ref := r.sessionRef(session)
		class, reason := r.resolvedClass(session.ID)
		if class == "" {
			switch {
			case session.Ephemeral:
				// Fail closed on a scratch session that also carries a role
				// marker: ephemeral grants nothing, control grants a global read
				// default, and no canonical writer produces both.
				class, reason = ClassEphemeral, "auto-attach ephemeral session, never part of the Project hierarchy"
			case session.isControlSession(host):
				class, reason = ClassControl, "app-owned session carrying role "+ControlSessionRole
			default:
				class, reason = r.unownedClass(host, false)
			}
		}
		r.runtime = append(r.runtime, RuntimeNode{
			Ref: ref, Class: class, UID: session.ProjectUID,
			ResourceUID: r.runtimeResource[session.ID], Reason: reason,
		})
	}
	for _, window := range r.inventory.Windows {
		class, reason := r.resolvedClass(window.ID)
		if class == "" {
			class, reason = r.unownedClass(host, r.managedSession[window.SessionID])
		}
		r.runtime = append(r.runtime, RuntimeNode{
			Ref: r.windowRef(window), Class: class, UID: window.UID,
			ResourceUID: r.runtimeResource[window.ID], ContainerID: window.SessionID, Reason: reason,
		})
	}
	for _, pane := range r.inventory.Panes {
		class, reason := r.resolvedClass(pane.ID)
		if class == "" {
			enclosed := r.managedWindow[pane.WindowID]
			if !enclosed {
				if window, ok := r.windowByID[pane.WindowID]; ok {
					enclosed = r.managedSession[window.SessionID]
				}
			}
			class, reason = r.unownedClass(host, enclosed)
		}
		r.runtime = append(r.runtime, RuntimeNode{
			Ref: r.paneRef(pane), Class: class, UID: pane.UID,
			ResourceUID: r.runtimeResource[pane.ID], ContainerID: pane.WindowID, Reason: reason,
			AgentSessionID: pane.AgentSessionID, AgentThreadID: pane.AgentThreadID,
		})
	}
}

func (r *resolver) resolvedClass(id string) (Class, string) {
	class, ok := r.runtimeClass[id]
	if !ok {
		return "", ""
	}
	return class, r.runtimeReason[id]
}

// unownedClass decides between unattributed and foreign for an object carrying
// no mirrored identity.
//
// The split is about the host, not about the object: inside a managed enclosure
// or on a server projmux started, an unmarked object is projmux's own world and
// is shown as unattributed. Everywhere else on someone else's tmux it is the
// operator's, and calling it unattributed would invite a later surface to offer
// an action on it.
func (r *resolver) unownedClass(host HostMode, managedEnclosure bool) (Class, string) {
	switch {
	case managedEnclosure:
		return ClassUnattributed, "no mirrored projmux identity inside a managed enclosure"
	case host == HostModeAppOwned:
		return ClassUnattributed, "no mirrored projmux identity on an app-owned server"
	default:
		return ClassForeign, "no mirrored projmux identity and no managed enclosure on a " + string(host) + " host"
	}
}

// rowClass reports the attribution of one Registry row. A row whose uid is
// contradicted by the machine is a conflict; every other row is managed, because
// a Registry row is by definition a managed resource.
func (r *resolver) rowClass(uid string) Class {
	if r.conflictedUID[uid] {
		return ClassConflict
	}
	return ClassManaged
}

// boundRef returns the exact runtime handle of uid, or nil. A contradicted uid
// never yields a handle: refusing to name one is the whole point of recording
// the conflict.
func (r *resolver) boundRef(uid string) *RuntimeRef {
	if uid == "" || r.conflictedUID[uid] {
		return nil
	}
	ref, ok := r.bound[uid]
	if !ok {
		return nil
	}
	out := ref
	return &out
}

// status applies the one derivation rule for every kind.
//
// missingRoot outranks every runtime answer, a bound handle is live, an
// observation that could not be taken is unknown, and only a readable
// observation with no handle is offline. Nothing here can invent a live row: an
// empty or failed observation can only downgrade.
func (r *resolver) status(missingRoot bool, ref *RuntimeRef, scope Scope) Status {
	switch {
	case missingRoot:
		return StatusMissingRoot
	case ref != nil:
		return StatusLive
	case !r.inventory.Transport.Present():
		return StatusUnknown
	case !r.inventory.Available(scope):
		return StatusUnknown
	default:
		return StatusOffline
	}
}

func (r *resolver) projectMissingRootByUID(uid string) bool {
	if uid == "" {
		return false
	}
	project, ok := r.registry.Project(uid)
	if !ok {
		return false
	}
	return projectMissingRoot(*project)
}

func projectMissingRoot(project coremetadata.Project) bool {
	condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot)
	return ok && condition.Status == coremetadata.ConditionTrue
}

// graph assembles the deterministic result.
func (r *resolver) graph() Graph {
	runtime := slices.Clone(r.runtime)
	slices.SortStableFunc(runtime, func(a, b RuntimeNode) int {
		if rank := objectKindRank(a.Ref.Kind) - objectKindRank(b.Ref.Kind); rank != 0 {
			return rank
		}
		return compareRuntimeID(a.Ref.ID, b.Ref.ID)
	})
	conflicts := slices.Clone(r.conflicts)
	slices.SortStableFunc(conflicts, func(a, b Conflict) int {
		if rank := objectKindRank(a.Kind) - objectKindRank(b.Kind); rank != 0 {
			return rank
		}
		if cmp := strings.Compare(a.UID, b.UID); cmp != 0 {
			return cmp
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return Graph{
		Transport:       r.inventory.Transport,
		HostMode:        r.inventory.HostMode,
		Unavailable:     slices.Clone(r.inventory.Unavailable),
		Projects:        r.projects,
		ControlSessions: r.controlSessions,
		Windows:         r.windows,
		Panes:           r.panes,
		Agents:          r.agents,
		Runtime:         runtime,
		Conflicts:       conflicts,
	}
}

// compareRuntimeID orders tmux ids the way an operator reads them: %9 before
// %10, which a plain string compare gets backwards. Anything unparseable falls
// back to a lexical compare so the order stays total and deterministic.
func compareRuntimeID(a, b string) int {
	na, aok := tmuxIDNumber(a)
	nb, bok := tmuxIDNumber(b)
	if aok && bok && na != nb {
		return na - nb
	}
	if aok != bok {
		if aok {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func tmuxIDNumber(id string) (int, bool) {
	if len(id) < 2 {
		return 0, false
	}
	switch id[0] {
	case '$', '@', '%':
	default:
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

func sortedKeys[V any](in map[string]V) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
