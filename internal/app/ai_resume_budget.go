package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

// The resume discovery stage budgets live with the discovery in the service
// layer; the picker reads them under its own names.
const (
	aiResumeRouteBudget    = mutationruntime.ResumeRouteBudget
	aiResumeNativeBudget   = mutationruntime.ResumeNativeBudget
	aiResumeFallbackBudget = mutationruntime.ResumeFallbackBudget
	aiResumeHandoffBudget  = mutationruntime.ResumeHandoffBudget
)

type aiResumeBudgetStage = mutationruntime.ResumeBudgetStage

const (
	aiResumeStageRoute    = mutationruntime.ResumeStageRoute
	aiResumeStageNative   = mutationruntime.ResumeStageNative
	aiResumeStageFallback = mutationruntime.ResumeStageFallback
	aiResumeStageHandoff  = mutationruntime.ResumeStageHandoff
)

type aiResumeBudgets = mutationruntime.ResumeBudgets

type aiResumeBudgetTimeoutError = mutationruntime.ResumeBudgetTimeoutError

func defaultAIResumeBudgets() aiResumeBudgets { return mutationruntime.DefaultResumeBudgets() }

// codexRouteReadRolloutBudget is the already spent rollout bound a per-route
// native read declares, so it enters no rollout scan of its own.
const codexRouteReadRolloutBudget = mutationruntime.ResumeCodexRouteReadRolloutBudget
