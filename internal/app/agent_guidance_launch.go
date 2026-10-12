package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

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
// A Codex fresh create (nativeCodexFreshCreateRequired) sends the guidance,
// followed by the Agent's identity paragraph, as its thread's developer
// instructions and records the digest; it passes no file. The reply-only
// lane, every other Codex lane and every other provider are left exactly as
// they were.
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




