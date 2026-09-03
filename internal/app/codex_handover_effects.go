package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	codexHandoverOperationPane  = "@projmux_codex_handover_operation"
	codexHandoverGenerationPane = "@projmux_codex_handover_generation"
)

// codexHandoverEffects is the production boundary for Phase 5. Every method
// starts from the journal's exact identity tuple and re-observes Registry/tmux
// authority before touching a process, endpoint ref, or Pane child.
type codexHandoverEffects struct {
	registry    codexHandoverRegistry
	mutator     coremetadata.Mutator
	runner      tmuxCommandRunner
	materialize *materializer
	launcher    *aiCommand
	open        codexHandoverOpen
}

type codexHandoverRegistry interface {
	LoadSnapshot() (coremetadata.Registry, error)
	UpdateConvergent(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
}

type codexHandoverClient interface {
	ResumeThread(context.Context, string, string, []string) (codexappserver.ThreadBinding, error)
	ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error)
	Close() error
}

type codexHandoverOpen func(context.Context, codexNativeEndpointRoute, bool) (codexHandoverClient, error)

func (effects *codexHandoverEffects) openRoute(ctx context.Context, route codexNativeEndpointRoute, experimental bool) (codexHandoverClient, error) {
	if effects != nil && effects.open != nil {
		return effects.open(ctx, route, experimental)
	}
	return openCodexNativeRoute(ctx, route, experimental)
}

func (effects *codexHandoverEffects) load() (coremetadata.Registry, error) {
	if effects == nil || effects.registry == nil {
		return coremetadata.Registry{}, errors.New("handover Registry is not configured")
	}
	return effects.registry.LoadSnapshot()
}

func (effects *codexHandoverEffects) routedRunner() (tmuxCommandRunner, error) {
	if effects == nil || effects.runner == nil {
		return nil, errors.New("handover tmux route is not configured")
	}
	return effects.runner, nil
}

func (effects *codexHandoverEffects) EnsureSameGenerationRecovered(ctx context.Context, _ string, route codexupgrade.GenerationRoute) (codexgenerationhost.LaunchProof, error) {
	if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate || route.Proof == nil || !route.Ready || route.LaunchOperationRef == "" {
		return codexgenerationhost.LaunchProof{}, errors.New("same-generation recovery has no exact durable launch authority")
	}
	if err := codexupgrade.ObserveRoute(ctx, route); err == nil {
		return *route.Proof, nil
	}
	var recovered codexgenerationhost.LaunchProof
	err := codexgenerationhost.PrepareDurableGeneration(ctx, route.Config.HostConfig(), route.LaunchOperationRef, nil, nil,
		func(proof codexgenerationhost.LaunchProof) error {
			recovered = proof
			return nil
		})
	if err != nil {
		return codexgenerationhost.LaunchProof{}, err
	}
	return recovered, nil
}

func (effects *codexHandoverEffects) EnsureNoTurnChoice(ctx context.Context, operationRef string, choice codexgeneration.NoTurnChoice, old, successor coremetadata.CodexEndpointRef) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	agent, exists := registry.Agent(choice.AgentUID)
	if !exists {
		// The journal prewrite is the authority for this idempotent absence.
		return nil
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
		!agent.Status.SessionRef.Codex.Endpoint.Same(old) || agent.Status.SessionRef.Codex.HasStartedTurn {
		return errors.New("no-turn choice target drifted")
	}
	if agent.Status.PaneRef != choice.PaneUID {
		return errors.New("no-turn choice exact Pane changed")
	}
	if choice.Decision == codexgeneration.NoTurnReplacement {
		replacement, ok := registry.Agent(choice.ReplacementAgentUID)
		if !ok || replacement.Status.SessionRef == nil || replacement.Status.SessionRef.Codex == nil ||
			replacement.Status.SessionRef.Codex.Endpoint == nil || !replacement.Status.SessionRef.Codex.Endpoint.Same(successor) ||
			replacement.Status.SessionRef.Codex.HasStartedTurn {
			return errors.New("explicit no-turn replacement identity drifted")
		}
	}
	pane, ok := registry.Pane(choice.PaneUID)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != choice.AgentUID ||
		pane.Status.Activation.AgentUID != choice.AgentUID || pane.Status.Activation.RuntimeID != choice.PaneRuntimeID ||
		pane.Status.Activation.Generation != choice.PaneGeneration {
		return errors.New("no-turn Agent has no exact Pane identity")
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	if err := effects.runHandoverMutation(ctx, mutationKillPane, choice.PaneRuntimeID, choice.PaneUID,
		[]string{"-t", choice.PaneRuntimeID}, nil, "explicit no-turn close", func(ctx context.Context) error {
			return guardHandoverPane(ctx, runner, choice.PaneRuntimeID, choice.PaneUID, choice.AgentUID)
		}); err != nil {
		return fmt.Errorf("close exact no-turn Pane: %w", err)
	}
	_, _, err = effects.registry.UpdateConvergent(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(choice.AgentUID)
		if !ok {
			return nil
		}
		if current.Status.SessionRef == nil || current.Status.SessionRef.Codex == nil || current.Status.SessionRef.Codex.Endpoint == nil ||
			!current.Status.SessionRef.Codex.Endpoint.Same(old) || current.Status.SessionRef.Codex.HasStartedTurn {
			return errors.New("no-turn choice compare-and-delete drifted")
		}
		return effects.mutator.DeleteAgent(working, choice.AgentUID)
	})
	if err != nil {
		return fmt.Errorf("close no-turn Agent identity: %w", err)
	}
	_ = operationRef // operationRef is retained by the handover journal choice receipt.
	return nil
}

func (effects *codexHandoverEffects) EnsureAdmissionFence(_ context.Context, operationRef string, old coremetadata.CodexEndpointRef) error {
	_, _, err := effects.registry.UpdateConvergent(func(registry *coremetadata.Registry) error {
		for i := range registry.Agents {
			agent := registry.Agents[i]
			if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
				!agent.Status.SessionRef.Codex.Endpoint.Same(old) {
				continue
			}
			lifecycle := coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationHandoverPending,
				Operation: &coremetadata.CodexGenerationOperationRef{ID: operationRef, Endpoint: old}}
			if _, _, err := effects.mutator.SetCodexGenerationLifecycle(registry, agent.Metadata.UID, old, lifecycle); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (effects *codexHandoverEffects) EnsureBindingFence(ctx context.Context, _ string, old coremetadata.CodexEndpointRef, targets []codexgeneration.HandoverTarget) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := guardRegistryHandoverTarget(registry, target, old, false); err != nil {
			return err
		}
		if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
			return err
		}
		if err := effects.runHandoverMutation(ctx, mutationCodexHandoverFence, target.PaneRuntimeID, target.PaneUID,
			nil, nil, "journaled old binding fence", func(ctx context.Context) error {
				return guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID)
			}); err != nil {
			return fmt.Errorf("fence exact Codex Pane binding: %w", err)
		}
	}
	return nil
}

func (effects *codexHandoverEffects) EnsureTargetAbsent(ctx context.Context, _ string, old coremetadata.CodexEndpointRef, route codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	if err := guardRegistryHandoverTarget(registry, target, old, false); err != nil {
		return err
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
		return err
	}
	client, err := effects.openRoute(ctx, rollingNativeRoute(route), true)
	if err != nil {
		return err
	}
	defer client.Close()
	snapshot, err := client.ReadLifecycleSnapshot(ctx, target.ThreadID)
	if err != nil {
		return err
	}
	if snapshot.ThreadID != target.ThreadID || snapshot.ThreadState != codexappserver.ThreadStateNotLoaded {
		return errors.New("target thread was already loaded on successor before old stop")
	}
	return nil
}

func (effects *codexHandoverEffects) EnsureOldStopped(ctx context.Context, operationRef string, route, successor codexupgrade.GenerationRoute, targets []codexgeneration.HandoverTarget) error {
	if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate || route.Proof == nil || route.LaunchOperationRef == "" {
		return errors.New("old generation has no exact managed launch authority")
	}
	registry, err := effects.load()
	if err != nil {
		return err
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := guardRegistryHandoverTarget(registry, target, route.Generation.Endpoint, false); err != nil {
			return err
		}
		if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
			return err
		}
		client, openErr := effects.openRoute(ctx, rollingNativeRoute(successor), true)
		if openErr != nil {
			return openErr
		}
		snapshot, snapshotErr := client.ReadLifecycleSnapshot(ctx, target.ThreadID)
		closeErr := client.Close()
		if snapshotErr != nil || closeErr != nil {
			return errors.Join(snapshotErr, closeErr)
		}
		if snapshot.ThreadID != target.ThreadID || snapshot.ThreadState != codexappserver.ThreadStateNotLoaded {
			return errors.New("successor target loaded before exact old stop")
		}
	}
	// The launch ref belongs to the Phase 4 generation creation operation; the
	// handover ref is independently checked by the coordinator/journal link.
	_ = operationRef
	return codexgenerationhost.StopDurableGeneration(ctx, route.Config.HostConfig(), route.LaunchOperationRef, *route.Proof)
}

func (effects *codexHandoverEffects) EnsureTargetResumed(ctx context.Context, operationRef string, old coremetadata.CodexEndpointRef, route codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	agent, ok := registry.Agent(target.AgentUID)
	if !ok {
		return errors.New("handover resume Agent disappeared")
	}
	if err := guardRegistryHandoverTarget(registry, target, old, false); err != nil {
		return err
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
		return err
	}
	if receipt := agent.Status.SessionRef.Codex.HandoverResume; receipt != nil {
		if handoverResumeReceiptMatches(*receipt, operationRef, route.Generation.Endpoint, target) {
			return nil
		}
		return errors.New("another resume receipt owns the exact old thread")
	}
	client, err := effects.openRoute(ctx, rollingNativeRoute(route), true)
	if err != nil {
		return err
	}
	defer client.Close()
	// A crash may happen after thread/resume but before the journal receipt.
	// The successor's loaded-state projection is the semantic receipt; a
	// not-loaded thread is the only state that permits the resume wire again.
	loaded := false
	if snapshot, snapshotErr := client.ReadLifecycleSnapshot(ctx, target.ThreadID); snapshotErr == nil &&
		snapshot.ThreadID == target.ThreadID && snapshot.ThreadState != codexappserver.ThreadStateNotLoaded {
		loaded = true
	}
	if !loaded {
		binding, resumeErr := client.ResumeThread(ctx, target.ThreadID, agent.Spec.Workspace.CWD, agent.Spec.Workspace.AdditionalWritableRoots)
		if resumeErr != nil {
			return resumeErr
		}
		if binding.ThreadID != target.ThreadID {
			return errors.New("successor resumed a different thread")
		}
	}
	_, _, err = effects.registry.UpdateConvergent(func(working *coremetadata.Registry) error {
		_, recordErr := effects.mutator.RecordCodexHandoverResume(working, coremetadata.CodexHandoverTarget{
			AgentUID: target.AgentUID, PaneUID: target.PaneUID, PaneRuntimeID: target.PaneRuntimeID,
			PaneGeneration: target.PaneGeneration, RelaunchGeneration: target.RelaunchGeneration, ThreadID: target.ThreadID,
		}, old, route.Generation.Endpoint, operationRef)
		return recordErr
	})
	if err != nil {
		return fmt.Errorf("persist exact handover resume receipt: %w", err)
	}
	return nil
}

func (effects *codexHandoverEffects) EnsureTargetSnapshot(ctx context.Context, operationRef string, old coremetadata.CodexEndpointRef, route codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	if err := guardRegistryHandoverTarget(registry, target, old, false); err != nil {
		return errors.Join(errors.New("snapshot target exact tuple drifted"), err)
	}
	agent, _ := registry.Agent(target.AgentUID)
	if agent.Status.SessionRef.Codex.HandoverResume == nil ||
		!handoverResumeReceiptMatches(*agent.Status.SessionRef.Codex.HandoverResume, operationRef, route.Generation.Endpoint, target) {
		return errors.New("snapshot has no exact operation-qualified resume receipt")
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
		return err
	}
	client, err := effects.openRoute(ctx, rollingNativeRoute(route), true)
	if err != nil {
		return err
	}
	defer client.Close()
	snapshot, err := client.ReadLifecycleSnapshot(ctx, target.ThreadID)
	if err != nil {
		return err
	}
	if snapshot.ThreadID != target.ThreadID ||
		(snapshot.ThreadState != codexappserver.ThreadStateIdle && snapshot.ThreadState != codexappserver.ThreadStateNotLoaded) ||
		snapshot.TurnID == "" || snapshot.TurnState != codexappserver.TurnStateCompleted {
		return errors.New("successor snapshot is not a completed persisted thread")
	}
	return nil
}

func handoverResumeReceiptMatches(receipt coremetadata.CodexHandoverResumeReceipt, operationRef string,
	successor coremetadata.CodexEndpointRef, target codexgeneration.HandoverTarget,
) bool {
	return receipt.OperationID == operationRef && receipt.SuccessorEndpoint.Same(successor) &&
		receipt.AgentUID == target.AgentUID && receipt.PaneUID == target.PaneUID && receipt.PaneRuntimeID == target.PaneRuntimeID &&
		receipt.PaneGeneration == target.PaneGeneration && receipt.ThreadID == target.ThreadID
}

func (effects *codexHandoverEffects) EnsureTargetCAS(_ context.Context, operationRef string, old, successor coremetadata.CodexEndpointRef, target codexgeneration.HandoverTarget) error {
	_, _, err := effects.registry.UpdateConvergent(func(registry *coremetadata.Registry) error {
		_, err := effects.mutator.CASCodexHandoverTarget(registry, coremetadata.CodexHandoverTarget{
			AgentUID: target.AgentUID, PaneUID: target.PaneUID, PaneRuntimeID: target.PaneRuntimeID,
			PaneGeneration: target.PaneGeneration, RelaunchGeneration: target.RelaunchGeneration, ThreadID: target.ThreadID,
		}, old, successor, operationRef)
		return err
	})
	return err
}

func (effects *codexHandoverEffects) EnsurePaneRelaunched(ctx context.Context, operationRef string, route codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	if err := guardRegistryHandoverTarget(registry, target, route.Generation.Endpoint, true); err != nil {
		return err
	}
	agent, _ := registry.Agent(target.AgentUID)
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
		return err
	}
	marker, err := readHandoverPaneMarker(ctx, runner, target.PaneRuntimeID)
	if err != nil {
		return err
	}
	if marker == operationRef+"\x1f"+target.RelaunchGeneration {
		return nil
	}
	if marker != "\x1f" {
		return errors.New("exact Pane carries another handover relaunch receipt")
	}
	if effects.launcher == nil || effects.materialize == nil {
		return errors.New("handover Pane relaunch is not configured")
	}
	title, child, err := effects.launcher.PlanNativeCodexResume(rollingNativeRoute(route), agent.Spec.Workspace, target.ThreadID)
	if err != nil {
		return err
	}
	spec := superviseSpec{PaneUID: target.PaneUID, AgentUID: target.AgentUID, Generation: target.RelaunchGeneration,
		OperationID: operationRef, RuntimeID: target.PaneRuntimeID}
	argv := effects.materialize.supervisedLaunch(ctx, spec, child)
	if len(argv) == 0 {
		return errors.New("handover relaunch produced an empty argv")
	}
	if err := effects.launcher.BindNativeCodexPaneOnRoute(ctx, runner, target.PaneRuntimeID, agent.Spec.Workspace.CWD, title, target.ThreadID); err != nil {
		return err
	}
	if err := effects.runHandoverMutation(ctx, mutationCodexHandoverRelaunch, target.PaneRuntimeID, target.PaneUID,
		[]string{operationRef, target.RelaunchGeneration}, argv, "all snapshots and exact endpoint CAS observed", func(ctx context.Context) error {
			return guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID)
		}); err != nil {
		return fmt.Errorf("relaunch exact Codex Pane: %w", err)
	}
	marker, err = readHandoverPaneMarker(ctx, runner, target.PaneRuntimeID)
	if err != nil || marker != operationRef+"\x1f"+target.RelaunchGeneration {
		return errors.Join(errors.New("exact Pane relaunch receipt was not observed"), err)
	}
	return nil
}

func (effects *codexHandoverEffects) EnsureRetired(_ context.Context, _ string, old coremetadata.CodexEndpointRef) error {
	registry, err := effects.load()
	if err != nil {
		return err
	}
	for _, agent := range registry.Agents {
		if agent.Status.SessionRef != nil && agent.Status.SessionRef.Codex != nil && agent.Status.SessionRef.Codex.Endpoint != nil &&
			agent.Status.SessionRef.Codex.Endpoint.Same(old) {
			return errors.New("old endpoint still has a Registry Agent owner")
		}
	}
	return nil
}

func (effects *codexHandoverEffects) EnsureLeaseReleased(_ context.Context, _ string, route codexupgrade.GenerationRoute) error {
	if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate {
		return nil
	}
	if route.Proof == nil {
		return errors.New("retired managed generation has no bundle proof")
	}
	return codexgenerationhost.ReleaseDurableGenerationLease(route.Config.HostConfig(), route.Proof.BundleID, true)
}

func (effects *codexHandoverEffects) EnsureOldAuthorityRestored(ctx context.Context, operationRef string, old coremetadata.CodexEndpointRef, targets []codexgeneration.HandoverTarget) error {
	_, _, err := effects.registry.UpdateConvergent(func(registry *coremetadata.Registry) error {
		for _, target := range targets {
			lifecycle := coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationDraining,
				Operation: &coremetadata.CodexGenerationOperationRef{ID: operationRef, Endpoint: old}}
			if _, _, setErr := effects.mutator.SetCodexGenerationLifecycle(registry, target.AgentUID, old, lifecycle); setErr != nil {
				return setErr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	runner, err := effects.routedRunner()
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID); err != nil {
			return err
		}
		if err := effects.runHandoverMutation(ctx, mutationCodexHandoverRestore, target.PaneRuntimeID, target.PaneUID,
			nil, nil, "pre-stop abort restores prior native binding", func(ctx context.Context) error {
				return guardHandoverPane(ctx, runner, target.PaneRuntimeID, target.PaneUID, target.AgentUID)
			}); err != nil {
			return err
		}
	}
	return nil
}

func guardRegistryHandoverTarget(registry coremetadata.Registry, target codexgeneration.HandoverTarget, endpoint coremetadata.CodexEndpointRef, afterCAS bool) error {
	agent, agentOK := registry.Agent(target.AgentUID)
	pane, paneOK := registry.Pane(target.PaneUID)
	if !agentOK || !paneOK || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != target.PaneUID ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != target.AgentUID ||
		pane.Status.Activation.RuntimeID != target.PaneRuntimeID || pane.Status.Activation.AgentUID != target.AgentUID ||
		agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
		!agent.Status.SessionRef.Codex.Endpoint.Same(endpoint) || agent.Status.SessionRef.Codex.ThreadID != target.ThreadID {
		return errors.New("exact Agent/Pane/thread handover tuple drifted")
	}
	wantGeneration := target.PaneGeneration
	if afterCAS {
		wantGeneration = target.RelaunchGeneration
	}
	if pane.Status.Activation.Generation != wantGeneration || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != target.ThreadID {
		return errors.New("exact handover activation generation drifted")
	}
	return nil
}

func guardHandoverPane(ctx context.Context, runner tmuxCommandRunner, runtimeID, paneUID, agentUID string) error {
	format := "#{" + tmuxopts.PaneUID + "}\x1f#{" + tmuxopts.AgentUIDPane + "}\x1f#{" + tmuxopts.PaneOwnerKind + "}\x1f#{" + tmuxopts.PaneOwnerUID + "}"
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", runtimeID, "-F", format)
	if err != nil {
		return fmt.Errorf("observe exact handover Pane: %w", err)
	}
	want := paneUID + "\x1f" + agentUID + "\x1f" + string(coremetadata.KindAgent) + "\x1f" + agentUID
	if strings.TrimSpace(string(out)) != want {
		return errors.New("live handover Pane identity does not match Registry tuple")
	}
	return nil
}

func readHandoverPaneMarker(ctx context.Context, runner tmuxCommandRunner, runtimeID string) (string, error) {
	format := "#{" + codexHandoverOperationPane + "}\x1f#{" + codexHandoverGenerationPane + "}"
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", runtimeID, "-F", format)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (effects *codexHandoverEffects) handoverRoute(ctx context.Context) (tmuxCommandRunner, runtimeMutationRoute, error) {
	runner, err := effects.routedRunner()
	if err != nil {
		return nil, runtimeMutationRoute{}, err
	}
	base := runner
	target := tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}
	switch routed := runner.(type) {
	case explicitTmuxRunner:
		base, target = routed.runner, routed.target
	case *explicitTmuxRunner:
		base, target = routed.runner, routed.target
	}
	route, err := resolveExistingRuntimeMutationRoute(ctx, base, target, nil)
	if err != nil {
		return nil, runtimeMutationRoute{}, fmt.Errorf("resolve exact handover tmux route: %w", err)
	}
	return base, route, nil
}

func (effects *codexHandoverEffects) runHandoverMutation(
	ctx context.Context,
	verb runtimeMutationVerb,
	runtimeID, paneUID string,
	operands, command []string,
	guardDetail string,
	guard func(context.Context) error,
) error {
	base, route, err := effects.handoverRoute(ctx)
	if err != nil {
		return err
	}
	target := runtimeMutationTarget{Kind: "pane", ID: runtimeID, UID: paneUID, Parent: "codex.handover"}
	bindRuntimeMutationRouteTarget(&target, route)
	action := newRuntimeMutation(1, verb, target)
	bindRuntimeMutationGuard(&action, guardDetail)
	action.Operands = append([]string(nil), operands...)
	action.Command = append([]string(nil), command...)
	exact := explicitTmuxRunner{runner: base, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	observe := func(ctx context.Context) (bool, error) {
		switch verb {
		case mutationKillPane:
			out, readErr := exact.Run(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id}")
			if readErr != nil {
				return false, readErr
			}
			for paneID := range strings.FieldsSeq(string(out)) {
				if paneID == runtimeID {
					return false, nil
				}
			}
			return true, nil
		case mutationCodexHandoverFence:
			out, readErr := exact.Run(ctx, "tmux", "show-options", "-pqv", "-t", runtimeID, aiPaneCodexAuthorityOption)
			return readErr == nil && strings.TrimSpace(string(out)) == "", readErr
		case mutationCodexHandoverRestore:
			out, readErr := exact.Run(ctx, "tmux", "show-options", "-pqv", "-t", runtimeID, aiPaneCodexAuthorityOption)
			return readErr == nil && strings.TrimSpace(string(out)) == codexAuthorityHook, readErr
		case mutationCodexHandoverRelaunch:
			marker, readErr := readHandoverPaneMarker(ctx, exact, runtimeID)
			return readErr == nil && len(operands) == 2 && marker == operands[0]+"\x1f"+operands[1], readErr
		default:
			return false, errors.New("unsupported Codex handover tmux mutation")
		}
	}
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardResolvedRuntimeMutationRoute(ctx, base, route)
		},
		Reobserve: observe,
		Guard:     guard,
		Apply: func(ctx context.Context) error {
			_, applyErr := runRuntimeMutationCommand(ctx, exact, action)
			return applyErr
		},
	}})
}
