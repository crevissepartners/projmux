package core

// ProjectCandidate is a configured path in discovery order. ProjectUID is
// absent for an unregistered path; Pinned follows typed or legacy pin identity.
type ProjectCandidate struct {
	Path       string `json:"path"`
	Pinned     bool   `json:"pinned"`
	ProjectUID string `json:"projectUID,omitempty"`
}

// ProjectCandidatesResult has a non-null list, including when discovery is empty.
type ProjectCandidatesResult struct {
	Candidates []ProjectCandidate `json:"candidates"`
}
