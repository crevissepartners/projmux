// Package claude contains Claude Code session restore helpers.
package claude

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	AgentName = "claude"
)

var ErrInvalidResumeID = errors.New("invalid claude resume id")

// ResumeArgs returns the structured Claude CLI argv for resuming a saved
// interactive session.
func ResumeArgs(resumeID string) ([]string, error) {
	id, err := NormalizeResumeID(resumeID)
	if err != nil {
		return nil, err
	}
	return []string{"claude", "--resume", id}, nil
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

// NormalizeResumeID trims and validates a Claude session resume id.
func NormalizeResumeID(resumeID string) (string, error) {
	id := strings.TrimSpace(resumeID)
	if id == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidResumeID)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: contains control character", ErrInvalidResumeID)
		}
	}
	return id, nil
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
