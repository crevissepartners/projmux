package app

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// projectLinksReasonUnavailable is the reason token of a launch that went
// ahead without its Project's label link rules.
const projectLinksReasonUnavailable = "project-link-rules-unavailable"

// projectLinksPlanner is the optional launcher seam that reads one Project's
// current label link rules for a Claude launch. The production launcher
// (*aiCommand) implements it; a launcher that does not is a launch without
// rules, which is exactly the launch every Agent had before the rules existed.
//
// The Project always comes from Registry ownership (the create's resolved
// Project, or Agent -> Window -> Project on a resume), never from a working
// directory, a workspace or a tmux name. Its UID names the rule file, and its
// name and labels are the rules' Project variables.
type projectLinksPlanner interface {
	PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch
}

var _ projectLinksPlanner = (*aiCommand)(nil)

// codexProjectLinksPlanner is the optional launcher seam that reads one
// Project's current label link rules for a Codex fresh create, the one Codex
// lane that starts a thread of its own. A launcher that does not implement it
// gives a Codex create no rules, exactly as before the rules reached Codex.
// The Project comes from Registry ownership, as for projectLinksPlanner.
type codexProjectLinksPlanner interface {
	PlanCodexProjectLinks(project coremetadata.Project) projectLinksLaunch
}

var _ codexProjectLinksPlanner = (*aiCommand)(nil)

























// planProjectLinksWith asks launcher for project's rules when it has the
// seam, and returns the zero launch otherwise.
func planProjectLinksWith(launcher any, provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	planner, ok := launcher.(projectLinksPlanner)
	if !ok {
		return projectLinksLaunch{}
	}
	return planner.PlanProjectLinks(provider, project, recorded)
}

// planCodexProjectLinksWith asks launcher for a Codex fresh create's rules
// when it has the seam, and returns the zero launch otherwise.
func planCodexProjectLinksWith(launcher any, project coremetadata.Project) projectLinksLaunch {
	planner, ok := launcher.(codexProjectLinksPlanner)
	if !ok {
		return projectLinksLaunch{}
	}
	return planner.PlanCodexProjectLinks(project)
}

// prepareProjectLinks resolves the rules one create launches with, from the
// create's resolved Project, into flags.projectLinks; its notice is disclosed
// once the Agent has a name. A resume-picker create joins a conversation whose recorded
// system prompt lacks the rules, so it launches (and records) the digest with
// the snapshot mode off; a fresh create records only the digest. A Codex
// fresh create (nativeCodexFreshCreateRequired) sends the rules as part of
// its thread's developer instructions and records the digest; it passes no
// file. The reply-only lane, every other Codex lane and every other provider
// are left exactly as they were.
func (c *createCommand) prepareProjectLinks(provider string, project coremetadata.Project, flags *resourceCreateFlags) {
	flags.projectLinks = projectLinksLaunch{}
	if provider == aiModeCodex && nativeCodexFreshCreateRequired(provider, *flags) {
		flags.projectLinks = planCodexProjectLinksWith(c.agents, project)
		return
	}
	if provider != aiModeClaude || flags.dialogueReplyOnly {
		return
	}
	var launcher any = c.agents
	if flags.resumeConversation != "" && c.resumes != nil {
		launcher = c.resumes
	}
	links := planProjectLinksWith(launcher, provider, project, flags.resumeLaunchValues)
	if flags.resumeConversation != "" {
		flags.resumeLaunchValues = links.resumeLaunchAnnotations(flags.resumeLaunchValues)
	} else {
		links = links.withCreateFile(flags.personaLaunch)
	}
	flags.projectLinks = links
}

// resumeSystemPromptFile is the one --append-system-prompt-file a Claude
// resume passes, given the persona snapshot personaFile ("" for none): the
// persona alone, the rules snapshot the digest annotation names, or the
// composite of both. Rules that cannot be found are dropped and disclosed;
// the persona is kept.
func (c *aiCommand) resumeSystemPromptFile(mode string, annotations map[string]string, personaFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]
	if mode != aiModeClaude || digest == "" {
		return personaFile, nil
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
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
	readPart := readPersonaSnapshot
	if annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != "" {
		readPart = readSystemPromptPart
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


