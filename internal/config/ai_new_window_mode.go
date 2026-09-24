package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AINewWindowModeFileName holds the central default for the mode a new AI
// window opens with. A front layer, such as the TUI's TmuxAISplitModeFileName,
// overrides it. It is a file rather than a config.toml key because older
// binaries reject unknown config.toml keys.
const AINewWindowModeFileName = "ai-new-window-mode"

// AINewWindowModes is the value set of AINewWindowModeFileName, the same
// words the TUI split default accepts. Callers must not modify it.
var AINewWindowModes = []string{"claude", "codex", "antigravity", "selective", "resume", "shell"}

func (p Paths) AINewWindowModeFile() string {
	return filepath.Join(p.ConfigDir, AINewWindowModeFileName)
}

// ValidAINewWindowMode trims value and reports whether it is exactly one of
// AINewWindowModes. The match is case-sensitive.
func ValidAINewWindowMode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, mode := range AINewWindowModes {
		if value == mode {
			return mode, true
		}
	}
	return "", false
}

// LoadAINewWindowModeFile returns the saved central new AI window mode. An
// empty path, a missing file, and content that is not one valid mode
// (including an empty file) all read as not saved. A read error other than a
// missing file is returned.
func LoadAINewWindowModeFile(path string) (mode string, saved bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read AI new window mode file: %w", err)
	}
	mode, ok := ValidAINewWindowMode(string(content))
	if !ok {
		return "", false, nil
	}
	return mode, true, nil
}

// SaveAINewWindowModeFile saves the central new AI window mode. A value that
// is not one of AINewWindowModes is refused and nothing is written.
func SaveAINewWindowModeFile(path, mode string) error {
	valid, ok := ValidAINewWindowMode(mode)
	if !ok {
		return fmt.Errorf("AI new window mode %q must be one of %s", mode, strings.Join(AINewWindowModes, ", "))
	}
	return saveAgentQuestionFile(path, AINewWindowModeFileName, "AI new window mode", valid)
}
