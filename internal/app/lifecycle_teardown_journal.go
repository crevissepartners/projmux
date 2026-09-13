package app

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// Teardown decision journaling happens here, at the app-layer consumption of
// the pure decision table, never inside it. The planner stays I/O-free and its
// table tests stay unbound to the journal; this file only projects an already
// consumed decision onto the closed diagnostics vocabulary.

// teardownReasonCodes maps every core TeardownReason onto its journal code.
// The omission guard in lifecycle_teardown_journal_test.go scans the core
// package for TeardownReason constants, so a new reason without a row here
// fails `make test` instead of silently losing its decisions.
var teardownReasonCodes = map[coremetadata.TeardownReason]diagnostics.TeardownReasonCode{
	coremetadata.TeardownReasonInvalidInput:          diagnostics.TeardownReasonInvalidInput,
	coremetadata.TeardownReasonStaleGeneration:       diagnostics.TeardownReasonStaleGeneration,
	coremetadata.TeardownReasonUnavailable:           diagnostics.TeardownReasonUnavailable,
	coremetadata.TeardownReasonEmptyObservation:      diagnostics.TeardownReasonEmptyObservation,
	coremetadata.TeardownReasonNoServer:              diagnostics.TeardownReasonNoServer,
	coremetadata.TeardownReasonPermissionDenied:      diagnostics.TeardownReasonPermissionDenied,
	coremetadata.TeardownReasonSiblingSocket:         diagnostics.TeardownReasonSiblingSocket,
	coremetadata.TeardownReasonForeignHost:           diagnostics.TeardownReasonForeignHost,
	coremetadata.TeardownReasonNonCausalTermination:  diagnostics.TeardownReasonNonCausalTermination,
	coremetadata.TeardownReasonPaneTeardown:          diagnostics.TeardownReasonPaneTeardown,
	coremetadata.TeardownReasonAwaitingPaneExit:      diagnostics.TeardownReasonAwaitingPaneExit,
	coremetadata.TeardownReasonAwaitingWindowUnlink:  diagnostics.TeardownReasonAwaitingWindowUnlink,
	coremetadata.TeardownReasonLiveSiblingPane:       diagnostics.TeardownReasonLiveSiblingPane,
	coremetadata.TeardownReasonWindowTeardown:        diagnostics.TeardownReasonWindowTeardown,
	coremetadata.TeardownReasonProjectTeardown:       diagnostics.TeardownReasonProjectTeardown,
	coremetadata.TeardownReasonMixedOwnerChain:       diagnostics.TeardownReasonMixedOwnerChain,
	coremetadata.TeardownReasonConflictingOwnerFacts: diagnostics.TeardownReasonConflictingOwnerFacts,
	coremetadata.TeardownReasonStaleOwnerBinding:     diagnostics.TeardownReasonStaleOwnerBinding,
	coremetadata.TeardownReasonDeadPaneCleanupRetry:  diagnostics.TeardownReasonDeadPaneCleanupRetry,
}

var teardownDecisionCodes = map[coremetadata.TeardownAction]diagnostics.TeardownDecision{
	coremetadata.TeardownRetain:          diagnostics.TeardownDecisionRetain,
	coremetadata.TeardownDeletePaneAgent: diagnostics.TeardownDecisionDeletePaneAgent,
	coremetadata.TeardownDeleteWindow:    diagnostics.TeardownDecisionDeleteWindow,
	coremetadata.TeardownRefuse:          diagnostics.TeardownDecisionRefuse,
}

// lifecycleTeardownSubject is the owner chain one decision was taken about. It
// carries opaque Registry UIDs only; tmux handles, socket, and session name
// stay in the planner.
type lifecycleTeardownSubject struct {
	paneUID        string
	windowUID      string
	classification coremetadata.TerminationClassification
}

// lifecycleTeardownRecord is one authoritative consumed decision.
type lifecycleTeardownRecord struct {
	action  coremetadata.TeardownAction
	reason  coremetadata.TeardownReason
	subject lifecycleTeardownSubject
}

// journal appends the record through the invocation's recorder. A nil record
// (no decision was consumed) and a nil recorder are both no-ops.
func (r *lifecycleTeardownRecord) journal(recorder *diagnostics.TeardownRecorder) {
	if r == nil || recorder == nil {
		return
	}
	decision, ok := teardownDecisionCodes[r.action]
	if !ok {
		return
	}
	reason, ok := teardownReasonCodes[r.reason]
	if !ok {
		return
	}
	recorder.Record(diagnostics.TeardownDecisionRecord{
		Decision: decision, Reason: reason,
		Classification: diagnostics.TeardownClassification(r.subject.classification),
		WindowUID:      r.subject.windowUID, PaneUID: r.subject.paneUID,
	})
}

// teardownRecord returns the plan's consumed decision, or nil when the event
// was not a teardown decision at all (wrong kind, no exact target, or an unlink
// still awaiting its causal pane-exited half). At most one of the three plans
// carries a decision for one event.
func (p exactLifecycleCascadePlan) teardownRecord() *lifecycleTeardownRecord {
	subject := p.subject
	var decision coremetadata.TeardownDecision
	switch {
	case p.root.Decision.Action != "":
		decision = p.root.Decision
		subject.paneUID = firstNonEmpty(subject.paneUID, p.root.PaneUID)
		subject.windowUID = firstNonEmpty(subject.windowUID, p.root.WindowUID)
	case p.pending.Decision.Action != "":
		decision = p.pending.Decision
		subject.windowUID = firstNonEmpty(subject.windowUID, p.pending.Evidence.WindowUID)
		if subject.classification == "" {
			subject.classification = p.pending.Evidence.Classification
		}
	case p.paneAgent.Decision.Action != "":
		decision = p.paneAgent.Decision
		subject.paneUID = firstNonEmpty(subject.paneUID, p.paneAgent.PaneUID)
		if subject.classification == "" && p.paneAgent.Evidence != nil {
			subject.classification = p.paneAgent.Evidence.Classification
		}
	default:
		return nil
	}
	return &lifecycleTeardownRecord{action: decision.Action, reason: decision.Reason, subject: subject}
}

// cleanupRetry is the authoritative outcome of a decision whose exact dead tmux
// Pane could not be removed: the transaction aborts and the Registry graph is
// retained for the strict retry, whatever the planner permitted.
func (r *lifecycleTeardownRecord) cleanupRetry() *lifecycleTeardownRecord {
	out := &lifecycleTeardownRecord{action: coremetadata.TeardownRetain, reason: coremetadata.TeardownReasonDeadPaneCleanupRetry}
	if r != nil {
		out.subject = r.subject
	}
	return out
}

// lifecycleTeardownRefusal projects a stable dead Pane authority conflict onto
// its refuse decision. Any other planner error is not a decision and yields
// nil. The subject is derived the way the planner derives it: the exact dead
// observation's Pane UID, its owner Window, and same-generation evidence.
func lifecycleTeardownRefusal(registry coremetadata.Registry, event lifecycleDirtyEvent, err error) *lifecycleTeardownRecord {
	reason, ok := stableDeadPaneAuthorityConflictReason(err)
	if !ok {
		return nil
	}
	record := &lifecycleTeardownRecord{action: coremetadata.TeardownRefuse, reason: reason}
	pane, ok := registry.Pane(strings.TrimSpace(event.paneUID))
	if !ok {
		return record
	}
	record.subject.paneUID = pane.Metadata.UID
	if windowUID, ok := paneWindowUID(registry, *pane); ok {
		record.subject.windowUID = windowUID
	}
	record.subject.classification = coremetadata.TerminationUnknown
	if stored := pane.Status.LastTermination; stored != nil &&
		stored.Generation == pane.Status.Activation.Generation &&
		coremetadata.ValidTerminationClassification(stored.Classification) {
		record.subject.classification = stored.Classification
	}
	return record
}

// stableDeadPaneAuthorityConflictReason recovers the typed reason from the
// planner's conflict error. The error value itself is unchanged so every
// existing caller, including the startup refusal seam that matches its text,
// keeps observing the same bytes; the reason is matched against the closed
// vocabulary and the shared format, never extracted as free text.
func stableDeadPaneAuthorityConflictReason(err error) (coremetadata.TeardownReason, bool) {
	if err == nil {
		return "", false
	}
	message := err.Error()
	for reason := range teardownReasonCodes {
		if strings.HasPrefix(message, stableDeadPaneAuthorityConflictPrefix(reason)) {
			return reason, true
		}
	}
	return "", false
}

// journalWindowUnlinkAwaitingPaneExit records the retain decision of an unpaired
// window-unlinked event: once when its own hook pass first waits, and once when
// the bounded causal wait is exhausted and the unlink is dropped. Carried
// retries in between are controller transport state and record nothing.
func journalWindowUnlinkAwaitingPaneExit(recorder *diagnostics.TeardownRecorder, subject lifecycleTeardownSubject) {
	(&lifecycleTeardownRecord{
		action: coremetadata.TeardownRetain, reason: coremetadata.TeardownReasonAwaitingPaneExit, subject: subject,
	}).journal(recorder)
}

// lifecycleWindowUnlinkAwaitingSubject names the Window an unpaired unlink is
// about, for the journal only. It never grants or narrows authority: the exact
// `$N/@N` hook handles select Registry Windows whose last exact binding carries
// them and whose owner resolves one managed root session. Exactly one Window
// sets the Window UID; exactly one Pane below it also sets the Pane UID and its
// current-generation termination classification. Any ambiguity omits the field.
func lifecycleWindowUnlinkAwaitingSubject(registry coremetadata.Registry, event lifecycleDirtyEvent) lifecycleTeardownSubject {
	var subject lifecycleTeardownSubject
	windowID, sessionID := strings.TrimSpace(event.runtimeWindowID), strings.TrimSpace(event.runtimeSessionID)
	if windowID == "" || sessionID == "" {
		return subject
	}
	var window *coremetadata.Window
	for i := range registry.Windows {
		candidate := &registry.Windows[i]
		if candidate.Status.RuntimeID != windowID || candidate.Status.RuntimeSessionID != sessionID {
			continue
		}
		root := candidate.Metadata.OwnerRef
		if root == nil || (root.Kind != coremetadata.KindProject && root.Kind != coremetadata.KindControlSession) ||
			lifecycleRootSessionName(registry, *root) == "" {
			continue
		}
		if window != nil {
			return subject
		}
		window = candidate
	}
	if window == nil {
		return subject
	}
	subject.windowUID = window.Metadata.UID
	var pane *coremetadata.Pane
	for i := range registry.Panes {
		candidate := &registry.Panes[i]
		if windowUID, ok := paneWindowUID(registry, *candidate); !ok || windowUID != window.Metadata.UID {
			continue
		}
		if pane != nil {
			return subject
		}
		pane = candidate
	}
	if pane == nil {
		return subject
	}
	subject.paneUID = pane.Metadata.UID
	if stored := pane.Status.LastTermination; stored != nil &&
		stored.Generation == pane.Status.Activation.Generation &&
		coremetadata.ValidTerminationClassification(stored.Classification) {
		subject.classification = stored.Classification
	}
	return subject
}

// awaitingTeardownSubject is the journal subject of an awaiting unlink plan and
// the zero subject for every other plan.
func (p exactLifecycleCascadePlan) awaitingTeardownSubject() lifecycleTeardownSubject {
	if !p.awaiting {
		return lifecycleTeardownSubject{}
	}
	return p.subject
}
