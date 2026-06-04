// Package antigravity contains Antigravity CLI session restore helpers.
package antigravity

import (
	"errors"
	"fmt"
	"strings"
)

const (
	AgentName = "antigravity"
)

var ErrInvalidResumeID = errors.New("invalid antigravity resume id")

// ResumeArgs returns the structured Antigravity CLI argv for resuming a saved
// conversation. Antigravity's stable external id is the conversation UUID
// surfaced as statusline `conversation_id` or hook `conversationId`.
func ResumeArgs(resumeID string) ([]string, error) {
	id, err := NormalizeResumeID(resumeID)
	if err != nil {
		return nil, err
	}
	return []string{"agy", "--conversation", id}, nil
}

// ResumeCommand renders ResumeArgs as a shell command suitable for tmux
// send-keys. Arguments are shell-quoted because the typed command is still
// interpreted by the pane's shell.
func ResumeCommand(resumeID string) (string, error) {
	args, err := ResumeArgs(resumeID)
	if err != nil {
		return "", err
	}
	return shellJoin(args), nil
}

// NormalizeResumeID trims and validates an Antigravity conversation id.
func NormalizeResumeID(resumeID string) (string, error) {
	id := strings.TrimSpace(resumeID)
	if id == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidResumeID)
	}
	if !isUUIDLike(id) {
		return "", fmt.Errorf("%w: expected conversation uuid", ErrInvalidResumeID)
	}
	return id, nil
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

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '-' ||
			r == '_' ||
			r == '.' ||
			r == '/' ||
			r == ':' ||
			r == '=')
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
