package projmux

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/settings"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const CodeAISplitModeSaveFailed Code = "ai-split-mode-save-failed"

// GetAISplitMode reads valid TUI, then central, then selective, ignoring read
// errors as the legacy synchronous settings path does. It observes no context.
func (c *Client) GetAISplitMode() core.AISplitModeState {
	return core.AISplitModeState((settings.AISplitMode{Paths: c.instructions.Paths}).Get())
}

// SetAISplitMode validates trimmed mode before I/O and writes the TUI default.
// This synchronous legacy operation observes no cancellation and uses a plain,
// non-atomic write (0644 on creation, subject to umask; existing mode preserved).
// After saving, it uses current TMUX/TMUX_PANE/PATH for best-effort display.
func (c *Client) SetAISplitMode(mode string) (core.AISplitModeState, error) {
	valid, ok := config.ValidAINewWindowMode(mode)
	if !ok {
		return core.AISplitModeState{}, &Refusal{Code: CodeConfigValueInvalid, message: fmt.Sprintf("invalid AI split mode %q", mode)}
	}
	v, err := (settings.AISplitMode{Paths: c.instructions.Paths, Display: settings.DisplayCurrentAISplitModeMessage}).Set(valid)
	if err != nil {
		return core.AISplitModeState{}, &Refusal{Code: CodeAISplitModeSaveFailed, message: err.Error(), cause: err}
	}
	return core.AISplitModeState(v), nil
}
