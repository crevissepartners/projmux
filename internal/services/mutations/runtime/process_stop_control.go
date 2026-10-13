package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

var ErrProcessStopUnsupported = errors.New("process-host-unsupported-action")

// Only the Stop fields of the existing private foreground protocol are needed.
// The host still owns admission and writes actual Wait evidence.
type processStopRequest struct {
	Authority                       processhost.Authority
	Action, Operation, Prompt, Turn string
}
type processStopEnvelope struct{ Foreground *processStopRequest }
type processStopResponse struct {
	Accepted, Stale, Busy, Closed, Unsupported bool
	DeleteRefusal                              string
}

func CallProcessAgentStop(ctx context.Context, registryPath string, reg coremetadata.Registry, agent coremetadata.Agent) error {
	return callProcessAgentStopAction(ctx, registryPath, reg, agent, "stop", "")
}
func callProcessAgentStopAction(ctx context.Context, registryPath string, reg coremetadata.Registry, agent coremetadata.Agent, action, prompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pane, found := reg.Pane(agent.Status.PaneRef)
	if !found || pane.Status.ProcessSession == nil {
		return processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	activation, provider, ok := reg.CurrentProcessActivation(session.Binding)
	if !ok || provider != agent.Spec.Provider {
		return processhost.ErrStale
	}
	name := "/host.sock"
	if provider == "codex" {
		name = "/codex-host.sock"
	}
	socket := claudeadapter.ActivationLeaseDir(registryPath, pane.Metadata.UID, session.Binding.Generation) + name
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return fmt.Errorf("process-host-unavailable: %w", err)
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	b := session.Binding
	binding := processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
	sessionID := session.SessionID
	if provider == "codex" {
		sessionID = session.ThreadID
	}
	request := processStopEnvelope{Foreground: &processStopRequest{Authority: processhost.Authority{Binding: binding, Connection: b.OperationID, Session: sessionID}, Action: action, Prompt: prompt, Operation: "op-" + hex.EncodeToString(nonce[:]), Turn: session.TurnID}}
	var result processStopResponse
	err = localipc.ExchangeOwnedJSON(ctx, socket, identity, activation.HostProcess, request, &result)
	if errors.Is(err, localipc.ErrSocketReplaced) {
		err = processhost.ErrStale
	}
	if err != nil {
		return fmt.Errorf("process-host-unavailable: %w", err)
	}
	switch {
	case result.DeleteRefusal != "":
		return &mutations.Failure{Code: mutations.CodeAgentProtected, Cause: errors.New(result.DeleteRefusal)}
	case result.Accepted:
		return nil
	case result.Busy:
		return processhost.ErrBusy
	case result.Closed:
		return processhost.ErrClosed
	case result.Unsupported:
		return fmt.Errorf("%w: the agent's process owner does not implement this action; relaunch the agent to run a current owner", ErrProcessStopUnsupported)
	default:
		return processhost.ErrStale
	}
}
