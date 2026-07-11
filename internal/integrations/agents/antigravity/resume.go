// Package antigravity contains Antigravity CLI session restore helpers.
package antigravity

import (
	"errors"
	"fmt"

	"github.com/crevissepartners/projmux/internal/integrations/agents"
)

const (
	AgentName = "antigravity"
)

var ErrInvalidResumeID = errors.New("invalid antigravity resume id")

var adapter = agents.ResumeAdapter{
	ErrInvalidResumeID: ErrInvalidResumeID,
	BuildArgv: func(id string) []string {
		return []string{"agy", "--conversation", id}
	},
	ValidateID: func(id string) error {
		if !isUUIDLike(id) {
			return fmt.Errorf("%w: expected conversation uuid", ErrInvalidResumeID)
		}
		return nil
	},
}

// ResumeArgs returns the structured Antigravity CLI argv for resuming a saved
// conversation. Antigravity's stable external id is the conversation UUID
// surfaced as statusline `conversation_id` or hook `conversationId`.
func ResumeArgs(resumeID string) ([]string, error) {
	return adapter.ResumeArgs(resumeID)
}

// ResumeCommand renders ResumeArgs as a shell command suitable for tmux
// send-keys. Arguments are shell-quoted because the typed command is still
// interpreted by the pane's shell.
func ResumeCommand(resumeID string) (string, error) {
	return adapter.ResumeCommand(resumeID)
}

// NormalizeResumeID trims and validates an Antigravity conversation id.
func NormalizeResumeID(resumeID string) (string, error) {
	return adapter.NormalizeResumeID(resumeID)
}

func isUUIDLike(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
