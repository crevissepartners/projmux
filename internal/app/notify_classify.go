package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// notifyRowDisplayState classifies how a queue entry should be rendered in
// user-facing surfaces (sidebar row, statusbar segment, `--live` diagnostics).
//
// The state is a *display* concept: it does not mutate the queue. Entries that
// resolve to anything other than [notifyDisplayLive] are ack-only — focus
// round-trips will not succeed, so we want to communicate that to the user
// before they click.
type notifyRowDisplayState int

const (
	// notifyDisplayLive means the entry is routable and (for AI-prefixed
	// entries) has a matching live reply-state pane.
	notifyDisplayLive notifyRowDisplayState = iota

	// notifyDisplayStale means the entry id has the `ai:` prefix but no live
	// pane currently satisfies the AI reply+agent state. This matches the
	// `queue-stale` row state in [notifyCommand.buildNotifyLiveReport].
	notifyDisplayStale

	// notifyDisplayGone means the entry has no routable target (typically the
	// session field is empty after trimming). Focus would exit with
	// `focusExitNotResolved`, so the segment should ack-only.
	notifyDisplayGone
)

// classifyNotifyRowState returns the display classification for a single
// queue entry. `liveByID` is the map of live AI reply-state panes keyed by
// notify id (the same map [notifyCommand.buildNotifyLiveReport] builds). Pass
// `nil` when live data is unavailable; the helper then only reports
// [notifyDisplayGone] for unroutable entries and treats every other entry as
// [notifyDisplayLive] (best-effort: a missing tmux server must not falsely
// dim every row).
func classifyNotifyRowState(entry notify.Notification, liveByID map[string]notifyLivePane) notifyRowDisplayState {
	if !notifyEntryIsRoutable(entry) {
		return notifyDisplayGone
	}
	if liveByID == nil {
		return notifyDisplayLive
	}
	if !strings.HasPrefix(entry.ID, "ai:") {
		return notifyDisplayLive
	}
	if _, ok := liveByID[entry.ID]; ok {
		return notifyDisplayLive
	}
	return notifyDisplayStale
}

// notifyEntryIsRoutable reports whether [notifyCommand.focusNotification]
// would have anything to act on. Empty session is the only hard rejection
// today (the push validator already enforces it on entry, but the queue file
// is user-editable so we defensively check both session and the formatted
// target).
func notifyEntryIsRoutable(entry notify.Notification) bool {
	if strings.TrimSpace(entry.Session) == "" {
		return false
	}
	target := notify.FormatTarget(notify.Target{
		Session: entry.Session,
		Window:  entry.Window,
		Pane:    entry.Pane,
	})
	return strings.TrimSpace(target) != ""
}

// notifyDisplayStateLabel maps a [notifyRowDisplayState] to its long-form
// badge label. The empty string means "no override; use the severity/source
// derived label".
func notifyDisplayStateLabel(state notifyRowDisplayState) string {
	switch state {
	case notifyDisplayStale:
		return "STALE"
	case notifyDisplayGone:
		return "GONE"
	}
	return ""
}

// notifyDisplayStateShortLabel maps a [notifyRowDisplayState] to its 3-rune
// statusbar abbreviation. The empty string means "no override".
func notifyDisplayStateShortLabel(state notifyRowDisplayState) string {
	switch state {
	case notifyDisplayStale:
		return "STL"
	case notifyDisplayGone:
		return "GON"
	}
	return ""
}
