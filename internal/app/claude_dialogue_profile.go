package app

import (
	"errors"
	"runtime"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// This mode belongs to one public create/resume invocation, never AgentSpec or
// the inherited environment. Every new generation needs another explicit opt-in.
const claudeDialogueReplyOnlyFlag = "dialogue-reply-only"

type claudeDialogueLauncher interface {
	PlanClaudeDialogueLaunch(coremetadata.AgentWorkspace, string) (string, []string, error)
}

func requireClaudeDialogueMode(provider string, enabled bool, payload []string) error {
	if !enabled {
		return nil
	}
	if provider != aiModeClaude {
		return usageError("--dialogue-reply-only requires a Claude Agent/provider")
	}
	if len(payload) != 0 {
		return usageError("--dialogue-reply-only starts with a fixed readiness turn and accepts no initial payload")
	}
	if runtime.GOOS != "linux" {
		return errors.New("claude reply-only activation requires Linux process birth and pinned executable support")
	}
	return nil
}

func hasClaudeDialogueModeFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--"+claudeDialogueReplyOnlyFlag || strings.HasPrefix(arg, "--"+claudeDialogueReplyOnlyFlag+"=") {
			return true
		}
	}
	return false
}
