package mutations

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"io"
)

// UnregisterOptions supplies transport-free storage and provenance seams. Actor
// is observed inside the transaction; nil means no ambient Pane, as in the SDK.
type UnregisterOptions struct {
	Mutate         func(coremetadata.Kind, []string, func(*coremetadata.Registry, coremetadata.Mutator) error) error
	Actor          func(*coremetadata.Registry) DeletionActor
	StateDir       func() (string, error)
	NewOperationID func() (string, error)
	Via            DeletionVia
	Operation      string
	Warnings       io.Writer
}
type UnregisterResult struct {
	Plan        DeletePlan
	Receipt     receipt.OperationReceipt
	Affected    []DeletionAffected
	OperationID string
	Committed   bool
}

// Unregister executes only an approved plan. Cancellation is checked before
// admission; the admitted transaction and best-effort record finish atomically
// with respect to Registry state, independently of later caller cancellation.
func Unregister(ctx context.Context, plan DeletePlan, resolution selector.Resolution, opts UnregisterOptions) (UnregisterResult, error) {
	out := UnregisterResult{Plan: plan}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if plan.Kind != coremetadata.KindProject || len(plan.Targets) == 0 {
		return out, &Failure{Code: "selector-invalid", Cause: errors.New("unregister project requires a Project plan")}
	}
	if opts.Mutate == nil {
		return out, &Failure{Code: "registry-unregister-failed", Cause: errors.New("resource registry store is not configured")}
	}
	if opts.Actor == nil {
		opts.Actor = func(*coremetadata.Registry) DeletionActor { return DeletionActor{} }
	}
	oldUIDs := resolution.UIDs()
	var actor DeletionActor
	var affected []DeletionAffected
	err := opts.Mutate(coremetadata.KindProject, oldUIDs, func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current := BuildDeletePlan(*working, coremetadata.KindProject, resolution).Signature(); current != plan.Signature() {
			return &Failure{Code: "unregister-plan-changed", Cause: errors.New("delete project: the unregister plan changed between preflight and execution; nothing was deleted")}
		}
		actor = opts.Actor(working)
		candidate := working.Clone()
		for _, uid := range oldUIDs {
			project, ok := working.Project(uid)
			if !ok {
				return fmt.Errorf("delete project: Project uid %q disappeared before state-table validation", uid)
			}
			state := coremetadata.ProjectLifecycleRetainedWindows
			descendants := coremetadata.ProjectDescendantUIDsRemoved
			if len(working.WindowsOf(project.Metadata.UID)) == 0 {
				state = coremetadata.ProjectLifecycleZeroWindows
				descendants = coremetadata.ProjectDescendantUIDsAbsent
			}
			decision := coremetadata.DecideProjectLifecycle(state, coremetadata.ProjectLifecycleDeleteProject, coremetadata.ProjectLifecyclePreconditions{})
			if err := RequireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationDeleteProject,
				coremetadata.ProjectUIDRemoved, descendants, coremetadata.ProjectStartupWriteDeleteProjectGraph); err != nil {
				return &Failure{Code: "project-lifecycle-refused", Cause: err}
			}
			if err := mutator.DeleteProject(&candidate, uid); err != nil {
				return err
			}
		}
		if err := candidate.Validate(); err != nil {
			return err
		}
		affected = DeletionAffectedBetween(*working, candidate)
		*working = candidate
		return nil
	})
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			return out, err
		}
		return out, &Failure{Code: "registry-unregister-failed", Cause: err}
	}
	out.Committed = true
	out.Affected = affected
	out.Receipt = ProjectUnregisterReceipt(plan)
	mint := opts.NewOperationID
	if mint == nil {
		mint = NewOperationID
	}
	out.OperationID, _ = mint()
	operation := opts.Operation
	if operation == "" {
		operation = DeletionOperationUnregisterProject
	}
	RecordDeletion(opts.StateDir, NewDeletionRecord(operation, out.OperationID, opts.Via, actor, DeletionTargetsOf(plan), affected), opts.Warnings)
	return out, nil
}

func ProjectUnregisterReceipt(plan DeletePlan) receipt.OperationReceipt {
	target := receipt.ReceiptTarget{Kind: "Project"}
	if len(plan.Targets) > 0 {
		target.UID = plan.Targets[0].Match.UID
		target.Name = plan.Targets[0].Match.Name
	}
	r := receipt.NewReceipt(receipt.OperationUnregisterProject, target, receipt.ReceiptEffects{
		Identity:     receipt.IdentityRemoved,
		Address:      receipt.AddressReleased,
		Topology:     receipt.TopologyRemoved,
		DesiredState: receipt.DesiredStateRemoved,
		Runtime:      receipt.RuntimePreserved,
		Focus:        receipt.FocusUnchanged,
	})
	for _, entry := range plan.Targets {
		r.Add("Project", entry.Match.UID, entry.Match.Name, receipt.ActionRemoved)
		for _, descendant := range entry.Descendants {
			r.Add(string(descendant.Kind), descendant.UID, descendant.Name, receipt.ActionRemoved)
		}
	}
	return r
}
