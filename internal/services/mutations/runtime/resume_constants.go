package runtime

import coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"

const (
	aiModeClaude                            = "claude"
	aiModeCodex                             = "codex"
	aiModeAntigravity                       = "antigravity"
	ClaudeSystemPromptSnapshotFlag          = "--system-prompt-snapshot"
	ClaudeEffortReasonInvalid               = "effort-invalid"
	PersonaReasonCodexInstructionsImmutable = "codex-instructions-immutable"
)

var ResumableAgentPhases = []coremetadata.AgentPhase{coremetadata.PhaseOffline, coremetadata.PhaseFailed}

const profileReasonResumeUnavailable = "profile-resume-unavailable"
