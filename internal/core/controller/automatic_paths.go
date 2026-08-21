package controller

import (
	"fmt"
	"slices"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// AutomaticRecoveryPath is the production inventory of recovery producers.
// A new automatic producer must add one row and therefore exercise one exact
// classification x trigger x level cell before it can be called.
type AutomaticRecoveryPath struct {
	Name       string
	Divergence resourcegraph.Divergence
	Trigger    RecoveryTrigger
	Level      RecoveryLevel
}

var automaticRecoveryPaths = []AutomaticRecoveryPath{
	{"hook-orphan-mirror-discard", resourcegraph.DivergenceOrphanMirror, RecoveryHookConverge, RecoveryDiscardMirror},
	{"explicit-orphan-mirror-discard", resourcegraph.DivergenceOrphanMirror, RecoveryExplicit, RecoveryDiscardMirror},
	{"project-open-orphan-mirror-discard", resourcegraph.DivergenceOrphanMirror, RecoveryProjectOpen, RecoveryDiscardMirror},
	{"hook-mirror-repair", resourcegraph.DivergenceDrifted, RecoveryHookConverge, RecoveryRepairMirror},
	{"hook-status-projection", resourcegraph.DivergenceDrifted, RecoveryHookConverge, RecoveryProjectStatus},
	{"explicit-mirror-repair", resourcegraph.DivergenceDrifted, RecoveryExplicit, RecoveryRepairMirror},
	{"explicit-status-projection", resourcegraph.DivergenceDrifted, RecoveryExplicit, RecoveryProjectStatus},
	{"project-open-materialize", resourcegraph.DivergenceUnrealized, RecoveryProjectOpen, RecoveryMaterialize},
	{"project-open-skip-item", resourcegraph.DivergenceUnrealized, RecoveryProjectOpen, RecoverySkipItem},
	{"hook-unknown-reobserve", resourcegraph.DivergenceUnknown, RecoveryHookConverge, RecoveryReobserve},
	{"explicit-unknown-reobserve", resourcegraph.DivergenceUnknown, RecoveryExplicit, RecoveryReobserve},
	{"project-open-unknown-reobserve", resourcegraph.DivergenceUnknown, RecoveryProjectOpen, RecoveryReobserve},
}

func AutomaticRecoveryPaths() []AutomaticRecoveryPath { return slices.Clone(automaticRecoveryPaths) }

// RequireAutomaticRecoveryPath is the production gate. Unknown names and
// rows that have drifted outside the authority table return an error rather
// than silently executing.
func RequireAutomaticRecoveryPath(name string) error {
	for _, path := range AutomaticRecoveryPaths() {
		if path.Name != name {
			continue
		}
		verdict := AuthorizeRecovery(path.Divergence, path.Trigger, path.Level, false, 1)
		if verdict.Decision != RecoveryAllowAutomatic {
			return fmt.Errorf("automatic recovery path %q is outside its authority cell: %s", name, verdict.Reason)
		}
		return nil
	}
	return fmt.Errorf("automatic recovery path %q is not classified", name)
}
