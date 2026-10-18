package runtime

import (
	"context"
	"errors"
	"io"
	"os"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const ResumePreparationStale = "process-resume-preparation-stale"

var ErrResumePreparationStale = errors.New(ResumePreparationStale)

// ProcessResumePreparation carries evidence only, never a claim credential.
type ProcessResumePreparation struct {
	AgentUID, PaneUID, Conversation, LaunchDigest string
	Binding                                       m.ProcessBinding
	HostLost                                      bool
}

// ExecuteProcessResumePreparation uses the same frozen context and internal
// lifecycle descriptors as detached Resume, but directly reaps its no-child
// worker. Neither operation hands off a provider or a claim.
func ExecuteProcessResumePreparation(ctx context.Context, frozen ProcessCreateContext, wire ProcessDetachedResumeRequest) (ProcessDetachedReply, error) {
	if err := ctx.Err(); err != nil {
		return ProcessDetachedReply{}, err
	}
	if err := ValidateProcessCreateContext(frozen); err != nil {
		return ProcessDetachedReply{}, err
	}
	if wire.PreparationAction != "prepare" && wire.PreparationAction != "validate" {
		return ProcessDetachedReply{}, errors.New("resume-request-invalid")
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return ProcessDetachedReply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessDetachedStartupTimeout)
	defer cancel()
	launcher := ProcessDetachedLauncher{Executable: frozen.Executable, Environment: frozen.Environment, Transient: true}
	launcher.Verify = func(reply ProcessDetachedReply, owner m.ProcessIdentity) error {
		p := reply.Preparation
		if p == nil || p.AgentUID != wire.Agent || p.Binding != wire.Previous || p.PaneUID != wire.Previous.PaneUID || p.Conversation == "" || p.LaunchDigest == "" {
			return ErrResumePreparationStale
		}
		if wire.PreparationAction == "validate" && (wire.Preparation == nil || *p != *wire.Preparation) {
			return ErrResumePreparationStale
		}
		return nil
	}
	reply, err := launcher.Launch(ctx, ProcessDetachedResumeEnvelope{Resume: &wire, Output: "none", Caller: caller, RegistryPath: metadata.PathFor(frozen.Paths.StateDir), ResumeContext: &frozen}, io.Discard, io.Discard)
	if err != nil {
		code := reply.ErrorCode
		if code == "" {
			code = "resume-execution-failed"
		}
		return reply, &ProcessResumeFailure{Owner: reply.Owner, Binding: reply.Binding, Code: code, Cause: errors.Join(err, ctx.Err()), message: err.Error()}
	}
	return reply, nil
}
