package core

// StatusbarResourcesApplyStage reports one synchronous preparation or live stage.
// Status is "ok", "skipped", or "failed"; Detail retains the apply diagnostic.
type StatusbarResourcesApplyStage struct {
	Status string
	Detail string
}

// StatusbarResourcesApplyResult preserves completed effects even on error.
// Saved means the preference was saved, not that the running server was updated.
type StatusbarResourcesApplyResult struct {
	Mode     string
	Saved    bool
	Prepared StatusbarResourcesApplyStage
	Live     StatusbarResourcesApplyStage
}
