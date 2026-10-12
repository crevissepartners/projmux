package app

import (
	"errors"
	"fmt"
	"maps"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

const projectGuidanceReasonUnavailable = "project-guidance-unavailable"






















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
