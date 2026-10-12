package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

type GuidanceService struct{ Paths func() (config.Paths, error) }

// projectLinksReasonUnavailable is the reason token of a launch that went
// ahead without its Project's label link rules.
const projectLinksReasonUnavailable = "project-link-rules-unavailable"

// agentGuidanceReasonUnavailable is the reason token of a launch that went
// ahead without the agent guidance.
const agentGuidanceReasonUnavailable = "agent-guidance-unavailable"

// AgentGuidanceLaunch is what one launch does with the agent guidance. The
// zero value is "not applicable" (another provider or lane, the reply-only
// lane, or a launcher without the seam) and changes no argv, no developer
// instructions and no annotation.
//
// The guidance is the first part of the one --append-system-prompt-file a
// Claude launch passes: guidance, then the persona, Project common instructions,
// then the Project's label
// link rules, each part present only when the launch has it and the parts
// joined by projectlinks.CompositeSeparator. A Codex fresh create sends it
// the same way as the thread's developer instructions
// (codexDeveloperInstructions): guidance, then the Agent's identity paragraph
// (CodexAgentIdentity), then the persona, Project instructions and rules. The identity goes
// only with the guidance.
type AgentGuidanceLaunch struct {
	Active bool
	Store  agentguidance.Store
	// recorded is the digest the Agent records, "" when it records none.
	Recorded string
	// digest, snapshotPath and text are the current guidance, all empty when
	// the guidance is off. text is sent only to a Codex thread and is never
	// put in an argv or the Registry.
	Digest       string
	SnapshotPath string
	Text         []byte
	// systemPromptFile is the one file a fresh create hands Claude: the
	// guidance snapshot, or the composite of the guidance and the file the
	// create would otherwise pass. Resumes find theirs in the seam from the
	// digest they launch with.
	SystemPromptFile string
	// unavailable is why the current guidance could not be read or composed.
	// The launch goes ahead without guidance and nothing is recorded.
	Unavailable error
}

// on reports that the launch sends the current guidance: active, readable
// and not off.
func (l AgentGuidanceLaunch) On() bool {
	return l.Active && l.Unavailable == nil && l.Digest != ""
}

// changed reports that the Agent's recorded digest is not the current one:
// guidance added, changed or turned off.
func (l AgentGuidanceLaunch) Changed() bool {
	return l.Active && l.Unavailable == nil && l.Digest != l.Recorded
}

// withCreateFile sets systemPromptFile for a fresh create whose launch would
// otherwise pass base ("" for none): the rules snapshot or the persona and
// rules composite, else the persona snapshot. A composite that cannot be
// written makes the guidance unavailable, so the create still launches, with
// base alone.
func (l AgentGuidanceLaunch) WithCreateFile(base string) AgentGuidanceLaunch {
	if !l.On() {
		return l
	}
	if base == "" {
		l.SystemPromptFile = l.SnapshotPath
		return l
	}
	composite, err := ComposeAgentGuidance(l.Store, l.Digest, base)
	if err != nil {
		l.Unavailable = err
		return l
	}
	l.SystemPromptFile = composite
	return l
}

// developerInstructions are the developer instructions a Codex fresh create
// starts its thread with, given persona, what it would send without guidance
// (the persona content and the Project's label link rules, "" for none): the
// guidance, then projectlinks.CompositeSeparator and persona, each part
// present only when the create has it. Without guidance it is persona itself,
// so a create with the guidance off sends exactly what it sent before.
func (l AgentGuidanceLaunch) DeveloperInstructions(persona string) string {
	if !l.On() {
		return persona
	}
	if persona == "" {
		return string(l.Text)
	}
	return string(l.Text) + projectlinks.CompositeSeparator + persona
}

// codexDeveloperInstructions are the developer instructions a Codex fresh
// create of agentUID starts its thread with, given rest, what it would send
// without guidance (the persona content and the Project's label link rules,
// "" for none): the guidance, the Agent's identity paragraph, then rest, each
// part joined by projectlinks.CompositeSeparator. Without guidance it is rest
// itself, identity left out, so a create with the guidance off sends exactly
// what it sent before.
func (l AgentGuidanceLaunch) CodexDeveloperInstructions(agentUID, rest string) string {
	if !l.On() {
		return rest
	}
	identity := CodexAgentIdentity(agentUID)
	if rest != "" {
		identity += projectlinks.CompositeSeparator + rest
	}
	return l.DeveloperInstructions(identity)
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without guidance it returns base itself, so a create with the guidance off
// stores exactly what it stored before -- nil included.
func (l AgentGuidanceLaunch) WithCreateAnnotation(base map[string]string) map[string]string {
	if !l.On() || base[coremetadata.AnnotationAgentGuidanceDigest] == l.Digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentGuidanceDigest] = l.Digest
	return out
}

// resumeLaunchAnnotations are the annotations a resume launch reads, given
// the ones it would otherwise read. Guidance that changed puts the current
// digest (or no digest) and the snapshot mode off in place, which is exactly
// what record writes, so the launch reads what the Agent will record.
// Unreadable guidance drops the digest so the Agent launches without guidance
// while its record stays as it was. Otherwise base itself is returned.
func (l AgentGuidanceLaunch) ResumeLaunchAnnotations(base map[string]string) map[string]string {
	if !l.Active {
		return base
	}
	if l.Unavailable != nil {
		if _, ok := base[coremetadata.AnnotationAgentGuidanceDigest]; !ok {
			return base
		}
		out := maps.Clone(base)
		delete(out, coremetadata.AnnotationAgentGuidanceDigest)
		return out
	}
	if !l.Changed() {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	if l.Digest == "" {
		delete(out, coremetadata.AnnotationAgentGuidanceDigest)
	} else {
		out[coremetadata.AnnotationAgentGuidanceDigest] = l.Digest
	}
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

// record writes, inside the resume transaction, the digest and snapshot mode
// resumeLaunchAnnotations launched with. Nothing changes when the guidance is
// the recorded one or could not be read.
func (l AgentGuidanceLaunch) Record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.Changed() {
		return nil
	}
	if _, err := mutator.SetAgentGuidance(registry, agentUID, l.Digest); err != nil {
		return MapMetadataError(err)
	}
	return nil
}

// notice is the one-line disclosure of guidance the launch could not read, or
// "" when there is nothing to disclose. label names the Agent the way the
// persona notice does.
func (l AgentGuidanceLaunch) Notice(label string) string {
	if !l.Active || l.Unavailable == nil {
		return ""
	}
	return AgentGuidanceNotice(label, l.Unavailable)
}

// PlanAgentGuidance reads the current guidance and writes its
// content-addressed snapshot. recorded are the Agent annotations the launch
// compares against (nil on a fresh create).
func (c *GuidanceService) PlanAgentGuidance(provider string, recorded map[string]string) AgentGuidanceLaunch {
	if NormalizeAIMode(provider) != "claude" {
		return AgentGuidanceLaunch{}
	}
	launch := c.LoadAgentGuidance()
	launch.Recorded = recorded[coremetadata.AnnotationAgentGuidanceDigest]
	return launch
}

// PlanCodexAgentGuidance reads the current guidance for a Codex fresh create
// and writes its snapshot, exactly as PlanAgentGuidance does for a Claude
// create. A Codex resume never asks: a thread keeps the developer
// instructions it was started with.
func (c *GuidanceService) PlanCodexAgentGuidance() AgentGuidanceLaunch {
	return c.LoadAgentGuidance()
}

// loadAgentGuidance is the active launch of the current guidance: its text,
// digest and content-addressed snapshot, or why it could not be read.
func (c *GuidanceService) LoadAgentGuidance() AgentGuidanceLaunch {
	return c.LoadAgentGuidanceWith(agentguidance.Store.Load)
}

func (c *GuidanceService) PlanProcessAgentGuidance() AgentGuidanceLaunch {
	return c.LoadAgentGuidanceWith(agentguidance.Store.LoadProcess)
}

func (c *GuidanceService) LoadAgentGuidanceWith(load func(agentguidance.Store) (agentguidance.Guidance, error)) AgentGuidanceLaunch {
	launch := AgentGuidanceLaunch{Active: true}
	paths, err := c.Paths()
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Store = agentguidance.NewDefaultStore(paths)
	guidance, err := load(launch.Store)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	if guidance.Off() {
		return launch
	}
	snapshot, err := launch.Store.WriteSnapshot(guidance.Text)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Digest, launch.SnapshotPath, launch.Text = snapshot.Digest, snapshot.Path, guidance.Text
	return launch
}

func AgentGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without the agent guidance (%s): %v",
		label, agentGuidanceReasonUnavailable, err)
}

// CodexAgentIdentity is the paragraph that tells a Codex Agent its own Agent
// UID and how to name itself and its targets: a Codex shell command runs in
// the shared app server, not in the Agent's pane, so projmux cannot infer
// either from the environment.
func CodexAgentIdentity(agentUID string) string {
	examples := make([]string, 0, len(CodexSocketCommands))
	for _, command := range CodexSocketCommands {
		examples = append(examples, "`projmux "+command+"`")
	}
	return fmt.Sprintf(`# Your projmux identity

Your projmux Agent UID is `+"`%[1]s`"+`.

- When you create an agent with `+"`projmux create agent`"+`, add `+"`--creator uid:%[1]s`"+` so projmux records you as its creator.
- When you send a message with `+"`projmux agent message send`"+`, add `+"`--source uid:%[1]s`"+` so the message is sent from you.
- Your shell commands run outside your own pane, in an app server shared with other agents, so projmux cannot tell your pane or agent from the environment: name the pane or agent a command acts on. Add `+"`--socket projmux`"+` only to a command that defines that flag, such as %[2]s; do not add it to %[3]s, which has no such flag and uses the `+"`%[4]s`"+` app socket itself.
`, agentUID, strings.Join(examples, " or "), "`projmux "+CodexNoSocketCommand+"`", DefaultAppSocket)
}

// ComposeAgentGuidance writes the composite of the guidance digest names and
// the content of base, and returns its path.
func ComposeAgentGuidance(store agentguidance.Store, digest, base string) (string, error) {
	content, err := ReadSystemPromptPart(base)
	if err != nil {
		return "", err
	}
	composite, err := store.WriteComposite(digest, content)
	if err != nil {
		return "", err
	}
	return composite.Path, nil
}

// ReadSystemPromptPart reads one file a Claude launch would pass as its
// system prompt (a persona snapshot, a rules snapshot, or their composite),
// bounded like every rules snapshot read. The file is opened inside its own
// directory, so the read cannot leave that directory.
func ReadSystemPromptPart(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no system prompt file at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no system prompt file at %s", path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, projectlinks.MaxSnapshotSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > projectlinks.MaxSnapshotSize {
		return nil, fmt.Errorf("system prompt file %s is larger than %d bytes", path, projectlinks.MaxSnapshotSize)
	}
	return content, nil
}

// ProjectLinksLaunch is what one launch does with its Project's label link
// rules. The zero value is "not applicable" (another provider or lane, the
// reply-only lane, no Project, or a launcher without the seam) and changes no
// argv, no developer instructions and no annotation.
//
// The Project common instructions precede the rules, independently of whether
// rules exist. A Claude launch passes the rules last in its one
// --append-system-prompt-file. A Codex fresh create sends them the same way,
// after the persona, as the thread's developer instructions
// (developerInstructions).
type ProjectLinksLaunch struct {
	// project is the independent common-instruction layer before link rules.
	Project ProjectGuidanceLaunch
	Active  bool
	Store   projectlinks.SnapshotStore
	// recorded is the digest the Agent records, "" when it records none.
	Recorded string
	// digest, snapshotPath and text are the Project's current rendered rules,
	// all empty when the Project has no rules. text is sent only to a Codex
	// thread and is never put in an argv or the Registry.
	Digest       string
	SnapshotPath string
	Text         []byte
	// systemPromptFile is the one file a fresh create hands Claude: the rules
	// snapshot, or the composite of the persona and the rules. Resumes find
	// theirs in the seam from the digest they launch with.
	SystemPromptFile string
	// unavailable is why the current rules could not be read. The launch goes
	// ahead without rules and nothing is recorded.
	Unavailable error
}

// changed reports that the Agent's recorded digest is not the current one:
// rules added, changed or removed.
func (l ProjectLinksLaunch) Changed() bool {
	return l.Active && l.Unavailable == nil && l.Digest != l.Recorded
}

// withCreateFile sets systemPromptFile for a fresh create that also carries
// the persona p. A composite that cannot be written makes the rules
// unavailable, so the create still launches, with the persona alone.
func (l ProjectLinksLaunch) WithCreateFile(p PersonaLaunch) ProjectLinksLaunch {
	var prefix []byte
	if p.Name != "" {
		prefix = []byte(p.Content)
	}
	if l.Project.On() {
		snapshot, err := l.Project.Store.WriteComposite(prefix, l.Project.Digest)
		if err != nil {
			l.Project.Unavailable = err
		} else {
			l.SystemPromptFile = snapshot.Path
			prefix = []byte(l.Project.DeveloperInstructions(string(prefix)))
		}
	}
	if !l.Active || l.Unavailable != nil || l.Digest == "" {
		return l
	}
	if len(prefix) == 0 {
		l.SystemPromptFile = l.SnapshotPath
		return l
	}
	composite, err := l.Store.WriteComposite(prefix, l.Digest)
	if err != nil {
		l.Unavailable = err
		return l
	}
	l.SystemPromptFile = composite.Path
	return l
}

// developerInstructions are the developer instructions a Codex fresh create
// starts its thread with, given persona, what it would send without rules
// ("" for none): persona, then projectlinks.CompositeSeparator and the rules,
// each part present only when the create has it. Without rules it is persona
// itself, so a create in a Project without rules sends exactly what it sent
// before.
func (l ProjectLinksLaunch) DeveloperInstructions(persona string) string {
	persona = l.Project.DeveloperInstructions(persona)
	if !l.Active || l.Unavailable != nil || l.Digest == "" {
		return persona
	}
	if persona == "" {
		return string(l.Text)
	}
	return persona + projectlinks.CompositeSeparator + string(l.Text)
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without rules it returns base itself, so a create in a Project without rules
// stores exactly what it stored before -- nil included.
func (l ProjectLinksLaunch) WithCreateAnnotation(base map[string]string) map[string]string {
	base = l.Project.WithCreateAnnotation(base)
	if !l.Active || l.Unavailable != nil || l.Digest == "" || base[coremetadata.AnnotationAgentProjectLinkRulesDigest] == l.Digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentProjectLinkRulesDigest] = l.Digest
	return out
}

// resumeLaunchAnnotations are the annotations a resume launch reads, given
// the ones it would otherwise read. Rules that changed put the current digest
// (or no digest) and the snapshot mode off in place, which is exactly what
// record writes, so the launch reads what the Agent will record. Unreadable
// rules drop the digest so the Agent launches without rules while its record
// stays as it was. Otherwise base itself is returned.
func (l ProjectLinksLaunch) ResumeLaunchAnnotations(base map[string]string) map[string]string {
	base = l.Project.ResumeLaunchAnnotations(base)
	if !l.Active {
		return base
	}
	if l.Unavailable != nil {
		if _, ok := base[coremetadata.AnnotationAgentProjectLinkRulesDigest]; !ok {
			return base
		}
		out := maps.Clone(base)
		delete(out, coremetadata.AnnotationAgentProjectLinkRulesDigest)
		return out
	}
	if !l.Changed() {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	if l.Digest == "" {
		delete(out, coremetadata.AnnotationAgentProjectLinkRulesDigest)
	} else {
		out[coremetadata.AnnotationAgentProjectLinkRulesDigest] = l.Digest
	}
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

// record writes, inside the resume transaction, the digest and snapshot mode
// resumeLaunchAnnotations launched with. Nothing changes when the rules are
// the recorded ones or could not be read.
func (l ProjectLinksLaunch) Record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if err := l.Project.Record(registry, mutator, agentUID); err != nil {
		return err
	}
	if !l.Changed() {
		return nil
	}
	if _, err := mutator.SetAgentProjectLinkRules(registry, agentUID, l.Digest); err != nil {
		return MapMetadataError(err)
	}
	return nil
}

// notice is the one-line disclosure of rules the launch could not read, or ""
// when there is nothing to disclose. label names the Agent the way the
// persona notice does.
func (l ProjectLinksLaunch) Notice(label string) string {
	if !l.Active || l.Unavailable == nil {
		return l.Project.Notice(label)
	}
	notice := ProjectLinksNotice(label, l.Unavailable)
	if projectNotice := l.Project.Notice(label); projectNotice != "" {
		notice += "\n" + projectNotice
	}
	return notice
}

// PlanProjectLinks reads project's current rules, renders them with the
// Project's variables and writes their content-addressed snapshot. recorded
// are the Agent annotations the launch compares against (nil on a fresh
// create).
func (c *GuidanceService) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) ProjectLinksLaunch {
	if NormalizeAIMode(provider) != "claude" || !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return ProjectLinksLaunch{}
	}
	launch := c.LoadProjectLinks(project)
	launch.Recorded = recorded[coremetadata.AnnotationAgentProjectLinkRulesDigest]
	launch.Project = c.LoadProjectGuidance(project, recorded)
	return launch
}

// PlanCodexProjectLinks reads project's current rules for a Codex fresh
// create and writes their snapshot, exactly as PlanProjectLinks does for a
// Claude create. A Codex resume never asks: a thread keeps the developer
// instructions it was started with.
func (c *GuidanceService) PlanCodexProjectLinks(project coremetadata.Project) ProjectLinksLaunch {
	if !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return ProjectLinksLaunch{}
	}
	launch := c.LoadProjectLinks(project)
	launch.Project = c.LoadProjectGuidance(project, nil)
	return launch
}

// loadProjectLinks is the active launch of project's current rules: their
// rendered text, digest and content-addressed snapshot, or why they could not
// be read.
func (c *GuidanceService) LoadProjectLinks(project coremetadata.Project) ProjectLinksLaunch {
	launch := ProjectLinksLaunch{Active: true}
	paths, err := c.Paths()
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Store = projectlinks.NewDefaultSnapshotStore(paths)
	rules, err := projectlinks.NewDefaultStore(paths).Load(project.Metadata.UID)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	rendered := projectlinks.Render(rules, projectlinks.ProjectOf(project))
	if len(rendered) == 0 {
		return launch
	}
	snapshot, err := launch.Store.WriteSnapshot(rendered)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Digest, launch.SnapshotPath, launch.Text = snapshot.Digest, snapshot.Path, rendered
	return launch
}

func ProjectLinksNotice(label string, err error) string {
	var projectErr *ProjectGuidanceUnavailable
	if errors.As(err, &projectErr) {
		return ProjectGuidanceNotice(label, err)
	}
	return fmt.Sprintf("projmux: agent/%s launched without its Project's label link rules (%s): %v",
		label, projectLinksReasonUnavailable, err)
}

// ReadPersonaSnapshot reads the persona snapshot at path, bounded like every
// persona read. The file is opened inside its own directory, the snapshot
// directory RecordedSnapshotPath already proved it a regular file of, so
// the read cannot leave that directory.
func ReadPersonaSnapshot(path string) ([]byte, error) {
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

// ProjectGuidanceLaunch travels with the Project prompt layer (ProjectLinksLaunch)
// so every create, resume and replay reads and records the same Project ownership.
// It is independent of both global guidance and link rules being enabled.
type ProjectGuidanceLaunch struct {
	Active           bool
	Store            agentguidance.ProjectStore
	Recorded, Digest string
	Text             []byte
	Unavailable      error
}

func (l ProjectGuidanceLaunch) On() bool { return l.Active && l.Unavailable == nil && l.Digest != "" }

func (l ProjectGuidanceLaunch) Changed() bool {
	return l.Active && l.Unavailable == nil && l.Digest != l.Recorded
}

func (l ProjectGuidanceLaunch) DeveloperInstructions(persona string) string {
	if !l.On() {
		return persona
	}
	if persona == "" {
		return string(l.Text)
	}
	return persona + projectlinks.CompositeSeparator + string(l.Text)
}

func (l ProjectGuidanceLaunch) WithCreateAnnotation(base map[string]string) map[string]string {
	if !l.On() || base[coremetadata.AnnotationAgentProjectGuidanceDigest] == l.Digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string)
	}
	out[coremetadata.AnnotationAgentProjectGuidanceDigest] = l.Digest
	return out
}

func (l ProjectGuidanceLaunch) ResumeLaunchAnnotations(base map[string]string) map[string]string {
	if !l.Active || (!l.Changed() && l.Unavailable == nil) {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string)
	}
	if l.Digest == "" || l.Unavailable != nil {
		delete(out, coremetadata.AnnotationAgentProjectGuidanceDigest)
	} else {
		out[coremetadata.AnnotationAgentProjectGuidanceDigest] = l.Digest
	}
	// Even unavailable instructions must not revive their recorded provider
	// snapshot. The durable annotations are preserved until a readable launch.
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

func (l ProjectGuidanceLaunch) Record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.Changed() {
		return nil
	}
	_, err := mutator.SetAgentProjectGuidance(registry, agentUID, l.Digest)
	return MapMetadataError(err)
}

func (l ProjectGuidanceLaunch) Notice(label string) string {
	if !l.Active || l.Unavailable == nil {
		return ""
	}
	return ProjectGuidanceNotice(label, l.Unavailable)
}

func (c *GuidanceService) LoadProjectGuidance(project coremetadata.Project, recorded map[string]string) ProjectGuidanceLaunch {
	if !coremetadata.IsProjectUIDShaped(project.Metadata.UID) {
		return ProjectGuidanceLaunch{}
	}
	launch := ProjectGuidanceLaunch{Active: true, Recorded: recorded[coremetadata.AnnotationAgentProjectGuidanceDigest]}
	paths, err := c.Paths()
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Store = agentguidance.NewDefaultProjectStore(paths)
	guidance, err := launch.Store.Load(project.Metadata.UID)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	if guidance.Off() {
		return launch
	}
	snapshot, err := launch.Store.WriteSnapshot(guidance.Text)
	if err != nil {
		launch.Unavailable = err
		return launch
	}
	launch.Digest, launch.Text = snapshot.Digest, guidance.Text
	return launch
}

func ProjectGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without its Project's common instructions (%s): %v", label, ProjectGuidanceReasonUnavailable, err)
}

// ProjectGuidanceUnavailable distinguishes a missing recorded Project snapshot
// from a missing link-rule snapshot at the shared composite-file seam.
type ProjectGuidanceUnavailable struct{ error }

func (e *ProjectGuidanceUnavailable) Error() string {
	return ProjectGuidanceReasonUnavailable + ": " + e.error.Error()
}

// CodexSocketCommands are the commands the identity paragraph names as taking
// `--socket projmux`. Each one defines a socket flag; a static test holds
// them to that.
var CodexSocketCommands = []string{"delete pane", "agent relaunch"}

// CodexNoSocketCommand is the command the identity paragraph tells a Codex
// Agent not to pass `--socket` to: it defines no socket flag, and outside
// tmux it routes to the app socket itself (defaultRuntimeMutationRoute).
const CodexNoSocketCommand = "create agent"

const ProjectGuidanceReasonUnavailable = "project-guidance-unavailable"
