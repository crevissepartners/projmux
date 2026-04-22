package lifecycle

import (
	"context"
	"fmt"
	"strings"
)

// SessionSwitcher moves the active client to another tmux session.
type SessionSwitcher interface {
	SwitchClient(ctx context.Context, sessionName string) error
}

// SessionKiller terminates tmux sessions by name.
type SessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

// TaggedKiller orchestrates the pre-kill switch and subsequent session kill loop.
type TaggedKiller struct {
	switcher SessionSwitcher
	killer   SessionKiller
}

// TaggedKillResult captures the deterministic execution summary.
type TaggedKillResult struct {
	SwitchPlan     TaggedKillPlan
	KilledSessions []string
}

// NewTaggedKiller builds a reusable tagged-kill service over injected boundaries.
func NewTaggedKiller(switcher SessionSwitcher, killer SessionKiller) *TaggedKiller {
	return &TaggedKiller{
		switcher: switcher,
		killer:   killer,
	}
}

// Execute applies the pure switch plan and then kills each normalized target.
func (k *TaggedKiller) Execute(ctx context.Context, inputs TaggedKillInputs) (TaggedKillResult, error) {
	plan, err := PlanTaggedKillSwitch(inputs)
	if err != nil {
		return TaggedKillResult{}, err
	}

	result := TaggedKillResult{
		SwitchPlan:     plan,
		KilledSessions: make([]string, 0, len(inputs.KillTargets)),
	}

	if plan.SwitchNeeded {
		if k.switcher == nil {
			return TaggedKillResult{}, fmt.Errorf("switch before tagged kill: switcher is required")
		}
		if err := k.switcher.SwitchClient(ctx, plan.Target); err != nil {
			return TaggedKillResult{}, fmt.Errorf("switch before tagged kill to %q: %w", plan.Target, err)
		}
	}

	targets := normalizeKillTargets(inputs.KillTargets)
	if len(targets) == 0 {
		return result, nil
	}

	if k.killer == nil {
		return TaggedKillResult{}, fmt.Errorf("kill tagged sessions: killer is required")
	}

	for _, target := range targets {
		if err := k.killer.KillSession(ctx, target); err != nil {
			return TaggedKillResult{}, fmt.Errorf("kill tagged session %q: %w", target, err)
		}
		result.KilledSessions = append(result.KilledSessions, target)
	}

	return result, nil
}

func normalizeKillTargets(targets []string) []string {
	normalized := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}

		seen[target] = struct{}{}
		normalized = append(normalized, target)
	}

	return normalized
}
