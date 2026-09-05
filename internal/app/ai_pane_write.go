package app

import "errors"

// aiPaneWriteReasonUnavailable is the closed vocabulary a failed reflection
// write reports.
//
// A reflection write is the tmux `set-option` that puts a hook event's outcome
// onto the Pane the event belongs to. Every one of those writes used to discard
// its error, so a hook whose route to tmux was broken still produced a
// `result:"state"` record: the log reported a delivery that never happened.
// That is a stronger failure than an unattributed hook, because an unattributed
// hook at least says so.
//
// Two tokens, and the split is structural rather than a list of call sites. A
// hook writes two different things onto its Pane: the reflection of the event
// itself, whose failure the ingest caller receives and reports, and the markers
// laid down alongside it, whose callers have no error channel at all. Those two
// really can fail apart -- observed live at 2026-09-05T09:53:46Z on one Pane,
// where `@projmux_ai_state`, `@projmux_ai_badge_kind` and
// `@projmux_attention_state` all held the values of a hook that had run at
// 09:48Z while `@projmux_ai_hook_active` and `@projmux_ai_resume_source` were
// empty. One token for both would have called that "nothing landed", which is
// its own inaccuracy.
//
// Neither token says why. tmux answers a rejected write with an exit status and
// a line of transport text, and both are opaque values the track's change
// boundary keeps out of a record. What is known is that the write did not land
// and which of the two kinds it was.
const (
	aiPaneWriteReasonUnavailable       = "pane write unavailable"
	aiPaneWriteReasonMarkerUnavailable = "pane marker write unavailable"
)

// errAIPaneWriteUnavailable is what a failed reflection write returns. Its
// message is the vocabulary token and nothing else, so the ingest call sites
// that already log `Reason: err.Error()` stay closed-vocabulary without knowing
// this value exists.
var errAIPaneWriteUnavailable = errors.New(aiPaneWriteReasonUnavailable)

// setAIPaneOption writes one Pane option and says whether it landed.
func (c *aiCommand) setAIPaneOption(paneID, option, value string) error {
	return c.writeAIPaneOption(paneID, "set-option", "-p", "-t", paneID, option, value)
}

// clearAIPaneOption unsets one Pane option and says whether it landed.
func (c *aiCommand) clearAIPaneOption(paneID, option string) error {
	return c.writeAIPaneOption(paneID, "set-option", "-p", "-u", "-t", paneID, option)
}

// writeAIPaneOption resolves each attempt without retaining a route after its
// server or Pane disappears. A refusal cannot fall back to tmux's default.
func (c *aiCommand) writeAIPaneOption(paneID string, args ...string) error {
	route, refusal := c.aiPaneOptionRoute(paneID)
	if refusal != nil {
		return errAIPaneWriteUnavailable
	}
	return classifyAIPaneWrite(c.run("tmux", route.args(args...)...))
}

// recordAIPaneOption is setAIPaneOption for a caller with no error channel of
// its own -- the marker writes. It is the honest spelling of what
// `_ = c.run("tmux", ...)` used to be: the write is still best-effort and the
// sequence of attempts is unchanged, but the failure is kept instead of dropped,
// and the ingest record reads it.
func (c *aiCommand) recordAIPaneOption(paneID, option, value string) {
	c.noteAIPaneMarkerWriteFailure(c.setAIPaneOption(paneID, option, value))
}

func classifyAIPaneWrite(err error) error {
	if err == nil {
		return nil
	}
	return errAIPaneWriteUnavailable
}

// noteAIPaneMarkerWriteFailure remembers that a marker write did not land. The
// first failure wins: a broken route fails every subsequent write in the same
// invocation, and the record needs one token, not a tally.
func (c *aiCommand) noteAIPaneMarkerWriteFailure(err error) {
	if c == nil || err == nil {
		return
	}
	c.paneWriteMu.Lock()
	defer c.paneWriteMu.Unlock()
	if c.paneWriteFailure == "" {
		c.paneWriteFailure = aiPaneWriteReasonMarkerUnavailable
	}
}

func (c *aiCommand) recordedAIPaneWriteFailure() string {
	if c == nil {
		return ""
	}
	c.paneWriteMu.Lock()
	defer c.paneWriteMu.Unlock()
	return c.paneWriteFailure
}

// aiIngestResultReportsSuccess names the result words that tell an operator the
// event reached its Pane. `ignored` and `error` are excluded because they
// already report a failure of their own and must keep their reason.
func aiIngestResultReportsSuccess(result string) bool {
	switch result {
	case "state", "notify", "quiet", "deduped":
		return true
	}
	return false
}

// aiIngestSourceIsHook names the record sources a reflection write belongs to.
// The observer journal writes into the same file from a process that lives for
// hours and reflects through its own authority path, so a write failure this
// process saw somewhere else must never colour its records.
func aiIngestSourceIsHook(source string) bool {
	switch source {
	case "codex-hook", "claude-hook", "antigravity-hook", "tmux-bell":
		return true
	}
	return false
}

// honestAIIngestResult refuses to let a record claim a delivery the marker
// writes did not achieve. It reports; it does not repair. Retry and recovery are
// a separate decision, and a record that says `error` with a bounded reason is
// exactly what the operator was missing.
//
// A record that already says `error` is left alone. The event's own reflection
// failing is the stronger statement of the two, and its caller has already
// written the reason for it.
func (c *aiCommand) honestAIIngestResult(entry aiIngestLogEntry) aiIngestLogEntry {
	reason := c.recordedAIPaneWriteFailure()
	if reason == "" {
		return entry
	}
	if !aiIngestSourceIsHook(entry.Source) || !aiIngestResultReportsSuccess(entry.Result) {
		return entry
	}
	entry.Result = "error"
	// The write path names its own bounded token, and it still passes through
	// admission: this is the one boundary where a reason arrives as a plain
	// string, and letting it through unchecked would make the helper a way
	// around the vocabulary rather than a member of it.
	entry.Reason = aiIngestRecordReason(reason)
	return entry
}

// aiPaneWriteOutcome carries the first failure out of a group of reflection
// writes that are all attempted regardless. Keeping the first one is deliberate:
// a route that rejects one option rejects the rest for the same reason, and the
// record wants a reason, not a count.
type aiPaneWriteOutcome struct {
	err error
}

func (o *aiPaneWriteOutcome) record(err error) {
	if err != nil && o.err == nil {
		o.err = err
	}
}

func (o *aiPaneWriteOutcome) failure() error { return o.err }
