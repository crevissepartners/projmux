// Package claude contains Claude Code session restore helpers.
package claude

import (
	"errors"

	"github.com/crevissepartners/projmux/internal/integrations/agents"
)

const (
	AgentName = "claude"
)

var ErrInvalidResumeID = errors.New("invalid claude resume id")

var adapter = agents.ResumeAdapter{
	ErrInvalidResumeID: ErrInvalidResumeID,
	BuildArgv: func(id string) []string {
		return []string{"claude", "--resume", id}
	},
	ValidateID: agents.RejectControlCharacters(ErrInvalidResumeID),
}

// ResumeArgs returns the structured Claude CLI argv for resuming a saved
// interactive session.
func ResumeArgs(resumeID string) ([]string, error) {
	return adapter.ResumeArgs(resumeID)
}

// ResumeCommand renders ResumeArgs as a shell command suitable for tmux
// send-keys. Arguments are shell-quoted because the typed command is still
// interpreted by the pane's shell.
func ResumeCommand(resumeID string) (string, error) {
	return adapter.ResumeCommand(resumeID)
}

// NormalizeResumeID trims and validates a Claude session resume id.
func NormalizeResumeID(resumeID string) (string, error) {
	return adapter.NormalizeResumeID(resumeID)
}
