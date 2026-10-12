package runtime

import (
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
)

type CreateReceiptResource struct {
	Kind      metadata.Kind
	UID, Name string
	Action    receipt.ReceiptAction
}

// CreateReceipt is the unchanged child-create receipt shared by CLI and SDK.
func CreateReceipt(kind metadata.Kind, rows []CreateReceiptResource, selected []string) receipt.OperationReceipt {
	operations := map[metadata.Kind]receipt.Operation{metadata.KindProject: receipt.OperationCreateProject, metadata.KindWindow: receipt.OperationCreateWindow, metadata.KindPane: receipt.OperationCreatePane, metadata.KindAgent: receipt.OperationCreateAgent}
	target := receipt.ReceiptTarget{Kind: string(kind)}
	if len(rows) > 0 {
		target.UID = rows[0].UID
		target.Name = rows[0].Name
	}
	runtime := receipt.RuntimeMaterialized
	if kind == metadata.KindProject {
		runtime = receipt.RuntimeUnchanged
	}
	out := receipt.NewReceipt(operations[kind], target, receipt.ReceiptEffects{Identity: receipt.IdentityCreated, Address: receipt.AddressAllocated, Topology: receipt.TopologyEstablished, DesiredState: receipt.DesiredStateCreated, Runtime: runtime, Focus: receipt.FocusUnchanged})
	for _, row := range rows {
		out.Add(string(row.Kind), row.UID, row.Name, row.Action)
	}
	out.SelectWindows(selected...)
	return out
}
