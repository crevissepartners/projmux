package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ReservedLaunchSidecar is the sidecar kind that holds the frozen launch of a
// reserved process Agent. It is a sibling of the deferred-launches kind and
// shares no file with it.
const ReservedLaunchSidecar = "created-launches"

// CreatedLaunchPath is the frozen launch file of one reserved Agent. It sits
// next to the deferred-claims directory, named by the digest of the Agent UID.
func CreatedLaunchPath(registryPath, uid string) string {
	if registryPath == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uid))
	return filepath.Join(filepath.Dir(filepath.Dir(registryPath)), ReservedLaunchSidecar, fmt.Sprintf("%x.json", digest[:]))
}

// RemoveCreatedLaunch deletes the frozen launch of a deleted Agent. A missing
// file is success. The caller holds the Agent's deferred-claim guard.
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

// ExecuteDetachedProcessReserve asks the internal owner route to reserve one
// Pending process Agent and freeze its launch. The worker owns no provider and
// is reaped before this returns; the reservation exists only after it commits.
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
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	wire := ProcessDetachedCreateRequest{Options: request.Options, Output: "none", Caller: caller, RegistryPath: storage.Path(), Context: &frozen, Reserve: true}
	launcher := ProcessDetachedLauncher{Transient: true, Executable: frozen.Executable, Environment: frozen.Environment, PayloadDir: frozen.Paths.StateDir, Verify: func(ready ProcessDetachedReply, _ coremetadata.ProcessIdentity) error {
		return VerifyDetachedProcessReserve(storage.LoadReadOnly, ready)
	}}
	reply, err = launcher.Launch(ctx, wire, io.Discard, io.Discard)
	if err != nil {
		code := "create-execution-failed"
		if reply.ErrorCode != "" {
			code = reply.ErrorCode
		}
		err = &ProcessCreateFailure{Code: code, Owner: reply.Owner, Binding: reply.Binding, Cause: errors.Join(err, ctx.Err())}
	}
	return reply, err
}

// VerifyDetachedProcessReserve proves the prepared reply names a Pending Agent
// with its reserved Binding and no activation, so no provider process exists.
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
