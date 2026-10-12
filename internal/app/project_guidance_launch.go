package app

import (
	"errors"
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

func (l projectGuidanceLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	return l.shared().Record(registry, mutator, agentUID)
}

func (l projectGuidanceLaunch) notice(label string) string { return l.shared().Notice(label) }

// projectGuidanceUnavailable distinguishes a missing recorded Project snapshot
// from a missing link-rule snapshot at the shared composite-file seam.
type projectGuidanceUnavailable struct{ error }

func (e *projectGuidanceUnavailable) Error() string {
	return projectGuidanceReasonUnavailable + ": " + e.error.Error()
}

// resumeProjectSystemPromptFile composes persona → Project → link rules. A
// missing Project snapshot drops only that part, and still launches the others.
func (c *aiCommand) resumeProjectSystemPromptFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	projectFile, projectErr := c.resumeProjectGuidanceFile(mode, annotations, personaFile)
	file, linksErr := c.resumeSystemPromptFile(mode, annotations, projectFile)
	return file, errors.Join(projectErr, linksErr)
}

func (c *aiCommand) resumeProjectGuidanceFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentProjectGuidanceDigest]
	if mode != aiModeClaude || digest == "" {
		return personaFile, nil
	}
	fail := func(err error) (string, error) { return personaFile, &projectGuidanceUnavailable{err} }
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return fail(err)
	}
	store := agentguidance.NewDefaultProjectStore(paths)
	if personaFile == "" {
		file, err := store.RecordedSnapshotPath(digest)
		if err != nil {
			return fail(err)
		}
		return file, nil
	}
	content, err := readSystemPromptPart(personaFile)
	if err != nil {
		return fail(err)
	}
	snapshot, err := store.WriteComposite(content, digest)
	if err != nil {
		return fail(err)
	}
	return snapshot.Path, nil
}
