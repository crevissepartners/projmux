package agentcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type Client struct {
	// Runner overrides tmux reads for isolated transport fixtures.
	Runner       store.Runner
	StateDir     string
	Transport    resourcegraph.Transport
	LoadRegistry func() (metadata.Registry, error)
}
type routedRunner struct {
	Transport resourcegraph.Transport
	Runner    store.Runner
}

func (r routedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	prefix := r.Transport.Args()
	if name != "tmux" || len(prefix) == 0 {
		return nil, errors.New("exact Agent native control unavailable: exact tmux route is not configured")
	}
	runner := r.Runner
	if runner == nil {
		runner = tmux.ExecRunner{}
	}
	return runner.Run(ctx, name, append(prefix, args...)...)
}
func (c Client) Bind(ctx context.Context, reg metadata.Registry, agent metadata.Agent) (Binding, error) {
	if err := RequireCapability("agent approval review", agent); err != nil {
		return Binding{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	runner := routedRunner{Transport: c.Transport, Runner: c.Runner}
	mirror := store.NewMirror(runner)
	release := func() {}
	var err error
	if strings.TrimSpace(agent.Status.PaneRef) != "" {
		release, err = AcquireReadFence(ctx, c.StateDir, agent.Status.PaneRef)
	}
	if err != nil {
		return Binding{}, fmt.Errorf("exact Agent native control unavailable: read live binding: %w", err)
	}
	var live Live
	var found bool
	target, found, err := mirror.FindPaneTargetForUID(ctx, agent.Status.PaneRef)
	if err == nil && found {
		var out []byte
		out, err = runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, strings.Join([]string{"#{pane_id}", "#{@projmux_pane_uid}", "#{@projmux_ai_thread_id}", "#{@projmux_codex_authority}", "#{@projmux_codex_authority_epoch}", "#{@projmux_codex_authority_reason}"}, "\\037"))
		if err == nil {
			live, err = ParseFrame(out)
		}
	}
	release()
	if err != nil {
		return Binding{}, fmt.Errorf("exact Agent native control unavailable: read live binding: %w", err)
	}
	b, err := Resolve(reg, agent, live, found, c.StateDir)
	if err != nil {
		if agent.Status.Phase == metadata.PhaseOffline || agent.Status.Phase == metadata.PhaseFailed {
			return b, fmt.Errorf("%w; next: `projmux agent resume uid:%s`", err, agent.Metadata.UID)
		}
		if pane, ok := reg.Pane(agent.Status.PaneRef); ok {
			if window, ok := reg.Window(agent.Metadata.OwnerUID()); ok {
				if project, ok := reg.Project(window.Metadata.OwnerUID()); ok {
					return b, Recovery(err, Binding{Identity: Identity{PaneUID: pane.Metadata.UID}, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID})
				}
			}
		}
	}
	return b, err
}
func Recovery(err error, b Binding) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w; Open Codex: `projmux focus pane uid:%s --project uid:%s --window uid:%s`", err, b.Identity.PaneUID, b.ProjectUID, b.WindowUID)
}
func (c Client) Call(ctx context.Context, b Binding, r Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	reg, err := c.LoadRegistry()
	if err != nil {
		return Response{}, Recovery(&BindingError{Reason: "canonical generation consumer Registry read failed"}, b)
	}
	if err := ValidateConsumer(reg, b); err != nil {
		return Response{}, Recovery(err, b)
	}
	r.Identity, r.Epoch = b.Identity, b.Epoch
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := Call(ctx, b.StateDir, b.Endpoint, b.Identity, r)
	if err != nil {
		err = Recovery(fmt.Errorf("exact Agent native control unavailable: %w", err), b)
	}
	return out, err
}

func RequireCapability(spelling string, agent metadata.Agent) error {
	action := strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(spelling), "agent "), " ", ".")
	provider, ok := aiprovider.Lookup(agent.Spec.Provider)
	if !ok {
		return fmt.Errorf("%s unavailable: agent/%s has unsupported provider %q", spelling, agent.Metadata.Name, agent.Spec.Provider)
	}
	_, cell, ok := aiprovider.LookupAgentCapability(action, provider.ID)
	if !ok || cell.Mode != aiprovider.SupportNativeExact {
		return fmt.Errorf("%s unavailable: agent/%s provider %s does not support native exact control", spelling, agent.Metadata.Name, provider.ID)
	}
	return nil
}

// ValidateConsumer rechecks canonical authority immediately before transport admission.
func ValidateConsumer(reg metadata.Registry, b Binding) error {
	agent, ok := reg.Agent(b.Identity.AgentUID)
	if !ok {
		return &BindingError{Reason: "canonical generation consumer Agent is missing"}
	}
	pane, ok := reg.Pane(b.Identity.PaneUID)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil {
		return &BindingError{Reason: "canonical generation consumer Pane authority is missing"}
	}
	lifecycle, durable, authorized := LifecycleInput(reg, *agent, b.Identity.PaneUID, b.Identity.RuntimeID, agent.EffectiveInteraction(time.Now()).Kind)
	if !durable || !authorized {
		return &BindingError{Reason: "canonical generation consumer fence is stale"}
	}
	authority := pane.Status.Activation.Codex.Authority
	consumer := codexgeneration.ProjectConsumers(lifecycle, codexgeneration.RuntimeMutationInput{DurableEndpoint: agent.Status.SessionRef.Codex.Endpoint, StoredAuthority: authority, PresentedAuthority: authority, TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: b.Identity.RuntimeID}, true)
	if !durable || !authorized || consumer.Effect != codexgeneration.MutationSemanticEffect || !consumer.Endpoint.Same(b.Endpoint) || consumer.Fence == "" || consumer.Fence != b.Fence {
		return &BindingError{Reason: "canonical generation consumer fence is stale"}
	}
	return nil
}
