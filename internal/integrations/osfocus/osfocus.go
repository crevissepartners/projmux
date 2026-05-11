// Package osfocus contains terminal/OS-window focus adapters that run after
// the tmux pane focus dispatcher succeeds. Tier-1 ships exactly one adapter,
// for Windows Terminal hosting WSL → Windows; other terminals listed in the
// spike matrix (docs/notify-os-focus-poc.md) will land in tier-2 once they
// are measured.
//
// Design notes:
//
//   - Detect() should be cheap (env lookups, optional binary existence checks)
//     because it runs on every focus call.
//   - Focus() is best-effort and must not block the caller; the adapter is
//     responsible for any goroutine dispatch internally. The roadmap's risk
//     section requires the call to stay snappy.
//   - The Chain returns nil whenever no adapter detects, matching the
//     documented "silent fallback" failure policy: the calling notify path
//     still has the tmux pane focus and the in-app notify queue, so doing
//     nothing on the OS-focus step is the correct no-op.
//
// TODO(tier-2): add an OS-level Windows `SetForegroundWindow` fallback adapter
// for non-WT Windows hosts. Test 2 in the spike doc recorded that path as
// Partial — it works with the `AttachThreadInput` + `BringWindowToTop` combo
// but must `IsIconic`-guard `ShowWindow(SW_RESTORE)` so maximized windows
// aren't restored to normal size. Same for the other matrix rows (Ghostty,
// Kitty, WezTerm, iTerm2, …) once each environment is measured.
package osfocus

// Target identifies what the adapter should focus. Fields are advisory — an
// adapter is free to ignore fields it does not need. Tier-1's
// WindowsTerminalWSLAdapter ignores all of them because `wt.exe -w 0`
// raises the active WT window, which is the right behavior when projmux
// is doing focus from inside the tmux pane the user just clicked toward.
type Target struct {
	Socket  string // tmux socket name (-L) the target lives under
	Session string // tmux session name
	Window  string // tmux window index/id (rendered form, may be empty)
	Pane    string // tmux pane index/id (rendered form, may be empty)
}

// Adapter is the per-environment focus implementation. Detect runs on every
// focus call and should be cheap; Focus is best-effort and must return
// without blocking the caller.
type Adapter interface {
	Name() string
	Detect() bool
	Focus(target Target) error
}

// Chain holds adapters in priority order. Focus runs the first Detect() that
// returns true. If no adapter matches, Focus returns nil (treat the OS-focus
// step as a no-op rather than a failure).
type Chain struct {
	adapters []Adapter
}

// NewChain returns a Chain populated with the supplied adapters in priority
// order. Nil entries are filtered out defensively so callers can compose the
// chain conditionally without panicking.
func NewChain(adapters ...Adapter) *Chain {
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		if a == nil {
			continue
		}
		out = append(out, a)
	}
	return &Chain{adapters: out}
}

// Focus walks the chain in order and dispatches to the first adapter whose
// Detect() returns true. The chain swallows the adapter's error so the
// overall focus path stays silent on OS-focus failure; only the per-adapter
// implementation observes the failure (for its own logging, if any). When no
// adapter detects, Focus returns nil.
func (c *Chain) Focus(target Target) error {
	if c == nil {
		return nil
	}
	for _, a := range c.adapters {
		if a == nil {
			continue
		}
		if !a.Detect() {
			continue
		}
		// Silent fallback policy: the adapter's own error never propagates to
		// the caller. The chain still stops at the first detect because
		// further adapters would not match anyway.
		_ = a.Focus(target)
		return nil
	}
	return nil
}
