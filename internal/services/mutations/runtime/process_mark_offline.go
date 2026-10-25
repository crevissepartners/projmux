package runtime

import (
	"context"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Stable refusal tokens of agent mark-offline. Each refusal writes nothing.
const (
	MarkOfflineProcessUnsupported      = "agent-mark-offline-process-unsupported"
	MarkOfflineOwnerPresent            = "agent-mark-offline-owner-present"
	MarkOfflineNotHostLost             = "agent-mark-offline-not-host-lost"
	MarkOfflineConversationUnavailable = "agent-mark-offline-conversation-unavailable"
	MarkOfflineStale                   = "agent-mark-offline-stale"
)

// MarkOfflineRefusal carries one stable token. Its text starts with the token.
type MarkOfflineRefusal struct {
	Code   string
	Detail string
}

func (e *MarkOfflineRefusal) Error() string { return e.Code + ": " + e.Detail }

func markOfflineRefusal(code, format string, args ...any) error {
	return &MarkOfflineRefusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ProcessAgentMarkOffline is the shared CLI/SDK executor for one process Agent
// whose recorded host and provider child are both gone. It never starts or
// signals a provider and never writes Wait evidence. Paths and readers are
// explicit; Update must be a convergent Registry transaction.
type ProcessAgentMarkOffline struct {
	Update       func(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
	Mutator      coremetadata.Mutator
	ReadIdentity ProcessIdentityReader

	// Results, also set on refusal when known.
	PaneUID     string
	Binding     coremetadata.ProcessBinding
	Termination *coremetadata.TerminationEvidence
	Changed     bool
	Latest      coremetadata.Registry
}

func (m *ProcessAgentMarkOffline) identityReader() ProcessIdentityReader {
	if m.ReadIdentity != nil {
		return m.ReadIdentity
	}
	return localipc.Process
}

// MarkExact proves absence on the caller's snapshot, then again inside the
// Registry transaction together with an exact activation CAS. An Agent that
// already carries this exact lost-host retirement is an unchanged success.
func (m *ProcessAgentMarkOffline) MarkExact(ctx context.Context, reg coremetadata.Registry, agentUID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.Changed = false
	activation, done, err := m.admit(reg, agentUID)
	if err != nil {
		return err
	}
	if done {
		m.Latest = reg.Clone()
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	latest, changed, err := m.Update(func(working *coremetadata.Registry) error {
		current, done, err := m.admit(*working, agentUID)
		if err != nil || done {
			return err
		}
		if current != activation {
			return markOfflineRefusal(MarkOfflineStale, "agent uid:%s changed generation since it was read", agentUID)
		}
		return m.Mutator.MarkProcessHostLostOffline(working, current)
	})
	if err != nil {
		return err
	}
	m.Changed, m.Latest = changed, latest
	if pane, ok := latest.Pane(m.PaneUID); ok {
		m.Termination = pane.Status.LastTermination.Clone()
	}
	return nil
}

// admit returns the exact dead activation to retire, or done for an Agent that
// already holds this retirement. It reads the kernel but writes nothing.
func (m *ProcessAgentMarkOffline) admit(reg coremetadata.Registry, agentUID string) (coremetadata.ProcessActivation, bool, error) {
	agent, found := reg.Agent(agentUID)
	pane, ambiguous := ProcessAgentPane(reg, agentUID)
	if !found || ambiguous || pane == nil || pane.Status.ProcessSession == nil {
		return coremetadata.ProcessActivation{}, false, markOfflineRefusal(MarkOfflineProcessUnsupported, "agent mark-offline requires one exact process Agent; tmux Agents are unsupported")
	}
	if m.PaneUID != "" && m.PaneUID != pane.Metadata.UID {
		return coremetadata.ProcessActivation{}, false, markOfflineRefusal(MarkOfflineStale, "agent uid:%s changed Pane since it was read", agentUID)
	}
	m.PaneUID = pane.Metadata.UID
	m.Binding = pane.Status.ProcessSession.Binding
	m.Termination = pane.Status.LastTermination.Clone()
	if reg.ProcessHostLostOffline(pane.Metadata.UID) {
		return coremetadata.ProcessActivation{}, true, nil
	}
	process := pane.Status.Activation.Process
	if process == nil || agent.Status.PaneRef != pane.Metadata.UID {
		return coremetadata.ProcessActivation{}, false, markOfflineRefusal(MarkOfflineNotHostLost, "agent uid:%s phase %s has no recorded process host to retire; use agent stop or agent resume", agentUID, agent.Status.Phase)
	}
	record := pane.Status.ProcessSession
	if record.Binding != process.Binding || record.ConnectionID != process.Binding.OperationID || !processConversationRecorded(record) {
		return coremetadata.ProcessActivation{}, false, markOfflineRefusal(MarkOfflineConversationUnavailable, "agent uid:%s has no resumable conversation for its current generation", agentUID)
	}
	if !ProcessActivationAbsent(*process, m.identityReader()) {
		return coremetadata.ProcessActivation{}, false, markOfflineRefusal(MarkOfflineOwnerPresent, "agent uid:%s process host or provider is alive, or its absence cannot be proven; use agent stop", agentUID)
	}
	return *process, false, nil
}

func processConversationRecorded(record *coremetadata.ProcessSessionRecord) bool {
	return (record.Provider == "claude" && record.SessionID != "") || (record.Provider == "codex" && record.ThreadID != "")
}

// MarkOfflineReceipt records that the Agent and Pane were preserved. The
// runtime effect is unchanged: the host was already gone and nothing stopped.
func MarkOfflineReceipt(reg coremetadata.Registry, agentUID, paneUID string, changed bool) receipt.OperationReceipt {
	agent, _ := reg.Agent(agentUID)
	pane, _ := reg.Pane(paneUID)
	if agent == nil || pane == nil {
		return receipt.OperationReceipt{}
	}
	action := receipt.ActionUnchanged
	if changed {
		action = receipt.ActionPreserved
	}
	r := receipt.NewReceipt(receipt.OperationMarkOfflineAgent, receipt.ReceiptTarget{Kind: string(coremetadata.KindAgent), UID: agentUID, Name: agent.Metadata.Name}, receipt.ReceiptEffects{Identity: receipt.IdentityUnchanged, Address: receipt.AddressUnchanged, Topology: receipt.TopologyUnchanged, DesiredState: receipt.DesiredStateUnchanged, Runtime: receipt.RuntimeUnchanged, Focus: receipt.FocusUnchanged})
	r.Add(string(coremetadata.KindAgent), agentUID, agent.Metadata.Name, action)
	r.Add(string(coremetadata.KindPane), paneUID, pane.Metadata.Name, action)
	r.SelectWindows(agent.Metadata.OwnerUID())
	return r
}
