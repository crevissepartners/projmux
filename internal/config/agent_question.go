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
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
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

// AgentQuestionAnsweringFileName names how a Claude Agent's AskUserQuestion is
// answered. It holds one word naming the way, not an on/off switch.
const AgentQuestionAnsweringFileName = "agent-question-answering"

// AgentQuestionAnswering is the way a Claude question is answered.
type AgentQuestionAnswering string

const (
	// AgentQuestionAnsweringClaude is way 1 and the default: Claude Code shows
	// its own question prompt, and the question hook stays out of it.
	AgentQuestionAnsweringClaude AgentQuestionAnswering = "claude"
	// AgentQuestionAnsweringProjmux is way 2: projmux records the question,
	// opens its own picker in a popup on the client viewing the Agent's Pane,
	// and `projmux agent question answer` answers the same record.
	AgentQuestionAnsweringProjmux AgentQuestionAnswering = "projmux"
)

func (p Paths) AgentQuestionAnsweringFile() string {
	return filepath.Join(p.ConfigDir, AgentQuestionAnsweringFileName)
}

// NormalizeAgentQuestionAnswering reads one saved value. Only the way-2 word,
// in any case and with surrounding whitespace, is way 2; everything else,
// including an empty value, is way 1.
func NormalizeAgentQuestionAnswering(value string) AgentQuestionAnswering {
	if AgentQuestionAnswering(strings.ToLower(strings.TrimSpace(value))) == AgentQuestionAnsweringProjmux {
		return AgentQuestionAnsweringProjmux
	}
	return AgentQuestionAnsweringClaude
}

// LoadAgentQuestionAnsweringFile returns the saved answering way. Like the
// window it always returns a usable value: way 1 for an empty path, a missing
// or unreadable file, and any content other than the way-2 word. A read error
// other than a missing file is also returned, alongside way 1.
func LoadAgentQuestionAnsweringFile(path string) (AgentQuestionAnswering, error) {
	if strings.TrimSpace(path) == "" {
		return AgentQuestionAnsweringClaude, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentQuestionAnsweringClaude, nil
		}
		return AgentQuestionAnsweringClaude, fmt.Errorf("read agent question answering file: %w", err)
	}
	return NormalizeAgentQuestionAnswering(string(content)), nil
}
