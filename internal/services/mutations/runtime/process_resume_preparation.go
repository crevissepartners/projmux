package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const ResumePreparationStale = "process-resume-preparation-stale"

var ErrResumePreparationStale = errors.New(ResumePreparationStale)

// ResumeNotPrepared is the error text of a prepare worker that finished
// without leaving a prepared plan.
const ResumeNotPrepared = "process-resume-not-prepared"

// ErrResumeNotPrepared is the sentinel behind ResumeNotPrepared.
var ErrResumeNotPrepared = errors.New(ResumeNotPrepared)

// ResumeNotPreparedDetail is the structured marker a failed prepare reply
// carries in ErrorDetail when err is ErrResumeNotPrepared, so the caller does
// not have to read the error text. It is nil for any other error.
func ResumeNotPreparedDetail(err error) json.RawMessage {
	if !errors.Is(err, ErrResumeNotPrepared) {
		return nil
	}
	detail, _ := json.Marshal(struct{ Code string }{ResumeNotPrepared})
	return detail
}

// IsResumeNotPreparedDetail reports whether a reply's ErrorDetail is the
// marker ResumeNotPreparedDetail writes. Empty, damaged and other detail are
// false.
func IsResumeNotPreparedDetail(detail json.RawMessage) bool {
	var marker struct{ Code string }
	return len(detail) != 0 && json.Unmarshal(detail, &marker) == nil && marker.Code == ResumeNotPrepared
}

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
