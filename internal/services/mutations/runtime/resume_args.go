package runtime

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
)

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
