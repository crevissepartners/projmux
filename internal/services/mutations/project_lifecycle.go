package mutations

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"slices"
)

func RequireProjectLifecyclePlan(plan coremetadata.ProjectLifecyclePlan, operation coremetadata.ProjectLifecycleOperation, projectUID coremetadata.ProjectUIDOutcome, descendantUIDs coremetadata.ProjectDescendantUIDOutcome, writes ...coremetadata.ProjectStartupWrite) error {
	if !plan.Available || plan.Operation != operation || plan.ProjectUID != projectUID ||
		plan.DescendantUIDs != descendantUIDs || !slices.Equal(plan.AtomicWriteSet, writes) {
		return fmt.Errorf("project lifecycle state-table cell is not executable: state=%s action=%s operation=%s project_uid=%s descendant_uids=%s writes=%v reason=%s",
			plan.State, plan.Action, plan.Operation, plan.ProjectUID, plan.DescendantUIDs, plan.AtomicWriteSet, plan.Reason)
	}
	return nil
}
