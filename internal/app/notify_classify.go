package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// notifyRowDisplayState classifies how a queue entry should be rendered in
// user-facing surfaces (sidebar row, statusbar segment, `--live` diagnostics).
//
// The state is a *display* concept: it does not mutate the queue. Inactive
// entries are live reply+agent mismatches that may still be routable; only
// gone entries are cleanup-only.
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
	// `focusExitNotResolved`, so the segment should clean up without focusing.
	notifyDisplayGone
)

// classifyNotifyRowState returns the display classification for a single
// queue entry.
//
// `liveByID` is the map of live AI reply-state panes keyed by notify id (the
// same map [notifyCommand.buildNotifyLiveReport] builds). It drives the
// inactive/stale decision. Pass `nil` when live data is unavailable; the
// helper then never reports [notifyDisplayStale] (best-effort: a missing tmux
// server must not falsely dim every row).
//
// `paneSet` is the full live tmux pane inventory (every pane on the server,
// not just attention/reply panes). It drives the real GONE decision: a
// pane-target queue row whose pane is no longer present in tmux is gone. Pass
// `nil` to mean "inventory unavailable" — membership-based GONE is then
// skipped entirely so a missing/empty tmux reply does not falsely gone every
// row. See [notifyLivePaneSet].
//
// Classification order (fixed):
//
//  1. GONE — the entry has no routable target (malformed/empty session), OR
//     the inventory is available AND the entry has a pane target AND that pane
//     is NOT present in the live inventory. Only pane-target rows are eligible
//     for membership-based GONE; window/session-only rows keep the routable
//     string check only (we do not have a reliable pane id to test).
//  2. STALE/INACTIVE — an `ai:`-prefixed entry whose pane still exists but no
//     longer satisfies live reply+agent state (a `liveByID` miss).
//  3. LIVE — everything else.
func classifyNotifyRowState(entry notify.Notification, liveByID map[string]notifyLivePane, paneSet notifyLivePaneSet) notifyRowDisplayState {
	// 1. GONE: malformed target, or a real pane-inventory miss.
	if !notifyEntryIsRoutable(entry) {
		return notifyDisplayGone
	}
	// Pane-first policy: only rows that carry a concrete pane target are
	// eligible for membership-based GONE. Window/session-only rows have no
	// pane id to look up, so we leave them to the routable-string check above.
	// A nil paneSet means the inventory was unavailable (read failed or the
	// tmux reply was empty/unrecognized); skip membership-based GONE so a
	// missing tmux server cannot gone every row.
	if paneSet != nil && strings.TrimSpace(entry.Pane) != "" && !paneSet.Has(entry) {
		return notifyDisplayGone
	}

	// 2. STALE: ai-prefixed entry whose pane exists but no longer matches live
	// reply+agent state.
	if liveByID == nil {
		return notifyDisplayLive
	}
	if !strings.HasPrefix(entry.ID, "ai:") {
		return notifyDisplayLive
	}
	if live, ok := liveByID[entry.ID]; ok && notifyEntryMatchesGenerationAuthority(entry, live) {
		return notifyDisplayLive
	}
	return notifyDisplayStale
}

func notifyEntryMatchesGenerationAuthority(entry notify.Notification, live notifyLivePane) bool {
	keys := []string{
		notify.MetaAgentUID, notify.MetaPaneUID, notify.MetaStateDomainID,
		notify.MetaEndpointGenerationID, notify.MetaAuthorityFence,
	}
	generationAware := false
	for _, key := range keys {
		if strings.TrimSpace(entry.Metadata[key]) != "" {
			generationAware = true
			break
		}
	}
	if !generationAware {
		return true
	}
	return strings.TrimSpace(entry.Metadata[notify.MetaAgentUID]) != "" &&
		entry.Metadata[notify.MetaAgentUID] == live.AgentUID &&
		entry.Metadata[notify.MetaPaneUID] == live.PaneUID &&
		entry.Metadata[notify.MetaStateDomainID] == live.StateDomainID &&
		entry.Metadata[notify.MetaEndpointGenerationID] == live.EndpointGenerationID &&
		entry.Metadata[notify.MetaAuthorityFence] != "" && entry.Metadata[notify.MetaAuthorityFence] == live.AuthorityFence
}

func notifyEntryHasGenerationAuthority(entry notify.Notification) bool {
	for _, key := range []string{
		notify.MetaAgentUID, notify.MetaPaneUID, notify.MetaStateDomainID,
		notify.MetaEndpointGenerationID, notify.MetaAuthorityFence,
	} {
		if strings.TrimSpace(entry.Metadata[key]) != "" {
			return true
		}
	}
	return false
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
		return "INACTIVE"
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
		return "INA"
	case notifyDisplayGone:
		return "GON"
	}
	return ""
}
