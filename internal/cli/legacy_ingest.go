package cli

// IsLegacyAIProducerArgv recognizes the sole Phase 2 old-spelling exception.
// The argument slice starts after the top-level `ai` token.
func IsLegacyAIProducerArgv(args []string) bool {
	if len(args) < 2 || args[0] != "ingest" {
		return false
	}
	switch args[1] {
	case "codex-hook", "claude-hook":
		return len(args) == 2
	case "antigravity-hook":
		if len(args) != 4 || args[2] != "--event" {
			return false
		}
		switch args[3] {
		case "PreInvocation", "PostInvocation", "PostToolUse", "Stop", "Statusline":
			return true
		default:
			return false
		}
	case "bell":
		return len(args) == 4 && args[2] == "--pane" && exactTmuxPaneID(args[3])
	default:
		return false
	}
}

func exactTmuxPaneID(value string) bool {
	if len(value) < 2 || value[0] != '%' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
