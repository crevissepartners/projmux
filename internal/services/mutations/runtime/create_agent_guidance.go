package runtime

import (
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/core/agentguidance"
"github.com/crevissepartners/projmux/internal/core/persona"
"github.com/crevissepartners/projmux/internal/core/projectlinks"
"io"
"io/fs"
"maps"
"os"
"path/filepath"
"strings"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentGuidanceLaunch is what one launch does with the agent guidance. The
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
// (codexAgentIdentity), then the persona, Project instructions and rules. The identity goes
// only with the guidance.
type agentGuidanceLaunch struct {
	active bool
	store  agentguidance.Store
	// recorded is the digest the Agent records, "" when it records none.
	recorded string
	// digest, snapshotPath and text are the current guidance, all empty when
	// the guidance is off. text is sent only to a Codex thread and is never
	// put in an argv or the Registry.
	digest       string
	snapshotPath string
	text         []byte
	// systemPromptFile is the one file a fresh create hands Claude: the
	// guidance snapshot, or the composite of the guidance and the file the
	// create would otherwise pass. Resumes find theirs in the seam from the
	// digest they launch with.
	systemPromptFile string
	// unavailable is why the current guidance could not be read or composed.
	// The launch goes ahead without guidance and nothing is recorded.
	unavailable error
}

// on reports that the launch sends the current guidance: active, readable
// and not off.
func (l agentGuidanceLaunch) on() bool {
	return l.active && l.unavailable == nil && l.digest != ""
}

// changed reports that the Agent's recorded digest is not the current one:
// guidance added, changed or turned off.
func (l agentGuidanceLaunch) changed() bool {
	return l.active && l.unavailable == nil && l.digest != l.recorded
}

// withCreateFile sets systemPromptFile for a fresh create whose launch would
// otherwise pass base ("" for none): the rules snapshot or the persona and
// rules composite, else the persona snapshot. A composite that cannot be
// written makes the guidance unavailable, so the create still launches, with
// base alone.
func (l agentGuidanceLaunch) withCreateFile(base string) agentGuidanceLaunch {
	if !l.on() {
		return l
	}
	if base == "" {
		l.systemPromptFile = l.snapshotPath
		return l
	}
	composite, err := composeAgentGuidance(l.store, l.digest, base)
	if err != nil {
		l.unavailable = err
		return l
	}
	l.systemPromptFile = composite
	return l
}

// developerInstructions are the developer instructions a Codex fresh create
// starts its thread with, given persona, what it would send without guidance
// (the persona content and the Project's label link rules, "" for none): the
// guidance, then projectlinks.CompositeSeparator and persona, each part
// present only when the create has it. Without guidance it is persona itself,
// so a create with the guidance off sends exactly what it sent before.
func (l agentGuidanceLaunch) developerInstructions(persona string) string {
	if !l.on() {
		return persona
	}
	if persona == "" {
		return string(l.text)
	}
	return string(l.text) + projectlinks.CompositeSeparator + persona
}

// codexDeveloperInstructions are the developer instructions a Codex fresh
// create of agentUID starts its thread with, given rest, what it would send
// without guidance (the persona content and the Project's label link rules,
// "" for none): the guidance, the Agent's identity paragraph, then rest, each
// part joined by projectlinks.CompositeSeparator. Without guidance it is rest
// itself, identity left out, so a create with the guidance off sends exactly
// what it sent before.
func (l agentGuidanceLaunch) codexDeveloperInstructions(agentUID, rest string) string {
	if !l.on() {
		return rest
	}
	identity := codexAgentIdentity(agentUID)
	if rest != "" {
		identity += projectlinks.CompositeSeparator + rest
	}
	return l.developerInstructions(identity)
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without guidance it returns base itself, so a create with the guidance off
// stores exactly what it stored before -- nil included.
func (l agentGuidanceLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	if !l.on() || base[coremetadata.AnnotationAgentGuidanceDigest] == l.digest {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentGuidanceDigest] = l.digest
	return out
}

// resumeLaunchAnnotations are the annotations a resume launch reads, given
// the ones it would otherwise read. Guidance that changed puts the current
// digest (or no digest) and the snapshot mode off in place, which is exactly
// what record writes, so the launch reads what the Agent will record.
// Unreadable guidance drops the digest so the Agent launches without guidance
// while its record stays as it was. Otherwise base itself is returned.
func (l agentGuidanceLaunch) resumeLaunchAnnotations(base map[string]string) map[string]string {
	if !l.active {
		return base
	}
	if l.unavailable != nil {
		if _, ok := base[coremetadata.AnnotationAgentGuidanceDigest]; !ok {
			return base
		}
		out := maps.Clone(base)
		delete(out, coremetadata.AnnotationAgentGuidanceDigest)
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
		delete(out, coremetadata.AnnotationAgentGuidanceDigest)
	} else {
		out[coremetadata.AnnotationAgentGuidanceDigest] = l.digest
	}
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	return out
}

// record writes, inside the resume transaction, the digest and snapshot mode
// resumeLaunchAnnotations launched with. Nothing changes when the guidance is
// the recorded one or could not be read.
func (l agentGuidanceLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.changed() {
		return nil
	}
	if _, err := mutator.SetAgentGuidance(registry, agentUID, l.digest); err != nil {
		return MapMetadataError(err)
	}
	return nil
}

// notice is the one-line disclosure of guidance the launch could not read, or
// "" when there is nothing to disclose. label names the Agent the way the
// persona notice does.
func (l agentGuidanceLaunch) notice(label string) string {
	if !l.active || l.unavailable == nil {
		return ""
	}
	return agentGuidanceNotice(label, l.unavailable)
}

// PlanAgentGuidance reads the current guidance and writes its
// content-addressed snapshot. recorded are the Agent annotations the launch
// compares against (nil on a fresh create).
func (c *aiCommand) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	if normalizeAIMode(provider) != aiModeClaude {
		return agentGuidanceLaunch{}
	}
	launch := c.loadAgentGuidance()
	launch.recorded = recorded[coremetadata.AnnotationAgentGuidanceDigest]
	return launch
}

// PlanCodexAgentGuidance reads the current guidance for a Codex fresh create
// and writes its snapshot, exactly as PlanAgentGuidance does for a Claude
// create. A Codex resume never asks: a thread keeps the developer
// instructions it was started with.
func (c *aiCommand) PlanCodexAgentGuidance() agentGuidanceLaunch {
	return c.loadAgentGuidance()
}

// loadAgentGuidance is the active launch of the current guidance: its text,
// digest and content-addressed snapshot, or why it could not be read.
func (c *aiCommand) loadAgentGuidance() agentGuidanceLaunch {
	return c.loadAgentGuidanceWith(agentguidance.Store.Load)
}

func (c *aiCommand) PlanProcessAgentGuidance() agentGuidanceLaunch {
	return c.loadAgentGuidanceWith(agentguidance.Store.LoadProcess)
}

func (c *aiCommand) loadAgentGuidanceWith(load func(agentguidance.Store) (agentguidance.Guidance, error)) agentGuidanceLaunch {
	launch := agentGuidanceLaunch{active: true}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.store = agentguidance.NewDefaultStore(paths)
	guidance, err := load(launch.store)
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
	launch.digest, launch.snapshotPath, launch.text = snapshot.Digest, snapshot.Path, guidance.Text
	return launch
}

func agentGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without the agent guidance (%s): %v",
		label, agentGuidanceReasonUnavailable, err)
}

// codexAgentIdentity is the paragraph that tells a Codex Agent its own Agent
// UID and how to name itself and its targets: a Codex shell command runs in
// the shared app server, not in the Agent's pane, so projmux cannot infer
// either from the environment.
func codexAgentIdentity(agentUID string) string {
	examples := make([]string, 0, len(codexSocketCommands))
	for _, command := range codexSocketCommands {
		examples = append(examples, "`projmux "+command+"`")
	}
	return fmt.Sprintf(`# Your projmux identity

Your projmux Agent UID is `+"`%[1]s`"+`.

- When you create an agent with `+"`projmux create agent`"+`, add `+"`--creator uid:%[1]s`"+` so projmux records you as its creator.
- When you send a message with `+"`projmux agent message send`"+`, add `+"`--source uid:%[1]s`"+` so the message is sent from you.
- Your shell commands run outside your own pane, in an app server shared with other agents, so projmux cannot tell your pane or agent from the environment: name the pane or agent a command acts on. Add `+"`--socket projmux`"+` only to a command that defines that flag, such as %[2]s; do not add it to %[3]s, which has no such flag and uses the `+"`%[4]s`"+` app socket itself.
`, agentUID, strings.Join(examples, " or "), "`projmux "+codexNoSocketCommand+"`", defaultAppSocket)
}

// composeAgentGuidance writes the composite of the guidance digest names and
// the content of base, and returns its path.
func composeAgentGuidance(store agentguidance.Store, digest, base string) (string, error) {
	content, err := readSystemPromptPart(base)
	if err != nil {
		return "", err
	}
	composite, err := store.WriteComposite(digest, content)
	if err != nil {
		return "", err
	}
	return composite.Path, nil
}

// readSystemPromptPart reads one file a Claude launch would pass as its
// system prompt (a persona snapshot, a rules snapshot, or their composite),
// bounded like every rules snapshot read. The file is opened inside its own
// directory, so the read cannot leave that directory.
func readSystemPromptPart(path string) ([]byte, error) {
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

func projectLinksNotice(label string, err error) string {
	var projectErr *projectGuidanceUnavailable
	if errors.As(err, &projectErr) {
		return projectGuidanceNotice(label, err)
	}
	return fmt.Sprintf("projmux: agent/%s launched without its Project's label link rules (%s): %v",
		label, projectLinksReasonUnavailable, err)
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

func projectGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without its Project's common instructions (%s): %v", label, projectGuidanceReasonUnavailable, err)
}

// projectGuidanceUnavailable distinguishes a missing recorded Project snapshot
// from a missing link-rule snapshot at the shared composite-file seam.
type projectGuidanceUnavailable struct{ error }

// codexSocketCommands are the commands the identity paragraph names as taking
// `--socket projmux`. Each one defines a socket flag; a static test holds
// them to that.
var codexSocketCommands = []string{"delete pane", "agent relaunch"}

// codexNoSocketCommand is the command the identity paragraph tells a Codex
// Agent not to pass `--socket` to: it defines no socket flag, and outside
// tmux it routes to the app socket itself (defaultRuntimeMutationRoute).
const codexNoSocketCommand = "create agent"
