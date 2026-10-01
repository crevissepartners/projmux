package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// agentGuidanceReasonUnavailable is the reason token of a launch that went
// ahead without the agent guidance.
const agentGuidanceReasonUnavailable = "agent-guidance-unavailable"

// agentGuidancePlanner is the optional launcher seam that reads the current
// agent guidance for a Claude launch. The production launcher (*aiCommand)
// implements it; a launcher that does not is a launch without guidance, which
// is exactly the launch every Agent had before the guidance existed.
type agentGuidancePlanner interface {
	PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch
}

var _ agentGuidancePlanner = (*aiCommand)(nil)

// codexAgentGuidancePlanner is the optional launcher seam that reads the
// current agent guidance for a Codex fresh create, the one Codex lane that
// starts a thread of its own. A launcher that does not implement it gives a
// Codex create no guidance, exactly as before the guidance reached Codex.
type codexAgentGuidancePlanner interface {
	PlanCodexAgentGuidance() agentGuidanceLaunch
}

var _ codexAgentGuidancePlanner = (*aiCommand)(nil)

// agentGuidanceLaunch is what one launch does with the agent guidance. The
// zero value is "not applicable" (another provider or lane, the reply-only
// lane, or a launcher without the seam) and changes no argv, no developer
// instructions and no annotation.
//
// The guidance is the first part of the one --append-system-prompt-file a
// Claude launch passes: guidance, then the persona, then the Project's label
// link rules, each part present only when the launch has it and the parts
// joined by projectlinks.CompositeSeparator. A Codex fresh create sends it
// the same way, ahead of the persona and the rules, as the thread's developer
// instructions (developerInstructions).
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
	launch := agentGuidanceLaunch{active: true}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		launch.unavailable = err
		return launch
	}
	launch.store = agentguidance.NewDefaultStore(paths)
	guidance, err := launch.store.Load()
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
	if !l.active || l.unavailable != nil || l.digest == "" {
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
	if !l.active || l.unavailable != nil || l.digest == "" {
		return persona
	}
	if persona == "" {
		return string(l.text)
	}
	return string(l.text) + projectlinks.CompositeSeparator + persona
}

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without guidance it returns base itself, so a create with the guidance off
// stores exactly what it stored before -- nil included.
func (l agentGuidanceLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	if !l.active || l.unavailable != nil || l.digest == "" || base[coremetadata.AnnotationAgentGuidanceDigest] == l.digest {
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

func agentGuidanceNotice(label string, err error) string {
	return fmt.Sprintf("projmux: agent/%s launched without the agent guidance (%s): %v",
		label, agentGuidanceReasonUnavailable, err)
}

// planAgentGuidanceWith asks launcher for the guidance when it has the seam,
// and returns the zero launch otherwise.
func planAgentGuidanceWith(launcher any, provider string, recorded map[string]string) agentGuidanceLaunch {
	planner, ok := launcher.(agentGuidancePlanner)
	if !ok {
		return agentGuidanceLaunch{}
	}
	return planner.PlanAgentGuidance(provider, recorded)
}

// planCodexAgentGuidanceWith asks launcher for a Codex fresh create's guidance
// when it has the seam, and returns the zero launch otherwise.
func planCodexAgentGuidanceWith(launcher any) agentGuidanceLaunch {
	planner, ok := launcher.(codexAgentGuidancePlanner)
	if !ok {
		return agentGuidanceLaunch{}
	}
	return planner.PlanCodexAgentGuidance()
}

// prepareAgentGuidance resolves the guidance one create launches with into
// flags.agentGuidance; its notice is disclosed once the Agent has a name.
// It runs after prepareProjectLinks, because the guidance goes in front of the
// file that prepared. A resume-picker create joins a conversation whose
// recorded system prompt lacks the guidance, so it launches (and records) the
// digest with the snapshot mode off; a fresh create records only the digest.
// A Codex fresh create (nativeCodexFreshCreateRequired) sends the guidance as
// its thread's developer instructions and records the digest; it passes no
// file. The reply-only lane, every other Codex lane and every other provider
// are left exactly as they were.
func (c *createCommand) prepareAgentGuidance(provider string, flags *resourceCreateFlags) {
	flags.agentGuidance = agentGuidanceLaunch{}
	if provider == aiModeCodex && nativeCodexFreshCreateRequired(provider, *flags) {
		flags.agentGuidance = planCodexAgentGuidanceWith(c.agents)
		return
	}
	if provider != aiModeClaude || flags.dialogueReplyOnly {
		return
	}
	var launcher any = c.agents
	if flags.resumeConversation != "" && c.resumes != nil {
		launcher = c.resumes
	}
	guidance := planAgentGuidanceWith(launcher, provider, flags.resumeLaunchValues)
	if flags.resumeConversation != "" {
		flags.resumeLaunchValues = guidance.resumeLaunchAnnotations(flags.resumeLaunchValues)
	} else {
		base := flags.projectLinks.systemPromptFile
		if base == "" && flags.personaLaunch.name != "" {
			base = flags.personaLaunch.snapshot.Path
		}
		guidance = guidance.withCreateFile(base)
	}
	flags.agentGuidance = guidance
}

// resumeGuidanceSystemPromptFile is the one --append-system-prompt-file a
// Claude resume passes, given baseFile, the file it would pass without
// guidance ("" for none): baseFile alone, the guidance snapshot the digest
// annotation names, or the composite of the guidance and baseFile. Guidance
// that cannot be found is dropped and disclosed; baseFile is kept.
func (c *aiCommand) resumeGuidanceSystemPromptFile(mode string, annotations map[string]string, baseFile string) (string, error) {
	digest := annotations[coremetadata.AnnotationAgentGuidanceDigest]
	if mode != aiModeClaude || digest == "" {
		return baseFile, nil
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return baseFile, err
	}
	store := agentguidance.NewDefaultStore(paths)
	if baseFile == "" {
		return store.RecordedSnapshotPath(digest)
	}
	composite, err := composeAgentGuidance(store, digest, baseFile)
	if err != nil {
		return baseFile, err
	}
	return composite, nil
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
