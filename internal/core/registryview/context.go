package registryview

import (
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// ContextSource states which non-identifying input supplied a resource's
// invocation-scoped human context.
type ContextSource string

const (
	ContextSourceProjectRoot    ContextSource = "project-root-basename"
	ContextSourceControlSession ContextSource = "control-session"
	ContextSourceLiveWindowName ContextSource = "live-window-name"
	ContextSourceAgentTopic     ContextSource = "agent-topic"
	ContextSourceAgentProvider  ContextSource = "agent-provider"
	ContextSourceCommand        ContextSource = "command-executable"
	ContextSourceWindowFallback ContextSource = "window-fallback"
	ContextSourceLivePaneTitle  ContextSource = "live-pane-title"
	ContextSourceCandidatePath  ContextSource = "candidate-path"
	ContextSourceRuntimeLink    ContextSource = "runtime-link"
)

// Context is ephemeral presentation derived for one read invocation.
//
// Value is never an address or selector. Source makes the winning priority
// leg explicit, and Observed is true only when Value came from the exact live
// runtime object bound to the resource uid.
type Context struct {
	Value    string        `json:"value,omitempty"`
	Source   ContextSource `json:"source,omitempty"`
	Observed bool          `json:"observed"`
}

// Empty reports whether no context input was available. In particular, it
// does not fall back to metadata.name: an absent context must not become a
// second copy of the durable address.
func (c Context) Empty() bool { return strings.TrimSpace(c.Value) == "" }

// Projector derives every resource context from one immutable Registry
// snapshot plus, optionally, one exact runtime graph observation.
//
// Stored metadata.displayName and status.displayTitle are deliberately not
// indexed. They remain in schema v3 for wire/write compatibility only.
type Projector struct {
	registry        coremetadata.Registry
	liveWindowNames map[string]string
	livePaneTitles  map[string]string
}

// NewContextProjector creates a no-transport projector over registry.
func NewContextProjector(registry coremetadata.Registry) Projector {
	return Projector{registry: registry.Clone()}
}

// NewObservedContextProjector creates a projector over one already-resolved
// graph. Live Window names and Pane titles are admitted only from managed row
// Runtime refs, which exist only after exact UID claim and containment checks.
func NewObservedContextProjector(graph resourcegraph.Graph) Projector {
	registry := coremetadata.NewRegistry()
	for _, node := range graph.Projects {
		registry.Projects = append(registry.Projects, node.Project)
	}
	for _, node := range graph.ControlSessions {
		registry.ControlSessions = append(registry.ControlSessions, node.ControlSession)
	}
	for _, node := range graph.Windows {
		registry.Windows = append(registry.Windows, node.Window)
	}
	for _, node := range graph.Panes {
		registry.Panes = append(registry.Panes, node.Pane)
	}
	for _, node := range graph.Agents {
		registry.Agents = append(registry.Agents, node.Agent)
	}
	p := NewContextProjector(registry)
	p.liveWindowNames = make(map[string]string)
	p.livePaneTitles = make(map[string]string)
	for _, node := range graph.Windows {
		if node.Runtime == nil {
			continue
		}
		if value := strings.TrimSpace(node.Runtime.Name); value != "" {
			p.liveWindowNames[node.Window.Metadata.UID] = value
		}
	}
	for _, node := range graph.Panes {
		if node.Runtime == nil {
			continue
		}
		if value := strings.TrimSpace(node.Runtime.Name); value != "" {
			p.livePaneTitles[node.Pane.Metadata.UID] = value
		}
	}
	return p
}

// For derives the context of uid as kind. Unknown or mismatched resources
// return an empty Context rather than guessing from a name or runtime title.
func (p Projector) For(kind coremetadata.Kind, uid string) Context {
	switch kind {
	case coremetadata.KindProject:
		return p.project(uid)
	case coremetadata.KindControlSession:
		return p.controlSession(uid)
	case coremetadata.KindWindow:
		return p.window(uid)
	case coremetadata.KindAgent:
		return p.agent(uid)
	case coremetadata.KindPane:
		return p.pane(uid)
	default:
		return Context{}
	}
}

func (p Projector) project(uid string) Context {
	project, ok := p.registry.Project(uid)
	if !ok {
		return Context{}
	}
	root := strings.TrimSpace(project.Spec.Root)
	if root == "" {
		return Context{}
	}
	return contextValue(filepath.Base(root), ContextSourceProjectRoot, false)
}

func (p Projector) controlSession(uid string) Context {
	control, ok := p.registry.ControlSession(uid)
	if !ok {
		return Context{}
	}
	return contextValue(control.Spec.Session, ContextSourceControlSession, false)
}

func (p Projector) window(uid string) Context {
	if value := p.liveWindowNames[uid]; value != "" {
		return contextValue(value, ContextSourceLiveWindowName, true)
	}
	window, ok := p.registry.Window(uid)
	if !ok {
		return Context{}
	}
	if pane, ok := p.registry.Pane(strings.TrimSpace(window.Spec.AnchorPaneRef)); ok {
		if agent := p.owningAgent(*pane); agent != nil {
			if topic := agentTopic(*agent); topic != "" {
				return contextValue(topic, ContextSourceAgentTopic, false)
			}
			if provider := strings.TrimSpace(agent.Spec.Provider); provider != "" {
				return contextValue(provider, ContextSourceAgentProvider, false)
			}
		}
		if command := commandExecutable(pane.Spec.Command); command != "" {
			return contextValue(command, ContextSourceCommand, false)
		}
	}
	return contextValue(coremetadata.FallbackWindowNameBase, ContextSourceWindowFallback, false)
}

func (p Projector) agent(uid string) Context {
	agent, ok := p.registry.Agent(uid)
	if !ok {
		return Context{}
	}
	if topic := agentTopic(*agent); topic != "" {
		return contextValue(topic, ContextSourceAgentTopic, false)
	}
	return contextValue(agent.Spec.Provider, ContextSourceAgentProvider, false)
}

func (p Projector) pane(uid string) Context {
	pane, ok := p.registry.Pane(uid)
	if !ok {
		return Context{}
	}
	if agent := p.owningAgent(*pane); agent != nil {
		if topic := agentTopic(*agent); topic != "" {
			return contextValue(topic, ContextSourceAgentTopic, false)
		}
	}
	if value := p.livePaneTitles[uid]; value != "" {
		return contextValue(value, ContextSourceLivePaneTitle, true)
	}
	return contextValue(commandExecutable(pane.Spec.Command), ContextSourceCommand, false)
}

func (p Projector) owningAgent(pane coremetadata.Pane) *coremetadata.Agent {
	owner := pane.Metadata.OwnerRef
	if owner == nil || owner.Kind != coremetadata.KindAgent {
		return nil
	}
	agent, ok := p.registry.Agent(owner.UID)
	if !ok {
		return nil
	}
	return agent
}

func agentTopic(agent coremetadata.Agent) string {
	return strings.TrimSpace(agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic])
}

func commandExecutable(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(filepath.Base(fields[0]))
}

func contextValue(value string, source ContextSource, observed bool) Context {
	value = strings.TrimSpace(value)
	if value == "" {
		return Context{}
	}
	return Context{Value: value, Source: source, Observed: observed}
}
