package app

import (
	"context"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
	"github.com/crevissepartners/projmux/internal/web"
)

// webBackend answers the web API from the same Registry and tmux observation
// the CLI reads.
//
// Every read takes its own snapshot: one read-only Registry load and one
// observation of the app-owned tmux server. Nothing is cached between
// requests, so closing a Pane shows up on the next request rather than after
// a TTL, which is the same rule the CLI read path follows.
//
// The transport is always the app socket. The server's own $TMUX is never
// consulted: a server started from some tmux pane would otherwise observe that
// pane's server, which need not be the one projmux owns.
type webBackend struct {
	loadRegistry func() (coremetadata.Registry, error)
	observe      func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory
	transport    resourcegraph.Transport

	// mutations serializes every CLI handler call; see web_mutations.go.
	mutations sync.Mutex
	// runCLI and socketPath replace the in-process handler call and the app
	// socket lookup in tests. Nil means the real ones.
	runCLI     func(argv []string) (string, error)
	socketPath func(ctx context.Context) (string, error)
	paths      func() (config.Paths, error)
	// home and env are what the settings a split follows are read from; nil
	// means the real home and webSettingsEnv.
	home func() (string, error)
	env  func(string) string

	// creatingWindows holds the Projects a window create is running for. A
	// create takes seconds, and a second press in that time would otherwise
	// make a second window once the first is done.
	creatingMu      sync.Mutex
	creatingWindows map[string]bool
}

var _ web.Backend = (*webBackend)(nil)

func newWebBackend() *webBackend {
	runner := inttmux.ExecRunner{}
	return &webBackend{
		loadRegistry: loadResourceRegistry,
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
		transport: resourcegraph.Transport{
			Kind:   resourcegraph.TransportSocketName,
			Value:  defaultAppSocket,
			Source: resourcegraph.TransportSourceSocketName,
		},
	}
}

// webSnapshot is one request's view of the machine.
type webSnapshot struct {
	registry coremetadata.Registry
	graph    resourcegraph.Graph
	contexts registryview.Projector
}

func (b *webBackend) snapshot(ctx context.Context) (webSnapshot, error) {
	registry, err := b.loadRegistry()
	if err != nil {
		return webSnapshot{}, err
	}
	graph := resourcegraph.Resolve(registry, b.observe(ctx, b.transport))
	return webSnapshot{
		registry: registry,
		graph:    graph,
		contexts: registryview.NewObservedContextProjector(graph),
	}, nil
}

// item renders one resource exactly as `get <kind> -o json` renders it.
func (s webSnapshot) item(kind coremetadata.Kind, uid string) (any, bool) {
	resource, _, ok := resourceFor(s.registry, kind, uid)
	if !ok {
		return nil, false
	}
	return newResourceJSONProjection(resource, s.contexts.For(kind, uid)), true
}

func (s webSnapshot) list(kind coremetadata.Kind, uids []string) resourceList {
	items := make([]any, 0, len(uids))
	for _, uid := range uids {
		if item, ok := s.item(kind, uid); ok {
			items = append(items, item)
		}
	}
	return resourceList{APIVersion: coremetadata.APIVersion, Kind: resourceListKind(kind, false), Items: items}
}

// window resolves a Window only under the Project the path names.
func (s webSnapshot) window(project, window string) (*coremetadata.Window, error) {
	if _, ok := s.registry.Project(project); !ok {
		return nil, web.NotFound("no project " + project)
	}
	found, ok := s.registry.Window(window)
	if !ok || found.Metadata.OwnerUID() != project {
		return nil, web.NotFound("no window " + window + " in project " + project)
	}
	return found, nil
}

func (b *webBackend) Version() string { return strings.TrimSpace(version.String()) }

func (b *webBackend) Graph(ctx context.Context) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	// The graph carries Agents as stored. `get` reports what an Agent is doing
	// through the same projection that ages a stale interaction out, so the
	// graph is given that projection too; otherwise a client would show an
	// hours-old "waiting" as current.
	for i := range s.graph.Agents {
		if projected, _, ok := resourceFor(s.registry, coremetadata.KindAgent, s.graph.Agents[i].Agent.Metadata.UID); ok {
			s.graph.Agents[i].Agent = projected.(coremetadata.Agent)
		}
	}
	return s.graph, nil
}

func (b *webBackend) Projects(ctx context.Context) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	uids := make([]string, 0, len(s.registry.Projects))
	for _, project := range s.registry.Projects {
		uids = append(uids, project.Metadata.UID)
	}
	return s.list(coremetadata.KindProject, uids), nil
}

func (b *webBackend) Project(ctx context.Context, project string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	item, ok := s.item(coremetadata.KindProject, project)
	if !ok {
		return nil, web.NotFound("no project " + project)
	}
	return item, nil
}

func (b *webBackend) Windows(ctx context.Context, project string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := s.registry.Project(project); !ok {
		return nil, web.NotFound("no project " + project)
	}
	var uids []string
	for _, window := range s.registry.Windows {
		if window.Metadata.OwnerUID() == project {
			uids = append(uids, window.Metadata.UID)
		}
	}
	return s.list(coremetadata.KindWindow, uids), nil
}

func (b *webBackend) Window(ctx context.Context, project, window string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	item, _ := s.item(coremetadata.KindWindow, window)
	return item, nil
}

func (b *webBackend) Panes(ctx context.Context, project, window string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	var uids []string
	for _, pane := range s.registry.Panes {
		// The owner chain decides, so an Agent-owned Pane is listed under the
		// Window of the Agent that holds it.
		if _, ok := s.registry.PaneInWindow(window, pane.Metadata.UID); ok {
			uids = append(uids, pane.Metadata.UID)
		}
	}
	return s.list(coremetadata.KindPane, uids), nil
}

func (b *webBackend) Pane(ctx context.Context, project, window, pane string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	if _, ok := s.registry.PaneInWindow(window, pane); !ok {
		return nil, web.NotFound("no pane " + pane + " in window " + window)
	}
	item, _ := s.item(coremetadata.KindPane, pane)
	return item, nil
}

func (b *webBackend) WindowAgents(ctx context.Context, project, window string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	var uids []string
	for _, agent := range s.registry.Agents {
		if agent.Metadata.OwnerUID() == window {
			uids = append(uids, agent.Metadata.UID)
		}
	}
	return s.list(coremetadata.KindAgent, uids), nil
}

func (b *webBackend) Agent(ctx context.Context, agent string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	item, ok := s.item(coremetadata.KindAgent, agent)
	if !ok {
		return nil, web.NotFound("no agent " + agent)
	}
	return item, nil
}
