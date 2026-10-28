package runtime

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
	ResumeRouteBudget    = 2500 * time.Millisecond
	ResumeNativeBudget   = 300 * time.Millisecond
	ResumeFallbackBudget = 1250 * time.Millisecond
	ResumeHandoffBudget  = 35 * time.Millisecond
)

// ResumeBudgetStage is the exact name of one independently measured stage.
type ResumeBudgetStage string

const (
	ResumeStageRoute    ResumeBudgetStage = "route"
	ResumeStageNative   ResumeBudgetStage = "native"
	ResumeStageFallback ResumeBudgetStage = "fallback"
	ResumeStageHandoff  ResumeBudgetStage = "handoff"
)

// ResumeBudgets is the single owner of the stage bounds and of the clock
// they are measured against. now and withTimeout are seams so an exact
// boundary table can be driven deterministically instead of slept through.
type ResumeBudgets struct {
	Route    time.Duration
	Native   time.Duration
	Fallback time.Duration
	Handoff  time.Duration

	Now         func() time.Time
	WithTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

func DefaultResumeBudgets() ResumeBudgets {
	return ResumeBudgets{
		Route:    ResumeRouteBudget,
		Native:   ResumeNativeBudget,
		Fallback: ResumeFallbackBudget,
		Handoff:  ResumeHandoffBudget,
	}
}

// Stage returns one declared bound. A zero field falls back to the package
// constant; a bound is never computed from time another stage already spent.
func (b ResumeBudgets) Stage(stage ResumeBudgetStage) time.Duration {
	var declared, fallback time.Duration
	switch stage {
	case ResumeStageRoute:
		declared, fallback = b.Route, ResumeRouteBudget
	case ResumeStageNative:
		declared, fallback = b.Native, ResumeNativeBudget
	case ResumeStageFallback:
		declared, fallback = b.Fallback, ResumeFallbackBudget
	case ResumeStageHandoff:
		declared, fallback = b.Handoff, ResumeHandoffBudget
	}
	if declared > 0 {
		return declared
	}
	return fallback
}

// ProviderTerminal is the declared worst case for one Codex provider result: a
// route that spends its whole bound, the native page that follows it, and the
// handoff that publishes the outcome. The rollout clock is not added because
// it runs concurrently inside that window rather than after it.
func (b ResumeBudgets) ProviderTerminal() time.Duration {
	return b.Stage(ResumeStageRoute) + b.Stage(ResumeStageNative) + b.Stage(ResumeStageHandoff)
}

func (b ResumeBudgets) Instant() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// ResumeBudgetSpan is one started stage: its own context, its own start
// instant, and the stop that releases it.
type ResumeBudgetSpan struct {
	Stage     ResumeBudgetStage
	Budget    time.Duration
	StartedAt time.Time
	Ctx       context.Context
	Stop      context.CancelFunc
}

// Start opens a span under parent. parent must be an invocation-lifetime
// context: handing it another stage's context, or the provider discovery
// envelope, would give this stage that context's remainder instead of the
// bound declared here.
func (b ResumeBudgets) Start(parent context.Context, stage ResumeBudgetStage) ResumeBudgetSpan {
	budget := b.Stage(stage)
	withTimeout := b.WithTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	ctx, stop := withTimeout(parent, budget)
	return ResumeBudgetSpan{Stage: stage, Budget: budget, StartedAt: b.Instant(), Ctx: ctx, Stop: stop}
}

// Elapsed is measured from this span's own start, never from a sibling's.
func (b ResumeBudgets) Elapsed(span ResumeBudgetSpan) time.Duration {
	if span.StartedAt.IsZero() {
		return 0
	}
	elapsed := b.Instant().Sub(span.StartedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// Timeout is the typed terminal reason for a stage that reached its own bound.
func (b ResumeBudgets) Timeout(span ResumeBudgetSpan) *ResumeBudgetTimeoutError {
	return &ResumeBudgetTimeoutError{Stage: span.Stage, Budget: span.Budget, Elapsed: b.Elapsed(span)}
}

// ResumeBudgetTimeoutError names the exact stage that spent its own bound.
// It wraps context.DeadlineExceeded so existing timeout classification keeps
// working without introducing a new failure vocabulary.
type ResumeBudgetTimeoutError struct {
	Stage   ResumeBudgetStage
	Budget  time.Duration
	Elapsed time.Duration
}

func (e *ResumeBudgetTimeoutError) Error() string {
	return fmt.Sprintf("ai resume %s budget %s exceeded after %s", e.Stage, e.Budget, e.Elapsed)
}

func (e *ResumeBudgetTimeoutError) Unwrap() error { return context.DeadlineExceeded }
