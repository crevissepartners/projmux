package app

import (
	"context"
	"errors"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// A short-lived CLI validates the same exact host, kernel birth and provider
// authority as the owner. This never discovers or wakes an offline provider.
func resolveLiveProcessCodexRoute(ctx context.Context, path string, reg coremetadata.Registry, uid string) (coremetadata.AgentRouteRef, error) {
	agent, found := reg.Agent(uid)
	if !found {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	pane, hasPane := reg.Pane(agent.Status.PaneRef)
	if !found || !hasPane || pane.Status.ProcessSession == nil {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	activation, provider, current := reg.CurrentProcessActivation(session.Binding)
	if !current || provider != aiModeCodex {
		return coremetadata.AgentRouteRef{}, processhost.ErrStale
	}
	b := session.Binding
	evidence := coremetadata.CodexProcessRouteEvidence{HostInstance: b.HostInstanceID, PaneUID: b.PaneUID, Generation: b.Generation, ThreadID: session.ThreadID, Connection: b.OperationID, Process: activation.Child, HostProcess: activation.HostProcess}
	route, reason := coremetadata.ResolveProcessCodexRoute(reg, uid, evidence, func(e coremetadata.CodexProcessRouteEvidence) bool {
		result, err := callLiveProcessCodex(ctx, path, e, processForegroundRequest{Action: "validate", Authority: processhost.Authority{Binding: processSchemaBinding(b), Session: e.ThreadID, Connection: e.Connection}})
		return err == nil && result.Accepted
	})
	if reason != "" {
		return coremetadata.AgentRouteRef{}, errors.New(reason)
	}
	return route, nil
}

func callLiveProcessCodex(ctx context.Context, path string, evidence coremetadata.CodexProcessRouteEvidence, request processForegroundRequest) (processForegroundResult, error) {
	socket := claudeActivationLeaseDir(path, evidence.PaneUID, evidence.Generation) + "/codex-host.sock"
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return processForegroundResult{}, err
	}
	return callProcessForeground(ctx, socket, identity, evidence.HostProcess, codexProcessExchange{Foreground: &request})
}

func (c *agentCommand) pushProcessCodexCoordination(record messagestore.Record, route coremetadata.AgentRouteRef) (messagestore.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	defer cancel()
	reg, err := c.messagePaths.loadRegistry()
	if err != nil {
		return record, err
	}
	pane, found := reg.Pane(route.PaneUID)
	evidence, exact := route.Authority().(coremetadata.CodexProcessRouteEvidence)
	if !found || !exact || pane.Status.ProcessSession == nil {
		return c.terminalCoordination(record, coremessage.EventStale, "stale-binding", false, processhost.ErrStale)
	}
	request := processForegroundRequest{Action: "message", MessageRef: record.Envelope.MessageRef, Authority: processhost.Authority{Binding: processSchemaBinding(pane.Status.ProcessSession.Binding), Session: evidence.ThreadID, Connection: evidence.Connection}}
	result, err := callLiveProcessCodex(ctx, c.messagePaths.registryPath, evidence, request)
	if err != nil {
		return c.terminalCoordination(record, coremessage.EventFail, "delivery-outcome-unknown", true, err)
	}
	if !result.Accepted || result.Receipt == nil {
		if result.Stale {
			return c.terminalCoordination(record, coremessage.EventStale, "stale-binding", false, processhost.ErrStale)
		}
		if result.Busy {
			return c.terminalCoordination(record, coremessage.EventRefuse, "host-busy", false, processhost.ErrBusy)
		}
		return c.terminalCoordination(record, coremessage.EventFail, "delivery-outcome-unknown", true, errors.New("process message receipt unavailable"))
	}
	updated, found, err := c.messageStore.Get(record.Envelope.MessageRef)
	if err != nil || !found {
		if err == nil {
			err = messagestore.ErrNotFound
		}
		return record, err
	}
	return updated, nil
}
