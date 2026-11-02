package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// The production caller moved to the runtime package with the candidate
// resolution; the app tests still list candidates through this helper.
func listResumableProcessAgents(registry coremetadata.Registry, filter processResumeFilter) []processResumeCandidate {
	return mutationruntime.ListResumableProcessAgents(registry, filter)
}
