package lifecycle

import (
	"errors"
	"sort"
	"strings"
)

// ErrEphemeralKeepCountInvalid is returned when the requested ephemeral keep
// count is negative.
var ErrEphemeralKeepCountInvalid = errors.New("ephemeral keep count must be non-negative")

// SessionInventory captures the lifecycle metadata needed for ephemeral
// selection and pruning decisions.
type SessionInventory struct {
	Name         string
	Attached     bool
	LastAttached int64
	Ephemeral    bool
}

// AutoAttachInputs captures the deterministic inputs for auto-attach planning.
type AutoAttachInputs struct {
	Sessions    []SessionInventory
	HomeSession string
	KeepCount   int
}

// AutoAttachPlan describes whether an existing ephemeral session should be
// reused or whether the caller should prune and fall back to the home session.
type AutoAttachPlan struct {
	AttachTarget      string
	EnsureHomeSession bool
	PruneTargets      []string
}

// PlanAutoAttach chooses the reusable ephemeral session if one exists.
// Otherwise it instructs the caller to prune stale ephemeral sessions and
// attach the home session.
func PlanAutoAttach(inputs AutoAttachInputs) (AutoAttachPlan, error) {
	reusable, ok := SelectReusableEphemeral(inputs.Sessions)
	if ok {
		return AutoAttachPlan{
			AttachTarget: reusable,
		}, nil
	}

	pruneTargets, err := PruneEphemeralTargets(inputs.Sessions, inputs.KeepCount)
	if err != nil {
		return AutoAttachPlan{}, err
	}

	home := strings.TrimSpace(inputs.HomeSession)
	return AutoAttachPlan{
		AttachTarget:      home,
		EnsureHomeSession: home != "",
		PruneTargets:      pruneTargets,
	}, nil
}

// SelectReusableEphemeral returns the most recently attached unattached
// ephemeral session, if one exists.
func SelectReusableEphemeral(sessions []SessionInventory) (string, bool) {
	var selected SessionInventory
	found := false

	for _, session := range sessions {
		name := strings.TrimSpace(session.Name)
		if name == "" || !session.Ephemeral || session.Attached {
			continue
		}

		if !found || session.LastAttached > selected.LastAttached {
			selected = session
			selected.Name = name
			found = true
		}
	}

	if !found {
		return "", false
	}

	return selected.Name, true
}

// PruneEphemeralTargets returns the stale unattached ephemeral sessions beyond
// the keep limit, sorted from newest kept to oldest pruned like the legacy
// shell pipeline.
func PruneEphemeralTargets(sessions []SessionInventory, keepCount int) ([]string, error) {
	if keepCount < 0 {
		return nil, ErrEphemeralKeepCountInvalid
	}

	candidates := make([]SessionInventory, 0, len(sessions))
	for _, session := range sessions {
		name := strings.TrimSpace(session.Name)
		if name == "" || !session.Ephemeral || session.Attached {
			continue
		}

		session.Name = name
		candidates = append(candidates, session)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].LastAttached > candidates[j].LastAttached
	})

	if keepCount >= len(candidates) {
		return nil, nil
	}

	targets := make([]string, 0, len(candidates)-keepCount)
	for _, session := range candidates[keepCount:] {
		targets = append(targets, session.Name)
	}

	return targets, nil
}
