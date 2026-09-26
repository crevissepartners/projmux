package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentApprovalAnsweringFileName names how a Claude Agent's permission request
// ("Do you want to proceed?") is answered. It holds one word naming the way,
// like AgentQuestionAnsweringFileName, and is a separate setting: the question
// way never decides a permission request, nor this one a question.
//
// It covers Claude permission requests only. Codex approvals are not captured;
// `projmux agent approval review` answers those through the Codex app-server.
const AgentApprovalAnsweringFileName = "agent-approval-answering"

// AgentApprovalWindowSecondsFileName holds how long the Claude permission hook
// keeps one permission request open for a command-line answer, in whole
// seconds.
const AgentApprovalWindowSecondsFileName = "agent-approval-window-seconds"

// AgentApprovalAnswering is the way a Claude permission request is answered.
type AgentApprovalAnswering string

const (
	// AgentApprovalAnsweringClaude is way 1 and the default: Claude Code shows
	// its own permission prompt, and the permission hook stays out of it.
	AgentApprovalAnsweringClaude AgentApprovalAnswering = "claude"
	// AgentApprovalAnsweringProjmux captures the request: projmux records it,
	// and `projmux agent approval answer` can allow or deny it once. Claude
	// Code's own prompt stays usable, and the first answer wins.
	AgentApprovalAnsweringProjmux AgentApprovalAnswering = "projmux"
)

// The permission window bounds. Unlike the question window there is no
// unlimited word: a permission request always ends in Claude Code's own prompt
// or a denial-free expiry. A value outside the bounds, the word unlimited, and
// a file that does not hold one integer read as the default.
const (
	DefaultAgentApprovalWindowSeconds = 900
	MinAgentApprovalWindowSeconds     = 60
	MaxAgentApprovalWindowSeconds     = 3600
)

// The installed Claude permission hook timeout. Like the question hook it is a
// fixed ceiling independent of the window file, so a changed window applies to
// the next request without re-running `projmux agent integrate claude`; it is
// the longest window plus AgentApprovalHookTimeoutMarginSeconds, so the hook,
// not Claude Code's SIGTERM, is what ends an unanswered wait.
const (
	AgentApprovalHookTimeoutMarginSeconds = 15
	AgentApprovalHookTimeoutSeconds       = MaxAgentApprovalWindowSeconds + AgentApprovalHookTimeoutMarginSeconds
)

func (p Paths) AgentApprovalAnsweringFile() string {
	return filepath.Join(p.ConfigDir, AgentApprovalAnsweringFileName)
}

func (p Paths) AgentApprovalWindowSecondsFile() string {
	return filepath.Join(p.ConfigDir, AgentApprovalWindowSecondsFileName)
}

// NormalizeAgentApprovalAnswering reads one saved value. Only the capture word,
// in any case and with surrounding whitespace, captures; everything else,
// including an empty value, is way 1.
func NormalizeAgentApprovalAnswering(value string) AgentApprovalAnswering {
	if AgentApprovalAnswering(strings.ToLower(strings.TrimSpace(value))) == AgentApprovalAnsweringProjmux {
		return AgentApprovalAnsweringProjmux
	}
	return AgentApprovalAnsweringClaude
}

// LoadAgentApprovalAnsweringFile returns the saved answering way. It always
// returns a usable value: way 1 for an empty path, a missing or unreadable
// file, and any content other than the capture word. A read error other than
// a missing file is also returned, alongside way 1.
func LoadAgentApprovalAnsweringFile(path string) (AgentApprovalAnswering, error) {
	if strings.TrimSpace(path) == "" {
		return AgentApprovalAnsweringClaude, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentApprovalAnsweringClaude, nil
		}
		return AgentApprovalAnsweringClaude, fmt.Errorf("read agent approval answering file: %w", err)
	}
	return NormalizeAgentApprovalAnswering(string(content)), nil
}

// SaveAgentApprovalAnsweringFile saves the answering way, normalized first, so
// the file only ever holds one of the two words.
func SaveAgentApprovalAnsweringFile(path string, value AgentApprovalAnswering) error {
	value = NormalizeAgentApprovalAnswering(string(value))
	return saveAgentQuestionFile(path, AgentApprovalAnsweringFileName, "agent approval answering", string(value))
}

// LoadAgentApprovalWindowSecondsFile returns the saved permission window. It
// always returns a usable value: the default for an empty path, a missing or
// unreadable file, and content that is not one integer in
// MinAgentApprovalWindowSeconds..MaxAgentApprovalWindowSeconds (the unlimited
// word included). A read error other than a missing file is also returned,
// alongside the default.
func LoadAgentApprovalWindowSecondsFile(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultAgentApprovalWindowSeconds, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultAgentApprovalWindowSeconds, nil
		}
		return DefaultAgentApprovalWindowSeconds, fmt.Errorf("read agent approval window seconds file: %w", err)
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || seconds < MinAgentApprovalWindowSeconds || seconds > MaxAgentApprovalWindowSeconds {
		return DefaultAgentApprovalWindowSeconds, nil
	}
	return seconds, nil
}

// SaveAgentApprovalWindowSecondsFile saves the permission window. Any value
// outside MinAgentApprovalWindowSeconds..MaxAgentApprovalWindowSeconds is
// refused and nothing is written.
func SaveAgentApprovalWindowSecondsFile(path string, seconds int) error {
	if seconds < MinAgentApprovalWindowSeconds || seconds > MaxAgentApprovalWindowSeconds {
		return fmt.Errorf("agent approval window %d must be %d..%d seconds", seconds, MinAgentApprovalWindowSeconds, MaxAgentApprovalWindowSeconds)
	}
	return saveAgentQuestionFile(path, AgentApprovalWindowSecondsFileName, "agent approval window seconds", strconv.Itoa(seconds))
}
