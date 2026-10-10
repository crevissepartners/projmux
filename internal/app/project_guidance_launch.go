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
	if !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return projectGuidanceLaunch{}
	}
	launch := projectGuidanceLaunch{active: true, recorded: recorded[coremetadata.AnnotationAgentProjectGuidanceDigest]}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.store = agentguidance.NewDefaultProjectStore(paths)
	guidance, err := launch.store.Load(project.Metadata.UID)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	if guidance.Off() {
		return launch
	}
	snapshot, err := launch.store.WriteSnapshot(guidance.Text)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.digest, launch.text = snapshot.Digest, guidance.Text
	return launch
}

func (l projectGuidanceLaunch) on() bool { return l.active && l.unavailable == nil && l.digest != "" }
func (l projectGuidanceLaunch) changed() bool {
	return l.active && l.unavailable == nil && l.digest != l.recorded
}

func (l projectGuidanceLaunch) developerInstructions(persona string) string {
	if !l.on() {
		return persona
	}
	if persona == "" {
		return string(l.text)
	}
	return persona + projectlinks.CompositeSeparator + string(l.text)
}

func (l projectGuidanceLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	if !l.on() || base[coremetadata.AnnotationAgentProjectGuidanceDigest] == l.digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string)
	}
	out[coremetadata.AnnotationAgentProjectGuidanceDigest] = l.digest
	return out
}

func (l projectGuidanceLaunch) resumeLaunchAnnotations(base map[string]string) map[string]string {
	if !l.active || (!l.changed() && l.unavailable == nil) {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string)
	}
	if l.digest == "" || l.unavailable != nil {
		delete(out, coremetadata.AnnotationAgentProjectGuidanceDigest)
	} else {
		out[coremetadata.AnnotationAgentProjectGuidanceDigest] = l.digest
	}
	// Even unavailable instructions must not revive their recorded provider
	// snapshot. The durable annotations are preserved until a readable launch.
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

func (l projectGuidanceLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.changed() {
		return nil
	}
	_, err := mutator.SetAgentProjectGuidance(registry, agentUID, l.digest)
	return MapMetadataError(err)
}

func (l projectGuidanceLaunch) notice(label string) string {
	if !l.active || l.unavailable == nil {
		return ""
	}
	return projectGuidanceNotice(label, l.unavailable)
}

func projectGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without its Project's common instructions (%s): %v", label, projectGuidanceReasonUnavailable, err)
}

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
