package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentQuestionWindowSecondsFileName holds how long the Claude question hook
// keeps one AskUserQuestion open for a command-line answer, in whole seconds.
const AgentQuestionWindowSecondsFileName = "agent-question-window-seconds"

// The question window bounds. A value outside them, like a file that does not
// hold one integer, reads as the default rather than as an error: every
// Claude session runs the hook, and a broken setting may not fail it.
const (
	DefaultAgentQuestionWindowSeconds = 900
	MinAgentQuestionWindowSeconds     = 60
	MaxAgentQuestionWindowSeconds     = 3600
)

func (p Paths) AgentQuestionWindowSecondsFile() string {
	return filepath.Join(p.ConfigDir, AgentQuestionWindowSecondsFileName)
}

// LoadAgentQuestionWindowSecondsFile returns the saved question window. It
// always returns a usable value: the default for an empty path, a missing or
// unreadable file, and content that is not one integer in
// MinAgentQuestionWindowSeconds..MaxAgentQuestionWindowSeconds. A read error
// other than a missing file is also returned, alongside the default.
func LoadAgentQuestionWindowSecondsFile(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultAgentQuestionWindowSeconds, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultAgentQuestionWindowSeconds, nil
		}
		return DefaultAgentQuestionWindowSeconds, fmt.Errorf("read agent question window seconds file: %w", err)
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || seconds < MinAgentQuestionWindowSeconds || seconds > MaxAgentQuestionWindowSeconds {
		return DefaultAgentQuestionWindowSeconds, nil
	}
	return seconds, nil
}
