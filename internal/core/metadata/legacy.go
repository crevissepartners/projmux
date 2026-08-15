package metadata

import (
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/paneidentity"
)

// LegacyPane is one observed pre-v2 tmux pane. Label is the existing
// @projmux_pane_label value, which is the migration seed for the Pane *name*;
// in v2 vocabulary it is a name, not a label. metadata.labels stays reserved
// for key/value classification.
type LegacyPane struct {
	Label    string
	Provider string
	// Topic is carried for the derived display title only. It is never a name
	// seed for a Pane, a Window, or an Agent.
	Topic   string
	Command string
	Title   string
	CWD     string
}

// LegacyWindow is one observed pre-v2 tmux window.
type LegacyWindow struct {
	Name string
	// AutomaticRename is the observed window-scoped automatic-rename value.
	AutomaticRename bool
	Panes           []LegacyPane
}

// LegacySession is one observed pre-v2 tmux session anchored at a project root.
type LegacySession struct {
	Session string
	Root    string
	Windows []LegacyWindow
}

// LegacyWindowNameSeed derives the one-time Window name base for a legacy
// window.
//
//   - automatic-rename off: the current window_name is the seed.
//   - automatic-rename on: derive a stable base once, in order user Pane label,
//     provider, known shell, then "window".
//
// Agent topic and raw pane title are excluded from both paths.
func LegacyWindowNameSeed(window LegacyWindow) string {
	if !window.AutomaticRename {
		if base := SanitizeNameBase(window.Name); base != "" {
			return base
		}
	}
	for _, pane := range window.Panes {
		if base := SanitizeNameBase(pane.Label); base != "" {
			return base
		}
	}
	for _, pane := range window.Panes {
		if provider := NormalizeProvider(pane.Provider); provider != "" {
			return provider
		}
	}
	for _, pane := range window.Panes {
		command := strings.TrimSpace(pane.Command)
		if paneidentity.KnownInteractiveShell(command) {
			if base := SanitizeNameBase(command); base != "" {
				return base
			}
		}
	}
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

// ImportedWindow reports one Window created by a legacy import together with
// the transport work the tmux adapter still owes it.
type ImportedWindow struct {
	UID         string
	Name        string
	SourceIndex int
	// NeedsAutomaticRenameOff is true for every managed Window, so a
	// focused-Pane change can never overwrite the Window name.
	NeedsAutomaticRenameOff bool
}

// ImportedPane reports one Pane created by a legacy import.
type ImportedPane struct {
	UID         string
	Name        string
	WindowIndex int
	PaneIndex   int
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
// resources.
//
// Name collisions during import are automatic, not explicit, so they receive
// the lowest free suffix rather than failing: two projects whose roots share a
// basename become `name` and `name-1`, and two agents of the same provider in
// one window become `codex` and `codex-1`. An exact saved root that reappears
// reuses the same Project uid; nothing else merges uids.
func (m Mutator) ImportLegacySession(reg *Registry, legacy LegacySession, defaultShell, operationID string) (ImportResult, error) {
	const op = "import legacy session"

	root, err := m.validateRoot(op, legacy.Root)
	if err != nil {
		return ImportResult{}, err
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	result, err := m.importLegacySessionTx(txn, reg, op, legacy, root, defaultShell, now)
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

func (m Mutator) importLegacySessionTx(txn *Transaction, reg *Registry, op string, legacy LegacySession, root, defaultShell string, now time.Time) (ImportResult, error) {
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

	if !result.ProjectReused || len(reg.WindowsOf(projectUID)) == 0 {
		for wi, legacyWindow := range legacy.Windows {
			if err := m.importLegacyWindowTx(txn, reg, op, projectUID, root, defaultShell, wi, legacyWindow, now, &result); err != nil {
				return ImportResult{}, err
			}
		}
	}

	project, _ := reg.Project(projectUID)
	result.Project = project.Clone()
	return result, nil
}

func (m Mutator) importLegacyWindowTx(txn *Transaction, reg *Registry, op, projectUID, root, defaultShell string, windowIndex int, legacyWindow LegacyWindow, now time.Time, result *ImportResult) error {
	windowUID, err := m.mintUID(KindWindow)
	if err != nil {
		return err
	}
	windowName, err := reg.allocateName(op, projectUID, KindWindow, LegacyWindowNameSeed(legacyWindow), windowUID)
	if err != nil {
		return err
	}
	window := Window{
		APIVersion: APIVersion,
		Kind:       KindWindow,
		Metadata: ObjectMeta{
			UID:       windowUID,
			Name:      windowName,
			OwnerRef:  &OwnerRef{Kind: KindProject, UID: projectUID},
			CreatedAt: now,
		},
	}
	reg.Windows = append(reg.Windows, window)
	txn.record(KindWindow, windowUID)
	result.Windows = append(result.Windows, ImportedWindow{
		UID:                     windowUID,
		Name:                    windowName,
		SourceIndex:             windowIndex,
		NeedsAutomaticRenameOff: true,
	})

	legacyPanes := legacyWindow.Panes
	if len(legacyPanes) == 0 {
		legacyPanes = []LegacyPane{{}}
	}

	primaryPaneRef := ""
	for pi, legacyPane := range legacyPanes {
		cwd := strings.TrimSpace(legacyPane.CWD)
		if cwd == "" {
			cwd = root
		}
		provider := NormalizeProvider(legacyPane.Provider)
		if provider == "" && strings.TrimSpace(legacyPane.Provider) != "" {
			provider = FallbackAgentNameBase
		}

		if provider == "" {
			pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, "", LegacyPaneNameSeed(legacyPane, defaultShell), legacyPane.Command, cwd, now)
			if err != nil {
				return err
			}
			pane.Status.DisplayTitle = DerivePaneDisplayTitle(legacyPane.Provider, legacyPane.Topic, legacyPane.Command, legacyPane.Title)
			reg.storePaneStatus(pane.Metadata.UID, pane.Status)
			result.Panes = append(result.Panes, ImportedPane{UID: pane.Metadata.UID, Name: pane.Metadata.Name, WindowIndex: windowIndex, PaneIndex: pi})
			if primaryPaneRef == "" {
				primaryPaneRef = pane.Metadata.UID
			}
			continue
		}

		agentUID, err := m.mintUID(KindAgent)
		if err != nil {
			return err
		}
		agentName, err := reg.allocateName(op, windowUID, KindAgent, AgentNameBase("", legacyPane.Provider), agentUID)
		if err != nil {
			return err
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

		pane, err := m.addPaneTx(txn, reg, op, agentUID, KindAgent, PaneRoleAgent, "", ManagedPaneNameBase(agentName), legacyPane.Command, cwd, now)
		if err != nil {
			return err
		}
		pane.Status.DisplayTitle = DerivePaneDisplayTitle(legacyPane.Provider, legacyPane.Topic, legacyPane.Command, legacyPane.Title)
		reg.storePaneStatus(pane.Metadata.UID, pane.Status)

		stored, _ := reg.Agent(agentUID)
		stored.Status.Phase = PhaseRunning
		stored.Status.PaneRef = pane.Metadata.UID
		stored.Status.LastTransitionAt = now

		result.Agents = append(result.Agents, ImportedAgent{UID: agentUID, Name: agentName, PaneUID: pane.Metadata.UID, WindowIndex: windowIndex, PaneIndex: pi})
		result.Panes = append(result.Panes, ImportedPane{UID: pane.Metadata.UID, Name: pane.Metadata.Name, WindowIndex: windowIndex, PaneIndex: pi})
		if primaryPaneRef == "" {
			primaryPaneRef = pane.Metadata.UID
		}
	}

	stored, _ := reg.Window(windowUID)
	stored.Spec.PrimaryPaneRef = primaryPaneRef
	return nil
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
