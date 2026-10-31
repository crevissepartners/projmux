package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ExecuteDetachedProcessStartReserved asks the owner route to start the frozen
// launch of one reserved Agent. The caller owns startup only, as for a create.
func ExecuteDetachedProcessStartReserved(ctx context.Context, frozen ProcessCreateContext, agentUID string) (reply ProcessDetachedReply, err error) {
	if err = ctx.Err(); err != nil {
		return reply, err
	}
	if err = ValidateProcessCreateContext(frozen); err != nil {
		return reply, err
	}
	storage := intmetadata.NewStore(intmetadata.PathFor(frozen.Paths.StateDir))
	reg, err := storage.LoadReadOnly()
	if err != nil {
		return reply, err
	}
	reserved, err := reservedProcessBinding(reg, agentUID)
	if err != nil {
		return reply, err
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	wire := ProcessDetachedCreateRequest{Output: "none", Caller: caller, RegistryPath: storage.Path(), Context: &frozen, StartReserved: true, StartAgent: agentUID}
	launcher := ProcessDetachedLauncher{Executable: frozen.Executable, Environment: frozen.Environment, PayloadDir: frozen.Paths.StateDir, Verify: func(ready ProcessDetachedReply, owner coremetadata.ProcessIdentity) error {
		if ready.Binding != reserved {
			return processhost.ErrStale
		}
		if err := VerifyDetachedProcessCreate(storage.LoadReadOnly, ready, owner); err != nil {
			return err
		}
		if hook := StartVerifyHook; hook != nil {
			return hook(ready)
		}
		return nil
	}, Cleanup: func(owner coremetadata.ProcessIdentity, _ ProcessDetachedReply) error {
		return AwaitDetachedProcessStartCleanup(storage.LoadReadOnly, owner, reserved)
	}}
	reply, err = launcher.Launch(ctx, wire, io.Discard, io.Discard)
	if err != nil {
		code := "create-execution-failed"
		if reply.ErrorCode != "" {
			code = reply.ErrorCode
		}
		if reply.Binding.Agent == "" {
			reply.Binding = reserved
		}
		err = &ProcessCreateFailure{Code: code, Owner: reply.Owner, Binding: reply.Binding, Cause: errors.Join(err, ctx.Err())}
	}
	return reply, err
}

// StartVerifyHook is a test seam: a non-nil result rejects a started reservation.
var StartVerifyHook func(ProcessDetachedReply) error

// reservedProcessBinding is the Binding a Pending process Agent was reserved with.
func reservedProcessBinding(reg coremetadata.Registry, agentUID string) (processhost.Binding, error) {
	agent, exists := reg.Agent(agentUID)
	pane, ambiguous := ProcessAgentPane(reg, agentUID)
	if !exists || ambiguous || pane == nil || pane.Status.ProcessSession == nil {
		return processhost.Binding{}, &AgentCreateRefusal{Reason: "reservation-missing", Message: "start reserved agent: uid:" + agentUID + " has no reserved process Agent; nothing was started"}
	}
	if agent.Status.Phase != coremetadata.PhasePending || !agent.Status.Activation.IsZero() {
		return processhost.Binding{}, &AgentCreateRefusal{Reason: "reservation-missing", Message: "start reserved agent: uid:" + agentUID + " is not a Pending Agent, so it has no reservation to start; nothing was started"}
	}
	b := pane.Status.ProcessSession.Binding
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: agentUID, Pane: pane.Metadata.UID, Generation: b.Generation, Operation: b.OperationID}, nil
}

// AwaitDetachedProcessStartCleanup waits for the worker to retire, then accepts
// the three states a failed start may leave: the reservation untouched, exactly
// rolled back, or Offline with the worker's Wait. Anything else is unconfirmed.
func AwaitDetachedProcessStartCleanup(load func() (coremetadata.Registry, error), owner coremetadata.ProcessIdentity, binding processhost.Binding) error {
	ctx, cancel := context.WithTimeout(context.Background(), ProcessAgentStopTimeout)
	defer cancel()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	b := processCreateSchemaBinding(binding)
	for {
		reg, err := load()
		if err == nil && ProcessIdentityAbsent(owner, localipc.Process) && startSettled(reg, b) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("process-start-cleanup-unconfirmed: remaining agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", b.AgentUID, b.PaneUID, b.AgentUID, b.AgentUID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func startSettled(reg coremetadata.Registry, b coremetadata.ProcessBinding) bool {
	agent, exists := reg.Agent(b.AgentUID)
	pane, present := reg.Pane(b.PaneUID)
	if !exists || !present {
		return !exists && !present
	}
	if pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != b {
		return false
	}
	if agent.Status.Phase == coremetadata.PhasePending {
		return agent.Status.Activation.IsZero() && pane.Status.Activation.IsZero()
	}
	return agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(b, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
}
