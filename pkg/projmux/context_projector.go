package projmux

import (
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ContextProjector derives the presentation context of each resource in one
// resolved graph. It is immutable and safe for concurrent use. Its contexts
// are presentation only: never an address, selector or admission evidence.
type ContextProjector struct {
	projector registryview.Projector
}

// NewResourceContextProjector indexes graph once and returns its projector.
// The rules are the CLI's: a Project's root basename, a ControlSession's
// session, a Window's live name (only from a managed runtime ref), its anchor
// Agent's topic or provider, its anchor command, else the fallback name; an
// Agent's topic or provider; a Pane's owning Agent topic, its live title, else
// its command. The projector copies only the fields those rules read, so later
// changes to graph do not reach it.
func NewResourceContextProjector(graph core.ResourceGraph) ContextProjector {
	view := graph.Graph
	var internal resourcegraph.Graph
	for _, node := range view.Projects {
		internal.Projects = append(internal.Projects, resourcegraph.ProjectNode{Project: metadata.Project{
			Metadata: metadata.ObjectMeta{UID: node.Project.Metadata.UID},
			Spec:     metadata.ProjectSpec{Root: node.Project.Spec.Root},
		}})
	}
	for _, node := range view.ControlSessions {
		internal.ControlSessions = append(internal.ControlSessions, resourcegraph.ControlSessionNode{ControlSession: metadata.ControlSession{
			Metadata: metadata.ObjectMeta{UID: node.ControlSession.Metadata.UID},
			Spec:     metadata.ControlSessionSpec{Session: node.ControlSession.Spec.Session},
		}})
	}
	for _, node := range view.Windows {
		internal.Windows = append(internal.Windows, resourcegraph.WindowNode{
			Window: metadata.Window{
				Metadata: metadata.ObjectMeta{UID: node.Window.Metadata.UID},
				Spec:     metadata.WindowSpec{AnchorPaneRef: node.Window.Spec.AnchorPaneRef},
			},
			Runtime: contextRuntimeRef(node.Runtime),
		})
	}
	for _, node := range view.Panes {
		meta := metadata.ObjectMeta{UID: node.Pane.Metadata.UID}
		if owner := node.Pane.Metadata.OwnerRef; owner != nil {
			meta.OwnerRef = &metadata.OwnerRef{Kind: metadata.Kind(owner.Kind), UID: owner.UID}
		}
		internal.Panes = append(internal.Panes, resourcegraph.PaneNode{
			Pane:    metadata.Pane{Metadata: meta, Spec: metadata.PaneSpec{Command: node.Pane.Spec.Command}},
			Runtime: contextRuntimeRef(node.Runtime),
		})
	}
	for _, node := range view.Agents {
		meta := metadata.ObjectMeta{UID: node.Agent.Metadata.UID}
		if topic, ok := node.Agent.Metadata.Annotations[metadata.AnnotationAgentTopic]; ok {
			meta.Annotations = map[string]string{metadata.AnnotationAgentTopic: topic}
		}
		internal.Agents = append(internal.Agents, resourcegraph.AgentNode{Agent: metadata.Agent{
			Metadata: meta,
			Spec:     metadata.AgentSpec{Provider: node.Agent.Spec.Provider},
		}})
	}
	return ContextProjector{projector: registryview.NewObservedContextProjector(internal)}
}

// For returns the context of uid as kind. An unknown UID, a kind mismatch or
// an unsupported kind is the zero ResourceContext.
func (p ContextProjector) For(kind core.Kind, uid string) core.ResourceContext {
	got := p.projector.For(metadata.Kind(kind), uid)
	return core.ResourceContext{Value: got.Value, Source: core.ResourceContextSource(got.Source), Observed: got.Observed}
}

// Only the runtime name reaches context; the ref's presence marks the managed
// binding.
func contextRuntimeRef(ref *core.GraphRuntimeRef) *resourcegraph.RuntimeRef {
	if ref == nil {
		return nil
	}
	return &resourcegraph.RuntimeRef{Name: ref.Name}
}
