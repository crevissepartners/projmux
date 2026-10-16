package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ProcessResumeFirstFrame preserves operator and peer input authority.
type ProcessResumeFirstFrame struct{ Kind, Text string }
type ProcessDetachedResumeRequest struct {
	Agent         string
	Model, Effort string
	Prompt        ProcessResumeFirstFrame
	WaitForPeer   bool
	Previous      m.ProcessBinding
}

type ProcessDetachedResumeEnvelope struct {
	Resume       *ProcessDetachedResumeRequest
	Output       string
	Caller       m.ProcessIdentity
	RegistryPath string
	// ResumeContext is distinct from the create-only Context route.
	ResumeContext *ProcessCreateContext `json:",omitempty"`
}

// ProcessResumeFailure retains exact recovery resources, including uncertain startup.
type ProcessResumeFailure struct {
	Owner   m.ProcessIdentity
	Binding processhost.Binding
	Code    string
	Cause   error
}

func (e *ProcessResumeFailure) Error() string { return e.Cause.Error() }
func (e *ProcessResumeFailure) Unwrap() error { return e.Cause }

// LaunchDetachedProcessResume is the CLI/SDK startup transaction. The owner
// revalidates the previous binding before reservation and owns all later work.
func LaunchDetachedProcessResume(ctx context.Context, executable string, wire ProcessDetachedResumeEnvelope, load func() (m.Registry, error), stdout, stderr io.Writer) (ProcessDetachedReply, error) {
	l := ProcessDetachedLauncher{Executable: executable}
	if wire.ResumeContext != nil {
		if err := ValidateProcessCreateContext(*wire.ResumeContext); err != nil {
			return ProcessDetachedReply{}, err
		}
		l.Environment, l.PayloadDir = wire.ResumeContext.Environment, wire.ResumeContext.Paths.StateDir
	}
	l.Verify = func(ready ProcessDetachedReply, owner m.ProcessIdentity) error {
		reg, err := load()
		if err != nil {
			return err
		}
		b := processCreateSchemaBinding(ready.Binding)
		old := wire.Resume.Previous
		if b.AgentUID != wire.Resume.Agent || b.PaneUID != old.PaneUID || b.ProjectUID != old.ProjectUID || b.WindowUID != old.WindowUID {
			return processhost.ErrStale
		}
		if len(ready.Claim) != 0 {
			var claim DeferredClaimRecord
			if !wire.Resume.WaitForPeer || json.Unmarshal(ready.Claim, &claim) != nil {
				return processhost.ErrStale
			}
			current, e := ReadDeferredClaim(DeferredClaimPath(wire.RegistryPath, b.AgentUID))
			if e != nil || !reflect.DeepEqual(current, claim) || !claim.Detached || claim.StopRequested || claim.Process != owner || claim.Binding != old || b != old || !DeferredClaimLive(claim) || !DeferredClaimMatches(claim, reg, b.AgentUID) {
				return processhost.ErrStale
			}
			return nil
		}
		if wire.Resume.WaitForPeer || b.Generation == old.Generation {
			return processhost.ErrStale
		}
		return VerifyDetachedProcessCreateBinding(reg, ready.Binding, owner)
	}
	l.Cleanup = func(owner m.ProcessIdentity, ready ProcessDetachedReply) error {
		return AwaitDetachedProcessResumeCleanup(load, *wire.Resume, owner, ready.Binding)
	}
	reply, err := l.Launch(ctx, wire, stdout, stderr)
	if err != nil {
		code := reply.ErrorCode
		if code == "" {
			code = "resume-execution-failed"
		}
		if code == "process-resume-refused" {
			err = errors.Join(err, processhost.ErrResumeRefused)
		}
		err = &ProcessResumeFailure{Owner: reply.Owner, Binding: reply.Binding, Code: code, Cause: errors.Join(err, ctx.Err())}
	}
	return reply, err
}

func ExecuteDetachedProcessResume(ctx context.Context, frozen ProcessCreateContext, wire ProcessDetachedResumeRequest) (ProcessDetachedReply, error) {
	if err := ctx.Err(); err != nil {
		return ProcessDetachedReply{}, err
	}
	if err := ValidateProcessCreateContext(frozen); err != nil {
		return ProcessDetachedReply{}, err
	}
	storage := metadata.NewStore(metadata.PathFor(frozen.Paths.StateDir))
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return ProcessDetachedReply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	return LaunchDetachedProcessResume(ctx, frozen.Executable, ProcessDetachedResumeEnvelope{Resume: &wire, Output: "none", Caller: caller, RegistryPath: storage.Path(), ResumeContext: &frozen}, storage.LoadReadOnly, io.Discard, io.Discard)
}

func AwaitDetachedProcessResumeCleanup(load func() (m.Registry, error), request ProcessDetachedResumeRequest, owner m.ProcessIdentity, binding processhost.Binding) error {
	ctx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
	defer cancel()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for {
		reg, err := load()
		if err == nil {
			agent, exists := reg.Agent(request.Agent)
			pane, ambiguous := ProcessAgentPane(reg, request.Agent)
			if exists && pane != nil && !ambiguous && pane.Status.ProcessSession != nil {
				b := pane.Status.ProcessSession.Binding
				settled := agent.Status.Phase == m.PhaseOffline && ((b == request.Previous && pane.Status.Activation.IsZero()) || (b == processCreateSchemaBinding(binding) && m.MatchesProcessWait(b, pane.Status.LastTermination) && m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)))
				if settled && ProcessIdentityAbsent(owner, localipc.Process) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("process-detached-cleanup-unconfirmed: remaining agent uid:%s pane uid:%s; cleanup: projmux agent stop uid:%s: %w", request.Agent, request.Previous.PaneUID, request.Agent, ctx.Err())
		case <-ticker.C:
		}
	}
}
