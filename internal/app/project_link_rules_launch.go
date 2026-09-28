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

// projectLinksLaunch is what one Claude launch does with its Project's label
// link rules. The zero value is "not applicable" (another provider, the
// reply-only lane, no Project, or a launcher without the seam) and changes no
// argv and no annotation.
type projectLinksLaunch struct {
	active bool
	store  projectlinks.SnapshotStore
	// recorded is the digest the Agent records, "" when it records none.
	recorded string
	// digest and snapshotPath are the Project's current rendered rules, both
	// empty when the Project has no rules.
	digest       string
	snapshotPath string
	// systemPromptFile is the one file a fresh create hands Claude: the rules
	// snapshot, or the composite of the persona and the rules. Resumes find
	// theirs in the seam from the digest they launch with.
	systemPromptFile string
	// unavailable is why the current rules could not be read. The launch goes
	// ahead without rules and nothing is recorded.
	unavailable error
}

// PlanProjectLinks reads project's current rules, renders them with the
// Project's variables and writes their content-addressed snapshot. recorded
// are the Agent annotations the launch compares against (nil on a fresh
// create).
func (c *aiCommand) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	projectUID := project.Metadata.UID
	if normalizeAIMode(provider) != aiModeClaude || !coremetadata.IsProjectUIDShaped(projectUID) {
		return projectLinksLaunch{}
	}
	launch := projectLinksLaunch{active: true, recorded: recorded[coremetadata.AnnotationAgentProjectLinkRulesDigest]}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.store = projectlinks.NewDefaultSnapshotStore(paths)
	rules, err := projectlinks.NewDefaultStore(paths).Load(projectUID)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	rendered := projectlinks.Render(rules, projectlinks.ProjectOf(project))
	if len(rendered) == 0 {
		return launch
	}
	snapshot, err := launch.store.WriteSnapshot(rendered)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.digest, launch.snapshotPath = snapshot.Digest, snapshot.Path
	return launch
}

// changed reports that the Agent's recorded digest is not the current one:
// rules added, changed or removed.
func (l projectLinksLaunch) changed() bool {
	return l.active && l.unavailable == nil && l.digest != l.recorded
}

// withCreateFile sets systemPromptFile for a fresh create that also carries
// the persona p. A composite that cannot be written makes the rules
// unavailable, so the create still launches, with the persona alone.
func (l projectLinksLaunch) withCreateFile(p personaLaunch) projectLinksLaunch {
	if !l.active || l.unavailable != nil || l.digest == "" {
		return l
	}
	if p.name == "" {
		l.systemPromptFile = l.snapshotPath
		return l
	}
	composite, err := l.store.WriteComposite([]byte(p.content), l.digest)
	if err != nil {
		l.unavailable = err
		return l
	}
	l.systemPromptFile = composite.Path
	return l
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without rules it returns base itself, so a create in a Project without rules
// stores exactly what it stored before -- nil included.
func (l projectLinksLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	if !l.active || l.unavailable != nil || l.digest == "" || base[coremetadata.AnnotationAgentProjectLinkRulesDigest] == l.digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentProjectLinkRulesDigest] = l.digest
	return out
}

// resumeLaunchAnnotations are the annotations a resume launch reads, given
// the ones it would otherwise read. Rules that changed put the current digest
// (or no digest) and the snapshot mode off in place, which is exactly what
// record writes, so the launch reads what the Agent will record. Unreadable
// rules drop the digest so the Agent launches without rules while its record
// stays as it was. Otherwise base itself is returned.
func (l projectLinksLaunch) resumeLaunchAnnotations(base map[string]string) map[string]string {
	if !l.active {
		return base
	}
	if l.unavailable != nil {
		if _, ok := base[coremetadata.AnnotationAgentProjectLinkRulesDigest]; !ok {
			return base
		}
		out := maps.Clone(base)
		delete(out, coremetadata.AnnotationAgentProjectLinkRulesDigest)
		return out
	}
	if !l.changed() {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	if l.digest == "" {
		delete(out, coremetadata.AnnotationAgentProjectLinkRulesDigest)
	} else {
		out[coremetadata.AnnotationAgentProjectLinkRulesDigest] = l.digest
	}
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

// record writes, inside the resume transaction, the digest and snapshot mode
// resumeLaunchAnnotations launched with. Nothing changes when the rules are
// the recorded ones or could not be read.
func (l projectLinksLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.changed() {
		return nil
	}
	if _, err := mutator.SetAgentProjectLinkRules(registry, agentUID, l.digest); err != nil {
		return MapMetadataError(err)
	}
	return nil
}

// notice is the one-line disclosure of rules the launch could not read, or ""
// when there is nothing to disclose. label names the Agent the way the
// persona notice does.
func (l projectLinksLaunch) notice(label string) string {
	if !l.active || l.unavailable == nil {
		return ""
	}
	return projectLinksNotice(label, l.unavailable)
}

func projectLinksNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without its Project's label link rules (%s): %v",
		label, projectLinksReasonUnavailable, err)
}

// planProjectLinksWith asks launcher for project's rules when it has the
// seam, and returns the zero launch otherwise.
func planProjectLinksWith(launcher any, provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	planner, ok := launcher.(projectLinksPlanner)
	if !ok {
		return projectLinksLaunch{}
	}
	return planner.PlanProjectLinks(provider, project, recorded)
}

// prepareProjectLinks resolves the rules one Claude create launches with,
// from the create's resolved Project, into flags.projectLinks; its notice is
// disclosed once the Agent has a name. A resume-picker create joins a conversation whose recorded
// system prompt lacks the rules, so it launches (and records) the digest with
// the snapshot mode off; a fresh create records only the digest. The
// reply-only lane and every other provider are left exactly as they were.
func (c *createCommand) prepareProjectLinks(provider string, project coremetadata.Project, flags *resourceCreateFlags) {
	flags.projectLinks = projectLinksLaunch{}
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
	content, err := readPersonaSnapshot(personaFile)
	if err != nil {
		return personaFile, err
	}
	composite, err := store.WriteComposite(content, digest)
	if err != nil {
		return personaFile, err
	}
	return composite.Path, nil
}

// readPersonaSnapshot reads the persona snapshot at path, bounded like every
// persona read. The file is opened inside its own directory, the snapshot
// directory RecordedSnapshotPath already proved it a regular file of, so
// the read cannot leave that directory.
func readPersonaSnapshot(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no persona snapshot at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no persona snapshot at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return persona.ReadLimited(file)
}
