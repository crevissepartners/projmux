// Package codex contains Codex CLI session restore helpers.
package codex

import (
	"errors"

	"github.com/crevissepartners/projmux/internal/integrations/agents"
)

const (
	AgentName = "codex"
)

var ErrInvalidResumeID = errors.New("invalid codex resume id")

var adapter = agents.ResumeAdapter{
	ErrInvalidResumeID: ErrInvalidResumeID,
	BuildArgv: func(id string) []string {
		return []string{"codex", "resume", id}
	},
	ValidateID: agents.RejectControlCharacters(ErrInvalidResumeID),
}

// ResumeArgs returns the structured Codex CLI argv for resuming a saved
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

// NormalizeResumeID trims and validates a Codex session resume id.
func NormalizeResumeID(resumeID string) (string, error) {
	return adapter.NormalizeResumeID(resumeID)
}
