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

// projectLinksLaunch is what one launch does with its Project's label link
// rules. The zero value is "not applicable" (another provider or lane, the
// reply-only lane, no Project, or a launcher without the seam) and changes no
// argv, no developer instructions and no annotation.
//
// The Project common instructions precede the rules, independently of whether
// rules exist. A Claude launch passes the rules last in its one
// --append-system-prompt-file. A Codex fresh create sends them the same way,
// after the persona, as the thread's developer instructions
// (developerInstructions).
type projectLinksLaunch struct {
	// project is the independent common-instruction layer before link rules.
	project projectGuidanceLaunch
	active  bool
	store   projectlinks.SnapshotStore
	// recorded is the digest the Agent records, "" when it records none.
	recorded string
	// digest, snapshotPath and text are the Project's current rendered rules,
	// all empty when the Project has no rules. text is sent only to a Codex
	// thread and is never put in an argv or the Registry.
	digest       string
	snapshotPath string
	text         []byte
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
	if normalizeAIMode(provider) != aiModeClaude || !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return projectLinksLaunch{}
	}
	launch := c.loadProjectLinks(project)
	launch.recorded = recorded[coremetadata.AnnotationAgentProjectLinkRulesDigest]
	launch.project = c.loadProjectGuidance(project, recorded)
	return launch
}

// PlanCodexProjectLinks reads project's current rules for a Codex fresh
// create and writes their snapshot, exactly as PlanProjectLinks does for a
// Claude create. A Codex resume never asks: a thread keeps the developer
// instructions it was started with.
func (c *aiCommand) PlanCodexProjectLinks(project coremetadata.Project) projectLinksLaunch {
	if !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return projectLinksLaunch{}
	}
	launch := c.loadProjectLinks(project)
	launch.project = c.loadProjectGuidance(project, nil)
	return launch
}

// loadProjectLinks is the active launch of project's current rules: their
// rendered text, digest and content-addressed snapshot, or why they could not
// be read.
func (c *aiCommand) loadProjectLinks(project coremetadata.Project) projectLinksLaunch {
	launch := projectLinksLaunch{active: true}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.store = projectlinks.NewDefaultSnapshotStore(paths)
	rules, err := projectlinks.NewDefaultStore(paths).Load(project.Metadata.UID)
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
	launch.digest, launch.snapshotPath, launch.text = snapshot.Digest, snapshot.Path, rendered
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
	var prefix []byte
	if p.name != "" {
		prefix = []byte(p.content)
	}
	if l.project.on() {
		snapshot, err := l.project.store.WriteComposite(prefix, l.project.digest)
		if err != nil {
			l.project.unavailable = err
		} else {
			l.systemPromptFile = snapshot.Path
			prefix = []byte(l.project.developerInstructions(string(prefix)))
		}
	}
	if !l.active || l.unavailable != nil || l.digest == "" {
		return l
	}
	if len(prefix) == 0 {
		l.systemPromptFile = l.snapshotPath
		return l
	}
	composite, err := l.store.WriteComposite(prefix, l.digest)
	if err != nil {
		l.unavailable = err
		return l
	}
	l.systemPromptFile = composite.Path
	return l
}

// developerInstructions are the developer instructions a Codex fresh create
// starts its thread with, given persona, what it would send without rules
// ("" for none): persona, then projectlinks.CompositeSeparator and the rules,
// each part present only when the create has it. Without rules it is persona
// itself, so a create in a Project without rules sends exactly what it sent
// before.
func (l projectLinksLaunch) developerInstructions(persona string) string {
	persona = l.project.developerInstructions(persona)
	if !l.active || l.unavailable != nil || l.digest == "" {
		return persona
	}
	if persona == "" {
		return string(l.text)
	}
	return persona + projectlinks.CompositeSeparator + string(l.text)
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without rules it returns base itself, so a create in a Project without rules
// stores exactly what it stored before -- nil included.
func (l projectLinksLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	base = l.project.withCreateAnnotation(base)
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
	base = l.project.resumeLaunchAnnotations(base)
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
	if err := l.project.record(registry, mutator, agentUID); err != nil {
		return err
	}
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
		return l.project.notice(label)
	}
	notice := projectLinksNotice(label, l.unavailable)
	if projectNotice := l.project.notice(label); projectNotice != "" {
		notice += "\n" + projectNotice
	}
	return notice
}

func projectLinksNotice(label string, err error) string {
	var projectErr *projectGuidanceUnavailable
	if errors.As(err, &projectErr) {
		return projectGuidanceNotice(label, err)
	}
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

// readPersonaSnapshot reads the persona snapshot at path, bounded like every
// persona read. The file is opened inside its own directory, the snapshot
// directory RecordedSnapshotPath already proved it a regular file of, so
// the read cannot leave that directory.
func readPersonaSnapshot(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no instructions snapshot at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no instructions snapshot at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return persona.ReadLimited(file)
}
