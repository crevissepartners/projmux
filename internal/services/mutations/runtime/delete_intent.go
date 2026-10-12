package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// RecordIntentionalTermination commits this delete's own statement of intent
// before any live tmux object is touched.
//
// Ordering is the whole contract here, and it is the opposite of the obvious
// one. The kills already run inside the resource transaction, so a store
// failure after them leaves live objects gone and the Registry unchanged --
// retryable drift, which the route already reports. What it cannot do is
// explain *why* those objects went away: an operator's deliberate delete and a
// crash look identical to anything that arrives afterwards.
//
// Writing the intent in its own earlier commit fixes both halves. It is durable
// before the first mutation, so a supervisor, a reconcile, or a later projmux
// process that sees the Pane disappear finds a receipt that says a control
// action did it. And a failure to write it aborts with zero tmux mutations,
// because nothing live has been touched yet.
//
// The receipt is stamped with each Pane's *current* generation, read inside
// this same transaction. That is what keeps it from being applied to a Pane
// some other operation has since relaunched.
func (c *DeleteExecutor) RecordIntentionalTermination(
	spelling string,
	plan DeletePlan,
	live WindowLiveDeletePlan,
	livePanes PaneLiveDeletePlan,
	operationID string,
) ([]string, error) {
	panes := IntentionalTerminationPanes(plan, live, livePanes)
	if len(panes) == 0 {
		return nil, nil
	}
	if c == nil || c.Update == nil || c.Mutator == nil {
		return nil, fmt.Errorf("%s: the resource registry store is not configured", spelling)
	}
	_, err := c.Update(func(working *coremetadata.Registry) error {
		if err := mutations.ValidateDeleteTargets(*working, plan.Kind, planUIDs(plan)); err != nil {
			return err
		}
		if c.Allow != nil {
			if err := c.Allow(*working, plan); err != nil {
				return err
			}
		}
		mutator := c.Mutator()
		for _, paneUID := range panes {
			pane, ok := working.Pane(paneUID)
			if !ok {
				// The plan is re-derived and compared inside the resource
				// transaction, so a Pane that vanished between the preflight and
				// this commit is caught there with a full refusal. Recording
				// intent for a resource that no longer exists would be the only
				// thing that could go wrong here, so it is skipped rather than
				// invented.
				continue
			}
			receipt := coremetadata.TerminationEvidence{
				Source:         coremetadata.TerminationSourceControlAction,
				Classification: coremetadata.TerminationIntentional,
				PaneUID:        paneUID,
				Generation:     pane.Status.Activation.Generation,
				OperationID:    operationID,
			}
			if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == coremetadata.KindAgent {
				receipt.AgentUID = owner.UID
			}
			if _, err := mutator.RecordTermination(working, receipt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: intentional termination evidence was not made durable, so nothing live was touched: %w",
			spelling, c.mapMetadataError(err))
	}
	return panes, nil
}

// WithdrawIntentionalTermination compensates a delete that recorded intent and
// then refused or failed.
//
// Without it a refused delete would leave a live Pane claiming a control action
// ended it, and the sticky-intent rule would then suppress the real exit
// evidence when that Pane genuinely stopped later. The withdrawal is scoped by
// this operation's id, so it can only remove what this operation wrote.
//
// A withdrawal that itself fails is reported alongside the original error
// rather than swallowed: the residual state is a stale intentional receipt on a
// Pane that is still running, which is exactly the kind of drift the route
// already names in its other partial-failure messages.
func (c *DeleteExecutor) WithdrawIntentionalTermination(panes []string, operationID string) error {
	if len(panes) == 0 || c == nil || c.Update == nil || c.Mutator == nil {
		return nil
	}
	_, err := c.Update(func(working *coremetadata.Registry) error {
		mutator := c.Mutator()
		for _, paneUID := range panes {
			if _, err := mutator.ClearTermination(working, paneUID, operationID); err != nil {
				return err
			}
		}
		return nil
	})
	return c.mapMetadataError(err)
}
