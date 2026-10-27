package projmux

import (
	"context"
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"strings"
)

const (
	CodeStopAgentProcessUnsupported Code = "stop-agent-process-unsupported"
	CodeStopAgentStale              Code = "stop-agent-stale"
	CodeStopAgentBusy               Code = "stop-agent-busy"
	CodeStopAgentWaitUnconfirmed    Code = "agent-stop-wait-unconfirmed"
	CodeStopAgentClaimUnconfirmed   Code = "agent-stop-claim-release-unconfirmed"
	CodeStopAgentExecutionFailed    Code = "stop-agent-execution-failed"
)

// StopAgent shares the CLI exact owner retirement executor. It uses only frozen
// explicit Client paths, never a CLI subprocess. Cancellation prevents admission;
// after admission, bounded retirement completes independently of caller cancellation.
func (c *Client) StopAgent(ctx context.Context, req core.StopAgentRequest) (core.StopAgentResult, error) {
	var result core.StopAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "StopAgentRequest.Ref must name one Agent"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "StopAgentRequest")
	if err != nil {
		return result, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return result, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return result, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbReview, Kind: metadata.KindAgent}, query, resolution); err != nil {
		return result, resourceRefusal(err)
	}
	agent, found := reg.Agent(resolution.Matches[0].UID)
	if !found {
		return result, stopAgentRefusal(processhost.ErrStale, result)
	}
	result.AgentUID = agent.Metadata.UID
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, agent.Metadata.UID)
	if ambiguous {
		return result, &Refusal{Code: CodeSelectorAmbiguous, AgentUID: result.AgentUID, message: "agent stop requires one exact process Agent; tmux Agents are unsupported"}
	}
	if pane == nil || pane.Status.ProcessSession == nil {
		return result, &Refusal{Code: CodeStopAgentProcessUnsupported, AgentUID: result.AgentUID, message: "agent stop requires one exact process Agent; tmux Agents are unsupported"}
	}
	result.PaneUID = pane.Metadata.UID
	if err = unregisterCopy(pane.Status.ProcessSession.Binding, &result.Binding); err != nil {
		return result, err
	}
	if req.Binding != (core.ProcessBinding{}) && req.Binding != result.Binding {
		return result, stopAgentRefusal(processhost.ErrStale, result)
	}
	registryPath := store.PathFor(c.paths.StateDir)
	storage := store.NewStore(registryPath)
	stopper := mutationruntime.ProcessAgentStopper{RegistryPath: registryPath, Load: storage.LoadReadOnly}
	err = stopper.StopExplicit(ctx, reg, agent.Clone(), pane.Clone())
	if copyErr := unregisterCopy(stopper.Termination, &result.Termination); copyErr != nil {
		return result, errors.Join(err, copyErr)
	}

	result.Stopped = err == nil
	result.ClaimReleased = stopper.ClaimReleased
	return result, stopAgentRefusal(err, result)
}

func stopAgentRefusal(err error, result core.StopAgentResult) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	code := CodeStopAgentExecutionFailed
	var failure *mutationruntime.ProcessStopFailure
	switch {
	case errors.As(err, &failure):
		code = Code(failure.Code)
	case errors.Is(err, processhost.ErrStale):
		code = CodeStopAgentStale
	case errors.Is(err, processhost.ErrBusy):
		code = CodeStopAgentBusy
	}
	return &Refusal{Code: code, AgentUID: result.AgentUID, message: err.Error(), cause: err}
}
