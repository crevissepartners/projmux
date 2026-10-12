package app

import (
	"context"
	"errors"
	"fmt"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"os"

	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// deletedPaneMirrorPrefix is a transport tombstone used only while a
// self-target delete is being handed to tmux. A queued kill can fail after the
// Registry result is durable; keeping that live pane out of orphan import is
// safer than silently minting a new identity for a resource the operator just
// deleted.
const deletedPaneMirrorPrefix = coremetadata.DeletedPaneMirrorPrefix

type paneDeleteRuntime interface {
	// useExactTarget pins every read and write of this seam to one resolved
	// server. It is called before the first inventory of an invocation.
	useExactTarget(tmuxTransport)
	preflight(context.Context, coremetadata.Registry, deletePlan) (paneLiveDeletePlan, error)
	prepareReplacements(context.Context, []paneReplacementShell) (paneReplacementReceipt, error)
	rollbackReplacements(context.Context, paneReplacementReceipt) error
	kill(context.Context, paneLiveDeleteTarget) error
	tombstoneSelfKill(context.Context, []paneLiveDeleteTarget) error
	restoreSelfKill(context.Context, []paneLiveDeleteTarget) error
	queueSelfKill(context.Context, []paneLiveDeleteTarget) error
}

type tmuxPaneDeleteRuntime struct {
	mutationruntime.TmuxPaneDeleteRuntime
}

func newTmuxPaneDeleteRuntime() *tmuxPaneDeleteRuntime {
	r := &tmuxPaneDeleteRuntime{TmuxPaneDeleteRuntime: mutationruntime.TmuxPaneDeleteRuntime{Runner: inttmux.ExecRunner{}, Getenv: os.Getenv, ProcessAlive: notifyQueueEventProcessAlive}}
	r.CurrentInvocationPane = r.currentInvocationPane
	return r
}

func (r *tmuxPaneDeleteRuntime) currentInvocationPane(ctx context.Context) (string, string, error) {
	if r.Getenv == nil || strings.TrimSpace(r.Getenv("TMUX")) == "" || strings.TrimSpace(r.Getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.Getenv("TMUX")), ",")
	serverSocket, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", fmt.Errorf("delete pane: inspect exact caller socket: %w", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.Getenv("TMUX_PANE")),
		"-F", tmuxRowFormat("#{socket_path}", "#{pane_id}"))
	if err != nil {
		return "", "", fmt.Errorf("delete pane: inspect exact caller pane %s: %w", strings.TrimSpace(r.Getenv("TMUX_PANE")), err)
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	paneID := exactTmuxHandle(strings.TrimSpace(rows[0][1]), "%")
	if paneID == "" {
		return "", "", errors.New("delete pane: exact caller tmux Pane handle is malformed")
	}
	return paneID, inheritedSocket, nil
}

func (r *tmuxPaneDeleteRuntime) tombstoneSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	applied := 0
	undo := func(ctx context.Context, target paneLiveDeleteTarget) error {
		want := deletedPaneMirrorPrefix + target.PaneUID
		if err := r.RevalidateMutationTarget(ctx, target, want); err != nil {
			// A tmux command may fail either before or after applying its effect.
			// If the original exact evidence still holds, there is no current-step
			// effect to undo. Any third state is real drift and must fail closed.
			if originalErr := r.RevalidateMutationTarget(ctx, target, target.PaneUID); originalErr == nil {
				return nil
			} else {
				return errors.Join(err, originalErr)
			}
		}
		action := r.MutationAction(mutationRestorePane, target,
			"exact tombstoned Pane remains operation-owned", "Pane UID mirror restored",
			"-pq", "-t", target.PaneID, tmuxopts.PaneUID, target.PaneUID)
		_, err := runRuntimeMutationCommand(ctx, r.Routed(), action)
		return err
	}
	steps := r.MutationSteps(targets, mutationTombstonePane,
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) []string {
			return []string{"-pq", "-t", target.PaneID, tmuxopts.PaneUID, deletedPaneMirrorPrefix + target.PaneUID}
		}, undo, &applied)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		marked := targets[:applied]
		if strings.Contains(err.Error(), "owned reverse rollback incomplete") {
			return fmt.Errorf("%w; rollback of earlier exact Pane tombstone(s) %s was incomplete; Registry resources remain authoritative and the reported tombstone drift cannot be orphan-imported",
				fmt.Errorf("tombstone exact live Pane before Registry commit: %w", err), paneDeleteIDs(marked))
		}
		return fmt.Errorf("%w; earlier exact Pane tombstone(s) %s were restored and Registry resources remain unchanged",
			fmt.Errorf("tombstone exact live Pane before Registry commit: %w", err), paneDeleteIDs(marked))
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) restoreSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	steps := r.MutationSteps(targets, mutationRestorePane,
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) []string {
			return []string{"-pq", "-t", target.PaneID, tmuxopts.PaneUID, target.PaneUID}
		}, nil, nil)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		return fmt.Errorf("restore exact Pane tombstone(s): %w", err)
	}
	return nil
}

func paneDeleteIDs(targets []paneLiveDeleteTarget) string {
	return mutationruntime.PaneDeleteIDs(targets)
}

func (r *tmuxPaneDeleteRuntime) queueSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	ordered := selfLastPaneDeleteTargets(targets)
	queuedCount := 0
	steps := r.MutationSteps(ordered, mutationQueuePaneKill,
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(paneLiveDeleteTarget) []string { return nil }, nil, &queuedCount)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		queued := ordered[:queuedCount]
		remaining := ordered[queuedCount:]
		return fmt.Errorf("%w; queued exact Pane(s) %s may complete, while tombstoned unqueued Pane(s) %s remain as retryable drift and cannot be orphan-imported",
			fmt.Errorf("queue exact live Pane plan for self-target deletion: %w", err),
			paneDeleteIDs(queued), paneDeleteIDs(remaining))
	}
	return nil
}

func selfLastPaneDeleteTargets(targets []paneLiveDeleteTarget) []paneLiveDeleteTarget {
	ordered := make([]paneLiveDeleteTarget, 0, len(targets))
	for _, target := range targets {
		if !target.Self {
			ordered = append(ordered, target)
		}
	}
	for _, target := range targets {
		if target.Self {
			ordered = append(ordered, target)
		}
	}
	return ordered
}

type LivePaneDeleteRow = mutationruntime.LivePaneDeleteRow

func (r *tmuxPaneDeleteRuntime) useExactTarget(t tmuxTransport) { r.UseExactTarget(t) }

func (r *tmuxPaneDeleteRuntime) prepareReplacements(ctx context.Context, p []paneReplacementShell) (paneReplacementReceipt, error) {
	return r.PrepareReplacements(ctx, p)
}
func (r *tmuxPaneDeleteRuntime) rollbackReplacements(ctx context.Context, p paneReplacementReceipt) error {
	return r.RollbackReplacements(ctx, p)
}
func (r *tmuxPaneDeleteRuntime) kill(ctx context.Context, p paneLiveDeleteTarget) error {
	return r.Kill(ctx, p)
}

func (r *tmuxPaneDeleteRuntime) Preflight(ctx context.Context, reg coremetadata.Registry, p deletePlan) (paneLiveDeletePlan, error) {
	r.CurrentInvocationPane = r.currentInvocationPane
	return r.TmuxPaneDeleteRuntime.Preflight(ctx, reg, p)
}

func (r *tmuxPaneDeleteRuntime) preflight(ctx context.Context, reg coremetadata.Registry, p deletePlan) (paneLiveDeletePlan, error) {
	return r.Preflight(ctx, reg, p)
}
