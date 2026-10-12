package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

type projectStopInterruption = mutationruntime.ProjectStopInterruption

func recordProjectStopInterruptions(s *resourceStore, uid, op string) ([]projectStopInterruption, error) {
	return mutationruntime.RecordProjectStopInterruptions(stopServiceStore(s), uid, op)
}

func projectProjectStopInterruptions(s *resourceStore, interruptions []projectStopInterruption, op string) error {
	return mutationruntime.ProjectProjectStopInterruptions(stopServiceStore(s), interruptions, op)
}
