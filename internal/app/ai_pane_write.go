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
// One token, deliberately. tmux answers a rejected write with an exit status
// and a line of transport text, and both are opaque values the track's change
// boundary keeps out of a record. What is actually known is that the write did
// not land; splitting that into several tokens would name the call site rather
// than the outcome, and the record already carries the event and the Pane, so
// the site stays recoverable without inventing words for it.
const aiPaneWriteReasonUnavailable = "pane write unavailable"

// errAIPaneWriteUnavailable is what every failed reflection write returns. Its
// message is the vocabulary token and nothing else, so the ingest call sites
// that already log `Reason: err.Error()` stay closed-vocabulary without knowing
// this value exists.
var errAIPaneWriteUnavailable = errors.New(aiPaneWriteReasonUnavailable)

// setAIPaneOption writes one Pane option and says whether it landed.
func (c *aiCommand) setAIPaneOption(paneID, option, value string) error {
	return classifyAIPaneWrite(c.run("tmux", "set-option", "-p", "-t", paneID, option, value))
}

// clearAIPaneOption unsets one Pane option and says whether it landed.
func (c *aiCommand) clearAIPaneOption(paneID, option string) error {
	return classifyAIPaneWrite(c.run("tmux", "set-option", "-p", "-u", "-t", paneID, option))
}

// recordAIPaneOption is setAIPaneOption for a caller with no error channel of
// its own. It is the honest spelling of what `_ = c.run("tmux", ...)` used to
// be: the write is still best-effort and the sequence of attempts is unchanged,
// but the failure is kept instead of dropped, and the ingest record reads it.
func (c *aiCommand) recordAIPaneOption(paneID, option, value string) {
	c.noteAIPaneWriteFailure(c.setAIPaneOption(paneID, option, value))
}

func classifyAIPaneWrite(err error) error {
	if err == nil {
		return nil
	}
	return errAIPaneWriteUnavailable
}

// noteAIPaneWriteFailure remembers that a reflection write did not land. The
// first failure wins: a broken route fails every subsequent write in the same
// invocation, and the record needs one token, not a tally.
func (c *aiCommand) noteAIPaneWriteFailure(err error) {
	if c == nil || err == nil {
		return
	}
	c.paneWriteMu.Lock()
	defer c.paneWriteMu.Unlock()
	if c.paneWriteFailure == "" {
		c.paneWriteFailure = aiPaneWriteReasonUnavailable
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

// honestAIIngestResult refuses to let a record claim a delivery the reflection
// writes did not achieve. It reports; it does not repair. Retry and recovery are
// a separate decision, and a record that says `error` with a bounded reason is
// exactly what the operator was missing.
func (c *aiCommand) honestAIIngestResult(entry aiIngestLogEntry) aiIngestLogEntry {
	reason := c.recordedAIPaneWriteFailure()
	if reason == "" {
		return entry
	}
	if !aiIngestSourceIsHook(entry.Source) || !aiIngestResultReportsSuccess(entry.Result) {
		return entry
	}
	entry.Result = "error"
	entry.Reason = reason
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
