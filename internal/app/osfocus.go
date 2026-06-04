package app

import (
	"context"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/focus"
	"github.com/crevissepartners/projmux/internal/integrations/osfocus"
)

// osFocusDispatcher abstracts the chain interface so tests can stub it out
// (the dispatcher is invoked from execute() and we don't want unit tests to
// shell out to wt.exe via the default adapter).
type osFocusDispatcher interface {
	Focus(target osfocus.Target) error
}

// defaultOSFocusChain returns the tier-1 chain. As of tier-1 it contains a
// single adapter (Windows Terminal hosting WSL → Windows); tier-2 will add
// more matrix entries here. The chain itself is cheap to construct on each
// call — Detect() is what runs on the focus hot path.
func defaultOSFocusChain() osFocusDispatcher {
	return osfocus.NewChain(
		osfocus.WindowsTerminalWSLAdapter{},
	)
}

// dispatchOSFocus is the focus.go-side hook point. It is called from
// execute() only after the tmux pane focus has succeeded — we don't want to
// raise the host window when tmux failed, because the user would arrive at
// the wrong place.
//
// The function is best-effort by contract: the chain swallows adapter
// errors, and we ignore the chain's return value entirely. If no adapter
// detects (the common case until the user is on a measured terminal × OS
// combo), this is a no-op.
func (c *focusCommand) dispatchOSFocus(target focus.Target, socket string) {
	if c.desktopNotifyMode(socket) != desktopNotifyModeRaise {
		return
	}
	chain := c.osFocusChain
	if chain == nil {
		chain = defaultOSFocusChain()
	}
	_ = chain.Focus(osfocus.Target{
		Socket:  socket,
		Session: target.Session,
		Window:  target.WindowSelector(),
		Pane:    target.PaneSelector(),
	})
}

func (c *focusCommand) desktopNotifyMode(socket string) desktopNotifyMode {
	lookupEnv := c.lookupEnv
	resolver := desktopNotifyResolver{
		lookupEnv: lookupEnv,
		readConfigMode: func() (desktopNotifyMode, bool) {
			return loadSavedDesktopNotifyMode(c.homeDir, lookupEnv)
		},
		readTmuxOption: func(name string) string {
			if c.runner == nil {
				return ""
			}
			if strings.TrimSpace(socket) == "" {
				if lookupEnv == nil || strings.TrimSpace(lookupEnv("TMUX")) == "" {
					return ""
				}
			}
			out, err := c.runner.Run(context.Background(), "tmux", c.tmuxArgs(socket, "show-options", "-gqv", name)...)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
		isWSL:     lookupEnv != nil && strings.TrimSpace(lookupEnv("WSL_DISTRO_NAME")) != "",
		wtPresent: lookupEnv != nil && strings.TrimSpace(lookupEnv("WT_SESSION")) != "",
	}
	mode, _ := resolver.resolveMode()
	return mode
}
