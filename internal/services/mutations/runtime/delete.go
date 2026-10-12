package runtime

import (
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

type DeletePlan mutations.DeletePlan

func (p DeletePlan) Cascades() int           { return mutations.DeletePlan(p).Cascades() }
func (p DeletePlan) Signature() string       { return mutations.DeletePlan(p).Signature() }
func (p DeletePlan) NeedsConfirmation() bool { return mutations.DeletePlan(p).NeedsConfirmation() }
