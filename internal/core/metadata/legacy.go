package metadata

import (
	"strings"
	"time"
)

// LegacyPane is one observed pre-v2 tmux pane. Label is the existing
// @projmux_pane_label value, which is the migration seed for the Pane *name*;
// in v2 vocabulary it is a name, not a label. metadata.labels stays reserved
// for key/value classification.
type LegacyPane struct {
	Label    string
	Provider string
	// LaunchAuthorship is the raw canonical Projmux launch receipt. Only the
	// exact value "1" together with a provider authorizes topology promotion.
	// It is deliberately independent of hook/provider presentation markers.
	LaunchAuthorship string
	// Topic is carried for the derived display title only. It is never a name
	// seed for a Pane, a Window, or an Agent.
	Topic   string
	Command string
	Title   string
	CWD     string
	// UID is the `@projmux_pane_uid` the live tmux pane already carries, empty
	// when it carries none. It is a binding, never a name seed: adoption reads
	// it to tell "already ours" from "blank", and refuses either way to
	// re-identify anything.
	UID string
	// SessionID and ThreadID are the provider conversation identifiers the AI
	// routes wrote onto the live pane. They are read for exactly one decision --
	// whether an Agent that already records the same conversation in
	// status.sessionRef is the one this live pane belongs to -- and are never a
	// name seed, a selector, or a uid.
	SessionID string
	ThreadID  string
}

// LegacyWindow is one observed pre-v2 tmux window.
type LegacyWindow struct {
	Name string
	// RuntimeSessionID and RuntimeID are the exact live $N/@N owner binding.
	RuntimeSessionID string
	RuntimeID        string
	// AutomaticRename is the observed window-scoped automatic-rename value.
	AutomaticRename bool
	// UID is the `@projmux_window_uid` the live tmux window already carries,
	// empty when it carries none. Same role as LegacyPane.UID.
	UID   string
	Panes []LegacyPane
}

// LegacySession is one observed pre-v2 tmux session anchored at a project root.
type LegacySession struct {
	Session string
	Root    string
	Windows []LegacyWindow
}

// LegacyWindowNameSeed returns the stable allocator base for a newly imported
// Window. Nothing observed from tmux -- window_name, Pane label, provider,
// command, shell, topic, or title -- is an identity input. The observed
// window_name is projected separately onto metadata.displayName.
func LegacyWindowNameSeed(_ LegacyWindow) string {
	return FallbackWindowNameBase
}

// LegacyPaneNameSeed derives the one-time Pane name base for a legacy pane:
// the existing @projmux_pane_label, then the command basename, then the
// configured shell basename, then "pane".
func LegacyPaneNameSeed(pane LegacyPane, defaultShell string) string {
	if base := SanitizeNameBase(pane.Label); base != "" {
		return base
	}
	return PaneNameBase(pane.Command, defaultShell)
}

// ImportOrigin records how one reported Window or Pane came to be reported.
//
// The distinction matters to exactly two callers and for opposite reasons. The
// tmux adapter treats all three identically -- every reported object gets its
// binding written through the one mirror write path, because a created object
// has no binding yet and an adopted or rebound one has a binding that may have
// been wiped. The transaction ledger treats only ImportCreated as created: an
// adopted object existed before this operation, so rolling the operation back
// must not delete it.
type ImportOrigin string

const (
	// ImportCreated is a Window or Pane this import minted.
	ImportCreated ImportOrigin = "created"
	// ImportAdopted is a pre-existing registry object this import paired with a
	// live tmux object that carried no uid.
	ImportAdopted ImportOrigin = "adopted"
	// ImportRebound is a pre-existing registry object whose live tmux object
	// still carried its uid. Reported so its binding is reapplied.
	ImportRebound ImportOrigin = "rebound"
)

// ImportedWindow reports one Window a legacy import bound to a live tmux
// window, together with the transport work the tmux adapter still owes it.
type ImportedWindow struct {
	UID         string
	Name        string
	SourceIndex int
	// NeedsAutomaticRenameOff is true for every managed Window, so a
	// focused-Pane change can never overwrite the Window name.
	NeedsAutomaticRenameOff bool
	// Origin distinguishes a minted Window from an adopted or rebound one.
	Origin ImportOrigin
}

// ImportedPane reports one Pane a legacy import bound to a live tmux pane.
type ImportedPane struct {
	UID         string
	Name        string
	WindowIndex int
	PaneIndex   int
	// Origin distinguishes a minted Pane from an adopted or rebound one.
	Origin ImportOrigin
}

// ImportedAgent reports one Agent created by a legacy import.
type ImportedAgent struct {
	UID         string
	Name        string
	PaneUID     string
	WindowIndex int
	PaneIndex   int
}

// ImportResult is the outcome of one legacy session import.
//
// Windows and Panes report every object the import bound to a live tmux object,
// created or not, because the adapter owes all of them a mirror write. Created
// reports only what the transaction minted, so a rollback still removes exactly
// what this operation brought into existence and never an adopted object that
// predates it.
type ImportResult struct {
	Project       Project
	ProjectReused bool
	Windows       []ImportedWindow
	Panes         []ImportedPane
	Agents        []ImportedAgent
	OperationID   string
	Created       []string
}

// ImportLegacySession converts one observed pre-v2 tmux session into Projmux
// resources, and reattaches the ones that already exist.
//
// Name collisions during import are automatic, not explicit, so they receive
// the lowest free suffix rather than failing: two projects whose roots share a
// basename become `name` and `name-1`, and two agents of the same provider in
// one window become `codex` and `codex-1`. An exact saved root that reappears
// reuses the same Project uid; nothing else merges uids.
//
// binder carries the adoption decision across the whole reconciliation pass so
// one registry Window is never handed to two live tmux windows. A nil binder
// gets a private one over an empty observation, which is the right reading for
// a caller importing a single session in isolation: adoption still happens, and
// nothing is known to be bound elsewhere.
func (m Mutator) ImportLegacySession(reg *Registry, legacy LegacySession, defaultShell, operationID string, binder *BindingMatcher) (ImportResult, error) {
	const op = "import legacy session"

	root, err := m.validateRoot(op, legacy.Root)
	if err != nil {
		return ImportResult{}, err
	}
	if binder == nil {
		binder = NewBindingMatcher(RuntimeObservation{})
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	result, err := m.importLegacySessionTx(txn, reg, op, legacy, root, defaultShell, now, binder)
	if err != nil {
		txn.Rollback()
		return ImportResult{}, err
	}
	result.Created = txn.Created()
	result.OperationID = txn.ID()
	txn.Commit()
	reg.UpdatedAt = now
	return result, nil
}

func (m Mutator) importLegacySessionTx(txn *Transaction, reg *Registry, op string, legacy LegacySession, root, defaultShell string, now time.Time, binder *BindingMatcher) (ImportResult, error) {
	result := ImportResult{}

	var projectUID string
	if existing, ok := reg.ProjectByRoot(root); ok {
		projectUID = existing.Metadata.UID
		result.ProjectReused = true
	} else {
		uid, err := m.mintUID(KindProject)
		if err != nil {
			return ImportResult{}, err
		}
		name, err := reg.allocateName(op, "", KindProject, ProjectNameBase(root), uid)
		if err != nil {
			return ImportResult{}, err
		}
		project := Project{
			APIVersion: APIVersion,
			Kind:       KindProject,
			Metadata: ObjectMeta{
				UID:         uid,
				Name:        name,
				DisplayName: ProjectDisplayName(root),
				CreatedAt:   now,
			},
			Spec: ProjectSpec{Root: root},
		}
		reg.Projects = append(reg.Projects, project)
		txn.record(KindProject, uid)
		projectUID = uid
	}

	if session := strings.TrimSpace(legacy.Session); session != "" {
		if project, ok := reg.Project(projectUID); ok {
			project.Status.Session = &SessionProjection{Name: session, Live: true}
		}
	}

	// Every observed window is walked, every time.
	//
	// This used to be guarded by `!result.ProjectReused || len(WindowsOf) == 0`,
	// which skipped the whole topology for a Project that already owned Windows.
	// That guard avoided duplicates instead of repairing drift: once the
	// registry and the machine disagreed -- a tmux server restart wiping every
	// mirrored uid is enough -- no later pass could ever bring them back
	// together. The walk below reaches the same "no duplicate Window" outcome by
	// adopting rather than by skipping, and it additionally reapplies the
	// bindings the old path left permanently lost.
	for wi, legacyWindow := range legacy.Windows {
		if err := m.bindLegacyWindowTx(txn, reg, op, projectUID, root, defaultShell, wi, legacyWindow, now, &result, binder); err != nil {
			return ImportResult{}, err
		}
	}
	if project, ok := reg.Project(projectUID); ok && strings.TrimSpace(project.Spec.PrimaryWindowRef) == "" {
		for _, window := range reg.WindowsOf(projectUID) {
			if validWindowPrimary(reg, window) {
				project.Spec.PrimaryWindowRef = window.Metadata.UID
				break
			}
		}
	}

	project, _ := reg.Project(projectUID)
	result.Project = project.Clone()
	return result, nil
}

// bindLegacyWindowTx binds one observed tmux window to a registry Window and
// then binds that window's panes.
//
// The window resolves through the adoption matcher into exactly one of four
// outcomes, and the pane cascade below repeats the same four inside it:
//
//   - rebound: the live window still carries a uid this Project owns.
//   - adopted: the live window carries no uid and an unbound registry Window of
//     this Project, next in creation order, takes it.
//   - created: no eligible registry Window is left -- or the live window carries
//     a uid nothing in this registry knows -- so one is minted. This is the
//     pre-existing import path, unchanged.
//   - refused: the live window carries a uid that exists and belongs to
//     somebody else. Nothing is created, nothing is bound, and none of its
//     panes are considered either, because a pane can only be paired inside a
//     Window that was itself paired.
func (m Mutator) bindLegacyWindowTx(txn *Transaction, reg *Registry, op, projectUID, root, defaultShell string, windowIndex int, legacyWindow LegacyWindow, now time.Time, result *ImportResult, binder *BindingMatcher) error {
	match := binder.MatchWindow(reg, projectUID, legacyWindow.UID)
	if match.Kind == AdoptionRefused {
		return nil
	}

	windowUID := match.UID
	origin := ImportAdopted
	if match.Kind == AdoptionRebind {
		origin = ImportRebound
	}
	legacyPanes := legacyWindow.Panes

	if match.Kind == AdoptionUnmatched || match.Kind == AdoptionForeign {
		origin = ImportCreated
		uid, err := m.mintUID(KindWindow)
		if err != nil {
			return err
		}
		windowName, err := reg.allocateName(op, projectUID, KindWindow, LegacyWindowNameSeed(legacyWindow), uid)
		if err != nil {
			return err
		}
		reg.Windows = append(reg.Windows, Window{
			APIVersion: APIVersion,
			Kind:       KindWindow,
			Metadata: ObjectMeta{
				UID:         uid,
				Name:        windowName,
				DisplayName: legacyWindow.Name,
				OwnerRef:    &OwnerRef{Kind: KindProject, UID: projectUID},
				CreatedAt:   now,
			},
		})
		txn.record(KindWindow, uid)
		binder.Claim(uid)
		windowUID = uid
		// A minted Window must end up with a primary Pane, so an observation
		// with no panes at all still materializes one. An adopted Window keeps
		// the topology it already has; synthesizing there would invent a Pane
		// that no tmux pane corresponds to.
		if len(legacyPanes) == 0 {
			legacyPanes = []LegacyPane{{}}
		}
	}

	window, ok := reg.Window(windowUID)
	if !ok {
		return nil
	}
	window.Status.RuntimeSessionID = strings.TrimSpace(legacyWindow.RuntimeSessionID)
	window.Status.RuntimeID = strings.TrimSpace(legacyWindow.RuntimeID)
	result.Windows = append(result.Windows, ImportedWindow{
		UID:                     windowUID,
		Name:                    window.Metadata.Name,
		SourceIndex:             windowIndex,
		NeedsAutomaticRenameOff: true,
		Origin:                  origin,
	})

	for pi, legacyPane := range legacyPanes {
		_, err := m.bindLegacyPaneTx(txn, reg, op, windowUID, root, defaultShell, windowIndex, pi, legacyPane, now, result, binder)
		if err != nil {
			return err
		}
	}

	stored, _ := reg.Window(windowUID)
	if !validWindowPrimary(reg, *stored) {
		shellPaneRef := firstWindowOwnedShellUID(reg, windowUID)
		if shellPaneRef == "" {
			pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, "", FallbackPaneNameBase, "", root, nil, now)
			if err != nil {
				return err
			}
			shellPaneRef = pane.Metadata.UID
		}
		stored.Spec.AnchorPaneRef = shellPaneRef
		stored.Spec.DefaultShellPaneRef = shellPaneRef
	}
	// Existing identity is deliberately untouched. The runtime-owned spelling
	// has a separate duplicate-allowed projection, so an adopted or rebound
	// Window keeps its uid, metadata.name, owner, and reservation.
	if _, err := m.ObserveWindowDisplayName(reg, windowUID, legacyWindow.Name); err != nil {
		return err
	}
	return nil
}

// bindLegacyPaneTx binds one observed tmux pane inside an already-bound Window
// and returns the registry Pane uid it settled on, empty when it refused.
//
// A pane that resolves to an existing registry Pane keeps that Pane -- the
// ordinal alignment that put the two together is not second-guessed and no
// second Pane is minted for it. It is linked to an Agent when, and only when,
// the live pane carries the `@projmux_ai_agent` authorship marker this same
// function already trusts on its create path below. Linking there but not here
// was the inconsistency that left running agents invisible; see agentlinkage.go
// for why that marker is authorship rather than a content heuristic.
func (m Mutator) bindLegacyPaneTx(txn *Transaction, reg *Registry, op, windowUID, root, defaultShell string, windowIndex, paneIndex int, legacyPane LegacyPane, now time.Time, result *ImportResult, binder *BindingMatcher) (string, error) {
	match := binder.MatchPane(reg, windowUID, legacyPane.UID)
	if match.Kind == AdoptionRefused {
		return "", nil
	}
	// AdoptionForeign falls through to the create path below, for the same
	// reason the Window branch does: a uid nothing knows is never adopted, but
	// leaving the pane unmanaged forever is worse than minting a Pane for it.
	if match.Matched() {
		pane, ok := reg.Pane(match.UID)
		if !ok {
			return "", nil
		}
		origin := ImportAdopted
		if match.Kind == AdoptionRebind {
			origin = ImportRebound
		}
		linkage, err := m.linkAgentPaneTx(txn, reg, op, windowUID, pane.Metadata.UID, legacyPane, binder, now)
		if err != nil {
			return "", err
		}
		if linkage.Kind == AgentLinkMinted {
			agent, ok := reg.Agent(linkage.AgentUID)
			if ok {
				result.Agents = append(result.Agents, ImportedAgent{
					UID:         agent.Metadata.UID,
					Name:        agent.Metadata.Name,
					PaneUID:     pane.Metadata.UID,
					WindowIndex: windowIndex,
					PaneIndex:   paneIndex,
				})
			}
		}
		result.Panes = append(result.Panes, ImportedPane{
			UID:         pane.Metadata.UID,
			Name:        pane.Metadata.Name,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
			Origin:      origin,
		})
		return pane.Metadata.UID, nil
	}

	cwd := strings.TrimSpace(legacyPane.CWD)
	if cwd == "" {
		cwd = root
	}
	provider := ""
	if ResolveAgentPaneAuthority(legacyPane) == AgentPaneAuthorityLaunch {
		provider = NormalizeProvider(legacyPane.Provider)
		if provider == "" && strings.TrimSpace(legacyPane.Provider) != "" {
			provider = FallbackAgentNameBase
		}
	}

	if provider == "" {
		pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, "", LegacyPaneNameSeed(legacyPane, defaultShell), legacyPane.Command, cwd, nil, now)
		if err != nil {
			return "", err
		}
		pane.Status.DisplayTitle = DerivePaneDisplayTitle(legacyPane.Provider, legacyPane.Topic, legacyPane.Command, legacyPane.Title)
		reg.storePaneStatus(pane.Metadata.UID, pane.Status)
		binder.Claim(pane.Metadata.UID)
		result.Panes = append(result.Panes, ImportedPane{
			UID:         pane.Metadata.UID,
			Name:        pane.Metadata.Name,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
			Origin:      ImportCreated,
		})
		return pane.Metadata.UID, nil
	}

	agentUID, err := m.mintUID(KindAgent)
	if err != nil {
		return "", err
	}
	agentName, err := reg.allocateName(op, windowUID, KindAgent, AgentNameBase("", legacyPane.Provider), agentUID)
	if err != nil {
		return "", err
	}
	agent := Agent{
		APIVersion: APIVersion,
		Kind:       KindAgent,
		Metadata: ObjectMeta{
			UID:       agentUID,
			Name:      agentName,
			OwnerRef:  &OwnerRef{Kind: KindWindow, UID: windowUID},
			CreatedAt: now,
		},
		Spec:   AgentSpec{Provider: NormalizeProvider(legacyPane.Provider)},
		Status: AgentStatus{Phase: PhasePending, LastTransitionAt: now},
	}
	if topic := strings.TrimSpace(legacyPane.Topic); topic != "" {
		agent.Metadata.Annotations = map[string]string{AnnotationAgentTopic: topic}
	}
	reg.Agents = append(reg.Agents, agent)
	txn.record(KindAgent, agentUID)

	pane, err := m.addPaneTx(txn, reg, op, agentUID, KindAgent, PaneRoleAgent, "", ManagedPaneNameBase(agentName), legacyPane.Command, cwd, nil, now)
	if err != nil {
		return "", err
	}
	pane.Status.DisplayTitle = DerivePaneDisplayTitle(legacyPane.Provider, legacyPane.Topic, legacyPane.Command, legacyPane.Title)
	reg.storePaneStatus(pane.Metadata.UID, pane.Status)

	stored, _ := reg.Agent(agentUID)
	stored.Status.Phase = PhaseRunning
	stored.Status.PaneRef = pane.Metadata.UID
	stored.Status.LastTransitionAt = now
	binder.Claim(pane.Metadata.UID)

	result.Agents = append(result.Agents, ImportedAgent{UID: agentUID, Name: agentName, PaneUID: pane.Metadata.UID, WindowIndex: windowIndex, PaneIndex: paneIndex})
	result.Panes = append(result.Panes, ImportedPane{
		UID:         pane.Metadata.UID,
		Name:        pane.Metadata.Name,
		WindowIndex: windowIndex,
		PaneIndex:   paneIndex,
		Origin:      ImportCreated,
	})
	return pane.Metadata.UID, nil
}

// ImportOrphanPane mints the shell Pane a live tmux pane has never had, inside
// a Window that is already paired with a live tmux window.
//
// Adoption alone cannot reach this state. Adoption pairs a live tmux object
// with a registry object that already exists, and a pane produced by a route
// that registers nothing -- the non-resource `projmux create agent` bridge is
// the measured one -- has no
// registry counterpart to pair with. Those panes stayed permanently unbound, so
// `projmux delete pane` with no selector kept refusing with "carries no
// @projmux_pane_uid" in the operator's own active pane. Something has to be
// created before a uid exists to mirror back.
//
// The name base is FallbackPaneNameBase, uniquified by the registry's own
// allocator, and deliberately *not* LegacyPaneNameSeed. That seed reads
// `pane_current_command`, which changes the moment the operator runs something
// else in the pane, and the product contract is that metadata.name is never
// derived from a runtime attribute. What the runtime reported goes to
// status.displayTitle instead -- the duplicate-allowed field that exists for
// exactly this, and the same field the import path fills.
//
// No Agent is minted, whatever the pane happens to be running. Reading the pane
// options or its title to decide that a Window owes an Agent resource would be a
// content heuristic deciding registry topology, which is the judgment
// bindLegacyPaneTx already records for an adopted pane. Agent phase belongs to
// its own track.
//
// Nothing existing is touched: no uid is changed, merged, or reassigned, and no
// Window spec is rewritten. This adds one Pane and stops.
func (m Mutator) ImportOrphanPane(reg *Registry, windowUID string, observed LegacyPane, operationID string) (Pane, error) {
	const op = "import orphan pane"

	window, ok := reg.Window(windowUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "window %q does not exist", windowUID)
	}
	cwd := strings.TrimSpace(observed.CWD)
	if cwd == "" {
		if project, ok := reg.Project(window.Metadata.OwnerUID()); ok {
			cwd = project.Spec.Root
		}
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, "", FallbackPaneNameBase, observed.Command, cwd, nil, now)
	if err != nil {
		txn.Rollback()
		return Pane{}, err
	}
	pane.Status.DisplayTitle = DerivePaneDisplayTitle(observed.Provider, observed.Topic, observed.Command, observed.Title)
	reg.storePaneStatus(pane.Metadata.UID, pane.Status)
	txn.Commit()
	reg.UpdatedAt = now
	return pane, nil
}

// AnnotationAgentTopic is the non-identifying annotation that carries an AI
// topic. Topics are never a name or a selector input.
const AnnotationAgentTopic = "projmux.io/agent-topic"

func (r *Registry) storePaneStatus(uid string, status PaneStatus) {
	for i := range r.Panes {
		if r.Panes[i].Metadata.UID == uid {
			r.Panes[i].Status = status
			return
		}
	}
}
