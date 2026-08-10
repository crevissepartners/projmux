// Package agents contains shared session restore plumbing for the per-agent
// CLI adapter packages (claude, codex, antigravity).
package agents

import (
	"fmt"
	"strings"
	"unicode"
)

// ResumeAdapter parameterizes an agent CLI's session resume behavior. Each
// agent package declares one adapter and delegates its exported helpers to it.
type ResumeAdapter struct {
	// ErrInvalidResumeID is the agent's sentinel error wrapped by
	// NormalizeResumeID failures.
	ErrInvalidResumeID error
	// BuildArgv renders the agent CLI argv for a normalized resume id.
	BuildArgv func(id string) []string
	// ValidateID checks a trimmed, non-empty resume id and returns an error
	// wrapping ErrInvalidResumeID when the id is malformed.
	ValidateID func(id string) error
}

// ResumeArgs returns the structured agent CLI argv for resuming a saved
// interactive session.
func (a ResumeAdapter) ResumeArgs(resumeID string) ([]string, error) {
	id, err := a.NormalizeResumeID(resumeID)
	if err != nil {
		return nil, err
	}
	return a.BuildArgv(id), nil
}

// ResumeCommand renders ResumeArgs as a shell command suitable for tmux
// send-keys. Arguments are shell-quoted because the typed command is still
// interpreted by the pane's shell.
func (a ResumeAdapter) ResumeCommand(resumeID string) (string, error) {
	args, err := a.ResumeArgs(resumeID)
	if err != nil {
		return "", err
	}
	return shellJoin(args), nil
}

// NormalizeResumeID trims and validates an agent session resume id.
func (a ResumeAdapter) NormalizeResumeID(resumeID string) (string, error) {
	id := strings.TrimSpace(resumeID)
	if id == "" {
		return "", fmt.Errorf("%w: empty", a.ErrInvalidResumeID)
	}
	if err := a.ValidateID(id); err != nil {
		return "", err
	}
	return id, nil
}

// RejectControlCharacters returns a resume-id validator that rejects ids
// containing control characters, wrapping sentinel in the returned error.
func RejectControlCharacters(sentinel error) func(string) error {
	return func(id string) error {
		for _, r := range id {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: contains control character", sentinel)
			}
		}
		return nil
	}
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
