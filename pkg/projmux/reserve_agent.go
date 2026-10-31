package projmux

import (
	"context"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodeReserveProviderUnsupported refuses a reservation that is not Claude.
const CodeReserveProviderUnsupported Code = "reserve-provider-unsupported"

// ReserveAgent reserves one Claude process Agent in an existing or a new Window
// and starts nothing: the Agent is Pending with its launch frozen. StartReservedAgent
// starts it; until then it can only be deleted.
//
// A failure removes what the call created, including a new Window, and reports
// what it could not remove (protection, the cleanup bound, a lost final reply):
// the result carries the Binding, the Refusal the AgentUID, and errors.As reaches
// a CreateProcessError. A reservation is final only on nil.
func (c *Client) ReserveAgent(ctx context.Context, req core.ReserveAgentRequest) (core.ReserveAgentResult, error) {
	var result core.ReserveAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if (req.Window == "") == (req.NewWindow == nil) {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "ReserveAgentRequest requires exactly one of Window and NewWindow"}
	}
	if req.Provider != "" && req.Provider != "claude" {
		return result, &Refusal{Code: CodeReserveProviderUnsupported, message: "ReserveAgentRequest supports only a Claude process Agent"}
	}
	scope, labels, _, err := c.createInputs(ctx, "ReserveAgentRequest", req.Project, req.Name, req.Labels, req.SocketPath)
	if err != nil {
		return result, err
	}
	var window selector.Ref
	var newWindow *metadata.BootstrapWindow
	if req.NewWindow != nil {
		_, windowLabels, _, err := c.createInputs(ctx, "ReserveAgentRequest.NewWindow", req.Project, req.NewWindow.Name, req.NewWindow.Labels, req.SocketPath)
		if err != nil {
			return result, err
		}
		newWindow = &metadata.BootstrapWindow{Name: req.NewWindow.Name, Labels: windowLabels}
	} else if window, err = selector.ParseRef(metadata.KindWindow, req.Window); err != nil {
		return result, resourceRefusal(selectorInputError(err, "ReserveAgentRequest.Window"))
	}
	creator := ""
	if req.Creator != "" {
		var ok bool
		creator, ok = strings.CutPrefix(req.Creator, "uid:")
		if !ok || creator == "" || strings.TrimSpace(creator) != creator || strings.ContainsAny(creator, ":/ \t\r\n") {
			return result, &Refusal{Code: CodeCreateCreatorInvalid, message: "ReserveAgentRequest.Creator must be an exact Agent reference uid:<agent>"}
		}
	}
	options := mutationruntime.ProcessAgentCreateOptions{Project: scope.Project, Window: window, NewWindow: newWindow, Provider: req.Provider, Name: req.Name, CWD: req.Cwd, Model: req.Model, Effort: req.Effort, Instructions: req.Instructions, Profile: req.Profile, Creator: creator, AddDirs: req.AddDirs, Labels: labels}
	reply, err := mutationruntime.ExecuteDetachedProcessReserve(ctx, c.processCreateContext, options)
	if reply.Binding.Agent != "" {
		_ = unregisterCopy(metadata.ProcessBinding{HostInstanceID: reply.Binding.Host, ProjectUID: reply.Binding.Project, WindowUID: reply.Binding.Window, AgentUID: reply.Binding.Agent, PaneUID: reply.Binding.Pane, Generation: reply.Binding.Generation, OperationID: reply.Binding.Operation}, &result.Binding)
	}
	if err != nil {
		if reply.Result != nil {
			_ = unregisterCopy(reply.Result.Agent, &result.Agent)
		}
		var failure *mutationruntime.ProcessCreateFailure
		if errors.As(err, &failure) {
			var owner core.ProcessIdentity
			if copyErr := unregisterCopy(failure.Owner, &owner); copyErr != nil {
				return result, copyErr
			}
			detail := &CreateProcessError{Code: Code(failure.Code), Binding: result.Binding, Owner: owner, message: err.Error(), cause: err}
			return result, &Refusal{Code: Code(failure.Code), AgentUID: failure.Binding.Agent, message: err.Error(), cause: detail}
		}
		return result, createRefusal(err, "ReserveAgentRequest")
	}
	if reply.Result == nil {
		return core.ReserveAgentResult{}, &Refusal{Code: CodeCreateExecutionFailed, message: "reservation worker returned no committed Agent"}
	}
	var agent core.Agent
	if err = unregisterCopy(reply.Result.Agent, &agent); err != nil {
		return core.ReserveAgentResult{}, err
	}
	result.Agent = agent
	result.Projection = core.ResourceList{APIVersion: metadata.APIVersion, Kind: "AgentList", Items: []core.Resource{agent}}
	result.Notices = reply.Result.Notices
	if err = unregisterCopy(reply.Result.Receipt, &result.Receipt); err != nil {
		return core.ReserveAgentResult{}, err
	}
	return result, nil
}
