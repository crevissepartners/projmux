package app

import (
	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func (c *aiCommand) sharedGuidance() *mutationruntime.GuidanceService {
	return &mutationruntime.GuidanceService{Paths: func() (config.Paths, error) { return c.processCreatePaths() }}
}

func (l agentGuidanceLaunch) shared() mutationruntime.AgentGuidanceLaunch {
	return mutationruntime.AgentGuidanceLaunch{MigrationNotice: l.migrationNotice, Active: l.active, Store: l.store, Recorded: l.recorded, Digest: l.digest, SnapshotPath: l.snapshotPath, Text: l.text, SystemPromptFile: l.systemPromptFile, Unavailable: l.unavailable}
}
func localAgentGuidanceLaunch(l mutationruntime.AgentGuidanceLaunch) agentGuidanceLaunch {
	return agentGuidanceLaunch{migrationNotice: l.MigrationNotice, active: l.Active, store: l.Store, recorded: l.Recorded, digest: l.Digest, snapshotPath: l.SnapshotPath, text: l.Text, systemPromptFile: l.SystemPromptFile, unavailable: l.Unavailable}
}

func (l projectLinksLaunch) shared() mutationruntime.ProjectLinksLaunch {
	return mutationruntime.ProjectLinksLaunch{Project: l.project.shared(), Active: l.active, Store: l.store, Recorded: l.recorded, Digest: l.digest, SnapshotPath: l.snapshotPath, Text: l.text, SystemPromptFile: l.systemPromptFile, Unavailable: l.unavailable}
}
func localProjectLinksLaunch(l mutationruntime.ProjectLinksLaunch) projectLinksLaunch {
	return projectLinksLaunch{project: localProjectGuidanceLaunch(l.Project), active: l.Active, store: l.Store, recorded: l.Recorded, digest: l.Digest, snapshotPath: l.SnapshotPath, text: l.Text, systemPromptFile: l.SystemPromptFile, unavailable: l.Unavailable}
}

func (l projectGuidanceLaunch) shared() mutationruntime.ProjectGuidanceLaunch {
	return mutationruntime.ProjectGuidanceLaunch{Active: l.active, Store: l.store, Recorded: l.recorded, Digest: l.digest, Text: l.text, Unavailable: l.unavailable}
}
func localProjectGuidanceLaunch(l mutationruntime.ProjectGuidanceLaunch) projectGuidanceLaunch {
	return projectGuidanceLaunch{active: l.Active, store: l.Store, recorded: l.Recorded, digest: l.Digest, text: l.Text, unavailable: l.Unavailable}
}
