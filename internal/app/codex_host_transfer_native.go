package app

import (
	"context"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"reflect"
	"time"
)

// Only a host-transfer recovery plan supplies these callbacks. Ordinary resume
// keeps its existing planning, transaction, native init and observer behavior.
type codexNativeReservedInit struct {
	Pane       coremetadata.Pane
	Activation superviseSpec
	Expected   coremetadata.Agent
	Binding    codexappserver.ThreadBinding
	Receipt    codexbroker.TransferReceipt
}
type codexNativeRebindTransfer struct {
	initialize func(context.Context, *agentRebinder, agentResumePlan, coremetadata.AgentWorkspace, agentSettingsLaunch, projectLinksLaunch, agentGuidanceLaunch, agentResumeLaunch, codexappserver.ThreadSettings) (*codexNativeReservedInit, error)
	check      func(context.Context) error
	grant      func(context.Context, coremetadata.Registry, coremetadata.Pane, superviseSpec, string) (*codexbroker.NativeTransferGrant, error)
	complete   func(codexLifecycleObserverTarget) error
}

func (c *agentCommand) nativeTransferRebind(transfer *codexbroker.RemoteTransfer, record *codexHostTransferRecord, path string) *codexNativeRebindTransfer {
	return &codexNativeRebindTransfer{
		check: transfer.Check,
		initialize: func(ctx context.Context, r *agentRebinder, plan agentResumePlan, workspace coremetadata.AgentWorkspace, settings agentSettingsLaunch, links projectLinksLaunch, guidance agentGuidanceLaunch, resumed agentResumeLaunch, nativeSettings codexappserver.ThreadSettings) (*codexNativeReservedInit, error) {
			operation, err := newCreateOperationID()
			if err != nil {
				return nil, err
			}
			reserved := &codexNativeReservedInit{Receipt: transfer.Receipt()}
			_, err = c.store.update(func(working *coremetadata.Registry) error {
				agent, ok := working.Agent(plan.agentUID)
				if !ok || record.Retired == nil || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" || !sameHostTransferSpec(agent.Spec, record.Retired.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.Retired.Metadata.Annotations) || !reflect.DeepEqual(agent.Status.SessionRef, record.Retired.Status.SessionRef) {
					return processhost.ErrStale
				}
				name, _ := r.handOffResumedPaneName(ctx, working, c.store.mutator(), *agent, plan)
				agent, _ = working.Agent(plan.agentUID)
				agent.Spec.Workspace = workspace
				mut := c.store.mutator()
				if err := settings.record(working, mut, plan.agentUID); err != nil {
					return err
				}
				if err := recordResumedProfileDigest(working, mut, plan.agentUID, resumed); err != nil {
					return err
				}
				if err := links.record(working, mut, plan.agentUID); err != nil {
					return err
				}
				if err := guidance.record(working, mut, plan.agentUID); err != nil {
					return err
				}
				pane, err := mut.ReserveNativeAgentPane(working, plan.agentUID, coremetadata.BootstrapPane{CWD: workspace.CWD, Name: name}, operation)
				if err != nil && name != "" && (errors.Is(err, coremetadata.ErrNameConflict) || errors.Is(err, coremetadata.ErrInvalidName)) {
					pane, err = mut.ReserveNativeAgentPane(working, plan.agentUID, coremetadata.BootstrapPane{CWD: workspace.CWD}, operation)
				}
				if err != nil {
					return err
				}
				activation, err := r.create.issuePaneActivation(working, mut, pane.Metadata.UID, plan.agentUID, operation)
				if err != nil {
					return err
				}
				observed, _ := working.Pane(pane.Metadata.UID)
				agent, _ = working.Agent(plan.agentUID)
				reserved.Pane, reserved.Activation, reserved.Expected = observed.Clone(), activation, agent.Clone()
				tuple := codexbroker.NativeTransferTarget{Project: plan.projectUID, Window: plan.windowUID, Agent: plan.agentUID, Pane: pane.Metadata.UID, Generation: activation.Generation, Operation: operation, Thread: plan.conversationID}
				record.NativeTarget = &tuple
				expected := agent.Clone()
				record.NativeExpected = &expected
				record.Phase = "native-planned"
				return writeCodexHostTransfer(path, record)
			})
			if err != nil {
				return nil, err
			}
			// Both provider admissions run after the durable Registry reservation has
			// committed and released its mutation lock.
			if _, err = transfer.GrantNative(ctx, *record.NativeTarget); err != nil {
				return nil, err
			}
			if reserved.Binding, err = transfer.ResumeNative(ctx, workspace.CWD, workspace.AdditionalWritableRoots, nativeSettings); err != nil {
				return nil, err
			}
			if reserved.Binding.ThreadID != plan.conversationID {
				return nil, processhost.ErrStale
			}
			if err = transfer.Check(ctx); err != nil {
				return nil, err
			}
			record.NativeBinding = &reserved.Binding
			record.Phase = "native-initialized"
			if err = writeCodexHostTransfer(path, record); err != nil {
				return nil, err
			}
			return reserved, nil
		},
		grant: func(ctx context.Context, reg coremetadata.Registry, pane coremetadata.Pane, activation superviseSpec, runtime string) (*codexbroker.NativeTransferGrant, error) {
			if record.NativeTarget == nil || record.NativeTarget.Pane != pane.Metadata.UID || record.NativeTarget.Generation != activation.Generation || record.NativeTarget.Operation != activation.OperationID {
				return nil, processhost.ErrStale
			}
			record.NativeTarget.RuntimeID = runtime
			record.Phase = "native-starting"
			if err := writeCodexHostTransfer(path, record); err != nil {
				return nil, err
			}
			return transfer.ActivateNative(ctx, *record.NativeTarget)
		},
		complete: func(target codexLifecycleObserverTarget) error {
			reg, err := c.loadRegistry()
			if err != nil {
				return err
			}
			agent, ok := reg.Agent(record.Source.Metadata.UID)
			pane, present := reg.Pane(target.Identity.PaneUID)
			if !ok || !present || record.NativeTarget == nil || record.NativeExpected == nil || agent.Status.PaneRef != record.NativeTarget.Pane || agent.Status.Phase != coremetadata.PhaseRunning || !sameHostTransferSpec(agent.Spec, record.NativeExpected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NativeExpected.Metadata.Annotations) || !agent.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) || pane.Status.Activation.Generation != record.NativeTarget.Generation || pane.Status.Activation.OperationID != record.NativeTarget.Operation || pane.Status.Activation.RuntimeID != record.NativeTarget.RuntimeID || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != record.NativeTarget.Thread || pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() {
				return processhost.ErrStale
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = transfer.Finish(ctx); err != nil {
				return err
			}
			return completeCodexHostTransfer(path)
		},
	}
}

// A failed/vanished native consumer must actually disappear before a reclaim
// can requalify retirement. Its latest full recipe and activation are checked
// before Stop; an unrelated replacement is never overwritten.
func (c *agentCommand) retireFailedNativeTransfer(ctx context.Context, record *codexHostTransferRecord, path string, request agentRelaunchRequest) error {
	if record.NativeTarget == nil {
		return nil
	}
	tuple := record.NativeTarget
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	agent, ok := reg.Agent(tuple.Agent)
	pane, present := reg.Pane(tuple.Pane)
	if !ok || record.NativeExpected == nil || !sameHostTransferSpec(agent.Spec, record.NativeExpected.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, record.NativeExpected.Metadata.Annotations) || !agent.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
		return processhost.ErrStale
	}
	if present && agent.Status.PaneRef == tuple.Pane {
		a := pane.Status.Activation
		if a.Generation != tuple.Generation || a.OperationID != tuple.Operation || (a.RuntimeID != "" && a.RuntimeID != tuple.RuntimeID) {
			return processhost.ErrStale
		}
		if absent := c.verifyTmuxCodexPaneRetired(tuple.Pane, request); absent != nil {
			if a.RuntimeID == "" || a.RuntimeID != tuple.RuntimeID {
				return errors.New("native target runtime remains unknown; transfer stays fenced")
			}
			if err = c.stopTmuxCodexTransfer(ctx, agent.Clone(), pane.Clone(), request); err != nil {
				return err
			}
		}
	} else if agent.Status.PaneRef != "" {
		return processhost.ErrStale
	}
	if err = c.verifyTmuxCodexPaneRetired(tuple.Pane, request); err != nil {
		return errors.New("native target retirement unknown; transfer remains fenced")
	}
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	current, ok := latest.Agent(tuple.Agent)
	if !ok || (current.Status.Phase != coremetadata.PhaseOffline && current.Status.Phase != coremetadata.PhasePending) || (current.Status.PaneRef != "" && current.Status.PaneRef != tuple.Pane) || !sameHostTransferSpec(current.Spec, record.NativeExpected.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, record.NativeExpected.Metadata.Annotations) || !current.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
		return processhost.ErrStale
	}
	if observed, ok := latest.Pane(tuple.Pane); ok {
		record.NativeTerminated = append(record.NativeTerminated, observed.Clone())
		if err = writeCodexHostTransfer(path, record); err != nil {
			return err
		}
	}
	_, _, err = c.store.updateConvergent(func(working *coremetadata.Registry) error {
		now, ok := working.Agent(tuple.Agent)
		if !ok || (now.Status.Phase != coremetadata.PhaseOffline && now.Status.Phase != coremetadata.PhasePending) || now.Status.PaneRef != current.Status.PaneRef || !sameHostTransferSpec(now.Spec, current.Spec) || !reflect.DeepEqual(now.Metadata.Annotations, current.Metadata.Annotations) || !now.Status.SessionRef.SameConversation(current.Status.SessionRef) {
			return processhost.ErrStale
		}
		if observed, present := working.Pane(tuple.Pane); present {
			prior, _ := latest.Pane(tuple.Pane)
			if prior == nil || !reflect.DeepEqual(observed.Status.Activation, prior.Status.Activation) {
				return processhost.ErrStale
			}
			if err := c.store.mutator().DeletePane(working, tuple.Pane); err != nil {
				return err
			}
			now, _ = working.Agent(tuple.Agent)
		}
		restored := record.Retired.Clone()
		now.Spec, now.Metadata.Annotations, now.Status = restored.Spec, restored.Metadata.Annotations, restored.Status
		return nil
	})
	if err != nil {
		return err
	}
	record.NativeTarget = nil
	record.NativeExpected = nil
	record.NativeBinding = nil
	record.Phase = "restored"
	return writeCodexHostTransfer(path, record)
}
