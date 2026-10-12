package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"io"
	"strings"
)

// WindowDeleteRuntime owns exact-server inventory and Window mutation.
type WindowDeleteRuntime interface {
	UseExactTarget(TmuxTransport)
	Preflight(context.Context, coremetadata.Registry, DeletePlan) (WindowLiveDeletePlan, error)
	KillAll(context.Context, []WindowLiveDeleteTarget) (int, error)
	QueueSelfKill(context.Context, []WindowLiveDeleteTarget) error
}

// PaneDeleteRuntime supplies the CLI's existing Pane runtime until its service
// extraction. Nil KillAll uses the per-target Kill seam.
type PaneDeleteRuntime struct {
	Preflight            func(context.Context, coremetadata.Registry, DeletePlan) (PaneLiveDeletePlan, error)
	PrepareReplacements  func(context.Context, []PaneReplacementShell) (PaneReplacementReceipt, error)
	RollbackReplacements func(context.Context, PaneReplacementReceipt) error
	Kill                 func(context.Context, PaneLiveDeleteTarget) error
	KillAll              func(context.Context, []PaneLiveDeleteTarget) (int, error)
	TombstoneSelfKill    func(context.Context, []PaneLiveDeleteTarget) error
	RestoreSelfKill      func(context.Context, []PaneLiveDeleteTarget) error
}

// DeleteExecutor is the shared admitted transaction. The caller owns planning,
// confirmation and presentation; the executor owns intent, locked revalidation,
// runtime mutation, compensation, durable Registry deletion and deletion record.
type DeleteExecutor struct {
	Windows        WindowDeleteRuntime
	Panes          PaneDeleteRuntime
	Mutate         func(coremetadata.Kind, []string, func(*coremetadata.Registry, coremetadata.Mutator) error) error
	Update         func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	Mutator        func() coremetadata.Mutator
	StateDir       func() (string, error)
	NewOperationID func() (string, error)
	Actor          func(*coremetadata.Registry) mutations.DeletionActor
	Via            mutations.DeletionVia
	Warnings       io.Writer
	MapError       func(error) error
}

type DeleteResult struct {
	Committed   bool
	OperationID string
	SelfTarget  bool
	Receipt     receipt.OperationReceipt
	Affected    []mutations.DeletionAffected
}

func (c *DeleteExecutor) Execute(ctx context.Context, spelling string, plan DeletePlan, resolution selector.Resolution, livePlan WindowLiveDeletePlan, panePlan PaneLiveDeletePlan) (DeleteResult, error) {
	out := DeleteResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	kind := plan.Kind
	if c.Mutate == nil {
		return out, errors.New("delete: resource registry store is not configured")
	}
	// Accepted changes finish independently of later caller cancellation.
	// Intent is durable before the first live mutation. See
	// recordIntentionalTermination: a failure here aborts with zero tmux
	// mutations, because nothing live has been touched yet.
	operationID, err := c.MintOperationID()
	if err != nil {
		return out, err
	}
	recordedIntent, err := c.RecordIntentionalTermination(spelling, plan, livePlan, panePlan, operationID)
	if err != nil {
		return out, err
	}
	// Every refusal below leaves live processes running, so the recorded intent
	// has to be withdrawn on the way out.
	withdrawIntent := func(cause error) error {
		if withdrawErr := c.WithdrawIntentionalTermination(recordedIntent, operationID); withdrawErr != nil {
			return fmt.Errorf("%w; recorded intentional termination evidence for %s could not be withdrawn: %v; those Panes carry a stale intentional receipt until they are relaunched",
				cause, strings.Join(recordedIntent, ","), withdrawErr)
		}
		return cause
	}

	approved := plan.Signature()
	approvedLive := livePlan.Signature()
	approvedPanes := panePlan.Signature()
	selfTarget := (kind == coremetadata.KindWindow && livePlan.HasSelfTarget()) ||
		(kind != coremetadata.KindWindow && panePlan.HasSelfTarget())
	var killedLive []WindowLiveDeleteTarget
	var killedPanes []PaneLiveDeleteTarget
	paneTombstoned := false
	var replacementReceipt PaneReplacementReceipt
	var actor mutations.DeletionActor
	var affected []mutations.DeletionAffected
	if err := c.Mutate(kind, resolution.UIDs(), func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current := mutations.BuildDeletePlan(*working, kind, resolution).Signature(); current != approved {
			return fmt.Errorf("%s: the cascade plan changed between preflight and execution; nothing was deleted", spelling)
		}
		// The actor is judged here, against the pre-commit Registry and before
		// any kill: a delete can remove the actor's own Pane.
		if c.Actor != nil {
			actor = c.Actor(working)
		}
		var preparedDelete coremetadata.Registry
		if kind == coremetadata.KindWindow {
			currentLive, err := c.Windows.Preflight(context.Background(), *working, plan)
			if err != nil {
				return err
			}
			if currentLive.Signature() != approvedLive {
				return fmt.Errorf("%s: the exact live cascade changed between preflight and execution; nothing was deleted", spelling)
			}
			// Prepare and validate the complete Registry result before touching
			// tmux. Explicit Window deletion preserves the owning root identity
			// and allocates no replacement Window; validation must finish while
			// every exact live target is still intact, never after its kill.
			candidate := working.Clone()
			for _, uid := range resolution.UIDs() {
				if err := DeleteResource(&candidate, mutator, kind, uid); err != nil {
					return err
				}
			}
			if err := candidate.Validate(); err != nil {
				return err
			}
			preparedDelete = candidate
			// A self-target cannot synchronously kill its own Window: tmux tears
			// down the caller's pty before the registry transaction can commit or
			// the result can be written. Its exact kill is queued only after the
			// durable commit and flushed result below.
			if !selfTarget {
				applied, err := c.Windows.KillAll(context.Background(), currentLive.Targets)
				if applied > len(currentLive.Targets) {
					applied = len(currentLive.Targets)
				}
				killedLive = append(killedLive, currentLive.Targets[:applied]...)
				if err != nil {
					return err
				}
			}
		} else {
			currentPanes, err := c.Panes.Preflight(context.Background(), *working, plan)
			if err != nil {
				return err
			}
			if currentPanes.Signature() != approvedPanes {
				return fmt.Errorf("%s: the exact live cascade changed between preflight and execution; nothing was deleted", spelling)
			}
			candidate := working.Clone()
			for _, uid := range resolution.UIDs() {
				if err := DeleteResource(&candidate, mutator, kind, uid); err != nil {
					return err
				}
			}
			if err := candidate.Validate(); err != nil {
				return err
			}
			replacements := PaneDeleteReplacementShells(*working, candidate, currentPanes)
			replacementReceipt, err = c.Panes.PrepareReplacements(context.Background(), replacements)
			if err != nil {
				return fmt.Errorf("%s: create exact replacement shell before deleting the last Window descendant: %w", spelling, err)
			}
			preparedDelete = candidate
			// A caller-containing plan marks the complete exact live set before
			// deleting Registry resources. A partial mark is rolled back while the
			// Registry still owns every uid; after commit, every queued or unqueued
			// survivor is therefore protected from orphan import.
			if selfTarget {
				if err := c.Panes.TombstoneSelfKill(context.Background(), currentPanes.Targets); err != nil {
					return err
				}
				paneTombstoned = true
			} else {
				if c.Panes.KillAll != nil {
					removed, err := c.Panes.KillAll(context.Background(), currentPanes.Targets)
					killedPanes = append(killedPanes, currentPanes.Targets[:removed]...)
					if err != nil {
						return err
					}
				} else {
					for _, target := range currentPanes.Targets {
						if err := c.Panes.Kill(context.Background(), target); err != nil {
							return err
						}
						killedPanes = append(killedPanes, target)
					}
				}
			}
		}
		affected = mutations.DeletionAffectedBetween(*working, preparedDelete)
		*working = preparedDelete
		return nil
	}); err != nil {
		if paneTombstoned {
			if restoreErr := c.Panes.RestoreSelfKill(context.Background(), panePlan.Targets); restoreErr != nil {
				return out, withdrawIntent(fmt.Errorf("%w; Registry uid(s) %s remain, but rollback of pre-commit exact Pane tombstones was incomplete: %v; reported tombstoned drift cannot be orphan-imported",
					err, strings.Join(resolution.UIDs(), ","), restoreErr))
			}
			if rollbackErr := c.RollbackReplacements(replacementReceipt); rollbackErr != nil {
				return out, withdrawIntent(fmt.Errorf("%w; exact Pane tombstones were restored but replacement rollback failed: %v", err, rollbackErr))
			}
			return out, withdrawIntent(fmt.Errorf("%w; pre-commit exact Pane tombstones %s were restored and Registry uid(s) %s remain unchanged for retry",
				err, PaneDeleteIDs(panePlan.Targets), strings.Join(resolution.UIDs(), ",")))
		}
		if len(killedPanes) == 0 {
			if rollbackErr := c.RollbackReplacements(replacementReceipt); rollbackErr != nil {
				return out, withdrawIntent(fmt.Errorf("%w; exact owned replacement rollback failed: %v", err, rollbackErr))
			}
		}
		if len(killedLive) > 0 {
			var removed []string
			for _, target := range killedLive {
				removed = append(removed, fmt.Sprintf("%s/session=%s(%s)", target.WindowID, target.SessionName, target.SessionID))
			}
			// The exact live Windows are already gone, so the intent that
			// explains their removal stays recorded rather than being withdrawn.
			return out, fmt.Errorf("%w; exact live target(s) %s were removed before the store failure, while registry uid(s) %s remain as retryable drift; no unplanned Window was targeted",
				err, strings.Join(removed, ","), strings.Join(resolution.UIDs(), ","))
		}
		if len(killedPanes) > 0 {
			var removed []string
			for _, target := range killedPanes {
				removed = append(removed, fmt.Sprintf("%s/window=%s/session=%s(%s)/pane-uid=%s",
					target.PaneID, target.WindowID, target.SessionName, target.SessionID, target.PaneUID))
			}
			// As above: those Panes really were terminated on purpose. A created
			// replacement cannot be rolled back after that point without destroying
			// the only remaining runtime descendant of the retained Window.
			return out, fmt.Errorf("%w; exact live target(s) %s were removed before the store failure, while registry uid(s) %s remain as retryable drift%s; exact Registry preimage remains unchanged for retry; no unplanned Pane was targeted",
				err, strings.Join(removed, ","), strings.Join(resolution.UIDs(), ","), RetainedPaneReplacementDrift(replacementReceipt))
		}
		return out, withdrawIntent(err)
	}
	// The record follows the durable commit and precedes the result and any
	// queued self-kill, which can end this process.
	mutations.RecordDeletion(c.StateDir, mutations.NewDeletionRecord(ChildDeletionOperation(kind), operationID, c.Via, actor,
		mutations.DeletionTargetsOf(mutations.DeletePlan(plan)), affected), c.Warnings)

	out.Affected = affected
	out.Committed = true
	out.OperationID = operationID
	out.SelfTarget = selfTarget
	out.Receipt = ChildDeleteReceipt(kind, plan, livePlan, panePlan, selfTarget)
	return out, nil
}

func (c *DeleteExecutor) RollbackReplacements(r PaneReplacementReceipt) error {
	if c.Panes.RollbackReplacements == nil {
		return nil
	}
	return c.Panes.RollbackReplacements(context.Background(), r)
}
func (c *DeleteExecutor) MintOperationID() (string, error) {
	if c.NewOperationID != nil {
		return c.NewOperationID()
	}
	return mutations.NewOperationID()
}

func ChildDeletionOperation(kind coremetadata.Kind) string {
	switch kind {
	case coremetadata.KindPane:
		return mutations.DeletionOperationDeletePane
	case coremetadata.KindAgent:
		return mutations.DeletionOperationDeleteAgent
	default:
		return mutations.DeletionOperationDeleteWindow
	}
}
func (c *DeleteExecutor) mapMetadataError(err error) error {
	if c.MapError != nil {
		return c.MapError(err)
	}
	return err
}

// NeedsDeleteConfirmation is shared by prompt and noninteractive admission.
func NeedsDeleteConfirmation(plan DeletePlan, panes PaneLiveDeletePlan) bool {
	return plan.NeedsConfirmation() || panes.EndsWindows() > 0 || panes.EndsSessions() > 0
}
