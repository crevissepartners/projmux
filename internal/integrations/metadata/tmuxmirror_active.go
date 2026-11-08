package metadata

import (
	"context"
	"strings"
)

// InheritedTmuxPane reports the tmux pane the calling process runs in, read
// from its inherited environment. The process is inside tmux only when both
// $TMUX_PANE and $TMUX are non-blank after trimming; otherwise the result is
// ("", false) and the caller must not contact tmux at all.
func InheritedTmuxPane(getenv func(string) string) (paneID string, inside bool) {
	if getenv == nil {
		return "", false
	}
	paneID = strings.TrimSpace(getenv("TMUX_PANE"))
	if paneID == "" || strings.TrimSpace(getenv("TMUX")) == "" {
		return "", false
	}
	return paneID, true
}

// AnchoredTmuxPane reports an explicitly named pane for a caller that runs
// inside a tmux client but not inside that pane (for example a popup). The
// anchor is trimmed and must be non-blank, and $TMUX must still be set so an
// anchor leaked into a process outside any client addresses no server.
func AnchoredTmuxPane(paneID string, getenv func(string) string) (string, bool) {
	anchor := strings.TrimSpace(paneID)
	if getenv == nil || anchor == "" || strings.TrimSpace(getenv("TMUX")) == "" {
		return "", false
	}
	return anchor, true
}

// MirroredPaneUID reads the Projmux Pane uid mirrored onto paneID. A read
// failure is an empty value, not an error.
func (m Mirror) MirroredPaneUID(ctx context.Context, paneID string) string {
	value, err := m.ResolvePaneUID(ctx, paneID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// MirroredWindowUID reads the Projmux Window uid mirrored onto the window of
// paneID. A read failure is an empty value, not an error.
func (m Mirror) MirroredWindowUID(ctx context.Context, paneID string) string {
	value, err := m.ResolveWindowUID(ctx, paneID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
