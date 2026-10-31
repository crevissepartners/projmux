package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ReservedLaunchSidecar is the sidecar kind of a frozen launch.
const ReservedLaunchSidecar = "created-launches"

// CreatedLaunchPath is the frozen launch file of one reserved Agent.
func CreatedLaunchPath(registryPath, uid string) string {
	if registryPath == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uid))
	return filepath.Join(filepath.Dir(filepath.Dir(registryPath)), ReservedLaunchSidecar, fmt.Sprintf("%x.json", digest[:]))
}

// RemoveCreatedLaunch deletes a deleted Agent's frozen launch; missing is success.
func RemoveCreatedLaunch(registryPath, uid string) error {
	path := CreatedLaunchPath(registryPath, uid)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ExecuteDetachedProcessReserve asks the owner route to reserve one Pending Agent.
func ExecuteDetachedProcessReserve(ctx context.Context, frozen ProcessCreateContext, options ProcessAgentCreateOptions) (reply ProcessDetachedReply, err error) {
	if err = ctx.Err(); err != nil {
		return reply, err
	}
	if err = ValidateProcessCreateContext(frozen); err != nil {
		return reply, err
	}
	request, err := NewProcessAgentCreateRequest(options)
	if err != nil {
		return reply, err
	}
	storage := intmetadata.NewStore(intmetadata.PathFor(frozen.Paths.StateDir))
	reg, err := storage.LoadReadOnly()
	if err != nil {
		return reply, err
	}
	if _, _, err = ResolveProcessCreateScope(reg, request.Options); err != nil {
		return reply, err
	}
	if _, err = DeclaredAgentCreator(&reg, request.Options.Creator, "create agent", "ReserveAgentRequest.Creator"); err != nil {
		return reply, err
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return reply, err
	}
	operation, err := intmetadata.NewOperationID()
	if err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	wire := ProcessDetachedCreateRequest{Options: request.Options, Output: "none", Caller: caller, RegistryPath: storage.Path(), Context: &frozen, Reserve: true, ReserveOperation: operation}
	launcher := ProcessDetachedLauncher{Transient: true, Executable: frozen.Executable, Environment: frozen.Environment, PayloadDir: frozen.Paths.StateDir, Verify: func(ready ProcessDetachedReply, _ coremetadata.ProcessIdentity) error {
		if err := VerifyDetachedProcessReserve(storage.LoadReadOnly, ready); err != nil {
			return err
		}
		if hook := ReserveVerifyHook; hook != nil {
			return hook(ready)
		}
		return nil
	}, Cleanup: func(owner coremetadata.ProcessIdentity, ready ProcessDetachedReply) error {
		return AwaitDetachedProcessReserveCleanup(storage.LoadReadOnly, owner, operation)
	}}
	reply, err = launcher.Launch(ctx, wire, io.Discard, io.Discard)
	if err != nil {
		reply, err = reserveFailure(reply, errors.Join(err, ctx.Err()))
	}
	return reply, err
}

// reserveFailure wraps a failed reservation, carrying the Binding cleanup found.
func reserveFailure(reply ProcessDetachedReply, cause error) (ProcessDetachedReply, error) {
	var remains *ReserveRemains
	if reply.Binding.Agent == "" && errors.As(cause, &remains) {
		reply.Binding = remains.Binding
	}
	code := "create-execution-failed"
	if reply.ErrorCode != "" {
		code = reply.ErrorCode
	}
	return reply, &ProcessCreateFailure{Code: code, Owner: reply.Owner, Binding: reply.Binding, Cause: cause}
}

// ReserveRemains is what a failed reservation left behind, with its exact Binding.
type ReserveRemains struct {
	Operation string
	Binding   processhost.Binding
	message   string
}

func (e *ReserveRemains) Error() string { return e.message }

// VerifyDetachedProcessReserve proves the prepared reply names a Pending Agent
// with its reserved Binding and no activation.
func VerifyDetachedProcessReserve(load func() (coremetadata.Registry, error), ready ProcessDetachedReply) error {
	if len(ready.Claim) != 0 || ready.Result == nil {
		return processhost.ErrStale
	}
	reg, err := load()
	if err != nil {
		return err
	}
	b := processCreateSchemaBinding(ready.Binding)
	agent, exists := reg.Agent(b.AgentUID)
	pane, present := reg.Pane(b.PaneUID)
	if !exists || !present || agent.Status.Phase != coremetadata.PhasePending || !agent.Status.Activation.IsZero() || !pane.Status.Activation.IsZero() ||
		agent.Status.PaneRef != b.PaneUID || pane.Metadata.OwnerUID() != b.AgentUID || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != b ||
		ready.Result.Agent.Metadata.UID != b.AgentUID || ready.Result.Pane.Metadata.UID != b.PaneUID {
		return processhost.ErrStale
	}
	return nil
}


// ReserveVerifyHook is a test seam: a non-nil result rejects a prepared reservation.
var ReserveVerifyHook func(ProcessDetachedReply) error

var reserveOperationPattern = regexp.MustCompile(`^op-[0-9a-f]{16}$`)

// ValidReserveOperation reports whether id has the shape of a minted operation id.
func ValidReserveOperation(id string) bool { return reserveOperationPattern.MatchString(id) }

var (
	reserveCleanupBound = ProcessAgentStopTimeout
	reserveKillSettle   = 2 * time.Second
)

func awaitDetachedOwnerGone(owner coremetadata.ProcessIdentity, bound time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for !ProcessIdentityAbsent(owner, localipc.Process) {
		select {
		case <-ctx.Done():
			return ProcessIdentityAbsent(owner, localipc.Process)
		case <-ticker.C:
		}
	}
	return true
}

// killDetachedOwner kills only the exact identity and says what it did.
func killDetachedOwner(owner coremetadata.ProcessIdentity) string {
	current, _, err := localipc.Process(owner.PID)
	if err != nil || current != owner {
		return "kill not attempted: the worker identity no longer matches"
	}
	process, err := os.FindProcess(owner.PID)
	if err == nil {
		err = process.Kill()
	}
	if err != nil {
		return "kill failed: " + err.Error()
	}
	return "kill attempted"
}

// AwaitDetachedProcessReserveCleanup waits one bound for the worker to retire,
// kills it past the bound, then reports what this operation id left in the
// Registry. Another operation's resources are never reported; nil means it looked.
func AwaitDetachedProcessReserveCleanup(load func() (coremetadata.Registry, error), owner coremetadata.ProcessIdentity, operation string) error {
	if !ValidReserveOperation(operation) {
		return errors.New("process-reserve-cleanup-undetermined: the reservation has no valid operation id")
	}
	note := ""
	gone := awaitDetachedOwnerGone(owner, reserveCleanupBound)
	if !gone {
		attempt := killDetachedOwner(owner)
		gone = awaitDetachedOwnerGone(owner, reserveKillSettle)
		state := "it is still running"
		if gone {
			state = "it has exited"
		}
		note = fmt.Sprintf("the reservation worker pid %d start %s outlived the cleanup bound; %s; %s; ", owner.PID, owner.Start, attempt, state)
	}
	reg, err := load()
	if err != nil {
		return fmt.Errorf("process-reserve-cleanup-undetermined: %soperation %s: %w", note, operation, err)
	}
	for _, pane := range reg.Panes {
		session := pane.Status.ProcessSession
		if session == nil || session.Binding.OperationID != operation {
			continue
		}
		b, agent := session.Binding, pane.Metadata.OwnerUID()
		return &ReserveRemains{Operation: operation, Binding: processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: agent, Pane: pane.Metadata.UID, Generation: b.Generation, Operation: b.OperationID},
			message: fmt.Sprintf("process-reserve-remaining: %sagent uid:%s pane uid:%s window uid:%s remain after a failed reservation (operation %s); inspect with projmux describe agent uid:%s; cleanup: projmux delete agent uid:%s", note, agent, pane.Metadata.UID, b.WindowUID, operation, agent, agent)}
	}
	if !gone {
		return fmt.Errorf("process-reserve-cleanup-unconfirmed: %sthe worker is still running; remaining reservation undetermined, operation %s", note, operation)
	}
	if note != "" {
		return fmt.Errorf("process-reserve-worker-outlived: %sno reservation of operation %s remains", note, operation)
	}
	return nil
}
