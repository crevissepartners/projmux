package diagnostics

import (
	"fmt"
	"time"
)

// teardownDecisionEvent is the one typed record of an automatic Window/Pane
// teardown decision. It shares the topology component with Continue recovery
// because both explain why desired Window topology did or did not change.
const teardownDecisionEvent = "topology.teardown.decision"

// TeardownDecision is the closed Registry action one lifecycle teardown
// decision permitted. The spellings are the core TeardownAction values.
type TeardownDecision string

const (
	TeardownDecisionRetain          TeardownDecision = "retain"
	TeardownDecisionDeletePaneAgent TeardownDecision = "delete-pane-agent"
	TeardownDecisionDeleteWindow    TeardownDecision = "delete-window"
	TeardownDecisionRefuse          TeardownDecision = "refuse"
)

var teardownDecisions = [...]TeardownDecision{
	TeardownDecisionRetain, TeardownDecisionDeletePaneAgent,
	TeardownDecisionDeleteWindow, TeardownDecisionRefuse,
}

// TeardownReasonCode is the closed, content-free journal code for one core
// TeardownReason: the reason spelling under the `topology.teardown.` prefix.
// Decision sites supply it through the app mapping; prose and errors never do.
type TeardownReasonCode string

const (
	TeardownReasonInvalidInput          TeardownReasonCode = "topology.teardown.invalid-input"
	TeardownReasonStaleGeneration       TeardownReasonCode = "topology.teardown.stale-generation"
	TeardownReasonUnavailable           TeardownReasonCode = "topology.teardown.observation-unavailable"
	TeardownReasonEmptyObservation      TeardownReasonCode = "topology.teardown.empty-observation"
	TeardownReasonNoServer              TeardownReasonCode = "topology.teardown.no-server"
	TeardownReasonPermissionDenied      TeardownReasonCode = "topology.teardown.permission-denied"
	TeardownReasonSiblingSocket         TeardownReasonCode = "topology.teardown.sibling-socket"
	TeardownReasonForeignHost           TeardownReasonCode = "topology.teardown.foreign-host"
	TeardownReasonNonCausalTermination  TeardownReasonCode = "topology.teardown.non-causal-termination"
	TeardownReasonPaneTeardown          TeardownReasonCode = "topology.teardown.pane-teardown"
	TeardownReasonAwaitingPaneExit      TeardownReasonCode = "topology.teardown.awaiting-pane-exit"
	TeardownReasonAwaitingWindowUnlink  TeardownReasonCode = "topology.teardown.awaiting-window-unlink"
	TeardownReasonLiveSiblingPane       TeardownReasonCode = "topology.teardown.live-sibling-pane"
	TeardownReasonWindowTeardown        TeardownReasonCode = "topology.teardown.window-teardown"
	TeardownReasonProjectTeardown       TeardownReasonCode = "topology.teardown.project-teardown"
	TeardownReasonMixedOwnerChain       TeardownReasonCode = "topology.teardown.mixed-owner-chain"
	TeardownReasonConflictingOwnerFacts TeardownReasonCode = "topology.teardown.conflicting-owner-facts"
	TeardownReasonStaleOwnerBinding     TeardownReasonCode = "topology.teardown.stale-owner-binding"
	TeardownReasonDeadPaneCleanupRetry  TeardownReasonCode = "topology.teardown.exact-dead-pane-cleanup-retry"
)

var teardownReasonCodes = [...]TeardownReasonCode{
	TeardownReasonInvalidInput, TeardownReasonStaleGeneration, TeardownReasonUnavailable,
	TeardownReasonEmptyObservation, TeardownReasonNoServer, TeardownReasonPermissionDenied,
	TeardownReasonSiblingSocket, TeardownReasonForeignHost, TeardownReasonNonCausalTermination,
	TeardownReasonPaneTeardown, TeardownReasonAwaitingPaneExit, TeardownReasonAwaitingWindowUnlink,
	TeardownReasonLiveSiblingPane, TeardownReasonWindowTeardown, TeardownReasonProjectTeardown,
	TeardownReasonMixedOwnerChain, TeardownReasonConflictingOwnerFacts, TeardownReasonStaleOwnerBinding,
	TeardownReasonDeadPaneCleanupRetry,
}

// TeardownClassification is the closed termination evidence vocabulary. The
// spellings are the core TerminationClassification values.
type TeardownClassification string

const (
	TeardownClassificationIntentional TeardownClassification = "intentional"
	TeardownClassificationInterrupted TeardownClassification = "interrupted"
	TeardownClassificationNormal      TeardownClassification = "normal"
	TeardownClassificationKilled      TeardownClassification = "killed"
	TeardownClassificationAbnormal    TeardownClassification = "abnormal"
	TeardownClassificationUnknown     TeardownClassification = "unknown"
)

var teardownClassifications = [...]TeardownClassification{
	TeardownClassificationIntentional, TeardownClassificationInterrupted, TeardownClassificationNormal,
	TeardownClassificationKilled, TeardownClassificationAbnormal, TeardownClassificationUnknown,
}

// maxTeardownUIDLength bounds one opaque Registry UID. Minted UIDs are far
// shorter; the bound only keeps a malformed row from widening the record.
const maxTeardownUIDLength = 96

// TeardownDecisionRecord is one consumed decision. Classification and both
// UIDs are optional because a refusal or an exhausted pair wait may precede any
// resolved owner chain; when present they are closed or strictly shaped.
type TeardownDecisionRecord struct {
	Decision       TeardownDecision
	Reason         TeardownReasonCode
	Classification TeardownClassification
	WindowUID      string
	PaneUID        string
}

// TeardownRecorder appends typed teardown decisions under the invocation run
// ID. Unlike Topology it owns no top-level outcome: an automatic hook may make
// several decisions, and a convergence failure must still reach RecordOutcome.
// Appends are best-effort and never flow back into lifecycle control paths.
type TeardownRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) Teardown() *TeardownRecorder {
	if r == nil {
		return nil
	}
	return &TeardownRecorder{lifecycle: r}
}

// Record appends one decision. A UID that is not a strictly shaped Window or
// Pane UID is omitted rather than projected; any other invalid value drops the
// record, because the vocabulary is closed and the caller maps it statically.
func (r *TeardownRecorder) Record(record TeardownDecisionRecord) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "topology",
		Event: teardownDecisionEvent, Result: "success",
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Code: string(record.Reason), Decision: string(record.Decision), Classification: string(record.Classification),
	}
	if validTeardownUID(record.WindowUID, "win-") {
		event.WindowUID = record.WindowUID
	}
	if validTeardownUID(record.PaneUID, "pane-") {
		event.PaneUID = record.PaneUID
	}
	if validateTeardownDecisionEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

func validateTeardownDecisionEvent(event Event) error {
	if event.Component != "topology" || event.Level != "info" || event.Result != "success" || event.Kind != "" ||
		event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Operation != "" || event.Source != "" ||
		event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() {
		return fmt.Errorf("invalid teardown decision shape")
	}
	if !validTeardownDecision(TeardownDecision(event.Decision)) {
		return fmt.Errorf("invalid teardown decision")
	}
	if !validTeardownReasonCode(TeardownReasonCode(event.Code)) {
		return fmt.Errorf("invalid teardown reason code")
	}
	if event.Classification != "" && !validTeardownClassification(TeardownClassification(event.Classification)) {
		return fmt.Errorf("invalid teardown classification")
	}
	if event.WindowUID != "" && !validTeardownUID(event.WindowUID, "win-") {
		return fmt.Errorf("invalid teardown Window uid")
	}
	if event.PaneUID != "" && !validTeardownUID(event.PaneUID, "pane-") {
		return fmt.Errorf("invalid teardown Pane uid")
	}
	return nil
}

func (event Event) hasTeardownFields() bool {
	return event.Decision != "" || event.Classification != "" || event.WindowUID != "" || event.PaneUID != ""
}

func validTeardownDecision(decision TeardownDecision) bool {
	for _, candidate := range teardownDecisions {
		if decision == candidate {
			return true
		}
	}
	return false
}

func validTeardownReasonCode(code TeardownReasonCode) bool {
	for _, candidate := range teardownReasonCodes {
		if code == candidate {
			return true
		}
	}
	return false
}

func validTeardownClassification(classification TeardownClassification) bool {
	for _, candidate := range teardownClassifications {
		if classification == candidate {
			return true
		}
	}
	return false
}

// validTeardownUID accepts only an opaque Registry UID of the named kind: the
// kind prefix and a lowercase alphanumeric/hyphen body. tmux `%N`/`@N`/`$N`
// handles, session names, and paths all fail the charset.
func validTeardownUID(uid, prefix string) bool {
	if len(uid) <= len(prefix) || len(uid) > maxTeardownUIDLength || uid[:len(prefix)] != prefix {
		return false
	}
	for _, r := range uid[len(prefix):] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
