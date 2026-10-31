package projmux

import (
	"context"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Refusal codes of StartReservedAgent. Each is returned before a provider
// process exists and leaves the reservation intact.
const (
	// CodeReservationMissing means the Agent has no reservation to start.
	CodeReservationMissing Code = "reservation-missing"
	// CodeReservationStale means the frozen launch no longer matches the Agent.
	CodeReservationStale Code = "reservation-stale"
	// CodeReservationStarting means another worker owns the start.
	CodeReservationStarting Code = "reservation-starting"
)

// StartReservedAgent starts the provider process of an Agent that ReserveAgent
// left Pending, from the launch frozen then. It starts the same process a
// CreateAgent would have, with the same Binding the reservation returned.
//
// A refusal or a failure before the provider exists keeps the reservation, so
// the same call can be repeated. A failure after the provider started rolls
// the Agent back like a failed CreateAgent, including a Window the
// reservation created; what rollback could not remove is named in the result's
// Binding, the Refusal's AgentUID and a CreateProcessError. A start is final
// only on nil.
func (c *Client) StartReservedAgent(ctx context.Context, req core.StartReservedAgentRequest) (core.StartReservedAgentResult, error) {
	var result core.StartReservedAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	uid, ok := strings.CutPrefix(req.Agent, "uid:")
	if !ok || uid == "" || strings.TrimSpace(uid) != uid || strings.ContainsAny(uid, ":/ \t\r\n") {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "StartReservedAgentRequest.Agent must be an exact Agent reference uid:<agent>"}
	}
	reply, err := mutationruntime.ExecuteDetachedProcessStartReserved(ctx, c.processCreateContext, uid)
	if reply.Binding.Agent != "" {
		_ = unregisterCopy(metadata.ProcessBinding{HostInstanceID: reply.Binding.Host, ProjectUID: reply.Binding.Project, WindowUID: reply.Binding.Window, AgentUID: reply.Binding.Agent, PaneUID: reply.Binding.Pane, Generation: reply.Binding.Generation, OperationID: reply.Binding.Operation}, &result.Binding)
	}
	if err != nil {
		var failure *mutationruntime.ProcessCreateFailure
		if errors.As(err, &failure) {
			var owner core.ProcessIdentity
			if copyErr := unregisterCopy(failure.Owner, &owner); copyErr != nil {
				return result, copyErr
			}
			detail := &CreateProcessError{Code: Code(failure.Code), Binding: result.Binding, Owner: owner, message: err.Error(), cause: err}
			return result, &Refusal{Code: Code(failure.Code), AgentUID: failure.Binding.Agent, message: err.Error(), cause: detail}
		}
		return result, createRefusal(err, "StartReservedAgentRequest")
	}
	if reply.Result == nil {
		return core.StartReservedAgentResult{}, &Refusal{Code: CodeCreateExecutionFailed, message: "start worker returned no committed Agent"}
	}
	var agent core.Agent
	if err = unregisterCopy(reply.Result.Agent, &agent); err != nil {
		return core.StartReservedAgentResult{}, err
	}
	result.Agent = agent
	result.Projection = core.ResourceList{APIVersion: metadata.APIVersion, Kind: "AgentList", Items: []core.Resource{agent}}
	result.Notices = reply.Result.Notices
	if err = unregisterCopy(reply.Result.Receipt, &result.Receipt); err != nil {
		return core.StartReservedAgentResult{}, err
	}
	return result, nil
}
