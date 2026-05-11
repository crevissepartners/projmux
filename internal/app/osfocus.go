package app

import (
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
