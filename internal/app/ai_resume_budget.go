package app

import (
	"context"
	"fmt"
	"time"
)

// Codex resume discovery has four stages with different costs: route
// resolution against the generation inventory, one bounded native catalog
// page, one bounded rollout scan, and the handoff that publishes a settled
// result. Each stage is measured on its own clock from its own start instant.
// No stage derives its context from another stage's remaining time, so route
// resolution cannot be pre-cancelled by the provider discovery envelope and
// the rollout clock keeps running while a route is still resolving.
const (
	aiResumeRouteBudget    = 2500 * time.Millisecond
	aiResumeNativeBudget   = 300 * time.Millisecond
	aiResumeFallbackBudget = 1250 * time.Millisecond
	aiResumeHandoffBudget  = 35 * time.Millisecond
)

// aiResumeBudgetStage is the exact name of one independently measured stage.
type aiResumeBudgetStage string

const (
	aiResumeStageRoute    aiResumeBudgetStage = "route"
	aiResumeStageNative   aiResumeBudgetStage = "native"
	aiResumeStageFallback aiResumeBudgetStage = "fallback"
	aiResumeStageHandoff  aiResumeBudgetStage = "handoff"
)

// aiResumeBudgets is the single owner of the stage bounds and of the clock
// they are measured against. now and withTimeout are seams so an exact
// boundary table can be driven deterministically instead of slept through.
type aiResumeBudgets struct {
	route    time.Duration
	native   time.Duration
	fallback time.Duration
	handoff  time.Duration

	now         func() time.Time
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

func defaultAIResumeBudgets() aiResumeBudgets {
	return aiResumeBudgets{
		route:    aiResumeRouteBudget,
		native:   aiResumeNativeBudget,
		fallback: aiResumeFallbackBudget,
		handoff:  aiResumeHandoffBudget,
	}
}

// stage returns one declared bound. A zero field falls back to the package
// constant; a bound is never computed from time another stage already spent.
func (b aiResumeBudgets) stage(stage aiResumeBudgetStage) time.Duration {
	var declared, fallback time.Duration
	switch stage {
	case aiResumeStageRoute:
		declared, fallback = b.route, aiResumeRouteBudget
	case aiResumeStageNative:
		declared, fallback = b.native, aiResumeNativeBudget
	case aiResumeStageFallback:
		declared, fallback = b.fallback, aiResumeFallbackBudget
	case aiResumeStageHandoff:
		declared, fallback = b.handoff, aiResumeHandoffBudget
	}
	if declared > 0 {
		return declared
	}
	return fallback
}

// providerTerminal is the declared worst case for one Codex provider result: a
// route that spends its whole bound, the native page that follows it, and the
// handoff that publishes the outcome. The rollout clock is not added because
// it runs concurrently inside that window rather than after it.
func (b aiResumeBudgets) providerTerminal() time.Duration {
	return b.stage(aiResumeStageRoute) + b.stage(aiResumeStageNative) + b.stage(aiResumeStageHandoff)
}

func (b aiResumeBudgets) instant() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// aiResumeBudgetSpan is one started stage: its own context, its own start
// instant, and the stop that releases it.
type aiResumeBudgetSpan struct {
	stage     aiResumeBudgetStage
	budget    time.Duration
	startedAt time.Time
	ctx       context.Context
	stop      context.CancelFunc
}

// start opens a span under parent. parent must be an invocation-lifetime
// context: handing it another stage's context, or the provider discovery
// envelope, would give this stage that context's remainder instead of the
// bound declared here.
func (b aiResumeBudgets) start(parent context.Context, stage aiResumeBudgetStage) aiResumeBudgetSpan {
	budget := b.stage(stage)
	withTimeout := b.withTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	ctx, stop := withTimeout(parent, budget)
	return aiResumeBudgetSpan{stage: stage, budget: budget, startedAt: b.instant(), ctx: ctx, stop: stop}
}

// elapsed is measured from this span's own start, never from a sibling's.
func (b aiResumeBudgets) elapsed(span aiResumeBudgetSpan) time.Duration {
	if span.startedAt.IsZero() {
		return 0
	}
	elapsed := b.instant().Sub(span.startedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// timeout is the typed terminal reason for a stage that reached its own bound.
func (b aiResumeBudgets) timeout(span aiResumeBudgetSpan) *aiResumeBudgetTimeoutError {
	return &aiResumeBudgetTimeoutError{Stage: span.stage, Budget: span.budget, Elapsed: b.elapsed(span)}
}

// aiResumeBudgetTimeoutError names the exact stage that spent its own bound.
// It wraps context.DeadlineExceeded so existing timeout classification keeps
// working without introducing a new failure vocabulary.
type aiResumeBudgetTimeoutError struct {
	Stage   aiResumeBudgetStage
	Budget  time.Duration
	Elapsed time.Duration
}

func (e *aiResumeBudgetTimeoutError) Error() string {
	return fmt.Sprintf("ai resume %s budget %s exceeded after %s", e.Stage, e.Budget, e.Elapsed)
}

func (e *aiResumeBudgetTimeoutError) Unwrap() error { return context.DeadlineExceeded }
