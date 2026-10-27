package projmux

import (
	"encoding/json"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// projectorFixture is registryview's context fixture plus a duplicate Window
// row (the first row wins), a Window whose anchor is missing, a blank Agent
// topic and a Pane owned by an unknown Agent.
func projectorFixture() metadata.Registry {
	registry := metadata.NewRegistry()
	owner := func(kind metadata.Kind, uid string) *metadata.OwnerRef {
		return &metadata.OwnerRef{Kind: kind, UID: uid}
	}
	meta := func(uid, name string, ref *metadata.OwnerRef) metadata.ObjectMeta {
		return metadata.ObjectMeta{UID: uid, Name: name, OwnerRef: ref}
	}
	registry.Projects = []metadata.Project{{Kind: metadata.KindProject, Metadata: meta("project", "project", nil), Spec: metadata.ProjectSpec{Root: "/src/pretty project"}}}
	registry.ControlSessions = []metadata.ControlSession{{Kind: metadata.KindControlSession, Metadata: meta("control", "home", nil), Spec: metadata.ControlSessionSpec{Session: "Home"}}}
	window := func(uid, name, anchor string) metadata.Window {
		return metadata.Window{Kind: metadata.KindWindow, Metadata: meta(uid, name, owner(metadata.KindProject, "project")), Spec: metadata.WindowSpec{AnchorPaneRef: anchor}}
	}
	registry.Windows = []metadata.Window{
		window("window-topic", "topic", "pane-topic"), window("window-provider", "provider", "pane-provider"),
		window("window-command", "command", "pane-command"), window("window-empty", "stable-window", "pane-empty"),
		window("window-missing", "missing", "pane-absent"), window("window-empty", "duplicate", "pane-provider"),
	}
	registry.Agents = []metadata.Agent{
		{Kind: metadata.KindAgent, Metadata: meta("agent-topic", "topic-agent", owner(metadata.KindWindow, "window-topic")), Spec: metadata.AgentSpec{Provider: "codex"}, Status: metadata.AgentStatus{PaneRef: "pane-topic"}},
		{Kind: metadata.KindAgent, Metadata: meta("agent-provider", "provider-agent", owner(metadata.KindWindow, "window-provider")), Spec: metadata.AgentSpec{Provider: "claude"}, Status: metadata.AgentStatus{PaneRef: "pane-provider"}},
	}
	registry.Agents[0].Metadata.Annotations = map[string]string{metadata.AnnotationAgentTopic: "review release", "other": "ignored"}
	registry.Agents[1].Metadata.Annotations = map[string]string{metadata.AnnotationAgentTopic: "   "}
	registry.Panes = []metadata.Pane{
		{Kind: metadata.KindPane, Metadata: meta("pane-topic", "agent-pane", owner(metadata.KindAgent, "agent-topic")), Spec: metadata.PaneSpec{Role: metadata.PaneRoleAgent, Command: "/usr/bin/codex"}},
		{Kind: metadata.KindPane, Metadata: meta("pane-provider", "provider-pane", owner(metadata.KindAgent, "agent-provider")), Spec: metadata.PaneSpec{Role: metadata.PaneRoleAgent}},
		{Kind: metadata.KindPane, Metadata: meta("pane-command", "command-pane", owner(metadata.KindWindow, "window-command")), Spec: metadata.PaneSpec{Role: metadata.PaneRoleShell, Command: "/usr/bin/nvim ."}},
		{Kind: metadata.KindPane, Metadata: meta("pane-empty", "stable-pane", owner(metadata.KindWindow, "window-empty")), Spec: metadata.PaneSpec{Role: metadata.PaneRoleShell}},
		{Kind: metadata.KindPane, Metadata: meta("pane-orphan", "orphan-pane", owner(metadata.KindAgent, "agent-unknown")), Spec: metadata.PaneSpec{Role: metadata.PaneRoleAgent, Command: "less"}},
	}
	return registry
}

func projectorLiveInventory() resourcegraph.Inventory {
	return resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "isolated", Source: resourcegraph.TransportSourceSocketName},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions:  []resourcegraph.Session{{ID: "$1", Name: "project", ProjectUID: "project", ProjectName: "project", Root: "/src/pretty project"}},
		Windows: []resourcegraph.Window{
			{ID: "@1", SessionID: "$1", DisplayName: "exact live window", UID: "window-topic", MirroredName: "topic"},
			{ID: "@2", SessionID: "$1", DisplayName: "exact command window", UID: "window-command", MirroredName: "command"},
			{ID: "@3", SessionID: "$1", DisplayName: "tempting duplicate title", UID: "unknown-window", MirroredName: "topic"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%1", WindowID: "@1", Title: "exact live topic pane", UID: "pane-topic", MirroredName: "agent-pane"},
			{ID: "%2", WindowID: "@2", Title: "exact live command pane", UID: "pane-command", MirroredName: "command-pane"},
			{ID: "%3", WindowID: "@2", Title: "same title but wrong uid", UID: "unknown-pane", MirroredName: "command-pane"},
		},
	}
}

// For every kind and UID the public projector returns registryview's
// context for the same graph, with and without a live observation; unknown
// UIDs, kind mismatches and unsupported kinds are empty.
func TestPublicContextProjectorParity(t *testing.T) {
	registry := projectorFixture()
	kinds := []core.Kind{core.KindProject, core.KindControlSession, core.KindWindow, core.KindPane, core.KindAgent, "Bogus", ""}
	uids := []string{"", "unknown", " window-topic"}
	for _, project := range registry.Projects {
		uids = append(uids, project.Metadata.UID)
	}
	for _, control := range registry.ControlSessions {
		uids = append(uids, control.Metadata.UID)
	}
	for _, window := range registry.Windows {
		uids = append(uids, window.Metadata.UID)
	}
	for _, pane := range registry.Panes {
		uids = append(uids, pane.Metadata.UID)
	}
	for _, agent := range registry.Agents {
		uids = append(uids, agent.Metadata.UID)
	}
	sources := map[core.ResourceContextSource]bool{}
	observed := 0
	for name, inventory := range map[string]resourcegraph.Inventory{"live": projectorLiveInventory(), "no-transport": {}} {
		internal := resourcegraph.Resolve(registry, inventory)
		public, err := publicResourceGraph(internal, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := registryview.NewObservedContextProjector(internal)
		got := NewResourceContextProjector(public)
		for _, kind := range kinds {
			for _, uid := range uids {
				expected := want.For(metadata.Kind(kind), uid)
				actual := got.For(kind, uid)
				wantJSON, _ := json.Marshal(expected)
				gotJSON, _ := json.Marshal(actual)
				if string(wantJSON) != string(gotJSON) || actual.Value != expected.Value || string(actual.Source) != string(expected.Source) || actual.Observed != expected.Observed {
					t.Fatalf("%s For(%q, %q) = %+v, want %+v", name, kind, uid, actual, expected)
				}
				if actual.Value != "" {
					sources[actual.Source] = true
				}
				if actual.Observed {
					observed++
				}
			}
		}
	}
	for _, source := range []core.ResourceContextSource{
		core.ContextSourceProjectRoot, core.ContextSourceControlSession, core.ContextSourceLiveWindowName, core.ContextSourceAgentTopic,
		core.ContextSourceAgentProvider, core.ContextSourceCommand, core.ContextSourceWindowFallback, core.ContextSourceLivePaneTitle,
	} {
		if !sources[source] {
			t.Errorf("fixture never produced source %q", source)
		}
	}
	if observed == 0 {
		t.Fatal("fixture produced no observed context")
	}
	// The public source names are registryview's.
	for public, internal := range map[core.ResourceContextSource]registryview.ContextSource{
		core.ContextSourceProjectRoot: registryview.ContextSourceProjectRoot, core.ContextSourceControlSession: registryview.ContextSourceControlSession,
		core.ContextSourceLiveWindowName: registryview.ContextSourceLiveWindowName, core.ContextSourceAgentTopic: registryview.ContextSourceAgentTopic,
		core.ContextSourceAgentProvider: registryview.ContextSourceAgentProvider, core.ContextSourceCommand: registryview.ContextSourceCommand,
		core.ContextSourceWindowFallback: registryview.ContextSourceWindowFallback, core.ContextSourceLivePaneTitle: registryview.ContextSourceLivePaneTitle,
	} {
		if string(public) != string(internal) {
			t.Fatalf("source %q differs from %q", public, internal)
		}
	}
	var zero ContextProjector
	if got := zero.For(core.KindProject, "project"); got != (core.ResourceContext{}) {
		t.Fatalf("zero projector: %+v", got)
	}
}

// The projector copies what it reads: changing the graph afterwards does not
// change any context.
func TestPublicContextProjectorIsolation(t *testing.T) {
	public, err := publicResourceGraph(resourcegraph.Resolve(projectorFixture(), projectorLiveInventory()), nil)
	if err != nil {
		t.Fatal(err)
	}
	projector := NewResourceContextProjector(public)
	type query struct {
		kind core.Kind
		uid  string
	}
	queries := []query{{core.KindWindow, "window-topic"}, {core.KindAgent, "agent-topic"}, {core.KindPane, "pane-command"}, {core.KindProject, "project"}, {core.KindWindow, "window-command"}}
	before := map[query]core.ResourceContext{}
	for _, q := range queries {
		before[q] = projector.For(q.kind, q.uid)
	}
	for i := range public.Graph.Windows {
		if public.Graph.Windows[i].Runtime != nil {
			public.Graph.Windows[i].Runtime.Name = "changed"
		}
		public.Graph.Windows[i].Window.Spec.AnchorPaneRef = "changed"
	}
	for i := range public.Graph.Panes {
		if public.Graph.Panes[i].Runtime != nil {
			public.Graph.Panes[i].Runtime.Name = "changed"
		}
	}
	for i := range public.Graph.Agents {
		if public.Graph.Agents[i].Agent.Metadata.Annotations != nil {
			public.Graph.Agents[i].Agent.Metadata.Annotations[metadata.AnnotationAgentTopic] = "changed"
		}
	}
	public.Graph.Projects[0].Project.Spec.Root = "/changed"
	for _, q := range queries {
		if got := projector.For(q.kind, q.uid); !reflect.DeepEqual(got, before[q]) {
			t.Fatalf("For(%s, %s) changed with the graph: %+v, was %+v", q.kind, q.uid, got, before[q])
		}
	}
}
