package registryview

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// Section is the closed set of groups the primary navigation renders.
//
// The split is the whole point of the surface. A managed row is a resource with
// an identity projmux owns; a candidate is a directory someone might turn into
// one; the runtime entry is a link to everything that is neither. Keeping them
// in one undifferentiated list is what made "this Project is gone" and "this
// directory was never a Project" look the same.
type Section string

const (
	// SectionProjects holds the Registry resources: Projects and, beneath
	// each, its Windows, shell Panes, and Agents.
	SectionProjects Section = "projects"
	// SectionControl holds the Registry graph rooted at the app control
	// session. The Projects picker does not render this section; Home remains
	// its synthetic chrome row while the read model preserves exact ownership.
	SectionControl Section = "control"
	// SectionUnregistered holds discovered filesystem roots that no Registry
	// Project claims. They are bootstrap candidates, never managed rows.
	SectionUnregistered Section = "unregistered"
	// SectionRuntime holds the single link to the Runtime diagnostics surface.
	SectionRuntime Section = "runtime"
)

// RowKind is what one row is.
type RowKind string

const (
	RowKindProject        RowKind = "project"
	RowKindControlSession RowKind = "control-session"
	RowKindWindow         RowKind = "window"
	RowKindPane           RowKind = "pane"
	RowKindAgent          RowKind = "agent"
	RowKindCandidate      RowKind = "candidate"
	RowKindRuntimeLink    RowKind = "runtime-link"
)

// Action is the closed set of things the primary navigation may offer on a row.
//
// Eligibility is decided from resource state, never from the presence of a tmux
// object alone. That distinction is load bearing: an unobservable server and an
// observed-empty server are different facts, and neither of them is a reason to
// withhold the action that would bring a logical resource back.
type Action string

const (
	// ActionOpen moves the operator to an observed runtime object.
	ActionOpen Action = "open"
	// ActionStart materializes the runtime of a logical resource. The row
	// offers it; a separate command performs it.
	ActionStart Action = "start"
	// ActionResume is the Agent spelling of start.
	ActionResume Action = "resume"
	// ActionRebind repoints the owning Project at an existing root. It is the
	// only action a missing-root row offers besides delete, because nothing
	// else can succeed until the root is real again.
	ActionRebind Action = "rebind"
	// ActionDelete removes the logical resource. It is always eligible: a
	// resource whose runtime is gone is exactly the one an operator most often
	// wants to drop, and hiding the verb until tmux agrees would make the
	// Registry undeletable on a machine with no server.
	ActionDelete Action = "delete"
	// ActionBootstrap opens an unregistered filesystem candidate the way the
	// project picker always has. It creates no Registry resource by itself.
	ActionBootstrap Action = "bootstrap"
	// ActionRuntime opens the Runtime diagnostics surface.
	ActionRuntime Action = "runtime"
)

// Row is one navigation row.
//
// ID is the identity a selection is stored under and the only thing a refresh
// must preserve. It is derived from the resource uid, or from the absolute path
// for a candidate, so it cannot change when a display name, a tmux object, or a
// sort input changes.
type Row struct {
	Section Section `json:"section"`
	Kind    RowKind `json:"kind"`
	ID      string  `json:"id"`
	// UID is the Registry uid, empty for a candidate or the runtime link.
	UID string `json:"uid,omitempty"`
	// ParentID is the ID of the row that owns this one, empty at a section root.
	ParentID string `json:"parentID,omitempty"`
	// Depth is the nesting level inside its section, zero at the root.
	Depth int `json:"depth"`
	// Name is the stable within-scope name. It is a query key, not a label.
	Name string `json:"name,omitempty"`
	// DisplayName is what a human reads. It may duplicate and is never identity.
	DisplayName string `json:"displayName,omitempty"`
	// Root is the Project root for a Project or candidate row, and the owning
	// Project's root for a row beneath one.
	Root string `json:"root,omitempty"`
	// SessionName is the Project's projected persistent session name when the
	// Registry records one. It is empty when the Registry has never projected a
	// session, and the caller derives one from Root rather than guessing here.
	SessionName string `json:"sessionName,omitempty"`
	// Provider is the Agent's normalized provider id.
	Provider string `json:"provider,omitempty"`
	// Phase is the Agent lifecycle phase, reported verbatim from the Registry.
	// Nothing in this package transitions it.
	Phase string `json:"phase,omitempty"`
	// Progress is the Agent's current exact-turn bounded projection. Window rows
	// carry only read-time child counts; no sentence or provider content is
	// copied into the Window resource.
	Progress       coremetadata.AgentProgress `json:"progress,omitzero"`
	ActiveAgents   uint8                      `json:"activeAgents,omitempty"`
	ApprovalAgents uint8                      `json:"approvalAgents,omitempty"`
	WorkingAgents  uint8                      `json:"workingAgents,omitempty"`
	// Role is the Pane role, shell or agent.
	Role string `json:"role,omitempty"`
	// Status is the runtime overlay of this row on the exact observed host.
	Status resourcegraph.Status `json:"status"`
	// Live and Active are independent read-time facts for Window rows. Active
	// is exact window_active evidence, never an inference from stored metadata.
	Live   bool `json:"live,omitempty"`
	Active bool `json:"active,omitempty"`
	// MissingRoot marks a row whose owning Project lost spec.root.
	MissingRoot bool `json:"missingRoot,omitempty"`
	// Runtime is the exact handle of the observed object, nil when none was
	// observed. It is routing, never identity.
	Runtime *resourcegraph.RuntimeRef `json:"runtime,omitempty"`
	// Actions are the eligible actions, in declaration order.
	Actions []Action `json:"actions,omitempty"`
	// Reason states why this row looks the way it does, in one clause.
	Reason string `json:"reason,omitempty"`
	// Termination is the last stored termination receipt of a Pane or Agent row,
	// reported verbatim from the Registry and empty when none is stored.
	//
	// It is what turns an offline row from a fact into an explanation. Status
	// already says no runtime object mirrors this resource; this says whether a
	// control action ended it, its process exited cleanly, its process crashed,
	// or nothing accounted for the disappearance at all.
	//
	// Nothing in this package records, consumes, or transitions it. The view is a
	// read projection, and a refresh that advanced a lifecycle would make opening
	// the list change the thing the list describes.
	Termination *coremetadata.TerminationEvidence `json:"termination,omitempty"`
}

// IsLive reports whether an exact runtime object was observed for this row.
func (r Row) IsLive() bool { return r.Status == resourcegraph.StatusLive }

// Allows reports whether action is eligible on this row.
func (r Row) Allows(action Action) bool {
	return slices.Contains(r.Actions, action)
}

// RuntimeCounts is how many observed objects of each refused class the exact
// host is running.
//
// It exists so the runtime link can say what it leads to. A link that says
// "Runtime" is a menu item; one that says "3 unattributed, 1 control" is the
// answer to "where did my pane go", which is the question the Registry-first
// list creates.
type RuntimeCounts struct {
	Control      int `json:"control,omitempty"`
	Ephemeral    int `json:"ephemeral,omitempty"`
	Unattributed int `json:"unattributed,omitempty"`
	Foreign      int `json:"foreign,omitempty"`
	Recoverable  int `json:"recoverable,omitempty"`
	Conflict     int `json:"conflict,omitempty"`
}

// Total is the number of observed objects that are not managed rows.
func (c RuntimeCounts) Total() int {
	return c.Control + c.Ephemeral + c.Unattributed + c.Foreign + c.Recoverable + c.Conflict
}

// Candidate is one discovered filesystem root offered by the caller.
//
// The caller owns discovery and its order; this package owns only whether a
// candidate is already a Project. Path must be absolute and cleaned.
type Candidate struct {
	Path        string
	DisplayName string
	Pinned      bool
}

// Input is one navigation build.
type Input struct {
	// Graph is the resolved Registry-plus-runtime read model of one exact host.
	Graph resourcegraph.Graph
	// Candidates are the discovered filesystem roots, in the caller's order.
	Candidates []Candidate
}

// View is the built navigation model of one invocation.
type View struct {
	Transport   resourcegraph.Transport        `json:"transport"`
	HostMode    resourcegraph.HostMode         `json:"hostMode"`
	Unavailable []resourcegraph.Unavailability `json:"unavailable,omitempty"`
	Rows        []Row                          `json:"rows"`
	Runtime     RuntimeCounts                  `json:"runtimeCounts,omitzero"`
}

// ValidateWindowRuntimeState checks the projected Window invariants without
// changing row order or coercing malformed facts.
func (v View) ValidateWindowRuntimeState() error {
	activeByProject := map[string]string{}
	for _, row := range v.Rows {
		if row.Kind != RowKindWindow {
			continue
		}
		if row.Active && !row.Live {
			return fmt.Errorf("window row %s is active but not live", row.ID)
		}
		if !row.Active {
			continue
		}
		if previous := activeByProject[row.ParentID]; previous != "" {
			return fmt.Errorf("project row %s has multiple active windows: %s and %s", row.ParentID, previous, row.ID)
		}
		activeByProject[row.ParentID] = row.ID
	}
	return nil
}

// Observed reports whether a tmux server was reachable for this view.
func (v View) Observed() bool { return v.Transport.Present() }

// Section returns the rows of one section, in view order.
func (v View) Section(section Section) []Row {
	var out []Row
	for _, row := range v.Rows {
		if row.Section == section {
			out = append(out, row)
		}
	}
	return out
}

// Row returns the row with id.
func (v View) Row(id string) (Row, bool) {
	for _, row := range v.Rows {
		if row.ID == id {
			return row, true
		}
	}
	return Row{}, false
}

// Children returns the rows directly owned by id, in view order.
func (v View) Children(id string) []Row {
	var out []Row
	for _, row := range v.Rows {
		if row.ParentID == id {
			out = append(out, row)
		}
	}
	return out
}

// Descendants returns id's row followed by every row beneath it, in view order.
func (v View) Descendants(id string) []Row {
	var out []Row
	keep := map[string]bool{id: true}
	for _, row := range v.Rows {
		switch {
		case row.ID == id:
			out = append(out, row)
		case keep[row.ParentID]:
			keep[row.ID] = true
			out = append(out, row)
		}
	}
	return out
}

// ProjectID renders the stable row id of a Project uid.
func ProjectID(uid string) string { return rowID(uid) }

// CandidateID renders the stable row id of a filesystem candidate.
func CandidateID(path string) string { return "path:" + filepath.Clean(strings.TrimSpace(path)) }

// RuntimeLinkID is the fixed id of the Runtime diagnostics link row.
const RuntimeLinkID = "runtime:diagnostics"

func rowID(uid string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	return "uid:" + uid
}

// Build projects one resolved graph and the caller's discovery result onto the
// navigation rows.
//
// The traversal order is the Registry's own slice order at every level, which
// is insertion order and is preserved verbatim by the store. Deriving order
// from the Registry rather than from a sort over runtime facts is what makes
// the same Registry render identically on every host and across a refresh.
func Build(in Input) View {
	b := &builder{graph: in.Graph}
	b.projects()
	b.candidates(in.Candidates)
	b.runtimeLink()
	return View{
		Transport:   in.Graph.Transport,
		HostMode:    in.Graph.HostMode,
		Unavailable: in.Graph.Unavailable,
		Rows:        b.rows,
		Runtime:     b.counts,
	}
}

type builder struct {
	graph  resourcegraph.Graph
	rows   []Row
	counts RuntimeCounts
	// roots holds the cleaned spec.root of every Registry Project, so a
	// discovered directory that is already managed is not offered twice.
	roots map[string]bool
}

func (b *builder) projects() {
	b.roots = map[string]bool{}
	for _, project := range b.graph.Projects {
		root := cleanPath(project.Project.Spec.Root)
		if root != "" {
			b.roots[root] = true
		}
		projectID := rowID(project.Project.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section:     SectionProjects,
			Kind:        RowKindProject,
			ID:          projectID,
			UID:         project.Project.Metadata.UID,
			Depth:       0,
			Name:        project.Project.Metadata.Name,
			DisplayName: displayName(project.Project.Metadata),
			Root:        root,
			SessionName: projectSessionName(project.Project),
			Status:      project.Status,
			MissingRoot: project.MissingRoot,
			Runtime:     project.Runtime,
			Actions:     resourceActions(RowKindProject, project.Status, project.MissingRoot),
			Reason:      statusReason(project.Status, project.MissingRoot),
		})
		b.windows(project, projectID)
	}
	for _, control := range b.graph.ControlSessions {
		controlID := rowID(control.ControlSession.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section: SectionControl, Kind: RowKindControlSession, ID: controlID,
			UID: control.ControlSession.Metadata.UID, Name: control.ControlSession.Metadata.Name,
			DisplayName: displayName(control.ControlSession.Metadata), SessionName: control.ControlSession.Spec.Session,
			Status: control.Status, Runtime: control.Runtime, Reason: statusReason(control.Status, false),
		})
		b.controlWindows(control.ControlSession.Metadata.UID, controlID)
	}
}

func (b *builder) controlWindows(controlUID, controlID string) {
	for _, window := range b.graph.Windows {
		if window.RootKind != coremetadata.KindControlSession || window.RootUID != controlUID {
			continue
		}
		windowID := rowID(window.Window.Metadata.UID)
		counts := b.windowProgressCounts(window.Window.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section: SectionControl, Kind: RowKindWindow, ID: windowID, UID: window.Window.Metadata.UID,
			ParentID: controlID, Depth: 1, Name: window.Window.Metadata.Name,
			DisplayName: window.Window.DisplayName(), Status: window.Status, Live: window.Live, Active: window.Active, Runtime: window.Runtime,
			Reason: statusReason(window.Status, false), ActiveAgents: counts.active,
			ApprovalAgents: counts.approval, WorkingAgents: counts.working,
		})
		b.controlWindowPanes(window.Window.Metadata.UID, windowID)
	}
}

func (b *builder) controlWindowPanes(windowUID, windowID string) {
	for _, pane := range b.graph.Panes {
		if pane.WindowUID != windowUID || pane.AgentUID != "" {
			continue
		}
		row := b.paneRow(pane, windowID, 2, "")
		row.Section = SectionControl
		row.Actions = nil
		b.rows = append(b.rows, row)
	}
	for _, agent := range b.graph.Agents {
		if agent.WindowUID != windowUID {
			continue
		}
		agentID := rowID(agent.Agent.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section: SectionControl, Kind: RowKindAgent, ID: agentID, UID: agent.Agent.Metadata.UID,
			ParentID: windowID, Depth: 2, Name: agent.Agent.Metadata.Name,
			DisplayName: displayName(agent.Agent.Metadata), Provider: agent.Agent.Spec.Provider,
			Phase: string(agent.Agent.Status.Phase), Progress: agent.Agent.Status.Progress, Status: agent.Status, Runtime: agent.Runtime,
			Reason: statusReason(agent.Status, false), Termination: agent.Agent.Status.LastTermination.Clone(),
		})
		for _, pane := range b.graph.Panes {
			if pane.AgentUID != agent.Agent.Metadata.UID {
				continue
			}
			row := b.paneRow(pane, agentID, 3, "")
			row.Section = SectionControl
			row.Actions = nil
			b.rows = append(b.rows, row)
		}
	}
}

func (b *builder) windows(project resourcegraph.ProjectNode, projectID string) {
	projectUID := project.Project.Metadata.UID
	root := cleanPath(project.Project.Spec.Root)
	for _, window := range b.graph.Windows {
		if window.ProjectUID != projectUID {
			continue
		}
		windowID := rowID(window.Window.Metadata.UID)
		counts := b.windowProgressCounts(window.Window.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section:      SectionProjects,
			Kind:         RowKindWindow,
			ID:           windowID,
			UID:          window.Window.Metadata.UID,
			ParentID:     projectID,
			Depth:        1,
			Name:         window.Window.Metadata.Name,
			DisplayName:  window.Window.DisplayName(),
			Root:         root,
			Status:       window.Status,
			Live:         window.Live,
			Active:       window.Active,
			MissingRoot:  window.MissingRoot,
			Runtime:      window.Runtime,
			Actions:      resourceActions(RowKindWindow, window.Status, window.MissingRoot),
			Reason:       statusReason(window.Status, window.MissingRoot),
			ActiveAgents: counts.active, ApprovalAgents: counts.approval, WorkingAgents: counts.working,
		})
		b.windowPanes(window.Window.Metadata.UID, windowID, root)
		b.agents(window.Window.Metadata.UID, windowID, root)
	}
}

// windowPanes emits the shell Panes a Window owns directly.
//
// An Agent-owned Pane is deliberately skipped here and emitted under its Agent
// instead. Both spellings are true -- the graph resolves an Agent Pane's
// effective Window so tmux containment stays testable -- but listing it twice
// would make one runtime object into two rows, and a selection would then
// depend on which copy the operator happened to be on.
func (b *builder) windowPanes(windowUID, windowID, root string) {
	for _, pane := range b.graph.Panes {
		if pane.WindowUID != windowUID || pane.AgentUID != "" {
			continue
		}
		b.rows = append(b.rows, b.paneRow(pane, windowID, 2, root))
	}
}

func (b *builder) agents(windowUID, windowID, root string) {
	for _, agent := range b.graph.Agents {
		if agent.WindowUID != windowUID {
			continue
		}
		agentID := rowID(agent.Agent.Metadata.UID)
		b.rows = append(b.rows, Row{
			Section:     SectionProjects,
			Kind:        RowKindAgent,
			ID:          agentID,
			UID:         agent.Agent.Metadata.UID,
			ParentID:    windowID,
			Depth:       2,
			Name:        agent.Agent.Metadata.Name,
			DisplayName: displayName(agent.Agent.Metadata),
			Root:        root,
			Provider:    agent.Agent.Spec.Provider,
			Phase:       string(agent.Agent.Status.Phase),
			Progress:    agent.Agent.Status.Progress,
			Status:      agent.Status,
			MissingRoot: agent.MissingRoot,
			Runtime:     agent.Runtime,
			Actions:     resourceActions(RowKindAgent, agent.Status, agent.MissingRoot),
			Reason:      statusReason(agent.Status, agent.MissingRoot),
			Termination: agent.Agent.Status.LastTermination.Clone(),
		})
		for _, pane := range b.graph.Panes {
			if pane.AgentUID != agent.Agent.Metadata.UID {
				continue
			}
			b.rows = append(b.rows, b.paneRow(pane, agentID, 3, root))
		}
	}
}

type windowProgressCounts struct{ active, approval, working uint8 }

func (b *builder) windowProgressCounts(windowUID string) windowProgressCounts {
	var counts windowProgressCounts
	for _, agent := range b.graph.Agents {
		if agent.WindowUID != windowUID || agent.Agent.Status.Phase != coremetadata.PhaseRunning {
			continue
		}
		if !agent.Agent.Status.Progress.IsZero() {
			incrementUint8Saturating(&counts.active)
		}
		switch agent.Agent.Status.Interaction.Kind {
		case coremetadata.InteractionApprovalRequired:
			incrementUint8Saturating(&counts.approval)
		case coremetadata.InteractionInProgress:
			incrementUint8Saturating(&counts.working)
		}
	}
	return counts
}

func incrementUint8Saturating(value *uint8) {
	if *value != ^uint8(0) {
		*value++
	}
}

func (b *builder) paneRow(pane resourcegraph.PaneNode, parentID string, depth int, root string) Row {
	return Row{
		Section:     SectionProjects,
		Kind:        RowKindPane,
		ID:          rowID(pane.Pane.Metadata.UID),
		UID:         pane.Pane.Metadata.UID,
		ParentID:    parentID,
		Depth:       depth,
		Name:        pane.Pane.Metadata.Name,
		DisplayName: paneDisplayName(pane.Pane),
		Root:        root,
		Role:        string(pane.Pane.Spec.Role),
		Status:      pane.Status,
		MissingRoot: pane.MissingRoot,
		Runtime:     pane.Runtime,
		Actions:     resourceActions(RowKindPane, pane.Status, pane.MissingRoot),
		Reason:      statusReason(pane.Status, pane.MissingRoot),
		Termination: pane.Pane.Status.LastTermination.Clone(),
	}
}

// candidates emits the discovered roots no Registry Project claims.
//
// A directory that is already a Project root is dropped rather than duplicated:
// it is above, with an identity, and a second copy of it in the bootstrap
// section would be a row whose actions contradict the managed one's.
func (b *builder) candidates(candidates []Candidate) {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		path := cleanPath(candidate.Path)
		if path == "" || b.roots[path] || seen[path] {
			continue
		}
		seen[path] = true
		b.rows = append(b.rows, Row{
			Section:     SectionUnregistered,
			Kind:        RowKindCandidate,
			ID:          CandidateID(path),
			Depth:       0,
			Name:        path,
			DisplayName: strings.TrimSpace(candidate.DisplayName),
			Root:        path,
			Status:      resourcegraph.StatusOffline,
			Actions:     []Action{ActionBootstrap},
			Reason:      "discovered directory with no Registry Project",
		})
	}
}

// runtimeLink emits the single row that leads to the Runtime surface.
//
// It is emitted unconditionally, including when the counts are zero and when
// there is no transport at all. A link that appears only when something was
// found is a link an operator cannot learn, and "nothing here that projmux does
// not own" is itself an answer worth being able to ask for.
func (b *builder) runtimeLink() {
	for _, node := range b.graph.Runtime {
		switch node.Class {
		case resourcegraph.ClassControl:
			b.counts.Control++
		case resourcegraph.ClassEphemeral:
			b.counts.Ephemeral++
		case resourcegraph.ClassUnattributed:
			b.counts.Unattributed++
		case resourcegraph.ClassForeign:
			b.counts.Foreign++
		case resourcegraph.ClassRecoverable:
			b.counts.Recoverable++
		case resourcegraph.ClassConflict:
			b.counts.Conflict++
		}
	}
	status := resourcegraph.StatusUnknown
	if b.graph.Transport.Present() {
		status = resourcegraph.StatusLive
	}
	b.rows = append(b.rows, Row{
		Section:     SectionRuntime,
		Kind:        RowKindRuntimeLink,
		ID:          RuntimeLinkID,
		Name:        "runtime",
		DisplayName: "Runtime",
		Status:      status,
		Actions:     []Action{ActionRuntime},
		Reason:      runtimeLinkReason(b.graph.Transport.Present(), b.counts),
	})
}

// resourceActions decides eligibility from resource state.
//
// There are exactly three inputs and no fourth: the kind, whether an exact
// runtime object was observed, and whether the owning Project lost its root.
// Everything else an operator might expect to matter -- which host answered,
// whether tmux was reachable, whether the observation succeeded -- deliberately
// does not, because a row's logical existence does not depend on any of them.
//
// StatusUnknown is treated exactly like StatusOffline here. That is not a
// conflation: unknown and offline are reported as different statuses, and a
// caller renders them differently, but neither of them is an observed runtime
// object, so neither may offer to move an operator to one. What they both may
// offer is the action that creates one.
func resourceActions(kind RowKind, status resourcegraph.Status, missingRoot bool) []Action {
	if missingRoot || status == resourcegraph.StatusMissingRoot {
		return []Action{ActionRebind, ActionDelete}
	}
	revive := ActionStart
	if kind == RowKindAgent {
		revive = ActionResume
	}
	if status == resourcegraph.StatusLive {
		return []Action{ActionOpen, ActionDelete}
	}
	return []Action{revive, ActionDelete}
}

// statusReason states, in one clause, why a row carries the status it does.
func statusReason(status resourcegraph.Status, missingRoot bool) string {
	if missingRoot {
		return "the owning Project lost spec.root"
	}
	switch status {
	case resourcegraph.StatusLive:
		return "an exact runtime object mirrors this resource"
	case resourcegraph.StatusOffline:
		return "the observation was readable and no runtime object mirrors this resource"
	case resourcegraph.StatusUnknown:
		return "the observation this row would be judged against could not be taken"
	case resourcegraph.StatusMissingRoot:
		return "the owning Project lost spec.root"
	default:
		return ""
	}
}

func runtimeLinkReason(observed bool, counts RuntimeCounts) string {
	if !observed {
		return "no tmux transport; runtime objects cannot be observed"
	}
	if counts.Total() == 0 {
		return "the exact host is running nothing projmux does not manage"
	}
	return "objects on the exact host that are not managed rows"
}

func projectSessionName(project coremetadata.Project) string {
	if project.Status.Session == nil {
		return ""
	}
	return strings.TrimSpace(project.Status.Session.Name)
}

func displayName(meta coremetadata.ObjectMeta) string {
	if name := strings.TrimSpace(meta.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(meta.Name)
}

func paneDisplayName(pane coremetadata.Pane) string {
	if title := strings.TrimSpace(pane.Status.DisplayTitle); title != "" {
		return title
	}
	return displayName(pane.Metadata)
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
