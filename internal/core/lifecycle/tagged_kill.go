package lifecycle

import (
	"errors"
	"strings"
)

// ErrHomeSessionRequired is returned when a fallback switch is required but no
// home session identity was provided.
var ErrHomeSessionRequired = errors.New("home session required")

// TaggedKillInputs captures the pure inputs required to decide whether the
// caller should switch away before killing tagged sessions.
type TaggedKillInputs struct {
	CurrentSession string
	KillTargets    []string
	RecentSessions []string
	HomeSession    string
}

// TaggedKillPlan describes whether a pre-kill session switch is needed and, if
// so, which session should be opened first.
type TaggedKillPlan struct {
	SwitchNeeded bool
	Target       string
}

// PlanTaggedKillSwitch decides whether the current client must move to a
// different session before the kill loop starts.
func PlanTaggedKillSwitch(inputs TaggedKillInputs) (TaggedKillPlan, error) {
	current := strings.TrimSpace(inputs.CurrentSession)
	if current == "" {
		return TaggedKillPlan{}, nil
	}

	killSet := make(map[string]struct{}, len(inputs.KillTargets))
	for _, target := range inputs.KillTargets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}

		killSet[target] = struct{}{}
	}

	if _, ok := killSet[current]; !ok {
		return TaggedKillPlan{}, nil
	}

	for _, recent := range inputs.RecentSessions {
		recent = strings.TrimSpace(recent)
		if recent == "" {
			continue
		}

		if _, ok := killSet[recent]; ok {
			continue
		}

		return TaggedKillPlan{
			SwitchNeeded: true,
			Target:       recent,
		}, nil
	}

	home := strings.TrimSpace(inputs.HomeSession)
	if home == "" {
		return TaggedKillPlan{}, ErrHomeSessionRequired
	}

	return TaggedKillPlan{
		SwitchNeeded: true,
		Target:       home,
	}, nil
}
