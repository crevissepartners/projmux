package pins

import "strings"

// ProjectDeletionPlan is the pure pin-store half of a Project cascade. It is a
// desired ordered set only; persisting it belongs to the later composite
// mutation phase.
type ProjectDeletionPlan struct {
	Desired    Set
	Changed    bool
	Replaced   int
	DeletedUID string
	PriorRoot  string
}

// PlanProjectDeletion replaces each exact managed pin for a deleted Project
// with a candidate pin for its pre-delete exact root, in the same position.
// There is deliberately no new Project uid input: a later reopen cannot be
// silently retargeted by this plan.
func PlanProjectDeletion(set Set, deletedProjectUID, priorRoot string) (ProjectDeletionPlan, error) {
	managed, err := ProjectPin(strings.TrimSpace(deletedProjectUID))
	if err != nil {
		return ProjectDeletionPlan{}, err
	}
	candidate, err := CandidatePin(strings.TrimSpace(priorRoot))
	if err != nil {
		return ProjectDeletionPlan{}, err
	}
	replaced := 0
	for _, pin := range set.Pins {
		if pin == managed {
			replaced++
		}
	}
	desired := Set{Format: set.Format, Pins: make([]Pin, 0, len(set.Pins))}
	if replaced == 0 {
		desired.Pins = append(desired.Pins, set.Pins...)
	} else {
		candidateWritten := false
		for _, pin := range set.Pins {
			switch {
			case pin == managed && !candidateWritten:
				// The candidate occupies the deleted managed pin's exact order.
				desired.Pins = append(desired.Pins, candidate)
				candidateWritten = true
			case pin == managed:
				// A Set is unique by contract; remain duplicate-free if a caller
				// nevertheless hand-assembles duplicate managed entries.
			case pin == candidate:
				// Prefer the converted managed slot over an older duplicate so
				// the Project pin's ordering preference is preserved.
			default:
				desired.Pins = append(desired.Pins, pin)
			}
		}
	}
	if replaced > 0 {
		// A project/candidate union requires the typed envelope. A legacy file
		// cannot contain a managed pin in the first place, but fail closed into
		// the only format that can represent this desired set if handed one.
		desired.Format = FormatTyped
	}
	return ProjectDeletionPlan{
		Desired: desired, Changed: replaced > 0, Replaced: replaced,
		DeletedUID: managed.Value, PriorRoot: candidate.Value,
	}, nil
}
