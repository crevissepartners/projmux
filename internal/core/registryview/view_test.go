package registryview

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// registryFixture builds a two-Project Registry whose second Project owns a
// Window with one shell Pane, one Agent, and the Agent's managed Pane.
//
// It is written by hand rather than through the mutator so the slice order --
// which is the view's order contract -- is stated here and cannot drift with a
// change to insertion behavior elsewhere.
func registryFixture() coremetadata.Registry {
	created := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	meta := func(uid, name, owner string, kind coremetadata.Kind) coremetadata.ObjectMeta {
		out := coremetadata.ObjectMeta{UID: uid, Name: name, CreatedAt: created}
		if owner != "" {
			out.OwnerRef = &coremetadata.OwnerRef{Kind: kind, UID: owner}
		}
		return out
	}
	return coremetadata.Registry{
		APIVersion:    coremetadata.APIVersion,
		SchemaVersion: coremetadata.SchemaVersion,
		Projects: []coremetadata.Project{
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
				Metadata: meta("proj-alpha", "alpha", "", ""),
				Spec:     coremetadata.ProjectSpec{Root: "/src/alpha"},
			},
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
				Metadata: meta("proj-bravo", "bravo", "", ""),
				Spec:     coremetadata.ProjectSpec{Root: "/src/bravo"},
				Status: coremetadata.ProjectStatus{
					Session: &coremetadata.SessionProjection{Name: "src-bravo"},
				},
			},
		},
		Windows: []coremetadata.Window{
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
				Metadata: meta("win-main", "main", "proj-bravo", coremetadata.KindProject),
				Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pane-shell"},
			},
		},
		Panes: []coremetadata.Pane{
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
				Metadata: meta("pane-shell", "shell", "win-main", coremetadata.KindWindow),
				Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
			},
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
				Metadata: meta("pane-agent", "codex", "agent-one", coremetadata.KindAgent),
				Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent},
			},
		},
		Agents: []coremetadata.Agent{
			{
				APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
				Metadata: meta("agent-one", "codex", "win-main", coremetadata.KindWindow),
				Spec:     coremetadata.AgentSpec{Provider: "codex"},
				Status: coremetadata.AgentStatus{
					Phase:   coremetadata.PhaseRunning,
					PaneRef: "pane-agent",
				},
			},
		},
	}
}

// liveInventory observes every fixture resource on an app-owned server plus one
// object projmux does not own and one control session.
func liveInventory() resourcegraph.Inventory {
	return resourcegraph.Inventory{
		Transport: resourcegraph.Transport{
			Kind: resourcegraph.TransportSocketPath, Value: "/run/projmux",
			Source: resourcegraph.TransportSourceInheritedEnv,
		},
		HostMode: resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$1", Name: "src-bravo", ProjectUID: "proj-bravo"},
			{ID: "$2", Name: "home", Role: resourcegraph.ControlSessionRole},
			{ID: "$3", Name: "scratch", Ephemeral: true},
		},
		Windows: []resourcegraph.Window{
			{ID: "@1", SessionID: "$1", Index: "0", UID: "win-main", DisplayName: "main"},
			{ID: "@9", SessionID: "$1", Index: "1", DisplayName: "hand-opened"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%1", WindowID: "@1", UID: "pane-shell"},
			{ID: "%2", WindowID: "@1", UID: "pane-agent", AgentProvider: "codex"},
		},
	}
}

func rowIDs(view View) []string {
	out := make([]string, 0, len(view.Rows))
	for _, row := range view.Rows {
		out = append(out, row.ID)
	}
	return out
}

func rowByID(t *testing.T, view View, id string) Row {
	t.Helper()
	row, ok := view.Row(id)
	if !ok {
		t.Fatalf("row %q is absent from %v", id, rowIDs(view))
	}
	return row
}

func TestBuildProjectsBoundedAgentProgressAndAggregatesWindowAtReadTime(t *testing.T) {
	t.Parallel()
	registry := registryFixture()
	progress := coremetadata.AgentProgress{
		TurnRef: "turn-1", Activity: coremetadata.ProgressCommand,
		PlanCompleted: 2, PlanTotal: 4, ChangedFiles: 3,
		ObservedAt: time.Unix(1, 0), Source: coremetadata.AgentProgressSource,
	}
	registry.Agents[0].Status.Progress = progress
	registry.Agents[0].Status.Interaction = coremetadata.AgentInteraction{
		Kind: coremetadata.InteractionInProgress, ObservedAt: time.Unix(1, 0), Source: coremetadata.AgentProgressSource,
	}
	view := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})
	if got := rowByID(t, view, "uid:agent-one").Progress; got != progress {
		t.Fatalf("agent progress = %+v, want %+v", got, progress)
	}
	window := rowByID(t, view, "uid:win-main")
	if window.ActiveAgents != 1 || window.WorkingAgents != 1 || window.ApprovalAgents != 0 {
		t.Fatalf("window read projection = active %d working %d approval %d", window.ActiveAgents, window.WorkingAgents, window.ApprovalAgents)
	}
	if stored, _ := registry.Window("win-main"); stored == nil {
		t.Fatal("fixture window missing")
	} else if raw := strings.ToLower(fmt.Sprintf("%+v", stored.Status)); strings.Contains(raw, "working") || strings.Contains(raw, "progress") {
		t.Fatalf("window stored an aggregate: %+v", stored.Status)
	}
}

func TestWindowProgressCountsSaturateAboveUint8Capacity(t *testing.T) {
	t.Parallel()
	agents := make([]resourcegraph.AgentNode, 0, 600)
	for range 300 {
		agents = append(agents, resourcegraph.AgentNode{
			WindowUID: "win-many",
			Agent: coremetadata.Agent{Status: coremetadata.AgentStatus{
				Phase: coremetadata.PhaseRunning,
				Progress: coremetadata.AgentProgress{
					TurnRef: "turn", Source: coremetadata.AgentProgressSource,
				},
				Interaction: coremetadata.AgentInteraction{Kind: coremetadata.InteractionInProgress},
			}},
		})
	}
	for range 300 {
		agents = append(agents, resourcegraph.AgentNode{
			WindowUID: "win-many",
			Agent: coremetadata.Agent{Status: coremetadata.AgentStatus{
				Phase:       coremetadata.PhaseRunning,
				Interaction: coremetadata.AgentInteraction{Kind: coremetadata.InteractionApprovalRequired},
			}},
		})
	}
	counts := (&builder{graph: resourcegraph.Graph{Agents: agents}}).windowProgressCounts("win-many")
	if counts.active != ^uint8(0) || counts.working != ^uint8(0) || counts.approval != ^uint8(0) {
		t.Fatalf("saturated counts = active %d working %d approval %d", counts.active, counts.working, counts.approval)
	}
}

// TestBuildOrdersProjectsThenWindowsThenPanesAndAgents pins the hierarchy and
// the order. The order is the Registry's slice order at every level, so this is
// the golden the identity-stability tests below compare against.
func TestBuildOrdersProjectsThenWindowsThenPanesAndAgents(t *testing.T) {
	t.Parallel()

	view := Build(Input{Graph: resourcegraph.Resolve(registryFixture(), liveInventory())})

	want := []string{
		"uid:proj-alpha",
		"uid:proj-bravo",
		"uid:win-main",
		"uid:pane-shell",
		"uid:agent-one",
		"uid:pane-agent",
		RuntimeLinkID,
	}
	if got := rowIDs(view); !reflect.DeepEqual(got, want) {
		t.Fatalf("row ids = %#v, want %#v", got, want)
	}

	depths := map[string]int{
		"uid:proj-bravo": 0, "uid:win-main": 1, "uid:pane-shell": 2,
		"uid:agent-one": 2, "uid:pane-agent": 3,
	}
	for id, depth := range depths {
		if got := rowByID(t, view, id).Depth; got != depth {
			t.Fatalf("row %q depth = %d, want %d", id, got, depth)
		}
	}
	parents := map[string]string{
		"uid:win-main": "uid:proj-bravo", "uid:pane-shell": "uid:win-main",
		"uid:agent-one": "uid:win-main", "uid:pane-agent": "uid:agent-one",
	}
	for id, parent := range parents {
		if got := rowByID(t, view, id).ParentID; got != parent {
			t.Fatalf("row %q parent = %q, want %q", id, got, parent)
		}
	}
}

// TestBuildEmitsAnAgentPaneOnlyUnderItsAgent guards the one row an Agent-owned
// Pane could plausibly be emitted twice as: the graph resolves its effective
// Window, so it is a member of both scopes.
func TestBuildEmitsAnAgentPaneOnlyUnderItsAgent(t *testing.T) {
	t.Parallel()

	view := Build(Input{Graph: resourcegraph.Resolve(registryFixture(), liveInventory())})

	count := 0
	for _, row := range view.Rows {
		if row.ID == "uid:pane-agent" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("agent pane row count = %d, want exactly one", count)
	}
	if got := rowByID(t, view, "uid:pane-agent").ParentID; got != "uid:agent-one" {
		t.Fatalf("agent pane parent = %q, want the Agent row", got)
	}
}

// TestBuildKeepsIdentityAndOrderWithoutATmuxServer is acceptance (1): the same
// Registry produces the same rows in the same order with no transport at all,
// and every row still offers the action that would bring it back.
func TestBuildKeepsIdentityAndOrderWithoutATmuxServer(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	live := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})
	dark := Build(Input{Graph: resourcegraph.Resolve(registry, resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportNone, Source: resourcegraph.TransportSourceNone},
	})})

	if got, want := rowIDs(dark), rowIDs(live); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-server row ids = %#v, want the same ids as a live server %#v", got, want)
	}
	for _, id := range []string{"uid:proj-bravo", "uid:win-main", "uid:pane-shell"} {
		row := rowByID(t, dark, id)
		if row.Status != resourcegraph.StatusUnknown {
			t.Fatalf("row %q status = %q, want unknown when the observation could not be taken", id, row.Status)
		}
		if !row.Allows(ActionStart) {
			t.Fatalf("row %q actions = %v, want the offline start action", id, row.Actions)
		}
		if row.Allows(ActionOpen) {
			t.Fatalf("row %q offers open with no observed runtime object: %v", id, row.Actions)
		}
	}
	if agent := rowByID(t, dark, "uid:agent-one"); !agent.Allows(ActionResume) {
		t.Fatalf("agent actions = %v, want resume", agent.Actions)
	}
}

// TestBuildRuntimeTransitionChangesOnlyStatus is acceptance (2): opening or
// closing a runtime object must move no row and rename no row.
func TestBuildRuntimeTransitionChangesOnlyStatus(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	before := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})

	closed := liveInventory()
	closed.Panes = nil
	after := Build(Input{Graph: resourcegraph.Resolve(registry, closed)})

	if got, want := rowIDs(after), rowIDs(before); !reflect.DeepEqual(got, want) {
		t.Fatalf("row ids after closing panes = %#v, want %#v", got, want)
	}
	if got := rowByID(t, before, "uid:pane-shell").Status; got != resourcegraph.StatusLive {
		t.Fatalf("shell pane status before = %q, want live", got)
	}
	if got := rowByID(t, after, "uid:pane-shell").Status; got != resourcegraph.StatusOffline {
		t.Fatalf("shell pane status after = %q, want offline", got)
	}
	if got := rowByID(t, after, "uid:win-main").Status; got != resourcegraph.StatusLive {
		t.Fatalf("window status after = %q, want live; only the pane closed", got)
	}
}

// TestBuildKeepsRowsIdenticalAcrossHostModes is acceptance (5): identity, order,
// and action eligibility are the same on an app-owned and a standalone server;
// only the attribution of objects projmux does not own differs, and that lives
// in the runtime tally rather than in a row.
func TestBuildKeepsRowsIdenticalAcrossHostModes(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	app := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})
	guest := liveInventory()
	guest.HostMode = resourcegraph.HostModeStandalone
	standalone := Build(Input{Graph: resourcegraph.Resolve(registry, guest)})

	if got, want := rowIDs(standalone), rowIDs(app); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone row ids = %#v, want %#v", got, want)
	}
	for _, row := range app.Rows {
		other := rowByID(t, standalone, row.ID)
		if !reflect.DeepEqual(other.Actions, row.Actions) {
			t.Fatalf("row %q actions = %v on standalone, want %v", row.ID, other.Actions, row.Actions)
		}
		if other.Status != row.Status {
			t.Fatalf("row %q status = %q on standalone, want %q", row.ID, other.Status, row.Status)
		}
	}
	if app.Runtime.Unattributed == 0 {
		t.Fatalf("app-owned runtime tally = %+v, want the hand-opened window counted as unattributed", app.Runtime)
	}
	if standalone.Runtime.Foreign == 0 {
		t.Fatalf("standalone runtime tally = %+v, want the hand-opened window counted as foreign", standalone.Runtime)
	}
}

// TestBuildNeverEmitsAControlOrEphemeralRow is acceptance (3) on the producing
// side: Home and a scratch session are runtime, not resources, so no section
// can contain them.
func TestBuildNeverEmitsAControlOrEphemeralRow(t *testing.T) {
	t.Parallel()

	view := Build(Input{Graph: resourcegraph.Resolve(registryFixture(), liveInventory())})

	for _, row := range view.Rows {
		for _, forbidden := range []string{"home", "scratch", "hand-opened"} {
			if strings.EqualFold(row.Name, forbidden) || strings.EqualFold(row.DisplayName, forbidden) {
				t.Fatalf("row %+v names a runtime-only object; it belongs to the Runtime surface", row)
			}
		}
	}
	if view.Runtime.Control != 1 {
		t.Fatalf("control tally = %d, want the one marked control session", view.Runtime.Control)
	}
	if view.Runtime.Ephemeral != 1 {
		t.Fatalf("ephemeral tally = %d, want the one scratch session", view.Runtime.Ephemeral)
	}
}

// TestBuildDoesNotInventControlFromASessionName is the honesty guard: a session
// literally called "home" with no role marker is unattributed, and nothing here
// may promote it.
func TestBuildDoesNotInventControlFromASessionName(t *testing.T) {
	t.Parallel()

	inventory := liveInventory()
	for i := range inventory.Sessions {
		if inventory.Sessions[i].Name == "home" {
			inventory.Sessions[i].Role = ""
		}
	}
	view := Build(Input{Graph: resourcegraph.Resolve(registryFixture(), inventory)})

	if view.Runtime.Control != 0 {
		t.Fatalf("control tally = %d, want zero: no session carries the role marker", view.Runtime.Control)
	}
	if view.Runtime.Unattributed == 0 {
		t.Fatalf("runtime tally = %+v, want the unmarked home session counted as unattributed", view.Runtime)
	}
}

// TestBuildSeparatesUnregisteredCandidatesFromProjects pins the section split
// and the deduplication of a directory that is already a Project root.
func TestBuildSeparatesUnregisteredCandidatesFromProjects(t *testing.T) {
	t.Parallel()

	view := Build(Input{
		Graph: resourcegraph.Resolve(registryFixture(), liveInventory()),
		Candidates: []Candidate{
			{Path: "/src/alpha"},
			{Path: "/src/gamma", DisplayName: "gamma"},
			{Path: "/src/gamma"},
		},
	})

	unregistered := view.Section(SectionUnregistered)
	if len(unregistered) != 1 {
		t.Fatalf("unregistered rows = %#v, want exactly the one directory no Project claims", unregistered)
	}
	row := unregistered[0]
	if row.ID != CandidateID("/src/gamma") || row.Kind != RowKindCandidate {
		t.Fatalf("unregistered row = %+v, want the gamma candidate", row)
	}
	if !reflect.DeepEqual(row.Actions, []Action{ActionBootstrap}) {
		t.Fatalf("candidate actions = %v, want bootstrap only", row.Actions)
	}
	if got := len(view.Section(SectionProjects)); got != 6 {
		t.Fatalf("project section rows = %d, want the six Registry rows", got)
	}
}

// TestBuildMissingRootOffersRebindOnly pins the one eligibility rule that
// outranks the runtime: a Project that lost spec.root needs an explicit repair
// whatever tmux is doing.
func TestBuildMissingRootOffersRebindOnly(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	registry.Projects[1].Status.Conditions = []coremetadata.Condition{{
		Type:   coremetadata.ConditionMissingRoot,
		Status: coremetadata.ConditionTrue,
	}}
	view := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})

	for _, id := range []string{"uid:proj-bravo", "uid:win-main", "uid:pane-shell", "uid:agent-one"} {
		row := rowByID(t, view, id)
		if row.Status != resourcegraph.StatusMissingRoot {
			t.Fatalf("row %q status = %q, want missing-root", id, row.Status)
		}
		if !reflect.DeepEqual(row.Actions, []Action{ActionRebind, ActionDelete}) {
			t.Fatalf("row %q actions = %v, want rebind and delete only", id, row.Actions)
		}
	}
}

// TestBuildAlwaysEmitsTheRuntimeLink keeps the escape hatch learnable: it is
// present on an empty Registry, on a quiet server, and with no transport.
func TestBuildAlwaysEmitsTheRuntimeLink(t *testing.T) {
	t.Parallel()

	empty := Build(Input{Graph: resourcegraph.Resolve(coremetadata.NewRegistry(), resourcegraph.Inventory{})})
	if got := rowIDs(empty); !reflect.DeepEqual(got, []string{RuntimeLinkID}) {
		t.Fatalf("empty view rows = %#v, want the runtime link alone", got)
	}
	link := rowByID(t, empty, RuntimeLinkID)
	if !reflect.DeepEqual(link.Actions, []Action{ActionRuntime}) {
		t.Fatalf("runtime link actions = %v, want the runtime action", link.Actions)
	}
	if link.Status != resourcegraph.StatusUnknown {
		t.Fatalf("runtime link status = %q, want unknown with no transport", link.Status)
	}
}

// TestDescendantsReturnsOneProjectSubtree pins the selector the hierarchy
// surface enters through.
func TestDescendantsReturnsOneProjectSubtree(t *testing.T) {
	t.Parallel()

	view := Build(Input{Graph: resourcegraph.Resolve(registryFixture(), liveInventory())})

	got := make([]string, 0, 5)
	for _, row := range view.Descendants(ProjectID("proj-bravo")) {
		got = append(got, row.ID)
	}
	want := []string{"uid:proj-bravo", "uid:win-main", "uid:pane-shell", "uid:agent-one", "uid:pane-agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants = %#v, want %#v", got, want)
	}
	if got := view.Descendants(ProjectID("proj-alpha")); len(got) != 1 {
		t.Fatalf("childless project descendants = %#v, want the project row alone", got)
	}
}

// TestBuildReportsAgentPhaseVerbatim guards the one field this package could be
// tempted to interpret. Phase is a lifecycle decision and lifecycle is not here.
func TestBuildReportsAgentPhaseVerbatim(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	registry.Agents[0].Status.Phase = coremetadata.PhaseFailed
	view := Build(Input{Graph: resourcegraph.Resolve(registry, liveInventory())})

	agent := rowByID(t, view, "uid:agent-one")
	if agent.Phase != string(coremetadata.PhaseFailed) {
		t.Fatalf("agent phase = %q, want the Registry value verbatim", agent.Phase)
	}
	if agent.Status != resourcegraph.StatusLive {
		t.Fatalf("agent status = %q, want live: its managed Pane is observed", agent.Status)
	}
}

// TestBuildMutatesNeitherArgument is the purity guard.
func TestBuildMutatesNeitherArgument(t *testing.T) {
	t.Parallel()

	registry := registryFixture()
	before := registry.Clone()
	graph := resourcegraph.Resolve(registry, liveInventory())
	Build(Input{Graph: graph, Candidates: []Candidate{{Path: "/src/gamma"}}})

	if !reflect.DeepEqual(registry, before) {
		t.Fatal("Build mutated the Registry it was given")
	}
}
