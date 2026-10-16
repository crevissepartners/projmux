package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ResumeProcessError exposes recovery identity without parsing diagnostics.
type ResumeProcessError struct {
	Code    Code
	Owner   core.ProcessIdentity
	Binding core.ProcessBinding
	cause   error
}

func (e *ResumeProcessError) Error() string { return e.cause.Error() }
func (e *ResumeProcessError) Unwrap() error { return e.cause }

func (c *Client) resumeProcessAgent(ctx context.Context, req core.ResumeAgentRequest, agent m.Agent, reg m.Registry) (core.ResumeAgentResult, error) {
	var result core.ResumeAgentResult
	if !req.Detach || req.DialogueReplyOnly || req.SocketPath != "" || (req.WaitForPeer && req.Prompt != "") {
		return result, &Refusal{Code: CodeResumeRequestInvalid, message: "ResumeAgentRequest process requires Detach and does not support SocketPath, DialogueReplyOnly, or a prompt with WaitForPeer"}
	}
	if !filepath.IsAbs(c.projmuxExecutable) {
		return result, &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
	}
	info, executableErr := os.Stat(c.projmuxExecutable)
	if executableErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return result, &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: executableErr}
	}
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, agent.Metadata.UID)
	if ambiguous || pane == nil || pane.Status.ProcessSession == nil || pane.Metadata.OwnerUID() != agent.Metadata.UID || (agent.Status.PaneRef != "" && agent.Status.PaneRef != pane.Metadata.UID) {
		return result, &Refusal{Code: CodeResumePlanInvalid, AgentUID: agent.Metadata.UID, message: "process resume requires an exact reciprocal Agent/Pane binding"}
	}
	if agent.Spec.Provider != "claude" && agent.Spec.Provider != "codex" {
		return result, &Refusal{Code: CodeResumeRequestInvalid, message: "process resume requires Claude or Codex"}
	}
	if agent.Spec.Provider == "claude" && !req.WaitForPeer && strings.TrimSpace(req.Prompt) == "" {
		return result, &Refusal{Code: CodeResumeRequestInvalid, message: "agent resume: process Claude requires -- <prompt>; stream-json emits init only after the first user frame"}
	}
	previous := pane.Status.ProcessSession.Binding
	var observed core.ProcessBinding
	if err := unregisterCopy(previous, &observed); err != nil {
		return result, err
	}
	if req.Binding != (core.ProcessBinding{}) && req.Binding != observed {
		return result, &Refusal{Code: CodeResumePlanInvalid, AgentUID: agent.Metadata.UID, message: "process resume binding changed", cause: processhost.ErrStale}
	}
	wire := mutationruntime.ProcessDetachedResumeRequest{Agent: agent.Metadata.UID, Model: req.Model, Effort: req.Effort, WaitForPeer: req.WaitForPeer, Previous: previous}
	if req.Prompt != "" {
		wire.Prompt = mutationruntime.ProcessResumeFirstFrame{Kind: "user", Text: req.Prompt}
	}
	reply, err := mutationruntime.ExecuteDetachedProcessResume(ctx, c.processCreateContext, wire)
	if reply.Binding.Agent != "" {
		_ = unregisterCopy(m.ProcessBinding{HostInstanceID: reply.Binding.Host, ProjectUID: reply.Binding.Project, WindowUID: reply.Binding.Window, AgentUID: reply.Binding.Agent, PaneUID: reply.Binding.Pane, Generation: reply.Binding.Generation, OperationID: reply.Binding.Operation}, &result.Binding)
	}
	result.ClaimWaiting = len(reply.Claim) != 0
	if reply.Result != nil {
		if copyErr := unregisterCopy(reply.Result.Agent, &result.Agent); copyErr != nil {
			return result, copyErr
		}
		result.Projection = core.ResourceList{APIVersion: m.APIVersion, Kind: "AgentList", Items: []core.Resource{result.Agent}}
		result.Notices = reply.Result.Notices
		if copyErr := unregisterCopy(reply.Result.Receipt, &result.Receipt); copyErr != nil {
			return result, copyErr
		}
	}
	if err != nil {
		if len(reply.ErrorDetail) != 0 {
			var recipe core.ResumeRecipeChangedError
			if json.Unmarshal(reply.ErrorDetail, &recipe) == nil && recipe.Code == "process-resume-refused" {
				err = errors.Join(err, &recipe)
			}
		}
		var failure *mutationruntime.ProcessResumeFailure
		if errors.As(err, &failure) {
			detail := &ResumeProcessError{Code: Code(failure.Code), Binding: result.Binding, cause: err}
			_ = unregisterCopy(failure.Owner, &detail.Owner)
			return result, &Refusal{Code: detail.Code, AgentUID: agent.Metadata.UID, message: err.Error(), cause: detail}
		}
		return result, resumeRefusal(err)
	}
	return result, nil
}
