package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const (
	processResumeNotResumable = mutationruntime.ProcessResumeNotResumable
	processResumeOwned        = mutationruntime.ProcessResumeOwned
	processResumeRefused      = mutationruntime.ProcessResumeRefused
)

type processResumeFilter = mutationruntime.ProcessResumeFilter
type processResumePrevious = mutationruntime.ProcessResumePrevious
type processResumeCandidate = mutationruntime.ProcessResumeCandidate

func processResumeRefusal(registry coremetadata.Registry, agentUID string, ownerAlive bool) error {
	return mutationruntime.ProcessResumeRefusal(registry, agentUID, ownerAlive)
}

func processResumeCandidateToken(registry coremetadata.Registry, agentUID string) string {
	return mutationruntime.ProcessResumeCandidateToken(registry, agentUID)
}

func processResumePane(registry coremetadata.Registry, agentUID string) (*coremetadata.Pane, bool) {
	return mutationruntime.ProcessAgentPane(registry, agentUID)
}
