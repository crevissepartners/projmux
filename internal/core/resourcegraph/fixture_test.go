package resourcegraph

import (
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// testRegistry is the desired graph every attribution test resolves against.
//
// It carries two independent Projects on purpose. A single-Project fixture
// cannot fail the invariant that matters most here -- a runtime object must
// never bind to a resource in another Project -- because there is no other
// Project to cross into.
func testRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()
	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	meta := func(uid, name string, owner *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: owner, CreatedAt: created}
	}
	own := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("project-alpha", "alpha", nil),
			Spec:     coremetadata.ProjectSpec{Root: "/src/alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("project-beta", "beta", nil),
			Spec:     coremetadata.ProjectSpec{Root: "/src/beta"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: meta("project-gone", "gone", nil),
			Spec:     coremetadata.ProjectSpec{Root: "/src/gone"},
			Status: coremetadata.ProjectStatus{Conditions: []coremetadata.Condition{{
				Type: coremetadata.ConditionMissingRoot, Status: coremetadata.ConditionTrue,
				Reason: "RootMissing", FirstObservedAt: created, LastTransitionAt: created,
			}}},
		},
	}
	registry.Windows = []coremetadata.Window{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha-1", "editor", own(coremetadata.KindProject, "project-alpha")),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pane-alpha-1"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha-2", "offline", own(coremetadata.KindProject, "project-alpha")),
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-beta-1", "editor", own(coremetadata.KindProject, "project-beta")),
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-gone-1", "editor", own(coremetadata.KindProject, "project-gone")),
		},
	}
	registry.Panes = []coremetadata.Pane{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pane-alpha-1", "shell", own(coremetadata.KindWindow, "win-alpha-1")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pane-alpha-agent", "claude", own(coremetadata.KindAgent, "agent-alpha-1")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pane-beta-1", "shell", own(coremetadata.KindWindow, "win-beta-1")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pane-gone-1", "shell", own(coremetadata.KindWindow, "win-gone-1")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		},
		// A Pane whose owner Agent is not in the registry. Validation refuses this
		// shape on write, so it exists here only to pin what the graph does when a
		// row it is handed is already damaged: refuse to bind, never guess.
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pane-dangling", "orphan", own(coremetadata.KindAgent, "agent-vanished")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent},
		},
	}
	registry.Agents = []coremetadata.Agent{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
			Metadata: meta("agent-alpha-1", "claude", own(coremetadata.KindWindow, "win-alpha-1")),
			Spec:     coremetadata.AgentSpec{Provider: "claude"},
			Status: coremetadata.AgentStatus{
				Phase: coremetadata.PhaseRunning, PaneRef: "pane-alpha-agent", LastTransitionAt: created,
			},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
			Metadata: meta("agent-alpha-2", "codex", own(coremetadata.KindWindow, "win-alpha-1")),
			Spec:     coremetadata.AgentSpec{Provider: "codex"},
			Status: coremetadata.AgentStatus{
				Phase: coremetadata.PhaseOffline, LastTransitionAt: created,
			},
		},
	}
	return registry
}

// liveInventory is the machine that matches testRegistry: Project alpha's
// session with its Window, shell Pane, and Agent Pane, plus Project beta's
// session with its Window and Pane. Project gone and win-alpha-2 are
// deliberately absent, so offline rows are exercised in every test that uses it.
func liveInventory(host HostMode) Inventory {
	return Inventory{
		Transport: Transport{Kind: TransportSocketName, Value: "projmux", Source: TransportSourceSocketName},
		HostMode:  host,
		Sessions: []Session{
			{ID: "$1", Name: "alpha", ProjectUID: "project-alpha", ProjectName: "alpha", Root: "/src/alpha"},
			{ID: "$2", Name: "beta", ProjectUID: "project-beta", ProjectName: "beta", Root: "/src/beta"},
		},
		Windows: []Window{
			{ID: "@1", SessionID: "$1", Index: "0", DisplayName: "editor", UID: "win-alpha-1", MirroredName: "editor", Active: true},
			{ID: "@2", SessionID: "$2", Index: "0", DisplayName: "editor", UID: "win-beta-1", MirroredName: "editor", Active: true},
		},
		Panes: []Pane{
			{ID: "%1", WindowID: "@1", UID: "pane-alpha-1", MirroredName: "shell"},
			{ID: "%2", WindowID: "@1", UID: "pane-alpha-agent", MirroredName: "claude", AgentProvider: "claude"},
			{ID: "%3", WindowID: "@2", UID: "pane-beta-1", MirroredName: "shell"},
		},
	}
}

func projectNode(t *testing.T, graph Graph, uid string) ProjectNode {
	t.Helper()
	for _, node := range graph.Projects {
		if node.Project.Metadata.UID == uid {
			return node
		}
	}
	t.Fatalf("graph has no Project row %q", uid)
	return ProjectNode{}
}

func windowNode(t *testing.T, graph Graph, uid string) WindowNode {
	t.Helper()
	for _, node := range graph.Windows {
		if node.Window.Metadata.UID == uid {
			return node
		}
	}
	t.Fatalf("graph has no Window row %q", uid)
	return WindowNode{}
}

func paneNode(t *testing.T, graph Graph, uid string) PaneNode {
	t.Helper()
	for _, node := range graph.Panes {
		if node.Pane.Metadata.UID == uid {
			return node
		}
	}
	t.Fatalf("graph has no Pane row %q", uid)
	return PaneNode{}
}

func agentNode(t *testing.T, graph Graph, uid string) AgentNode {
	t.Helper()
	for _, node := range graph.Agents {
		if node.Agent.Metadata.UID == uid {
			return node
		}
	}
	t.Fatalf("graph has no Agent row %q", uid)
	return AgentNode{}
}

func runtimeNode(t *testing.T, graph Graph, id string) RuntimeNode {
	t.Helper()
	for _, node := range graph.Runtime {
		if node.Ref.ID == id {
			return node
		}
	}
	t.Fatalf("graph has no runtime object %q", id)
	return RuntimeNode{}
}
