package app

// notifyQueueReason names the guard that kept a hook's notification out of the
// internal queue. The tokens are diagnostic: they appear in `ai-ingest.log` and
// `projmux diagnostics agent-hook` so an operator can tell a queued hook from a
// dropped one, and they are not a public contract.
type notifyQueueReason string

const (
	// notifyQueueReasonNoStore: there is no queue to write to (the notify store
	// could not be resolved, or the caller handed no tmux lookup).
	notifyQueueReasonNoStore notifyQueueReason = "no_store"

	// notifyQueueReasonNoPane: the caller named no Pane.
	notifyQueueReasonNoPane notifyQueueReason = "no_pane"

	// notifyQueueReasonNoProvider: the Pane carries no provider marker and the
	// caller supplied no exact hook provider (a manual attention toggle).
	notifyQueueReasonNoProvider notifyQueueReason = "no_provider"

	// notifyQueueReasonNotAppOwned: the Pane has no provider marker and the
	// server does not prove it is app-owned (foreign or standalone server).
	notifyQueueReasonNotAppOwned notifyQueueReason = "not_app_owned"

	// notifyQueueReasonNoPaneUID: the Pane is app-owned but the app never gave
	// it a Pane UID.
	notifyQueueReasonNoPaneUID notifyQueueReason = "no_pane_uid"

	// notifyQueueReasonNoSession: tmux answered no session for the Pane.
	notifyQueueReasonNoSession notifyQueueReason = "no_session"

	// notifyQueueReasonPushFailed: the queue store refused the entry.
	notifyQueueReasonPushFailed notifyQueueReason = "push_failed"

	// notifyQueueReasonVisiblePane: the Pane was already in front of the user
	// and the event does not force delivery.
	notifyQueueReasonVisiblePane notifyQueueReason = "visible_pane"

	// notifyQueueReasonStateOnly: the event updates state and never queues.
	notifyQueueReasonStateOnly notifyQueueReason = "state_only"

	// notifyQueueReasonObservationIgnored: the Registry ignored the observation
	// before the queue decision was reached.
	notifyQueueReasonObservationIgnored notifyQueueReason = "observation_ignored"
)

const (
	aiIngestQueueQueued    = "queued"
	aiIngestQueueNotQueued = "not_queued"
)

// notifyQueueOutcome is what the queue decision did for one hook: it either
// created a queue entry or it did not, and then says which guard stopped it.
type notifyQueueOutcome struct {
	Queued bool
	Reason notifyQueueReason
}

func notNotifyQueued(reason notifyQueueReason) notifyQueueOutcome {
	return notifyQueueOutcome{Reason: reason}
}

// noteNotifyQueueOutcome remembers the queue decision of this invocation for
// the ingest record written later. The record is written by a caller far from
// the decision, so the outcome is carried on the command, the way a failed
// marker write is (see noteAIPaneMarkerWriteFailure). The last decision wins.
func (c *aiCommand) noteNotifyQueueOutcome(outcome notifyQueueOutcome) {
	if c == nil {
		return
	}
	c.notifyQueueMu.Lock()
	defer c.notifyQueueMu.Unlock()
	c.notifyQueue = outcome
	c.notifyQueueRecorded = true
}

// takeNotifyQueueOutcome hands the remembered decision to the record that
// reports it and forgets it, so one decision describes one record.
func (c *aiCommand) takeNotifyQueueOutcome() (notifyQueueOutcome, bool) {
	if c == nil {
		return notifyQueueOutcome{}, false
	}
	c.notifyQueueMu.Lock()
	defer c.notifyQueueMu.Unlock()
	outcome, ok := c.notifyQueue, c.notifyQueueRecorded
	c.notifyQueue, c.notifyQueueRecorded = notifyQueueOutcome{}, false
	return outcome, ok
}

// withNotifyQueueOutcome adds the queue fields to a hook's `notify` record. It
// adds fields only: the result word and every other field stay as the caller
// wrote them, because the report and the classification read those.
func (c *aiCommand) withNotifyQueueOutcome(entry aiIngestLogEntry) aiIngestLogEntry {
	if !aiIngestSourceIsHook(entry.Source) {
		return entry
	}
	outcome, ok := c.takeNotifyQueueOutcome()
	if !ok || entry.Result != "notify" || entry.Queue != "" {
		return entry
	}
	if outcome.Queued {
		entry.Queue = aiIngestQueueQueued
		return entry
	}
	entry.Queue = aiIngestQueueNotQueued
	entry.QueueReason = string(outcome.Reason)
	return entry
}
