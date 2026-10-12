package runtime

import (
	"errors"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// ResumeProjectSystemPromptFile composes persona → Project → link rules. A
// missing Project snapshot drops only that part, and still launches the others.
func (c *GuidanceService) ResumeProjectSystemPromptFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	projectFile, projectErr := c.ResumeProjectGuidanceFile(mode, annotations, personaFile)
	file, linksErr := c.ResumeSystemPromptFile(mode, annotations, projectFile)
	return file, errors.Join(projectErr, linksErr)
}

func (c *GuidanceService) ResumeProjectGuidanceFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentProjectGuidanceDigest]
	if mode != aiModeClaude || digest == "" {
		return personaFile, nil
	}
	fail := func(err error) (string, error) { return personaFile, &ProjectGuidanceUnavailable{err} }
	paths, err := c.Paths()
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
	content, err := ReadSystemPromptPart(personaFile)
	if err != nil {
		return fail(err)
	}
	snapshot, err := store.WriteComposite(content, digest)
	if err != nil {
		return fail(err)
	}
	return snapshot.Path, nil
}

// ResumeGuidanceSystemPromptFile is the one --append-system-prompt-file a
// Claude resume passes, given baseFile, the file it would pass without
// guidance ("" for none): baseFile alone, the guidance snapshot the digest
// annotation names, or the composite of the guidance and baseFile. Guidance
// that cannot be found is dropped and disclosed; baseFile is kept.
func (c *GuidanceService) ResumeGuidanceSystemPromptFile(mode string, annotations map[string]string, baseFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentGuidanceDigest]
	if mode != aiModeClaude || digest == "" {
		return baseFile, nil
	}
	paths, err := c.Paths()
	if err != nil {
		return baseFile, err
	}
	store := agentguidance.NewDefaultStore(paths)
	if baseFile == "" {
		return store.RecordedSnapshotPath(digest)
	}
	composite, err := ComposeAgentGuidance(store, digest, baseFile)
	if err != nil {
		return baseFile, err
	}
	return composite, nil
}

// ResumeSystemPromptFile is the one --append-system-prompt-file a Claude
// resume passes, given the persona snapshot personaFile ("" for none): the
// persona alone, the rules snapshot the digest annotation names, or the
// composite of both. Rules that cannot be found are dropped and disclosed;
// the persona is kept.
func (c *GuidanceService) ResumeSystemPromptFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]
	if mode != aiModeClaude || digest == "" {
		return personaFile, nil
	}
	paths, err := c.Paths()
	if err != nil {
		return personaFile, err
	}
	store := projectlinks.NewDefaultSnapshotStore(paths)
	rulesPath, err := store.RecordedSnapshotPath(digest)
	if err != nil {
		return personaFile, err
	}
	if personaFile == "" {
		return rulesPath, nil
	}
	readPart := ReadPersonaSnapshot
	if annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != "" {
		readPart = ReadSystemPromptPart
	}
	content, err := readPart(personaFile)
	if err != nil {
		return personaFile, err
	}
	composite, err := store.WriteComposite(content, digest)
	if err != nil {
		return personaFile, err
	}
	return composite.Path, nil
}
