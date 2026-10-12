package app

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)





// requireClaudeLaunchOptions validates the shared model and effort create
// surface before any resource is allocated. The name is retained for callers
// that originally used the Claude-only validator.
func requireClaudeLaunchOptions(spelling, provider string, flags resourceCreateFlags) error {
	return requireLaunchOptions(spelling, provider, flags.model, flags.effort, flags.dialogueReplyOnly, "nothing was created")
}









// claudeSystemPromptSnapshotFlag is Claude's switch for the system prompt it
// records on a conversation's first request and replays on every resume.
const claudeSystemPromptSnapshotFlag = "--system-prompt-snapshot"

// claudeResumeSnapshotArgs spells the system prompt snapshot mode a resumed
// Agent records. Only a Claude Agent annotated with
// coremetadata.SystemPromptSnapshotOff gets an argument; any other Agent, and
// any other provider carrying the annotation, gets none, so its resume argv
// stays byte-identical to the one it had before the annotation existed.
func claudeResumeSnapshotArgs(mode string, annotations map[string]string) []string {
	if mode != aiModeClaude || annotations[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		return nil
	}
	return []string{claudeSystemPromptSnapshotFlag, coremetadata.SystemPromptSnapshotOff}
}









// claudeEffortReasonInvalid is the reason token of a recorded effort a resume
// does not re-pass because it is not one of claudeEffortLevels.
const claudeEffortReasonInvalid = "effort-invalid"

// claudeResumeEffort returns the recorded effort a Claude or Codex Agent
// re-passes. Unsupported values are skipped and disclosed by the caller.
func claudeResumeEffort(mode string, annotations map[string]string) (effort, invalid string, skipped bool) {
	if mode != aiModeClaude && mode != aiModeCodex {
		return "", "", false
	}
	value, ok := annotations[coremetadata.AnnotationAgentEffort]
	if !ok {
		return "", "", false
	}
	if !slices.Contains(claudeEffortLevels, value) {
		return "", value, true
	}
	return value, "", false
}

// personaReasonCodexInstructionsImmutable is the public refusal for trying to
// change the developer instructions of an existing Codex conversation.
const personaReasonCodexInstructionsImmutable = "codex-instructions-immutable"




