package runtime

import (
	"strings"
)

const (
	AiPaneStateOption       = "@projmux_ai_state"
	AiPaneBadgeKindOption   = "@projmux_ai_badge_kind"
	AiPaneTopicOption       = "@projmux_ai_topic"
	AiPaneTopicManualOption = "@projmux_ai_topic_manual"
)

func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
