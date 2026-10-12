package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
)

// ResumeReceipt describes the unchanged Agent identity and its new Pane. The
// CLI tmux route has no receipt output mode; both adapters use this same model.
func ResumeReceipt(reg coremetadata.Registry, agentUID, paneUID string) receipt.OperationReceipt {
	agent, _ := reg.Agent(agentUID)
	pane, _ := reg.Pane(paneUID)
	if agent == nil || pane == nil {
		return receipt.OperationReceipt{}
	}
	r := receipt.NewReceipt(receipt.OperationResumeAgent, receipt.ReceiptTarget{Kind: string(coremetadata.KindAgent), UID: agentUID, Name: agent.Metadata.Name}, receipt.ReceiptEffects{Identity: receipt.IdentityReused, Address: receipt.AddressUnchanged, Topology: receipt.TopologyEstablished, DesiredState: receipt.DesiredStateUnchanged, Runtime: receipt.RuntimeMaterialized, Focus: receipt.FocusUnchanged})
	r.Add(string(coremetadata.KindAgent), agentUID, agent.Metadata.Name, receipt.ActionReused)
	r.Add(string(coremetadata.KindPane), paneUID, pane.Metadata.Name, receipt.ActionCreated)
	r.SelectWindows(agent.Metadata.OwnerUID())
	return r
}
