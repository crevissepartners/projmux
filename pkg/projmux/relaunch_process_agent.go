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
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeRelaunchSelfTarget  Code = "relaunch-self-target"
	CodeRelaunchSourceStale Code = "relaunch-source-stale"
)

// RelaunchProcessError preserves exact recovery identity, including failures
// before the new binding can be acknowledged. Never automatically replay it.
type RelaunchProcessError struct {
	Code    Code
	Owner   core.ProcessIdentity
	Binding core.ProcessBinding
	cause   error
}

func (e *RelaunchProcessError) Error() string { return e.cause.Error() }
func (e *RelaunchProcessError) Unwrap() error { return e.cause }

func (c *Client) relaunchProcessAgent(ctx context.Context, q core.RelaunchAgentRequest, target m.Agent, reg m.Registry) (out core.RelaunchAgentResult, err error) {
	current := "tmux"
	pane, ambiguous := runtime.ProcessAgentPane(reg, target.Metadata.UID)
	if ambiguous {
		return out, &Refusal{Code: CodeRelaunchSourceStale, AgentUID: target.Metadata.UID, message: "process relaunch requires an exact reciprocal Agent/Pane binding", cause: processhost.ErrStale}
	}
	if pane != nil {
		current = "process"
	}
	destination := q.Host
	if destination == "" {
		destination = current
	}
	bad := func(text string) error {
		return &Refusal{Code: CodeRelaunchRequestInvalid, AgentUID: target.Metadata.UID, message: text}
	}
	if destination != "process" && destination != "tmux" {
		return out, bad("RelaunchAgentRequest.Host must be tmux or process")
	}
	if destination == "process" && !q.Detach {
		return out, bad("RelaunchAgentRequest process destination requires Detach")
	}
	if destination == "tmux" && q.Detach {
		return out, bad("agent relaunch: --detach requires a process destination; nothing was changed")
	}
	if current == "process" && destination == "process" && q.SocketPath != "" {
		return out, bad("agent relaunch: process agents do not use a tmux socket; nothing was changed")
	}
	if destination == "tmux" && q.Prompt != "" {
		return out, bad("agent relaunch: a first prompt applies only to the process target; nothing was changed")
	}
	if !filepath.IsAbs(c.projmuxExecutable) {
		return out, &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
	}
	info, e := os.Stat(c.projmuxExecutable)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return out, &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: e}
	}
	if current == "process" {
		if pane.Status.ProcessSession == nil || pane.Metadata.OwnerUID() != target.Metadata.UID || target.Status.PaneRef != pane.Metadata.UID {
			return out, &Refusal{Code: CodeRelaunchSourceStale, message: "process relaunch binding is not reciprocal", cause: processhost.ErrStale}
		}
		var observed core.ProcessBinding
		if err := unregisterCopy(pane.Status.ProcessSession.Binding, &observed); err != nil {
			return out, err
		}
		if q.Binding != (core.ProcessBinding{}) && q.Binding != observed {
			return out, &Refusal{Code: CodeRelaunchSourceStale, AgentUID: target.Metadata.UID, message: "process relaunch binding changed", cause: processhost.ErrStale}
		}
	} else if q.Binding != (core.ProcessBinding{}) {
		return out, bad("RelaunchAgentRequest.Binding requires a process source")
	}
	args := []string{"uid:" + target.Metadata.UID, "-o", "json"}
	add := func(k, v string) { args = append(args, k, v) }
	if q.Host != "" {
		add("--host", q.Host)
	}
	if q.Detach {
		args = append(args, "--detach")
	}
	if q.Model != "" {
		add("--model", q.Model)
	}
	if q.Effort != "" {
		add("--effort", q.Effort)
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"--profile", q.Profile}, {"--instructions", q.Instructions}} {
		if field.value != nil {
			v := *field.value
			if v == "" {
				v = "none"
			}
			add(field.key, v)
		}
	}
	if len(q.Reset) > 0 {
		add("--reset", strings.Join(q.Reset, ","))
	}
	if q.Confirm {
		args = append(args, "--yes")
	}
	if q.DryRun {
		args = append(args, "--dry-run")
	}
	if q.SocketPath != "" {
		add("--socket-path", q.SocketPath)
	}
	if q.Prompt != "" {
		args = append(args, "--", q.Prompt)
	}
	wire := runtime.ProcessDetachedRelaunchRequest{Args: args, Source: target.Clone()}
	if p, ok := reg.Pane(target.Status.PaneRef); ok {
		clone := p.Clone()
		wire.Pane = &clone
	}
	readPlan := func(reply runtime.ProcessDetachedReply) error {
		if err := json.Unmarshal(reply.Relaunch, &out); err != nil {
			return err
		}
		if out.AgentUID != target.Metadata.UID {
			return processhost.ErrStale
		}
		return ctx.Err()
	}
	reply, err := runtime.ExecuteDetachedProcessRelaunch(ctx, c.processCreateContext, wire, readPlan, destination == "tmux")
	if len(reply.Relaunch) != 0 {
		if e := json.Unmarshal(reply.Relaunch, &out); e != nil {
			err = errors.Join(err, e)
		}
	}
	if reply.Binding.Generation != "" {
		_ = unregisterCopy(m.ProcessBinding{HostInstanceID: reply.Binding.Host, ProjectUID: reply.Binding.Project, WindowUID: reply.Binding.Window, AgentUID: reply.Binding.Agent, PaneUID: reply.Binding.Pane, Generation: reply.Binding.Generation, OperationID: reply.Binding.Operation}, &out.Binding)
	}
	out.ClaimWaiting = len(reply.Claim) != 0
	if out.Outcome == "would-restart" || out.Outcome == "would-resume" {
		out.DryRun = q.DryRun
	}
	if err != nil {
		if !q.DryRun && strings.HasPrefix(out.Outcome, "would-") {
			out.Outcome = "relaunch-failed"
		}
		var failure *runtime.ProcessRelaunchFailure
		if errors.As(err, &failure) {
			detail := &RelaunchProcessError{Code: Code(failure.Code), Binding: out.Binding, cause: err}
			_ = unregisterCopy(failure.Owner, &detail.Owner)
			return out, &Refusal{Code: detail.Code, AgentUID: target.Metadata.UID, message: err.Error(), cause: detail}
		}
		return out, relaunchRefusal(err)
	}
	return out, nil
}
