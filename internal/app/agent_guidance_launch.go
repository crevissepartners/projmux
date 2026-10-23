package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
// Claude launch passes: guidance, then the persona, Project common instructions,
// then the Project's label
// link rules, each part present only when the launch has it and the parts
// joined by projectlinks.CompositeSeparator. A Codex fresh create sends it
// the same way as the thread's developer instructions
// (codexDeveloperInstructions): guidance, then the Agent's identity paragraph
// (codexAgentIdentity), then the persona, Project instructions and rules. The identity goes
// only with the guidance.
type agentGuidanceLaunch struct {
	codexBaseFile   bool
	migrationNotice string
	active          bool
	store           agentguidance.Store
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
	return localAgentGuidanceLaunch(c.sharedGuidance().PlanAgentGuidance(provider, recorded))
}

// PlanCodexAgentGuidance reads the current guidance for a Codex fresh create
// and writes its snapshot, exactly as PlanAgentGuidance does for a Claude
// create. A Codex resume never asks: a thread keeps the developer
// instructions it was started with.
func (c *aiCommand) PlanCodexAgentGuidance() agentGuidanceLaunch {
	return localAgentGuidanceLaunch(c.sharedGuidance().PlanCodexAgentGuidance())
}

func (c *aiCommand) PlanProcessAgentGuidance() agentGuidanceLaunch {
	return localAgentGuidanceLaunch(c.sharedGuidance().PlanProcessAgentGuidance())
}

// withCreateFile sets systemPromptFile for a fresh create whose launch would
// otherwise pass base ("" for none): the rules snapshot or the persona and
// rules composite, else the persona snapshot. A composite that cannot be
// written makes the guidance unavailable, so the create still launches, with
// base alone.
func (l agentGuidanceLaunch) withCreateFile(base string) agentGuidanceLaunch {
	return localAgentGuidanceLaunch(l.shared().WithCreateFile(base))
}

// codexDeveloperInstructions are the developer instructions a Codex fresh
// create of agentUID starts its thread with, given rest, what it would send
// without guidance (the persona content and the Project's label link rules,
// "" for none): the guidance, the Agent's identity paragraph, then rest, each
// part joined by projectlinks.CompositeSeparator. Without guidance it is rest
// itself, identity left out, so a create with the guidance off sends exactly
// what it sent before.
func (l agentGuidanceLaunch) codexDeveloperInstructions(agentUID, rest string) string {
	return l.shared().CodexDeveloperInstructions(agentUID, rest)
}

var codexSocketCommands = mutationruntime.CodexSocketCommands

const codexNoSocketCommand = mutationruntime.CodexNoSocketCommand

// withCreateAnnotation adds the digest a fresh create launched with to base.
// Without guidance it returns base itself, so a create with the guidance off
// stores exactly what it stored before -- nil included.
func (l agentGuidanceLaunch) withCreateAnnotation(base map[string]string) map[string]string {
	return l.shared().WithCreateAnnotation(base)
}

// resumeLaunchAnnotations are the annotations a resume launch reads, given
// the ones it would otherwise read. Guidance that changed puts the current
// digest (or no digest) and the snapshot mode off in place, which is exactly
// what record writes, so the launch reads what the Agent will record.
// Unreadable guidance drops the digest so the Agent launches without guidance
// while its record stays as it was. Otherwise base itself is returned.
func (l agentGuidanceLaunch) resumeLaunchAnnotations(base map[string]string) map[string]string {
	return l.shared().ResumeLaunchAnnotations(base)
}

// record writes, inside the resume transaction, the digest and snapshot mode
// resumeLaunchAnnotations launched with. Nothing changes when the guidance is
// the recorded one or could not be read.
func (l agentGuidanceLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	return l.shared().Record(registry, mutator, agentUID)
}

// notice is the one-line disclosure of guidance the launch could not read, or
// "" when there is nothing to disclose. label names the Agent the way the
// persona notice does.
func (l agentGuidanceLaunch) notice(label string) string { return l.shared().Notice(label) }

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

// prepareAgentGuidance resolves the composition after Project instructions
// and link rules. Native Codex sends it as baseInstructions; plain Codex
// carries only a private model_instructions_file snapshot path. Explicitly
// empty managed guidance still replaces the provider base. Reply-only and
// other-provider behavior retain their existing contracts.
func (c *createCommand) prepareAgentGuidance(provider string, flags *resourceCreateFlags) {
	flags.agentGuidance = agentGuidanceLaunch{}
	if provider == aiModeCodex && flags.resumeConversation == "" && !flags.dialogueReplyOnly {
		guidance := planCodexAgentGuidanceWith(c.agents)
		if guidance.active && !nativeCodexFreshCreateRequired(provider, *flags) {
			paths, err := c.processCreatePaths()
			if err != nil {
				guidance.unavailable = err
			} else {
				guidance = localAgentGuidanceLaunch(guidance.shared().WithCodexBaseFile(paths, flags.projectLinks.shared(), flags.personaLaunch.content))
			}
		}
		flags.agentGuidance = guidance
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

func (c *createCommand) prepareProcessAgentGuidance(flags *resourceCreateFlags) {
	planner, ok := c.agents.(interface{ PlanProcessAgentGuidance() agentGuidanceLaunch })
	if !ok {
		flags.agentGuidance = agentGuidanceLaunch{}
		return
	}
	guidance := planner.PlanProcessAgentGuidance()
	base := flags.projectLinks.systemPromptFile
	if base == "" && flags.personaLaunch.name != "" {
		base = flags.personaLaunch.snapshot.Path
	}
	flags.agentGuidance = guidance.withCreateFile(base)
}
