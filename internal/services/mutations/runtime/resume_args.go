package runtime

func ResumeArgsForAgent(mode, resumeID string) ([]string, error) {
	switch mode {
	case aiModeClaude:
		return claude.ResumeArgs(resumeID)
	case aiModeCodex:
		return codex.ResumeArgs(resumeID)
	case aiModeAntigravity:
		return antigravity.ResumeArgs(resumeID)
	default:
		return nil, fmt.Errorf("unsupported resume agent: %s", mode)
	}
}
