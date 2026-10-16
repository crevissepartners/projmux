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
			if err != nil || !reflect.DeepEqual(current, claim) || !claim.Detached || claim.StopRequested || claim.Process != owner || claim.Binding != processCreateSchemaBinding(ready.Binding) || !DeferredClaimLive(claim) || !DeferredClaimMatches(claim, reg, ready.Binding.Agent) {
				return processhost.ErrStale
			}
			return nil
		}
		return VerifyDetachedProcessCreateBinding(reg, ready.Binding, owner)
	}
	l.Cleanup = func(owner m.ProcessIdentity, ready ProcessDetachedReply) error {
		return AwaitDetachedProcessRelaunchCleanup(load, wire.RegistryPath, *wire.Relaunch, owner)
	}
	reply, err := l.Launch(ctx, wire, stdout, stderr)
	if err != nil {
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
