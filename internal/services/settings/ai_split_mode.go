package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
)

// AISplitModeState describes the legacy TUI-first new-window default.
type AISplitModeState struct{ Mode, Source string }

// AISplitMode shares the legacy synchronous read/plain-write execution.
// Paths is resolved independently for the TUI and central fallback reads.
// Display is called only after a successful save; its errors are best effort.
type AISplitMode struct {
	Paths   func() (config.Paths, error)
	Display func(string) error
}

func (s AISplitMode) Get() AISplitModeState {
	if p, err := s.Paths(); err == nil {
		path := p.TmuxAISplitModeFile()
		config.NoteFrontRead(config.TmuxAISplitModeFileName, path)
		if content, err := os.ReadFile(filepath.Clean(path)); err == nil {
			if mode, ok := config.ValidAINewWindowMode(string(content)); ok {
				return AISplitModeState{Mode: mode, Source: "tui"}
			}
		}
	}
	if p, err := s.Paths(); err == nil {
		if mode, saved, _ := config.LoadAINewWindowModeFile(p.AINewWindowModeFile()); saved {
			return AISplitModeState{Mode: mode, Source: "central"}
		}
	}
	return AISplitModeState{Mode: "selective", Source: "default"}
}

// NormalizeAISplitMode retains the low-level unknown-to-selective fallback.
func NormalizeAISplitMode(mode string) string {
	if valid, ok := config.ValidAINewWindowMode(mode); ok {
		return valid
	}
	return "selective"
}

// Set retains the CLI's error shape, newline, creation permissions and ordering.
// os.WriteFile is intentionally non-atomic and preserves an existing file's mode.
func (s AISplitMode) Set(mode string) (AISplitModeState, error) {
	mode = NormalizeAISplitMode(mode)
	p, err := s.Paths()
	if err != nil {
		return AISplitModeState{}, fmt.Errorf("save AI split default: %w", err)
	}
	path := p.TmuxAISplitModeFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return AISplitModeState{}, err
	}
	if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
		return AISplitModeState{}, err
	}
	if s.Display != nil {
		_ = s.Display("ai split default: " + mode)
	}
	return AISplitModeState{Mode: mode, Source: "tui"}, nil
}

// DisplayAISplitModeMessage shares the legacy ambient-tmux display condition.
func DisplayAISplitModeMessage(message string, lookupEnv func(string) string, run func(string, ...string) error) error {
	if strings.TrimSpace(message) == "" || strings.TrimSpace(lookupEnv("TMUX")) == "" {
		return nil
	}
	return run("tmux", "display-message", message)
}

// DisplayCurrentAISplitModeMessage reads environment and PATH only when invoked,
// after storage succeeds. It retains tmux -u and the CLI's inherited stdio.
func DisplayCurrentAISplitModeMessage(message string) error {
	return DisplayAISplitModeMessage(message, os.Getenv, func(name string, args ...string) error {
		cmd := tmuxexec.CommandContext(context.Background(), name, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	})
}
