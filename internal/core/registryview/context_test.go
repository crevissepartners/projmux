package registryview

import (
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func TestContextProjectorNoTransportPriorityMatrix(t *testing.T) {
	t.Parallel()

	registry := contextFixtureRegistry()
	projector := NewContextProjector(registry)
	tests := []struct {
		name string
		kind coremetadata.Kind
		uid  string
		want Context
	}{
		{name: "Project root basename", kind: coremetadata.KindProject, uid: "project", want: Context{Value: "pretty project", Source: ContextSourceProjectRoot}},
		{name: "ControlSession exact session", kind: coremetadata.KindControlSession, uid: "control", want: Context{Value: "Home", Source: ContextSourceControlSession}},
		{name: "Window anchor Agent topic", kind: coremetadata.KindWindow, uid: "window-topic", want: Context{Value: "review release", Source: ContextSourceAgentTopic}},
		{name: "Window anchor Agent provider", kind: coremetadata.KindWindow, uid: "window-provider", want: Context{Value: "claude", Source: ContextSourceAgentProvider}},
		{name: "Window anchor command executable", kind: coremetadata.KindWindow, uid: "window-command", want: Context{Value: "nvim", Source: ContextSourceCommand}},
		{name: "Window fallback", kind: coremetadata.KindWindow, uid: "window-empty", want: Context{Value: "window", Source: ContextSourceWindowFallback}},
		{name: "Agent topic", kind: coremetadata.KindAgent, uid: "agent-topic", want: Context{Value: "review release", Source: ContextSourceAgentTopic}},
		{name: "Agent provider", kind: coremetadata.KindAgent, uid: "agent-provider", want: Context{Value: "claude", Source: ContextSourceAgentProvider}},
		{name: "Pane owning Agent topic", kind: coremetadata.KindPane, uid: "pane-topic", want: Context{Value: "review release", Source: ContextSourceAgentTopic}},
		{name: "Pane command executable", kind: coremetadata.KindPane, uid: "pane-command", want: Context{Value: "nvim", Source: ContextSourceCommand}},
		{name: "Pane empty does not copy name", kind: coremetadata.KindPane, uid: "pane-empty", want: Context{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := projector.For(test.kind, test.uid); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("For(%s, %q) = %+v, want %+v", test.kind, test.uid, got, test.want)
			}
		})
	}
}

func TestContextProjectorLiveOverlayRequiresExactUIDBinding(t *testing.T) {
	t.Parallel()

	registry := contextFixtureRegistry()
	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "isolated"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{{
			ID: "$1", Name: "project", ProjectUID: "project", ProjectName: "project", Root: "/src/pretty project",
		}},
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
	projector := NewObservedContextProjector(resourcegraph.Resolve(registry, inventory))

	if got, want := projector.For(coremetadata.KindWindow, "window-topic"), (Context{Value: "exact live window", Source: ContextSourceLiveWindowName, Observed: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("live Window context = %+v, want %+v", got, want)
	}
	if got, want := projector.For(coremetadata.KindPane, "pane-topic"), (Context{Value: "review release", Source: ContextSourceAgentTopic}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Agent topic did not outrank live Pane title: got %+v, want %+v", got, want)
	}
	if got, want := projector.For(coremetadata.KindPane, "pane-command"), (Context{Value: "exact live command pane", Source: ContextSourceLivePaneTitle, Observed: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("exact live Pane context = %+v, want %+v", got, want)
	}
	if got := projector.For(coremetadata.KindAgent, "agent-topic"); got.Observed || got.Source != ContextSourceAgentTopic {
		t.Fatalf("Pane title overlaid Agent context: %+v", got)
	}
	if got := projector.For(coremetadata.KindWindow, "window-provider"); got.Source != ContextSourceAgentProvider || got.Observed {
		t.Fatalf("unbound Window guessed a live name: %+v", got)
	}
}

func TestStoredPresentationNeverChangesProjectedContext(t *testing.T) {
	t.Parallel()

	registry := contextFixtureRegistry()
	before := NewContextProjector(registry)
	for i := range registry.Projects {
		registry.Projects[i].Metadata.DisplayName = "stored project presentation"
	}
	for i := range registry.ControlSessions {
		registry.ControlSessions[i].Metadata.DisplayName = "stored control presentation"
	}
	for i := range registry.Windows {
		registry.Windows[i].Metadata.DisplayName = "stored window presentation"
	}
	for i := range registry.Agents {
		registry.Agents[i].Metadata.DisplayName = "stored agent presentation"
	}
	for i := range registry.Panes {
		registry.Panes[i].Metadata.DisplayName = "stored pane presentation"
		registry.Panes[i].Status.DisplayTitle = "stored pane title"
	}
	after := NewContextProjector(registry)
	for _, resource := range []struct {
		kind coremetadata.Kind
		uid  string
	}{
		{coremetadata.KindProject, "project"},
		{coremetadata.KindControlSession, "control"},
		{coremetadata.KindWindow, "window-topic"},
		{coremetadata.KindAgent, "agent-provider"},
		{coremetadata.KindPane, "pane-empty"},
	} {
		if got, want := after.For(resource.kind, resource.uid), before.For(resource.kind, resource.uid); !reflect.DeepEqual(got, want) {
			t.Fatalf("stored presentation changed %s/%s context: got %+v, want %+v", resource.kind, resource.uid, got, want)
		}
	}
}

func contextFixtureRegistry() coremetadata.Registry {
	registry := coremetadata.NewRegistry()
	owner := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	meta := func(uid, name string, ref *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: ref, DisplayName: "stored " + name}
	}
	registry.Projects = []coremetadata.Project{{
		Kind: coremetadata.KindProject, Metadata: meta("project", "project", nil), Spec: coremetadata.ProjectSpec{Root: "/src/pretty project"},
	}}
	registry.ControlSessions = []coremetadata.ControlSession{{
		Kind: coremetadata.KindControlSession, Metadata: meta("control", "home", nil), Spec: coremetadata.ControlSessionSpec{Session: "Home"},
	}}
	registry.Windows = []coremetadata.Window{
		{Kind: coremetadata.KindWindow, Metadata: meta("window-topic", "topic", owner(coremetadata.KindProject, "project")), Spec: coremetadata.WindowSpec{AnchorPaneRef: "pane-topic"}},
		{Kind: coremetadata.KindWindow, Metadata: meta("window-provider", "provider", owner(coremetadata.KindProject, "project")), Spec: coremetadata.WindowSpec{AnchorPaneRef: "pane-provider"}},
		{Kind: coremetadata.KindWindow, Metadata: meta("window-command", "command", owner(coremetadata.KindProject, "project")), Spec: coremetadata.WindowSpec{AnchorPaneRef: "pane-command"}},
		{Kind: coremetadata.KindWindow, Metadata: meta("window-empty", "stable-window", owner(coremetadata.KindProject, "project")), Spec: coremetadata.WindowSpec{AnchorPaneRef: "pane-empty"}},
	}
	registry.Agents = []coremetadata.Agent{
		{Kind: coremetadata.KindAgent, Metadata: meta("agent-topic", "topic-agent", owner(coremetadata.KindWindow, "window-topic")), Spec: coremetadata.AgentSpec{Provider: "codex"}, Status: coremetadata.AgentStatus{PaneRef: "pane-topic"}},
		{Kind: coremetadata.KindAgent, Metadata: meta("agent-provider", "provider-agent", owner(coremetadata.KindWindow, "window-provider")), Spec: coremetadata.AgentSpec{Provider: "claude"}, Status: coremetadata.AgentStatus{PaneRef: "pane-provider"}},
	}
	registry.Agents[0].Metadata.Annotations = map[string]string{coremetadata.AnnotationAgentTopic: "review release"}
	registry.Panes = []coremetadata.Pane{
		{Kind: coremetadata.KindPane, Metadata: meta("pane-topic", "agent-pane", owner(coremetadata.KindAgent, "agent-topic")), Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, Command: "/usr/bin/codex"}, Status: coremetadata.PaneStatus{DisplayTitle: "stored topic pane title"}},
		{Kind: coremetadata.KindPane, Metadata: meta("pane-provider", "provider-pane", owner(coremetadata.KindAgent, "agent-provider")), Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent}},
		{Kind: coremetadata.KindPane, Metadata: meta("pane-command", "command-pane", owner(coremetadata.KindWindow, "window-command")), Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, Command: "/usr/bin/nvim ."}},
		{Kind: coremetadata.KindPane, Metadata: meta("pane-empty", "stable-pane", owner(coremetadata.KindWindow, "window-empty")), Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell}},
	}
	return registry
}
