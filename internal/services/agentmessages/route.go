package agentmessages

import (
	"context"
	"encoding/json"
	"errors"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func resolveLiveRoute(ctx context.Context, path string, reg metadata.Registry, a metadata.Agent, target bool) (metadata.AgentRouteRef, error) {
	if err := ctx.Err(); err != nil {
		return metadata.AgentRouteRef{}, err
	}
	pane, found := reg.Pane(a.Status.PaneRef)
	var route metadata.AgentRouteRef
	var reason string
	if found && pane.Metadata.OwnerUID() == a.Metadata.UID && pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
		if a.Spec.Provider == "codex" {
			return resolveProcessCodex(ctx, path, reg, a)
		}
		if a.Spec.Provider == "claude" {
			route, reason = resolveProcessClaude(ctx, path, reg, a)
		} else {
			route, reason = metadata.ResolveAgentRoute(reg, a.Metadata.UID)
		}
	} else {
		route, reason = metadata.ResolveAgentRoute(reg, a.Metadata.UID)
	}
	if reason != "" {
		return metadata.AgentRouteRef{}, errors.New(reason)
	}
	if route.Authority().Provider() == "claude" {
		if err := probeError(claude.ClassifyRegistrationLease(path, route), "registration lease probe"); err != nil {
			return metadata.AgentRouteRef{}, err
		}
		if target {
			if err := probeError(claude.ClassifyCoordinationEligibility(path, route), "coordination eligibility probe"); err != nil {
				return metadata.AgentRouteRef{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return metadata.AgentRouteRef{}, err
	}
	return route, nil
}

func schemaBinding(b metadata.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}

type peerForeground struct {
	Authority          processhost.Authority
	Action, MessageRef string
}
type peerForegroundEnvelope struct{ Foreground *peerForeground }
type peerForegroundResult struct {
	Accepted, Stale, Busy, Closed, Unsupported bool
	Receipt                                    *json.RawMessage
}

func exchangeCodex(ctx context.Context, path string, e metadata.CodexProcessRouteEvidence, req peerForeground) (peerForegroundResult, error) {
	socket := claude.ActivationLeaseDir(path, e.PaneUID, e.Generation) + "/codex-host.sock"
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return peerForegroundResult{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	var result peerForegroundResult
	err = localipc.ExchangeOwnedJSON(bounded, socket, identity, e.HostProcess, peerForegroundEnvelope{&req}, &result)
	return result, err
}
func resolveProcessCodex(ctx context.Context, path string, reg metadata.Registry, a metadata.Agent) (metadata.AgentRouteRef, error) {
	pane, found := reg.Pane(a.Status.PaneRef)
	if !found || pane.Status.ProcessSession == nil {
		return metadata.AgentRouteRef{}, processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	b := session.Binding
	activation, provider, current := reg.CurrentProcessActivation(b)
	if !current || provider != "codex" {
		return metadata.AgentRouteRef{}, processhost.ErrStale
	}
	evidence := metadata.CodexProcessRouteEvidence{HostInstance: b.HostInstanceID, PaneUID: b.PaneUID, Generation: b.Generation, ThreadID: session.ThreadID, Connection: b.OperationID, Process: activation.Child, HostProcess: activation.HostProcess}
	route, reason := metadata.ResolveProcessCodexRoute(reg, a.Metadata.UID, evidence, func(e metadata.CodexProcessRouteEvidence) bool {
		result, err := exchangeCodex(ctx, path, e, peerForeground{Action: "validate", Authority: processhost.Authority{Binding: schemaBinding(b), Session: e.ThreadID, Connection: e.Connection}})
		return err == nil && result.Accepted
	})
	if reason != "" {
		return route, errors.New(reason)
	}
	return route, nil
}

func (s Service) pushProcessCodex(ctx context.Context, r store.Record, route metadata.AgentRouteRef) (store.Record, error) {
	reg, err := s.LoadRegistry()
	if err != nil {
		return r, err
	}
	pane, found := reg.Pane(route.PaneUID)
	evidence, exact := route.Authority().(metadata.CodexProcessRouteEvidence)
	if !found || !exact || pane.Status.ProcessSession == nil {
		return s.terminal(r, message.EventStale, "stale-binding", false, processhost.ErrStale)
	}
	request := peerForeground{Action: "peer-message", MessageRef: r.Envelope.MessageRef, Authority: processhost.Authority{Binding: schemaBinding(pane.Status.ProcessSession.Binding), Session: evidence.ThreadID, Connection: evidence.Connection}}
	result, err := exchangeCodex(ctx, s.RegistryPath, evidence, request)
	if err != nil {
		return s.terminal(r, message.EventFail, "delivery-outcome-unknown", true, err)
	}
	if !result.Accepted || result.Receipt == nil {
		if result.Unsupported {
			return s.terminal(r, message.EventRefuse, "peer-injection-unsupported", false, errors.New("process-host-unsupported-action"))
		}
		if result.Stale {
			return s.terminal(r, message.EventStale, "stale-binding", false, processhost.ErrStale)
		}
		if result.Busy {
			return s.terminal(r, message.EventRefuse, "host-busy", false, processhost.ErrBusy)
		}
		return s.terminal(r, message.EventFail, "delivery-outcome-unknown", true, errors.New("process message receipt unavailable"))
	}
	updated, err := s.acceptedReceipt(r.Envelope, r.Envelope.MessageRef, "process-peer-receipt-unavailable")
	if err != nil {
		return r, err
	}
	return updated, nil
}

type claudeLookupRequest struct {
	Binding processhost.Binding
	Session string
	Lookup  bool
}
type claudeLookupResult struct {
	Process      metadata.ProcessIdentity
	Valid        bool
	Binding      processhost.Binding
	Registration *metadata.ClaudeRegistration
}

func resolveProcessClaude(ctx context.Context, path string, reg metadata.Registry, a metadata.Agent) (metadata.AgentRouteRef, string) {
	refused := func() (metadata.AgentRouteRef, string) {
		return metadata.AgentRouteRef{}, "process Claude authority is unavailable"
	}
	pane, ok := reg.Pane(a.Status.PaneRef)
	if !ok || pane.Status.Activation.Process == nil || pane.Status.ProcessSession == nil {
		return refused()
	}
	binding := pane.Status.Activation.Process.Binding
	current, provider, valid := reg.CurrentProcessActivation(binding)
	if !valid || provider != "claude" {
		return refused()
	}
	process, supervisor, err := localipc.Process(current.Child.PID)
	if err != nil || process != current.Child {
		return refused()
	}
	_, hostPID, err := localipc.Process(supervisor)
	if err != nil {
		return refused()
	}
	host, _, err := localipc.Process(hostPID)
	if err != nil || host != current.HostProcess {
		return refused()
	}
	socket := claude.ActivationLeaseDir(path, pane.Metadata.UID, binding.Generation) + "/host.sock"
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return refused()
	}
	lookup := func() (*metadata.ClaudeRegistration, error) {
		bounded, cancel := context.WithTimeout(ctx, localipc.Deadline)
		defer cancel()
		var result claudeLookupResult
		err := localipc.ExchangeOwnedJSON(bounded, socket, identity, host, claudeLookupRequest{Binding: schemaBinding(binding), Session: pane.Status.ProcessSession.SessionID, Lookup: true}, &result)
		if err != nil {
			return nil, err
		}
		if !result.Valid || result.Process != process || result.Binding != schemaBinding(binding) || result.Registration == nil || !result.Registration.Ready {
			return nil, processhost.ErrStale
		}
		return result.Registration, nil
	}
	// Match the CLI proof and resolver's two independent registration reads.
	if _, err := lookup(); err != nil {
		return refused()
	}
	registration, err := lookup()
	if err != nil {
		return refused()
	}
	evidence := metadata.ClaudeProcessRouteEvidence{HostInstance: binding.HostInstanceID, PaneUID: binding.PaneUID, Generation: binding.Generation, SessionID: pane.Status.ProcessSession.SessionID, Process: process, HostProcess: host, Registration: *registration}
	return metadata.ResolveProcessClaudeRoute(reg, a.Metadata.UID, evidence, func(e metadata.ClaudeProcessRouteEvidence) bool {
		live, err := lookup()
		return err == nil && *live == e.Registration
	})
}
