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
	Agent             string
	Model, Effort     string
	Prompt            ProcessResumeFirstFrame
	WaitForPeer       bool
	Previous          m.ProcessBinding
	PreparationAction string
	AllowHostLost     bool
	Preparation       *ProcessResumePreparation
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
	message string
}

func (e *ProcessResumeFailure) Error() string { return e.message }
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
		if wire.ResumeContext != nil {
			if ready.Result == nil || ready.Result.Agent.Metadata.UID != b.AgentUID || ready.Result.Pane.Metadata.UID != b.PaneUID || ready.Result.Pane.Status.ProcessSession == nil || ready.Result.Pane.Status.ProcessSession.Binding != b {
				return processhost.ErrStale
			}
		}
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
	var cleanupBinding processhost.Binding
	l.Cleanup = func(owner m.ProcessIdentity, ready ProcessDetachedReply) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
		defer cancel()
		var err error
		cleanupBinding, err = awaitDetachedProcessResumeCleanup(cleanupCtx, load, wire.RegistryPath, *wire.Resume, owner, ready.Binding)
		return err
	}
	reply, err := l.Launch(ctx, wire, stdout, stderr)
	if reply.Binding == (processhost.Binding{}) {
		reply.Binding = cleanupBinding
	}
	if err != nil {
		code := reply.ErrorCode
		if code == "" {
			code = "resume-execution-failed"
		}
		message := err.Error()
		if code == "process-resume-refused" {
			err = errors.Join(err, processhost.ErrResumeRefused)
		}
		err = &ProcessResumeFailure{Owner: reply.Owner, Binding: reply.Binding, Code: code, Cause: errors.Join(err, ctx.Err()), message: message}
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

func AwaitDetachedProcessResumeCleanup(load func() (m.Registry, error), registryPath string, request ProcessDetachedResumeRequest, owner m.ProcessIdentity, binding processhost.Binding) error {
	ctx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
	defer cancel()
	_, err := awaitDetachedProcessResumeCleanup(ctx, load, registryPath, request, owner, binding)
	return err
}

// No generation is inferred from a PID or newest Registry record alone. A
// missing startup reply can recover only this request's exact owner witness.
func awaitDetachedProcessResumeCleanup(ctx context.Context, load func() (m.Registry, error), registryPath string, request ProcessDetachedResumeRequest, owner m.ProcessIdentity, binding processhost.Binding) (processhost.Binding, error) {
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for {
		reg, err := load()
		if err == nil {
			agent, exists := reg.Agent(request.Agent)
			pane, ambiguous := ProcessAgentPane(reg, request.Agent)
			if exists && pane != nil && !ambiguous && pane.Status.ProcessSession != nil {
				b := pane.Status.ProcessSession.Binding
				if binding == (processhost.Binding{}) && recoveredDetachedResumeBinding(registryPath, request, *agent, *pane, owner) {
					binding = processResumeHostBinding(b)
				}
				settled := agent.Status.Phase == m.PhaseOffline && ((b == request.Previous && pane.Status.Activation.IsZero()) || (b == processCreateSchemaBinding(binding) && m.MatchesProcessWait(b, pane.Status.LastTermination) && m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)))
				// A supplied-proof refusal can leave the original lost-host Running
				// record untouched. This request never owned that previous activation.
				unreserved := request.Preparation != nil && b == request.Previous && (pane.Status.Activation.Process == nil || pane.Status.Activation.Process.HostProcess != owner)
				if (settled || unreserved) && ProcessIdentityAbsent(owner, localipc.Process) {
					return binding, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return binding, fmt.Errorf("process-detached-cleanup-unconfirmed: remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", request.Agent, request.Previous.PaneUID, request.Agent, request.Agent, ctx.Err())
		case <-ticker.C:
		}
	}
}

func processResumeHostBinding(b m.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}

func recoveredDetachedResumeBinding(registryPath string, request ProcessDetachedResumeRequest, agent m.Agent, pane m.Pane, owner m.ProcessIdentity) bool {
	b, old := pane.Status.ProcessSession.Binding, request.Previous
	if !owner.Valid() || request.Agent != old.AgentUID || agent.Metadata.UID != old.AgentUID || agent.Status.PaneRef != old.PaneUID || pane.Metadata.UID != old.PaneUID || pane.Metadata.OwnerUID() != old.AgentUID || b.AgentUID != old.AgentUID || b.PaneUID != old.PaneUID || b.WindowUID != old.WindowUID || b.ProjectUID != old.ProjectUID || b.Generation == old.Generation || b.OperationID == old.OperationID || b.HostInstanceID == old.HostInstanceID {
		return false
	}
	if activation := pane.Status.Activation.Process; activation != nil && activation.Binding == b && activation.HostProcess == owner && activation.Child.Valid() {
		current, _, err := localipc.Process(owner.PID)
		if err == nil && current == owner {
			return true
		}
	}
	if !m.MatchesProcessWait(b, pane.Status.LastTermination) || !m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return false
	}
	activation, err := ReadProcessRetirement(registryPath, b, pane.Status.LastTermination)
	return err == nil && activation.HostProcess == owner
}
