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

// AgentQuestionWindowUnlimitedWord is the file word for a window that lasts
// until the question is answered. It is read case-insensitively with
// surrounding whitespace trimmed. A projmux binary older than the word reads
// it as the 900 second default, since it is not an integer: downgrading never
// breaks the hook, it only shortens the wait.
const AgentQuestionWindowUnlimitedWord = "unlimited"

// The installed Claude question hook timeout and the window it bounds. This
// file is their single authority.
//
// Claude Code has no "no timeout" hook value: an omitted timeout is its 600
// second default, and 0 or a negative value drops the hook entry, so it never
// runs. The installed timeout is therefore the fixed ceiling
// AgentQuestionHookTimeoutSeconds, independent of the window file. With the
// timeout fixed, a changed window applies to the next question without
// re-running `projmux agent integrate claude`.
//
// The ceiling is the safety net for a stuck hook, and this hook has no
// recover. Seven days is 168 times the longest bounded window (3600 seconds),
// so it never cuts a normal window, while a broken hook is reclaimed within a
// week. 604800000 ms still fits a signed 32-bit millisecond timer.
//
// AgentQuestionHookTimeoutMarginSeconds is how far that timeout outlasts the
// longest window, so the hook, not Claude Code's SIGTERM, is what ends an
// unanswered wait. Unlimited is the longest window that keeps the margin,
// 604785 seconds (about 7 days).
const (
	AgentQuestionHookTimeoutSeconds       = 7 * 24 * 60 * 60
	AgentQuestionHookTimeoutMarginSeconds = 15
	UnlimitedAgentQuestionWindowSeconds   = AgentQuestionHookTimeoutSeconds - AgentQuestionHookTimeoutMarginSeconds
)

func (p Paths) AgentQuestionWindowSecondsFile() string {
	return filepath.Join(p.ConfigDir, AgentQuestionWindowSecondsFileName)
}

// LoadAgentQuestionWindowSecondsFile returns the saved question window. The
// unlimited word reads as UnlimitedAgentQuestionWindowSeconds. It always
// returns a usable value: the default for an empty path, a missing or
// unreadable file, and content that is neither the unlimited word nor one
// integer in MinAgentQuestionWindowSeconds..MaxAgentQuestionWindowSeconds. A
// read error other than a missing file is also returned, alongside the
// default.
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
	text := strings.TrimSpace(string(content))
	if strings.EqualFold(text, AgentQuestionWindowUnlimitedWord) {
		return UnlimitedAgentQuestionWindowSeconds, nil
	}
	seconds, err := strconv.Atoi(text)
	if err != nil || seconds < MinAgentQuestionWindowSeconds || seconds > MaxAgentQuestionWindowSeconds {
		return DefaultAgentQuestionWindowSeconds, nil
	}
	return seconds, nil
}

// SaveAgentQuestionWindowSecondsFile saves the question window. Seconds in
// MinAgentQuestionWindowSeconds..MaxAgentQuestionWindowSeconds are written as
// the integer, and UnlimitedAgentQuestionWindowSeconds as the unlimited word;
// any other value is refused and nothing is written.
func SaveAgentQuestionWindowSecondsFile(path string, seconds int) error {
	content := ""
	switch {
	case seconds == UnlimitedAgentQuestionWindowSeconds:
		content = AgentQuestionWindowUnlimitedWord
	case seconds >= MinAgentQuestionWindowSeconds && seconds <= MaxAgentQuestionWindowSeconds:
		content = strconv.Itoa(seconds)
	default:
		return fmt.Errorf("agent question window %d must be %d..%d seconds or %s", seconds, MinAgentQuestionWindowSeconds, MaxAgentQuestionWindowSeconds, AgentQuestionWindowUnlimitedWord)
	}
	return saveAgentQuestionFile(path, AgentQuestionWindowSecondsFileName, "agent question window seconds", content)
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

// SaveAgentQuestionAnsweringFile saves the answering way, normalized first, so
// the file only ever holds one of the two words.
func SaveAgentQuestionAnsweringFile(path string, value AgentQuestionAnswering) error {
	value = NormalizeAgentQuestionAnswering(string(value))
	return saveAgentQuestionFile(path, AgentQuestionAnsweringFileName, "agent question answering", string(value))
}

// saveAgentQuestionFile writes one line the way SaveAIBadgeStyleFile does: a
// temp file in the same directory, chmod 0644, then an atomic rename.
func saveAgentQuestionFile(path, fileName, what, content string) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	dir := filepath.Dir(path)
	// #nosec G301 -- the projmux config directory keeps the 0755 mode its sibling settings files are created under.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s directory: %w", what, err)
	}

	tmp, err := os.CreateTemp(dir, fileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create %s temp file: %w", what, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(content + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s temp file: %w", what, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s temp file: %w", what, err)
	}
	// #nosec G302 -- a readable one-word setting like its siblings (SaveAIBadgeStyleFile); it holds no secret.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s temp file: %w", what, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s temp file: %w", what, err)
	}
	return nil
}
