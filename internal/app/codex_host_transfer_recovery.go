package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Recovery consumes the same owner-private reservation receipt; it never
// recreates a broker, adopts a process, or revives old control tokens.
func (c *agentCommand) recoverCodexHostTransfer(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, path string, record *codexHostTransferRecord, stdout, stderr io.Writer) error {
	recovery := codexTransferRecoveryCommand(target.Metadata.UID, request)
	if request.dryRun {
		return usageError("agent relaunch: pending Codex transfer recovery; nothing was changed; recover with: " + recovery)
	}
	if request.host != "tmux" || !request.yes || request.model != "" || request.effort != "" || request.profile != nil || request.instructions != nil || len(request.reset) != 0 || len(request.prompt) != 0 {
		return usageError("agent relaunch: recover the previous exact recipe first; recover with: " + recovery)
	}
	unlock, err := lockDeferredClaim(path)
	if err != nil {
		return err
	}
	defer unlock()
	currentRecord, err := readCodexHostTransfer(path)
	if err != nil {
		return err
	}
	if currentRecord == nil || !reflect.DeepEqual(currentRecord, record) {
		return processhost.ErrStale
	}
	if record.Phase == "preparing" {
		if err := c.cleanupNoEffectCodexTransfer(context.Background(), path, record); err == nil {
			return writeAgentRelaunchResult(stdout, request, agentRelaunchResult{Action: "relaunch", AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: aiModeCodex, Outcome: personaOutcomeResumed, CurrentHost: "tmux", TargetHost: "tmux", NewPaneUID: record.Pane.Metadata.UID})
		}
	}
	if record.NativeTarget != nil {
		status, inspectErr := c.inspectCodexHostTransfer(context.Background(), record)
		if inspectErr != nil {
			return inspectErr
		}
		if status.Completed {
			if err := verifyCompletedNativeTransfer(reg, record); err != nil {
				return err
			}
			if err := completeCodexHostTransfer(path); err != nil {
				return err
			}
			return writeAgentRelaunchResult(stdout, request, agentRelaunchResult{Action: "relaunch", AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: aiModeCodex, Outcome: personaOutcomeResumed, CurrentHost: "tmux", TargetHost: "tmux", NewPaneUID: record.NativeTarget.Pane})
		}
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer recoveryCancel()
		if err := c.retireFailedNativeTransfer(recoveryCtx, record, path, request); err != nil {
			return err
		}
		reg, err = c.loadRegistry()
		if err != nil {
			return err
		}
		current, ok := reg.Agent(target.Metadata.UID)
		if !ok {
			return processhost.ErrStale
		}
		target = current.Clone()
	}
	if record.Source.Metadata.UID != target.Metadata.UID || !target.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
		return processhost.ErrStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.verifyTmuxCodexPaneRetired(record.Pane.Metadata.UID, request); err != nil {
		// A surviving source may only receive pre-unsubscribe abort, never a new
		// writer. The exact original activation and recipe must still be present.
		old, present := reg.Pane(record.Pane.Metadata.UID)
		if !present || !reflect.DeepEqual(old.Status.Activation, record.Pane.Status.Activation) || !reflect.DeepEqual(target.Status.SessionRef, record.Source.Status.SessionRef) || !reflect.DeepEqual(target.Spec, record.Source.Spec) || !reflect.DeepEqual(target.Metadata.Annotations, record.Source.Metadata.Annotations) || (record.Phase != "prepared" && record.Phase != "preparing") {
			return fmt.Errorf("agent relaunch: source retirement remains unknown: %w", err)
		}
		transfer, err := c.reclaimCodexHostTransfer(ctx, record)
		if err != nil {
			return err
		}
		defer transfer.Close()
		if err = transfer.Abort(ctx); err != nil {
			return err
		}
		return removeCodexHostTransfer(path)
	}
	if record.Retired == nil {
		if target.Status.Phase != coremetadata.PhaseOffline || target.Status.PaneRef != "" || !reflect.DeepEqual(target.Spec, record.Source.Spec) || !reflect.DeepEqual(target.Metadata.Annotations, record.Source.Metadata.Annotations) {
			return processhost.ErrStale
		}
		observed := target.Clone()
		record.Retired = &observed
		if err := writeCodexHostTransfer(path, record); err != nil {
			return err
		}
	}
	// The source owner may have died after reservation and before journal update.
	// A target is discoverable only by this exact planned host/gen/op/owner tuple.
	if record.Target.Operation != "" && record.Target.Pane == "" {
		if pane, ambiguous := processResumePane(reg, target.Metadata.UID); pane != nil && !ambiguous && pane.Status.ProcessSession != nil {
			binding := pane.Status.ProcessSession.Binding
			expected := record.Target
			expected.Pane = pane.Metadata.UID
			if binding != metadataProcessBinding(expected) {
				return processhost.ErrStale
			}
			record.Target = expected
		} else if target.Status.PaneRef != "" {
			return processhost.ErrStale
		}
	}
	if record.Target.Pane != "" && record.Phase != "restored" {
		pane, present := reg.Pane(record.Target.Pane)
		if !present || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(record.Target) || target.Status.PaneRef != record.Target.Pane || record.Expected == nil || !sameHostTransferSpec(target.Spec, record.Expected.Spec) || !reflect.DeepEqual(target.Metadata.Annotations, record.Expected.Metadata.Annotations) {
			return processhost.ErrStale
		}
		if !coremetadata.MatchesProcessWait(metadataProcessBinding(record.Target), pane.Status.LastTermination) {
			if (record.Phase != "reserved" && record.Phase != "reserving") || !pane.Status.Activation.IsZero() {
				if target.Status.Phase != coremetadata.PhaseRunning {
					return errors.New("agent relaunch: target actual Wait is unknown; transfer remains fenced")
				}
				if _, err := c.callProcessTurn(reg, target, aiModeCodex, "stop", ""); err != nil {
					return err
				}
				ticker := time.NewTicker(processRelaunchRetirementPoll)
				defer ticker.Stop()
				for {
					latest, err := c.loadRegistry()
					if err != nil {
						return err
					}
					current, ok := latest.Agent(target.Metadata.UID)
					observed, present := latest.Pane(record.Target.Pane)
					if !ok || !present || observed.Status.ProcessSession == nil || observed.Status.ProcessSession.Binding != metadataProcessBinding(record.Target) || current.Status.PaneRef != record.Target.Pane || !sameHostTransferSpec(current.Spec, record.Expected.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, record.Expected.Metadata.Annotations) || !current.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
						return processhost.ErrStale
					}
					if coremetadata.MatchesProcessWait(metadataProcessBinding(record.Target), observed.Status.LastTermination) && coremetadata.SameProcessWait(observed.Status.LastTermination, current.Status.LastTermination) {
						break
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ticker.C:
					}
				}
			}
		} else if !coremetadata.SameProcessWait(pane.Status.LastTermination, target.Status.LastTermination) {
			return processhost.ErrStale
		}
		observed, readErr := c.loadRegistry()
		if readErr != nil {
			return readErr
		}
		if targetPane, ok := observed.Pane(record.Target.Pane); ok {
			copy := targetPane.Clone()
			record.TerminatedTarget = &copy
			if err := writeCodexHostTransfer(path, record); err != nil {
				return err
			}
		}
		_, err := c.store.update(func(working *coremetadata.Registry) error {
			current, ok := working.Agent(record.Target.Agent)
			if !ok || !reflect.DeepEqual(current.Status.SessionRef, record.Expected.Status.SessionRef) {
				return processhost.ErrStale
			}
			return restoreTmuxTransferReservation(working, c.store.mutator(), record.Target, *record.Expected, *record.Retired)
		})
		if err != nil {
			return err
		}
		record.Phase = "restored"
		if err = writeCodexHostTransfer(path, record); err != nil {
			return err
		}
	}
	latest, err := c.loadRegistry()
	if err != nil {
		return err
	}
	restored, ok := latest.Agent(target.Metadata.UID)
	if !ok || restored.Status.Phase != coremetadata.PhaseOffline || restored.Status.PaneRef != "" || !reflect.DeepEqual(restored.Spec, record.Retired.Spec) || !reflect.DeepEqual(restored.Metadata.Annotations, record.Retired.Metadata.Annotations) || !restored.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
		return processhost.ErrStale
	}
	transfer, err := c.reclaimCodexHostTransfer(ctx, record)
	if errors.Is(err, errCodexTransferCompleted) {
		plan, planErr := c.prepareResume(agentRelaunchSpelling, latest, restored)
		if planErr != nil {
			return planErr
		}
		if err = completeCodexHostTransfer(path); err != nil {
			return err
		}
		return c.rebind.rebind(agentRelaunchSpelling, plan, stdout, stderr)
	}
	if err != nil {
		return err
	}
	defer transfer.Close()
	if err = transfer.Retire(ctx); err != nil {
		return err
	}
	// The reservation remains pinned across fresh native init and observer ready.
	plan, err := c.prepareResume(agentRelaunchSpelling, latest, restored)
	if err != nil {
		return err
	}
	plan.codexTransfer = c.nativeTransferRebind(transfer, record, path)
	return c.rebind.rebind(agentRelaunchSpelling, plan, stdout, stderr)
}

func (c *agentCommand) reclaimCodexHostTransfer(ctx context.Context, record *codexHostTransferRecord) (*codexbroker.RemoteTransfer, error) {
	domain, err := codexBrokerStateDomain(c.lookupEnv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(domain, record.Receipt.Source.Endpoint)
	if err != nil {
		return nil, err
	}
	conn, err := codexbroker.DialTransfer(ctx, discovery, codexbroker.DialConfig{})
	if err != nil {
		return nil, err
	}
	status, err := conn.InspectTransfer(ctx, record.Receipt)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if status.Completed {
		_ = conn.Close()
		return nil, errCodexTransferCompleted
	}
	transfer, err := conn.ReclaimTransfer(ctx, status)
	if err != nil {
		_ = conn.Close()
	}
	return transfer, err
}

var errCodexTransferCompleted = errors.New("exact Codex transfer already completed")

func (c *agentCommand) inspectCodexHostTransfer(ctx context.Context, record *codexHostTransferRecord) (codexbroker.TransferReceipt, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	domain, err := codexBrokerStateDomain(c.lookupEnv, os.UserHomeDir)
	if err != nil {
		return codexbroker.TransferReceipt{}, err
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(domain, record.Receipt.Source.Endpoint)
	if err != nil {
		return codexbroker.TransferReceipt{}, err
	}
	conn, err := codexbroker.DialTransfer(bounded, discovery, codexbroker.DialConfig{})
	if err != nil {
		return codexbroker.TransferReceipt{}, err
	}
	defer conn.Close()
	return conn.InspectTransfer(bounded, record.Receipt)
}
func verifyCompletedNativeTransfer(reg coremetadata.Registry, record *codexHostTransferRecord) error {
	tuple := record.NativeTarget
	if tuple == nil || record.NativeExpected == nil {
		return processhost.ErrStale
	}
	current, ok := reg.Agent(tuple.Agent)
	pane, present := reg.Pane(tuple.Pane)
	if !ok || !present || current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != tuple.Pane || !sameHostTransferSpec(current.Spec, record.NativeExpected.Spec) || !reflect.DeepEqual(current.Metadata.Annotations, record.NativeExpected.Metadata.Annotations) || !current.Status.SessionRef.SameConversation(record.Source.Status.SessionRef) {
		return processhost.ErrStale
	}
	a := pane.Status.Activation
	if a.Generation != tuple.Generation || a.OperationID != tuple.Operation || a.RuntimeID != tuple.RuntimeID || a.Codex == nil || a.Codex.ThreadID != tuple.Thread || a.Codex.Authority == nil || !a.Codex.Authority.Valid() || a.Codex.Authority.BrokerRuntimeID != record.Receipt.Source.RuntimeID {
		return processhost.ErrStale
	}
	return nil
}

// No-effect terminal cleanup consumes live broker proof and an unchanged source
// under the Registry transaction. It launches nothing and restores no authority.
func (c *agentCommand) cleanupNoEffectCodexTransfer(ctx context.Context, path string, record *codexHostTransferRecord) error {
	if record.Phase != "preparing" || record.Retired != nil || record.Target != (processhost.Binding{}) || record.NativeTarget != nil {
		return processhost.ErrStale
	}
	before, err := readCodexHostTransferBytes(path)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	domain, err := codexBrokerStateDomain(c.lookupEnv, os.UserHomeDir)
	if err != nil {
		return err
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(domain, record.Receipt.Source.Endpoint)
	if err != nil {
		return err
	}
	conn, err := codexbroker.DialTransfer(bounded, discovery, codexbroker.DialConfig{})
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.InspectPrepareNoEffect(bounded, record.Receipt); err != nil {
		return err
	}
	_, err = c.store.update(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(record.Source.Metadata.UID)
		pane, present := working.Pane(record.Pane.Metadata.UID)
		if !ok || !present || !reflect.DeepEqual(*current, record.Source) || !reflect.DeepEqual(*pane, record.Pane) {
			return processhost.ErrStale
		}
		now, readErr := readCodexHostTransferBytes(path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(before, now) {
			return processhost.ErrStale
		}
		currentRecord, readErr := readCodexHostTransfer(path)
		if readErr != nil {
			return readErr
		}
		if !reflect.DeepEqual(currentRecord, record) {
			return processhost.ErrStale
		}
		if err := removeCodexHostTransfer(path); err != nil {
			return err
		}
		return errCodexNoEffectCleaned
	})
	if errors.Is(err, errCodexNoEffectCleaned) {
		return nil
	}
	return err
}

var errCodexNoEffectCleaned = errors.New("codex preparing journal cleared without registry mutation")
