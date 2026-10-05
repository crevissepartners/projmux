package codexbroker

import (
	"context"
	"encoding/json"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// NativeTransferTarget names the one fresh native activation authorized to
// bootstrap while all ordinary admissions remain frozen.
type NativeTransferTarget struct {
	Project, Window, Agent, Pane, Generation, Operation, RuntimeID, Thread string
}

func (t NativeTransferTarget) plannedValid() bool {
	return t.Project != "" && t.Window != "" && t.Agent != "" && t.Pane != "" && t.Generation != "" && t.Operation != "" && t.Thread != ""
}
func (t NativeTransferTarget) valid() bool { return t.plannedValid() && t.RuntimeID != "" }

type NativeTransferGrant struct {
	Receipt TransferReceipt
	Target  NativeTransferTarget
}

func (b *Broker) grantNativeTransfer(receipt TransferReceipt, owner string, target NativeTransferTarget) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, err := b.transferLocked(receipt, owner)
	if err != nil {
		return err
	}
	if !target.plannedValid() || target.RuntimeID != "" || target.Thread != receipt.Source.Thread || target.Agent != receipt.Source.Agent || target.Project != receipt.Source.Project || target.Window != receipt.Source.Window || target.Pane == receipt.Source.Pane || target.Generation == receipt.Source.Generation || target.Operation == receipt.Source.Operation {
		return refuse(RefusalLeaseIdentityMismatch, nil)
	}
	if t.busy || t.draining || !t.receipt.Retired || t.orphaned || t.nativeTarget != nil {
		return refuse(RefusalControlNotOpen, nil)
	}
	if err := b.transferCurrentLocked(t); err != nil {
		return err
	}
	// The old token is permanently revoked before the target is admitted.
	b.revokeBindingLocked(t.binding, RefusalBindingClosed)
	t.nativeTarget = &target
	return nil
}
func (t *RemoteTransfer) GrantNative(ctx context.Context, target NativeTransferTarget) (*NativeTransferGrant, error) {
	receipt := t.receipt
	receipt.NativeTarget = &target
	_, err := t.conn.transferCall(ctx, "native-grant", receipt)
	if err != nil {
		return nil, err
	}
	return &NativeTransferGrant{Receipt: t.receipt, Target: target}, nil
}
func (b *Broker) nativeBindingAllowedLocked(bd *Binding) bool {
	t := b.transfers[bd.threadID]
	return t != nil && !t.orphaned && t.nativeBinding == bd && t.conn == b.conn && t.nativeTarget != nil
}

// ResumeNative consumes the planned target grant on the captured connection.
// No ordinary Resume adapter, binding or external endpoint is substituted.
func (t *RemoteTransfer) ResumeNative(ctx context.Context, cwd string, roots []string, settings codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error) {
	params, err := json.Marshal(nativeResumeRequest{Action: "native-resume", Receipt: t.receipt, CWD: cwd, Roots: roots, Settings: settings})
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	reply, err := t.conn.call(ctx, wireRequest{Kind: requestTransfer, Thread: t.receipt.Source.Thread, Params: params})
	if err != nil {
		return codexappserver.ThreadBinding{}, err
	}
	if reply.Kind == replyRefused {
		return codexappserver.ThreadBinding{}, refuse(reply.Refusal, nil)
	}
	var binding codexappserver.ThreadBinding
	err = json.Unmarshal(reply.Result, &binding)
	return binding, err
}

type nativeResumeRequest struct {
	Action   string
	Receipt  TransferReceipt
	CWD      string
	Roots    []string
	Settings codexappserver.ThreadSettings
}

func (t *RemoteTransfer) ActivateNative(ctx context.Context, target NativeTransferTarget) (*NativeTransferGrant, error) {
	receipt := t.receipt
	receipt.NativeTarget = &target
	_, err := t.conn.transferCall(ctx, "native-activate", receipt)
	if err != nil {
		return nil, err
	}
	return &NativeTransferGrant{Receipt: t.receipt, Target: target}, nil
}
func (b *Broker) activateNativeTransfer(receipt TransferReceipt, owner string, target NativeTransferTarget) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, err := b.transferLocked(receipt, owner)
	if err != nil {
		return err
	}
	if t.nativeTarget == nil || !t.nativeReady || t.busy || t.orphaned || !target.valid() {
		return refuse(RefusalControlNotOpen, nil)
	}
	planned := target
	planned.RuntimeID = ""
	if *t.nativeTarget != planned {
		return refuse(RefusalLeaseIdentityMismatch, nil)
	}
	if err = b.transferCurrentLocked(t); err != nil {
		return err
	}
	t.nativeTarget = &target
	return nil
}
func (b *Broker) resumeNativeTransfer(ctx context.Context, input nativeResumeRequest, owner string) (codexappserver.ThreadBinding, error) {
	b.mu.Lock()
	t, err := b.transferLocked(input.Receipt, owner)
	if err == nil {
		err = b.transferCurrentLocked(t)
	}
	if err == nil && (t.nativeTarget == nil || t.nativeTarget.RuntimeID != "" || t.nativeInit || t.busy || t.draining || !t.receipt.Retired) {
		err = refuse(RefusalControlNotOpen, nil)
	}
	if err != nil {
		b.mu.Unlock()
		return codexappserver.ThreadBinding{}, err
	}
	endpoint, ok := t.conn.endpoint.(interface {
		ResumeThreadWithSettings(context.Context, string, string, []string, codexappserver.ThreadSettings) (codexappserver.ThreadBinding, error)
	})
	if !ok {
		b.mu.Unlock()
		return codexappserver.ThreadBinding{}, refuse(RefusalLifecycleUnsupported, nil)
	}
	t.nativeInit = true
	t.busy = true
	b.mu.Unlock()
	defer func() { b.mu.Lock(); t.busy = false; b.mu.Unlock() }()
	binding, err := endpoint.ResumeThreadWithSettings(ctx, input.Receipt.Source.Thread, input.CWD, input.Roots, input.Settings)
	b.mu.Lock()
	defer b.mu.Unlock()
	if guard := b.transferCurrentLocked(t); guard != nil {
		return codexappserver.ThreadBinding{}, guard
	}
	if err == nil && binding.ThreadID != input.Receipt.Source.Thread {
		return codexappserver.ThreadBinding{}, refuse(RefusalEndpointRefused, nil)
	}
	if err == nil {
		t.nativeReady = true
	}
	return binding, err
}
