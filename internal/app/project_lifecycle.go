package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const absentProjectLifecycleUID = "-"

// projectLifecycleStateFor is the app-side classifier for the core lifecycle
// table. It reads desired Registry topology only; runtime absence is never
// promoted to deleted Project identity.
func projectLifecycleStateFor(registry coremetadata.Registry, root string) (coremetadata.ProjectLifecycleState, string) {
	project, ok := registry.ProjectByRoot(cleanOptionalPath(root))
	if !ok {
		return coremetadata.ProjectLifecycleDeleted, ""
	}
	if len(registry.WindowsOf(project.Metadata.UID)) == 0 {
		return coremetadata.ProjectLifecycleZeroWindows, project.Metadata.UID
	}
	return coremetadata.ProjectLifecycleRetainedWindows, project.Metadata.UID
}

// projectLifecycleDecisionFor is the app projection of the core-owned closed
// table. App routes classify only desired Registry state and never maintain a
// second action/write-set table.
func projectLifecycleDecisionFor(registry coremetadata.Registry, root string, action coremetadata.ProjectLifecycleAction, preconditions coremetadata.ProjectLifecyclePreconditions) (coremetadata.ProjectLifecyclePlan, string) {
	state, uid := projectLifecycleStateFor(registry, root)
	return coremetadata.DecideProjectLifecycle(state, action, preconditions), uid
}

// requireProjectLifecyclePlan makes each production route prove that the one
// core table cell it selected owns exactly its mutation class and write set.
// This prevents a route from merely classifying state and then executing an
// independently maintained meaning of Stop, Continue, Fresh, or delete.
func requireProjectLifecyclePlan(plan coremetadata.ProjectLifecyclePlan, operation coremetadata.ProjectLifecycleOperation, projectUID coremetadata.ProjectUIDOutcome, descendantUIDs coremetadata.ProjectDescendantUIDOutcome, writes ...coremetadata.ProjectStartupWrite) error {
	if !plan.Available || plan.Operation != operation || plan.ProjectUID != projectUID ||
		plan.DescendantUIDs != descendantUIDs || !slices.Equal(plan.AtomicWriteSet, writes) {
		return fmt.Errorf("Project lifecycle state-table cell is not executable: state=%s action=%s operation=%s project_uid=%s descendant_uids=%s writes=%v reason=%s",
			plan.State, plan.Action, plan.Operation, plan.ProjectUID, plan.DescendantUIDs, plan.AtomicWriteSet, plan.Reason)
	}
	return nil
}

// projectLifecycleStageError is the shared failure disclosure for Stop,
// Continue, Fresh, and explicit Project unregister. UIDs are content-free
// resource identities and stage is a closed implementation label.
type projectLifecycleStageError struct {
	action        coremetadata.ProjectLifecycleAction
	stage         string
	oldProjectUID string
	newProjectUID string
	err           error
}

func (e projectLifecycleStageError) Error() string {
	oldUID := strings.TrimSpace(e.oldProjectUID)
	if oldUID == "" {
		oldUID = absentProjectLifecycleUID
	}
	newUID := strings.TrimSpace(e.newProjectUID)
	if newUID == "" {
		newUID = absentProjectLifecycleUID
	}
	return fmt.Sprintf("project lifecycle action=%s stage=%s old_uid=%s new_uid=%s: %v",
		e.action, e.stage, oldUID, newUID, e.err)
}

func (e projectLifecycleStageError) Unwrap() error { return e.err }

func wrapProjectLifecycleError(action coremetadata.ProjectLifecycleAction, stage, oldUID, newUID string, err error) error {
	if err == nil {
		return nil
	}
	var already projectLifecycleStageError
	if errors.As(err, &already) {
		return err
	}
	return projectLifecycleStageError{
		action: action, stage: strings.TrimSpace(stage),
		oldProjectUID: strings.TrimSpace(oldUID), newProjectUID: strings.TrimSpace(newUID), err: err,
	}
}
