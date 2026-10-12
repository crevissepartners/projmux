package runtime

// claudeResumeSnapshotArgs spells the system prompt snapshot mode a resumed
// Agent records. Only a Claude Agent annotated with
// coremetadata.SystemPromptSnapshotOff gets an argument; any other Agent, and
// any other provider carrying the annotation, gets none, so its resume argv
// stays byte-identical to the one it had before the annotation existed.
func ClaudeResumeSnapshotArgs(mode string, Annotations map[string]string) []string {
	if mode != aiModeClaude || Annotations[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		return nil
	}
	return []string{claudeSystemPromptSnapshotFlag, coremetadata.SystemPromptSnapshotOff}
}

// claudeResumeEffort returns the recorded effort a Claude or Codex Agent
// re-passes. Unsupported values are skipped and disclosed by the caller.
func ClaudeResumeEffort(mode string, Annotations map[string]string) (Effort, invalid string, skipped bool) {
	if mode != aiModeClaude && mode != aiModeCodex {
		return "", "", false
	}
	value, ok := Annotations[coremetadata.AnnotationAgentEffort]
	if !ok {
		return "", "", false
	}
	if !slices.Contains(ClaudeEffortLevels, value) {
		return "", value, true
	}
	return value, "", false
}
