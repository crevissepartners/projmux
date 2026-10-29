package projmux

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	// CodeProcessStale refuses a Binding that is not the Agent's current or
	// retired generation. The read had no effect.
	CodeProcessStale Code = "stale-binding"
	// CodeProcessClosed refuses a retired generation with no final evidence
	// left to read.
	CodeProcessClosed Code = "process-closed"
	// CodeProcessReadUnsupported refuses an owner built before it served
	// event reads; no request was sent to it.
	CodeProcessReadUnsupported Code = "process-host-unsupported-read"
	// CodeProcessOwnerUnreachable reports an owner that did not answer. It is
	// not a provider exit.
	CodeProcessOwnerUnreachable Code = "process-owner-unreachable"
)

// Process read sentinels. A refusal of ReadAgentProcess or
// ReadAgentProcessEvents is a *Refusal whose Unwrap reaches both the sentinel
// and the original cause.
var (
	ErrProcessStale            = errors.New("process binding is stale")
	ErrProcessClosed           = errors.New("process generation is closed")
	ErrProcessReadUnsupported  = errors.New("process owner does not serve this read")
	ErrProcessOwnerUnreachable = errors.New("process owner is unreachable")
)

// agentProcessPollInterval spaces the owner reads of one long poll. The owner
// answers each read at once, so a Wait repeats the read at this interval until
// an event follows After, the generation ends, or the Wait passes.
const agentProcessPollInterval = 50 * time.Millisecond

// ReadAgentProcess observes one exact process Agent generation. It reads the
// owner without changing it: cancelling the read or losing the connection
// never stops the owner or provider, and never records a Wait. A generation
// that ended returns its final Snapshot with Exit; a retired generation with
// no final evidence is ErrProcessClosed, and an owner that does not answer is
// ErrProcessOwnerUnreachable.
func (c *Client) ReadAgentProcess(ctx context.Context, req core.AgentProcessRequest) (core.AgentProcessSnapshot, error) {
	read, err := c.resolveAgentProcess(ctx, req.Agent, req.Binding, "AgentProcessRequest")
	if err != nil {
		return core.AgentProcessSnapshot{}, err
	}
	if snapshot, done, err := read.final(); done || err != nil {
		return snapshot, err
	}
	view, err := read.observer(ctx).ObserveHostView(read.binding, read.activation, read.provider)
	if err != nil {
		return core.AgentProcessSnapshot{}, read.refusal(ctx, err)
	}
	return read.snapshot(view, 0), nil
}

// ReadAgentProcessEvents reads the events after req.After from one exact
// process Agent generation, with the observation of the snapshot taken under
// the same owner lock. When no event follows After, it repeats the read until
// one does, the generation ends, or req.Wait passes; the Wait is capped by the
// owner read deadline (5s) and starts no goroutine. Cancellation ends only the
// read. An owner built before it served event reads is
// ErrProcessReadUnsupported, and no request is sent to it.
func (c *Client) ReadAgentProcessEvents(ctx context.Context, req core.AgentProcessEventsRequest) (core.AgentProcessEventBatch, error) {
	read, err := c.resolveAgentProcess(ctx, req.Agent, req.Binding, "AgentProcessEventsRequest")
	if err != nil {
		return core.AgentProcessEventBatch{}, err
	}
	if snapshot, done, err := read.final(); done || err != nil {
		if err != nil {
			return core.AgentProcessEventBatch{}, err
		}
		return core.AgentProcessEventBatch{Binding: read.public, Snapshot: snapshot, Next: req.After, Terminal: true}, nil
	}
	wait := min(max(req.Wait, 0), localipc.Deadline)
	deadline := time.Now().Add(wait)
	observer := read.observer(ctx)
	for {
		events, err := observer.ReadProcessHostEvents(req.After)
		if err != nil {
			return core.AgentProcessEventBatch{}, read.refusal(ctx, err)
		}
		batch := read.batch(events)
		if len(batch.Events) > 0 || batch.Terminal || !time.Now().Add(agentProcessPollInterval).Before(deadline) {
			return batch, nil
		}
		timer := time.NewTimer(agentProcessPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return core.AgentProcessEventBatch{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// agentProcessRead is one resolved process Agent generation. current reports
// the Registry's current activation; otherwise binding is the retired session.
type agentProcessRead struct {
	reg          metadata.Registry
	registryPath string
	agent        metadata.Agent
	pane         metadata.Pane
	provider     string
	binding      processhost.Binding
	public       core.AgentProcessBinding
	activation   metadata.ProcessActivation
	current      bool
}

func (c *Client) resolveAgentProcess(ctx context.Context, ref string, want core.AgentProcessBinding, field string) (agentProcessRead, error) {
	var read agentProcessRead
	if err := ctx.Err(); err != nil {
		return read, err
	}
	uid, ok := strings.CutPrefix(strings.TrimSpace(ref), "uid:")
	if !ok || uid == "" {
		return read, &Refusal{Code: CodeSelectorInvalid, Field: field + ".Agent", message: field + ".Agent must be one exact uid:<agent>"}
	}
	if c.pathsErr != nil {
		return read, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return read, resourceRefusal(err)
	}
	agent, found := reg.Agent(uid)
	if !found {
		return read, &Refusal{Code: CodeSelectorInvalid, AgentUID: uid, Field: field + ".Agent", message: "no Agent " + ref}
	}
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, uid)
	if ambiguous || pane == nil {
		return read, &Refusal{Code: CodeSelectorInvalid, AgentUID: uid, Field: field + ".Agent", message: "the read requires one exact process Agent; tmux Agents are unsupported"}
	}
	read = agentProcessRead{reg: reg, registryPath: store.PathFor(c.paths.StateDir), agent: agent.Clone(), pane: pane.Clone(), provider: agent.Spec.Provider}
	var stored metadata.ProcessBinding
	switch {
	case pane.Status.Activation.Process != nil:
		read.activation = *pane.Status.Activation.Process
		stored, read.current = read.activation.Binding, true
	case pane.Status.Activation.IsZero() && pane.Status.ProcessSession != nil:
		stored = pane.Status.ProcessSession.Binding
	default:
		return read, read.refusal(ctx, processhost.ErrStale)
	}
	read.binding = runtimeservice.ProcessSchemaBinding(stored)
	read.public = core.AgentProcessBinding{Host: stored.HostInstanceID, Pane: stored.PaneUID, Generation: stored.Generation}
	if !want.IsZero() && want != read.public {
		return read, read.refusal(ctx, processhost.ErrStale)
	}
	if read.current {
		if _, _, ok := reg.CurrentProcessActivation(stored); !ok {
			return read, read.refusal(ctx, processhost.ErrStale)
		}
	}
	return read, nil
}

func (r agentProcessRead) observer(ctx context.Context) runtimeservice.ProcessObserver {
	return runtimeservice.ProcessObserver{Ctx: ctx, Registry: r.reg, RegistryPath: r.registryPath, Binding: r.binding}
}

// final reports the generation's final Snapshot from durable Wait evidence.
// done is false for a live generation, which is read from its owner. A
// retired generation without such evidence is ErrProcessClosed.
func (r agentProcessRead) final() (core.AgentProcessSnapshot, bool, error) {
	if r.current {
		snap, ok := runtimeservice.RecordedProcessExit(r.reg, r.binding, r.pane, r.activation, r.provider)
		if !ok {
			return core.AgentProcessSnapshot{}, false, nil
		}
		return r.finalSnapshot(snap, r.activation.HostProcess, r.activation.Child), true, nil
	}
	snap, err := runtimeservice.ProcessObserver{Ctx: context.Background(), Registry: r.reg, RegistryPath: r.registryPath, Binding: r.binding}.Observe(r.binding)
	if err != nil || snap.Exit == nil {
		cause := err
		if cause == nil {
			cause = errors.New("retired generation has no exit evidence")
		}
		return core.AgentProcessSnapshot{}, true, r.refusalCode(CodeProcessClosed, ErrProcessClosed, cause)
	}
	return r.finalSnapshot(snap, metadata.ProcessIdentity{}, metadata.ProcessIdentity{}), true, nil
}

func (r agentProcessRead) finalSnapshot(snap processhost.Snapshot, host, child metadata.ProcessIdentity) core.AgentProcessSnapshot {
	return core.AgentProcessSnapshot{Binding: r.public, Provider: r.provider, State: snap.State, Attention: r.attention(), Host: publicProcessIdentity(host), Child: publicProcessIdentity(child), Exit: publicProcessExit(snap.Exit)}
}

func (r agentProcessRead) snapshot(view runtimeservice.ProcessHostObservation, sequence uint64) core.AgentProcessSnapshot {
	return core.AgentProcessSnapshot{
		Binding: r.public, Provider: view.Provider, State: view.State, Running: view.Turn, Pending: view.Pending,
		Attention: r.attention(), OwnerMode: view.OwnerMode, Host: publicProcessIdentity(view.Host), Child: publicProcessIdentity(view.Child),
		Sequence: sequence, Exit: publicProcessExit(view.Exit),
	}
}

func (r agentProcessRead) batch(events runtimeservice.ProcessHostEvents) core.AgentProcessEventBatch {
	snapshot := r.snapshot(events.Observation, events.Sequence)
	batch := core.AgentProcessEventBatch{
		Binding: r.public, Snapshot: snapshot, Next: events.Last, Gap: events.Gap, More: events.More,
		Terminal: snapshot.State == "exited" && snapshot.Exit != nil, Omitted: append([]uint64(nil), events.Omitted...),
	}
	for _, event := range events.Events {
		batch.Events = append(batch.Events, core.AgentProcessEvent{Sequence: event.Sequence, Kind: event.Kind, Payload: append([]byte(nil), event.Raw...), Exit: publicProcessExit(event.Exit)})
	}
	return batch
}

// attention is the CLI's Registry interaction projection of the Agent.
func (r agentProcessRead) attention() string {
	kind := r.agent.EffectiveInteraction(time.Now()).Kind
	return codexgeneration.ProjectLifecycle(codexgeneration.LifecycleProjectionInput{Interaction: kind}).Attention
}

// refusal classifies an owner read failure. A cancelled caller gets its own
// context error; any failure other than a stale binding or an unsupported
// read means the owner did not answer, which is not a provider exit.
func (r agentProcessRead) refusal(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case errors.Is(err, processhost.ErrStale):
		return r.refusalCode(CodeProcessStale, ErrProcessStale, err)
	case errors.Is(err, runtimeservice.ErrProcessHostUnsupportedRead):
		return r.refusalCode(CodeProcessReadUnsupported, ErrProcessReadUnsupported, err)
	default:
		return r.refusalCode(CodeProcessOwnerUnreachable, ErrProcessOwnerUnreachable, err)
	}
}

func (r agentProcessRead) refusalCode(code Code, sentinel, cause error) error {
	return &Refusal{Code: code, AgentUID: r.agent.Metadata.UID, message: sentinel.Error() + ": " + cause.Error(), cause: errors.Join(sentinel, cause)}
}

func publicProcessIdentity(p metadata.ProcessIdentity) core.ProcessIdentity {
	return core.ProcessIdentity{PID: p.PID, OwnerUID: p.OwnerUID, Start: p.Start}
}

func publicProcessExit(exit *processhost.Exit) *core.AgentProcessExit {
	if exit == nil {
		return nil
	}
	return &core.AgentProcessExit{Code: exit.Code, Signal: exit.Signal}
}
