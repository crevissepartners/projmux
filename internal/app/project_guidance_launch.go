package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const projectGuidanceReasonUnavailable = mutationruntime.ProjectGuidanceReasonUnavailable

// projectGuidanceLaunch travels with the Project prompt layer (projectLinksLaunch)
// so every create, resume and replay reads and records the same Project ownership.
// It is independent of both global guidance and link rules being enabled.
type projectGuidanceLaunch struct {
	active           bool
	store            agentguidance.ProjectStore
	recorded, digest string
	text             []byte
	unavailable      error
}

func (c *aiCommand) loadProjectGuidance(project coremetadata.Project, recorded map[string]string) projectGuidanceLaunch {
	return localProjectGuidanceLaunch(c.sharedGuidance().LoadProjectGuidance(project, recorded))
}

func (l projectGuidanceLaunch) resumeLaunchAnnotations(base map[string]string) map[string]string {
	return l.shared().ResumeLaunchAnnotations(base)
}

func (l projectGuidanceLaunch) notice(label string) string { return l.shared().Notice(label) }
