package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ProcessDetachedRelaunchRequest carries the exact source proof and public
// relaunch inputs to the existing owned executor, never a public CLI subprocess.
type ProcessDetachedRelaunchRequest struct {
	Args          []string
	Source        m.Agent
	Pane          *m.Pane
	AllowHostLost bool
	// Prepare asks for a frozen, ownerless recipe and its proof, never an owner.
	Prepare bool
}
type ProcessDetachedRelaunchEnvelope struct {
	Relaunch        *ProcessDetachedRelaunchRequest
	Caller          m.ProcessIdentity
	RegistryPath    string
	RelaunchContext *ProcessCreateContext `json:",omitempty"`
}
type ProcessRelaunchFailure struct {
	Owner   m.ProcessIdentity
	Binding processhost.Binding
	Code    string
	Cause   error
}

func (e *ProcessRelaunchFailure) Error() string { return e.Cause.Error() }
func (e *ProcessRelaunchFailure) Unwrap() error { return e.Cause }

// checkRelaunchPreparation accepts a proof only for a request that asked for
// one, and then only for an exact ownerless recipe: the Agent Offline on its own
// Pane, no activation and no claim. A reply without a proof passes here; the
// caller decides whether its request needed one.
func checkRelaunchPreparation(reg m.Registry, registryPath string, requested bool, p *ProcessResumePreparation, agent string) error {
	if p == nil {
		return nil
	}
	if !requested || p.AgentUID != agent || p.AgentUID == "" || p.PaneUID == "" || p.Conversation == "" || p.LaunchDigest == "" {
		return processhost.ErrStale
	}
	a, ok := reg.Agent(p.AgentUID)
	pane, ambiguous := ProcessAgentPane(reg, p.AgentUID)
	claim, claimErr := ReadDeferredClaim(DeferredClaimPath(registryPath, p.AgentUID))
	if !ok || ambiguous || pane == nil || a.Status.Phase != m.PhaseOffline || a.Status.PaneRef != p.PaneUID || pane.Metadata.UID != p.PaneUID || pane.Metadata.OwnerUID() != p.AgentUID || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != p.Binding || !pane.Status.Activation.IsZero() || claimErr != nil || claim.Agent != "" {
		return processhost.ErrStale
	}
	return nil
}

// checkRelaunchPrepareReply closes the one success a Prepare request has: a
// complete reply that agrees with the request on DryRun, carries no claim, and
// for an execution holds the proof. A waiting claim or a live owner reply never
// satisfies it. requestDryRun comes from the request, never from the reply.
func checkRelaunchPrepareReply(requested bool, requestDryRun bool, phase string, replyDryRun bool, claim bool, p *ProcessResumePreparation) error {
	if !requested {
		return nil
	}
	if phase != "complete" || replyDryRun != requestDryRun || claim || (!requestDryRun && p == nil) {
		return processhost.ErrStale
	}
	return nil
}

func DetachedRelaunchSourceEqual(a, b *m.Agent) bool {
	return reflect.DeepEqual(a.Spec, b.Spec) && reflect.DeepEqual(a.Metadata, b.Metadata) && a.Status.Phase == b.Status.Phase && a.Status.PaneRef == b.Status.PaneRef && a.Status.SessionRef.SameConversation(b.Status.SessionRef)
}

// LaunchDetachedProcessRelaunch is shared by the CLI adapter and the SDK. The
// executor revalidates the source and the caller's ancestry before Stop.
func LaunchDetachedProcessRelaunch(ctx context.Context, executable string, wire ProcessDetachedRelaunchEnvelope, load func() (m.Registry, error), stdout, stderr io.Writer, admit func(ProcessDetachedReply) error, tmuxDestination bool) (ProcessDetachedReply, error) {
	l := ProcessDetachedLauncher{Executable: executable, Admit: admit, CompleteAfterAdmission: tmuxDestination}
	if wire.RelaunchContext != nil {
		if err := ValidateProcessCreateContext(*wire.RelaunchContext); err != nil {
			return ProcessDetachedReply{}, err
		}
		l.Environment, l.PayloadDir = wire.RelaunchContext.Environment, wire.RelaunchContext.Paths.StateDir
	}
	l.Verify = func(ready ProcessDetachedReply, owner m.ProcessIdentity) error {
		if ready.Binding.Agent != wire.Relaunch.Source.Metadata.UID {
			return processhost.ErrStale
		}
		reg, err := load()
		if err != nil {
			return err
		}
		var plan struct{ DryRun bool }
		_ = json.Unmarshal(ready.Relaunch, &plan)
		if wire.RelaunchContext != nil {
			if err := checkRelaunchPrepareReply(wire.Relaunch.Prepare, slices.Contains(wire.Relaunch.Args, "--dry-run"), ready.Phase, plan.DryRun, len(ready.Claim) != 0, ready.Preparation); err != nil {
				return err
			}
		}
		if err := checkRelaunchPreparation(reg, wire.RegistryPath, wire.Relaunch.Prepare, ready.Preparation, ready.Binding.Agent); err != nil {
			return err
		}
		if ready.Phase == "complete" && wire.RelaunchContext != nil {
			var result struct {
				AgentUID   string
				DryRun     bool
				NewPaneUID string
				TargetHost string
			}
			if json.Unmarshal(ready.Relaunch, &result) != nil || result.AgentUID != ready.Binding.Agent {
				return processhost.ErrStale
			}
			if result.DryRun {
				return nil
			}
			if wire.Relaunch.Prepare {
				return nil
			}
			a, ok := reg.Agent(result.AgentUID)
			if !ok || result.TargetHost != "tmux" || a.Status.PaneRef != result.NewPaneUID {
				return processhost.ErrStale
			}
			p, ok := reg.Pane(result.NewPaneUID)
			if !ok || p.Metadata.OwnerUID() != a.Metadata.UID || p.Spec.Runtime.EffectiveKind() != m.RuntimeTmux {
				return processhost.ErrStale
			}
			return nil
		}
		if len(ready.Claim) != 0 {
			var claim DeferredClaimRecord
			if json.Unmarshal(ready.Claim, &claim) != nil {
				return processhost.ErrStale
			}
			current, err := ReadDeferredClaim(DeferredClaimPath(wire.RegistryPath, ready.Binding.Agent))
			if err != nil || !reflect.DeepEqual(current, claim) || !claim.Detached || claim.StopRequested || claim.Process != owner || (wire.RelaunchContext != nil && claim.Binding != processCreateSchemaBinding(ready.Binding)) || !DeferredClaimLive(claim) || !DeferredClaimMatches(claim, reg, ready.Binding.Agent) {
				return processhost.ErrStale
			}
			return nil
		}
		if wire.RelaunchContext == nil {
			activation, _, current := reg.CurrentProcessActivation(processCreateSchemaBinding(ready.Binding))
			if !current || activation.HostProcess != owner {
				return processhost.ErrStale
			}
			return nil
		}
		return VerifyDetachedProcessCreateBinding(reg, ready.Binding, owner)
	}
	var recovered processhost.Binding
	l.Cleanup = func(owner m.ProcessIdentity, ready ProcessDetachedReply) error {
		err := AwaitDetachedProcessRelaunchCleanup(load, wire.RegistryPath, *wire.Relaunch, owner)
		// SDK partial results can recover a missing reply only through the
		// exact T5 owner witness, never from the newest generation alone.
		if wire.RelaunchContext != nil && ready.Binding == (processhost.Binding{}) {
			recovered = RecoverDetachedProcessRelaunchBinding(load, wire.RegistryPath, *wire.Relaunch, owner)
		}
		return err
	}
	reply, err := l.Launch(ctx, wire, stdout, stderr)
	if reply.Binding == (processhost.Binding{}) {
		reply.Binding = recovered
	}
	if err != nil && wire.RelaunchContext != nil {
		code := reply.ErrorCode
		if code == "" {
			code = "relaunch-execution-failed"
		}
		if code == "relaunch-source-stale" {
			err = errors.Join(err, processhost.ErrStale)
		}
		err = &ProcessRelaunchFailure{Owner: reply.Owner, Binding: reply.Binding, Code: code, Cause: errors.Join(err, ctx.Err())}
	}
	return reply, err
}

func ExecuteDetachedProcessRelaunch(ctx context.Context, frozen ProcessCreateContext, wire ProcessDetachedRelaunchRequest, admit func(ProcessDetachedReply) error, tmuxDestination bool) (ProcessDetachedReply, error) {
	if err := ctx.Err(); err != nil {
		return ProcessDetachedReply{}, err
	}
	if err := ValidateProcessCreateContext(frozen); err != nil {
		return ProcessDetachedReply{}, err
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return ProcessDetachedReply{}, err
	}
	storage := metadata.NewStore(metadata.PathFor(frozen.Paths.StateDir))
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	return LaunchDetachedProcessRelaunch(ctx, frozen.Executable, ProcessDetachedRelaunchEnvelope{Relaunch: &wire, Caller: caller, RegistryPath: storage.Path(), RelaunchContext: &frozen}, storage.LoadReadOnly, io.Discard, io.Discard, admit, tmuxDestination)
}

// Cleanup never stops the source owner or another claimant. Offline is accepted
// only with unchanged source evidence or exact actual Wait, as in the CLI.
func AwaitDetachedProcessRelaunchCleanup(load func() (m.Registry, error), registryPath string, request ProcessDetachedRelaunchRequest, owner m.ProcessIdentity) error {
	ctx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
	defer cancel()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	source := request.Source
	for {
		reg, err := load()
		settled := false
		if err == nil {
			a, exists := reg.Agent(source.Metadata.UID)
			if exists {
				settled = DetachedRelaunchSourceEqual(a, &source)
				if settled && request.Pane != nil {
					p, present := reg.Pane(request.Pane.Metadata.UID)
					settled = present && reflect.DeepEqual(p.Status.Activation, request.Pane.Status.Activation) && reflect.DeepEqual(p.Status.ProcessSession, request.Pane.Status.ProcessSession)
				}
				p, ambiguous := ProcessAgentPane(reg, source.Metadata.UID)
				if p != nil && !ambiguous && p.Status.ProcessSession != nil && a.Status.Phase == m.PhaseOffline && p.Status.Activation.IsZero() {
					b := p.Status.ProcessSession.Binding
					settled = settled || (sameRelaunchSpec(a.Spec, source.Spec) && reflect.DeepEqual(a.Metadata.Annotations, source.Metadata.Annotations) && m.MatchesProcessWait(b, p.Status.LastTermination) && m.SameProcessWait(p.Status.LastTermination, a.Status.LastTermination))
				}
				if a.Status.Phase == m.PhaseOffline && a.Status.PaneRef == "" && p == nil && sameRelaunchSpec(a.Spec, source.Spec) && reflect.DeepEqual(a.Metadata.Annotations, source.Metadata.Annotations) {
					settled = true
				}
			}
		}
		claim, claimErr := ReadDeferredClaim(DeferredClaimPath(registryPath, source.Metadata.UID))
		if settled && claimErr == nil && (claim.Agent == "" || claim.Process != owner) && ProcessIdentityAbsent(owner, localipc.Process) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("process-detached-cleanup-unconfirmed: agent uid:%s; inspect with: projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", source.Metadata.UID, source.Metadata.UID, source.Metadata.UID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func sameRelaunchSpec(a, b m.AgentSpec) bool {
	return a.Provider == b.Provider && a.Workspace.CWD == b.Workspace.CWD && slices.Equal(a.Workspace.AdditionalWritableRoots, b.Workspace.AdditionalWritableRoots)
}

// RecoverDetachedProcessRelaunchBinding is read-only and SDK-only. The prior
// process binding and exact new owner must both be proven by the shared resume
// activation/actual-Wait witness. A missing proof deliberately returns zero.
func RecoverDetachedProcessRelaunchBinding(load func() (m.Registry, error), registryPath string, request ProcessDetachedRelaunchRequest, owner m.ProcessIdentity) processhost.Binding {
	if request.Pane == nil || request.Pane.Status.ProcessSession == nil {
		return processhost.Binding{}
	}
	reg, err := load()
	if err != nil {
		return processhost.Binding{}
	}
	agent, ok := reg.Agent(request.Source.Metadata.UID)
	pane, ambiguous := ProcessAgentPane(reg, request.Source.Metadata.UID)
	if !ok || pane == nil || ambiguous || pane.Status.ProcessSession == nil {
		return processhost.Binding{}
	}
	resume := ProcessDetachedResumeRequest{Agent: request.Source.Metadata.UID, Previous: request.Pane.Status.ProcessSession.Binding}
	if !recoveredDetachedResumeBinding(registryPath, resume, *agent, *pane, owner) {
		return processhost.Binding{}
	}
	return processResumeHostBinding(pane.Status.ProcessSession.Binding)
}
