package projmux

import (
	"context"
	"errors"
	"os"
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

// Refusal.Reason of a CodeProcessOwnerUnreachable refusal of ReadAgentProcess
// that proves the current owner gone before any read. An empty Reason is a
// transient or unknown failure.
const (
	// ProcessOwnerIdentityGone: the recorded owner process is absent or reused.
	ProcessOwnerIdentityGone = "owner-identity-gone"
	// ProcessOwnerSocketGone: the owner socket path does not exist.
	ProcessOwnerSocketGone = "owner-socket-gone"
	// ProcessOwnerSocketReplaced: the path is not a socket, or its identity
	// differs from AgentProcessRequest.OwnerSocket.
	ProcessOwnerSocketReplaced = "owner-socket-replaced"
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
// ErrProcessOwnerUnreachable. The owner read shares one localipc.Deadline (5s)
// budget; Registry file reads are outside it.
func (c *Client) ReadAgentProcess(ctx context.Context, req core.AgentProcessRequest) (core.AgentProcessSnapshot, error) {
	const field = "AgentProcessRequest"
	read, err := c.resolveAgentProcess(ctx, req.Agent, req.Binding, field)
	if err != nil {
		return core.AgentProcessSnapshot{}, err
	}
	if snapshot, done, err := read.final(); done || err != nil {
		return snapshot, err
	}
	// An ended generation's final Snapshot or closed/stale refusal wins over
	// a gone reason.
	if reason := read.ownerGone(req.OwnerSocket); reason != "" {
		if snapshot, done, err := c.settleAgentProcess(ctx, req.Agent, field, read); done {
			return snapshot, err
		}
		return core.AgentProcessSnapshot{}, read.goneRefusal(ctx, reason)
	}
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	view, err := read.observer(bounded).ObserveHostView(read.binding, read.activation, read.provider)
	if err != nil {
		if snapshot, done, err := c.settleAgentProcess(ctx, req.Agent, field, read); done {
			return snapshot, err
		}
		return core.AgentProcessSnapshot{}, read.refusal(ctx, bounded, err)
	}
	if err := ctx.Err(); err != nil {
		return core.AgentProcessSnapshot{}, err
	}
	snapshot := read.snapshot(view, 0)
	if identity, err := localipc.InspectOwnedSocket(read.socket()); err == nil {
		if socketReplaced(req.OwnerSocket, identity) {
			return core.AgentProcessSnapshot{}, read.goneRefusal(ctx, ProcessOwnerSocketReplaced)
		}
		snapshot.OwnerSocket = &core.AgentProcessSocket{Device: identity.Device, Inode: identity.Inode}
	}
	return snapshot, nil
}

func socketReplaced(expected core.AgentProcessSocket, current localipc.SocketIdentity) bool {
	return expected.Inode != 0 && (current.Inode != expected.Inode || current.Device != expected.Device)
}

func (r agentProcessRead) socket() string {
	return runtimeservice.ProcessHostSocket(r.provider, r.registryPath, r.binding.Pane, r.binding.Generation)
}

// ownerGone reports why the current activation's owner is definitely gone
// before any owner read, in this order: recorded owner process absent or
// reused, socket path missing, path not a socket, then a socket identity that
// differs from expected. A transient read or permission error gives "".
func (r agentProcessRead) ownerGone(expected core.AgentProcessSocket) string {
	if !r.current {
		return ""
	}
	if mutationruntime.ProcessIdentityAbsent(r.activation.HostProcess, localipc.Process) {
		return ProcessOwnerIdentityGone
	}
	socket := r.socket()
	if info, err := os.Lstat(socket); errors.Is(err, os.ErrNotExist) {
		return ProcessOwnerSocketGone
	} else if err == nil && info.Mode()&os.ModeSocket == 0 {
		return ProcessOwnerSocketReplaced
	}
	if expected.Inode != 0 {
		if current, err := localipc.InspectOwnedSocket(socket); err == nil && socketReplaced(expected, current) {
			return ProcessOwnerSocketReplaced
		}
	}
	return ""
}

// goneRefusal is the unreachable refusal with its Reason; a cancelled caller
// gets its own context error.
func (r agentProcessRead) goneRefusal(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.refusalCode(CodeProcessOwnerUnreachable, ErrProcessOwnerUnreachable, errors.New(reason))
	err.(*Refusal).Reason = reason
	return err
}

// ReadAgentProcessEvents reads the events after req.After from one exact
// process Agent generation, with the observation of the snapshot taken under
// the same owner lock. When no event follows After, it repeats the read until
// one does, the generation ends, or req.Wait passes, and then returns an empty
// batch. Wait only waits for new events; it never shortens a read of a slow
// owner. The owner reads and repeats share one localipc.Deadline (5s) budget,
// and an owner that never answers within it is ErrProcessOwnerUnreachable;
// Registry file reads and scheduling delay are outside that budget. It starts
// no polling goroutine, and cancellation ends only the read. An owner built
// before it served event reads is ErrProcessReadUnsupported, and no request is
// sent to it.
func (c *Client) ReadAgentProcessEvents(ctx context.Context, req core.AgentProcessEventsRequest) (core.AgentProcessEventBatch, error) {
	const field = "AgentProcessEventsRequest"
	read, err := c.resolveAgentProcess(ctx, req.Agent, req.Binding, field)
	if err != nil {
		return core.AgentProcessEventBatch{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	waitUntil := time.Now().Add(min(max(req.Wait, 0), localipc.Deadline))
	for {
		if snapshot, done, err := read.final(); done || err != nil {
			return read.terminal(snapshot, req.After, err)
		}
		events, err := read.observer(bounded).ReadProcessHostEvents(req.After)
		if err != nil {
			if snapshot, done, err := c.settleAgentProcess(ctx, req.Agent, field, read); done {
				return read.terminal(snapshot, req.After, err)
			}
			return core.AgentProcessEventBatch{}, read.refusal(ctx, bounded, err)
		}
		batch := read.batch(events)
		if len(batch.Events) > 0 || batch.Terminal || !time.Now().Add(agentProcessPollInterval).Before(waitUntil) {
			if err := ctx.Err(); err != nil {
				return core.AgentProcessEventBatch{}, err
			}
			return batch, nil
		}
		timer := time.NewTimer(agentProcessPollInterval)
		select {
		case <-bounded.Done():
			timer.Stop()
			if err := ctx.Err(); err != nil {
				return core.AgentProcessEventBatch{}, err
			}
			return batch, nil
		case <-timer.C:
		}
		// The generation may end between reads: read the Registry again for
		// the Binding fixed by the first read, never a newer one.
		if read, err = c.resolveAgentProcess(ctx, req.Agent, read.public, field); err != nil {
			return core.AgentProcessEventBatch{}, err
		}
	}
}

// settleAgentProcess reads the Registry again after an owner read failed. The
// fixed Binding may have ended meanwhile: its recorded Wait is the final
// Snapshot, a host-lost or bare retirement is ErrProcessClosed, and a newer
// generation is ErrProcessStale. done is false while the same generation is
// still current without final evidence; the owner is then unreachable.
func (c *Client) settleAgentProcess(ctx context.Context, ref, field string, read agentProcessRead) (core.AgentProcessSnapshot, bool, error) {
	if ctx.Err() != nil {
		return core.AgentProcessSnapshot{}, false, nil
	}
	next, err := c.resolveAgentProcess(ctx, ref, read.public, field)
	if err != nil {
		return core.AgentProcessSnapshot{}, true, err
	}
	if snapshot, done, err := next.final(); done || err != nil {
		return snapshot, true, err
	}
	return core.AgentProcessSnapshot{}, false, nil
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
		return read, read.refusal(ctx, ctx, processhost.ErrStale)
	}
	read.binding = runtimeservice.ProcessSchemaBinding(stored)
	read.public = core.AgentProcessBinding{Host: stored.HostInstanceID, Pane: stored.PaneUID, Generation: stored.Generation}
	if !want.IsZero() && want != read.public {
		return read, read.refusal(ctx, ctx, processhost.ErrStale)
	}
	if read.current {
		if _, _, ok := reg.CurrentProcessActivation(stored); !ok {
			return read, read.refusal(ctx, ctx, processhost.ErrStale)
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
		Binding: r.public, Provider: view.Provider, State: view.State, Running: view.Turn, Protocol: view.Protocol, Actions: append([]string(nil), view.Actions...), Pending: view.Pending,
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
		public := core.AgentProcessEvent{Sequence: event.Sequence, Kind: event.Kind, Exit: publicProcessExit(event.Exit)}
		// An event without a provider frame crosses the owner frame as JSON null.
		if len(event.Raw) > 0 && string(event.Raw) != "null" {
			public.Payload = append([]byte(nil), event.Raw...)
		}
		batch.Events = append(batch.Events, public)
	}
	return batch
}

// terminal is the last batch of a generation that ended with its final
// Snapshot; a closed or stale generation returns err instead.
func (r agentProcessRead) terminal(snapshot core.AgentProcessSnapshot, after uint64, err error) (core.AgentProcessEventBatch, error) {
	if err != nil {
		return core.AgentProcessEventBatch{}, err
	}
	return core.AgentProcessEventBatch{Binding: r.public, Snapshot: snapshot, Next: after, Terminal: true}, nil
}

// attention is the CLI's Registry interaction projection of the Agent.
func (r agentProcessRead) attention() string {
	kind := r.agent.EffectiveInteraction(time.Now()).Kind
	return codexgeneration.ProjectLifecycle(codexgeneration.LifecycleProjectionInput{Interaction: kind}).Attention
}

// refusal classifies an owner read failure. A cancelled caller gets its own
// context error. An owner that did not answer within the call's bound, or
// any failure other than a stale binding or an unsupported read, is
// unreachable, which is not a provider exit.
func (r agentProcessRead) refusal(ctx, bounded context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// The socket deadline can fire just before the context's timer runs, so
	// a reached deadline counts as well.
	if deadline, ok := bounded.Deadline(); bounded.Err() != nil || (ok && !time.Now().Before(deadline)) {
		return r.refusalCode(CodeProcessOwnerUnreachable, ErrProcessOwnerUnreachable, errors.Join(context.DeadlineExceeded, err))
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
