package diagnostics

import (
	"fmt"
	"strings"
	"time"
)

// claudeRegistrationEvent is the record of one Claude messaging endpoint
// registration attempt: the SessionStart hook's refusal before its helper is
// admitted, or the detached helper's refusal, Ready, or end after Ready.
const claudeRegistrationEvent = "agent.claude.registration"

// claudeRegistrationCodePrefix prefixes every reason in the record's `code`.
const claudeRegistrationCodePrefix = "claude.registration."

// ClaudeRegistrationSource is the closed name of the process that wrote one
// record, carried in the `source` field.
type ClaudeRegistrationSource string

const (
	ClaudeRegistrationSourceHook   ClaudeRegistrationSource = "hook"
	ClaudeRegistrationSourceHelper ClaudeRegistrationSource = "helper"
)

// ClaudeRegistrationReason is the closed, content-free reason one registration
// attempt stopped or became Ready. The record carries it as
// `claude.registration.<reason>`. claudeRegistrationReasonTable is its only
// authority: a reason outside the table drops the record. Tests hold the table
// and the declared constants to the same set, and the app's registration sweep
// holds every constant to a registration site.
type ClaudeRegistrationReason string

// ClaudeRegistrationUnmanagedSession is the hook's stop for a Claude session
// projmux did not launch: no activation Registry path is set at all. It is
// outside any managed activation, so it is never recorded.
const ClaudeRegistrationUnmanagedSession ClaudeRegistrationReason = "unmanaged-session"

// Hook route and bootstrap refusals.
const (
	ClaudeRegistrationHookArguments           ClaudeRegistrationReason = "hook-arguments-present"
	ClaudeRegistrationRegistryPathInvalid     ClaudeRegistrationReason = "registry-path-invalid"
	ClaudeRegistrationRegistryUnreadable      ClaudeRegistrationReason = "registry-unreadable"
	ClaudeRegistrationHookInputUnreadable     ClaudeRegistrationReason = "hook-input-unreadable"
	ClaudeRegistrationPayloadNotSessionStart  ClaudeRegistrationReason = "payload-not-session-start"
	ClaudeRegistrationPaneBindingMismatch     ClaudeRegistrationReason = "pane-binding-mismatch"
	ClaudeRegistrationAgentMismatch           ClaudeRegistrationReason = "agent-mismatch"
	ClaudeRegistrationProviderProcessMismatch ClaudeRegistrationReason = "provider-process-mismatch"
	ClaudeRegistrationMessagingEnvInvalid     ClaudeRegistrationReason = "messaging-credential-invalid"
	ClaudeRegistrationSessionIDEmbedsLocator  ClaudeRegistrationReason = "session-id-embeds-credential"
	ClaudeRegistrationMessagingSocket         ClaudeRegistrationReason = "messaging-socket-unavailable"
	ClaudeRegistrationNonceUnavailable        ClaudeRegistrationReason = "nonce-unavailable"
	ClaudeRegistrationAuthorityInvalid        ClaudeRegistrationReason = "authority-invalid"
	ClaudeRegistrationHookIdentity            ClaudeRegistrationReason = "hook-identity-unavailable"
	ClaudeRegistrationReplyToolPolicy         ClaudeRegistrationReason = "reply-tool-policy-unavailable"
)

// Helper start refusals, written by the hook.
const (
	ClaudeRegistrationHelperExecutable  ClaudeRegistrationReason = "helper-executable-unavailable"
	ClaudeRegistrationHelperBootstrap   ClaudeRegistrationReason = "helper-bootstrap-unavailable"
	ClaudeRegistrationHelperAckPipe     ClaudeRegistrationReason = "helper-ack-pipe-unavailable"
	ClaudeRegistrationHelperStartFailed ClaudeRegistrationReason = "helper-start-failed"
	ClaudeRegistrationHelperUnconfirmed ClaudeRegistrationReason = "helper-admission-unconfirmed"
)

// Helper route refusals.
const (
	ClaudeRegistrationHelperArguments  ClaudeRegistrationReason = "helper-arguments-invalid"
	ClaudeRegistrationHelperAckMissing ClaudeRegistrationReason = "helper-ack-missing"
	ClaudeRegistrationHelperAckNotPipe ClaudeRegistrationReason = "helper-ack-not-pipe"
	ClaudeRegistrationHelperInput      ClaudeRegistrationReason = "helper-input-unreadable"
	ClaudeRegistrationProducerMismatch ClaudeRegistrationReason = "producer-mismatch"
)

// Helper serve refusals before Ready, the claim and record transaction's
// classified failures included.
const (
	ClaudeRegistrationBootstrapInvalid       ClaudeRegistrationReason = "bootstrap-invalid"
	ClaudeRegistrationHelperIdentity         ClaudeRegistrationReason = "helper-identity-unavailable"
	ClaudeRegistrationLeaseUnavailable       ClaudeRegistrationReason = "lease-unavailable"
	ClaudeRegistrationCoordinationListener   ClaudeRegistrationReason = "coordination-unavailable"
	ClaudeRegistrationLeaseOwnerUnavailable  ClaudeRegistrationReason = "lease-owner-unavailable"
	ClaudeRegistrationProviderProcessGone    ClaudeRegistrationReason = "provider-process-gone"
	ClaudeRegistrationClaimRefusedActivation ClaudeRegistrationReason = "claim-refused-activation"
	ClaudeRegistrationClaimRefusedCompeting  ClaudeRegistrationReason = "claim-refused-competing"
	ClaudeRegistrationClaimRefusedNewer      ClaudeRegistrationReason = "claim-refused-newer"
	ClaudeRegistrationLockTimeout            ClaudeRegistrationReason = "lock-timeout"
	ClaudeRegistrationLockAcquireFailed      ClaudeRegistrationReason = "lock-acquire-failed"
	ClaudeRegistrationRegistryDegraded       ClaudeRegistrationReason = "registry-degraded"
	ClaudeRegistrationRegistryWriteFailed    ClaudeRegistrationReason = "registry-write-failed"
	ClaudeRegistrationRouteMismatch          ClaudeRegistrationReason = "route-mismatch"
	ClaudeRegistrationDialogueBroker         ClaudeRegistrationReason = "dialogue-broker-unavailable"
	ClaudeRegistrationReplyToolGate          ClaudeRegistrationReason = "reply-tool-gate-unavailable"
	ClaudeRegistrationStaleBeforeAck         ClaudeRegistrationReason = "stale-before-ack"
)

// Ready, and the ends of a helper's lifetime after Ready.
const (
	ClaudeRegistrationReady             ClaudeRegistrationReason = "ready"
	ClaudeRegistrationEndedContextDone  ClaudeRegistrationReason = "ended-context-done"
	ClaudeRegistrationEndedNotCurrent   ClaudeRegistrationReason = "ended-not-current"
	ClaudeRegistrationEndedAcceptFailed ClaudeRegistrationReason = "ended-accept-failed"
)

// claudeRegistrationStage is how a reason ends one attempt, which fixes the
// record's level and result.
type claudeRegistrationStage uint8

const (
	// claudeRegistrationRefused: the attempt stopped before Ready; error/error.
	claudeRegistrationRefused claudeRegistrationStage = iota + 1
	// claudeRegistrationAdmitted: the helper acknowledged Ready; info/success.
	claudeRegistrationAdmitted
	// claudeRegistrationEnded: a Ready helper stopped serving; info/success.
	claudeRegistrationEnded
	// claudeRegistrationOutside: the attempt never concerned a managed
	// activation. The recorder drops it and the validator rejects it, so it
	// is never journaled at any level.
	claudeRegistrationOutside
)

type claudeRegistrationSources uint8

const (
	claudeRegistrationFromHook claudeRegistrationSources = 1 << iota
	claudeRegistrationFromHelper
)

type claudeRegistrationReasonSpec struct {
	reason  ClaudeRegistrationReason
	sources claudeRegistrationSources
	stage   claudeRegistrationStage
}

// claudeRegistrationReasonTable is the one authority for the closed reason
// set: each reason, which process may record it, and how it ends the attempt.
var claudeRegistrationReasonTable = [...]claudeRegistrationReasonSpec{
	{ClaudeRegistrationUnmanagedSession, claudeRegistrationFromHook, claudeRegistrationOutside},
	{ClaudeRegistrationHookArguments, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationRegistryPathInvalid, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationRegistryUnreadable, claudeRegistrationFromHook | claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHookInputUnreadable, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationPayloadNotSessionStart, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationPaneBindingMismatch, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationAgentMismatch, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationProviderProcessMismatch, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationMessagingEnvInvalid, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationSessionIDEmbedsLocator, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationMessagingSocket, claudeRegistrationFromHook | claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationNonceUnavailable, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationAuthorityInvalid, claudeRegistrationFromHook | claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHookIdentity, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationReplyToolPolicy, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperExecutable, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperBootstrap, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperAckPipe, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperStartFailed, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperUnconfirmed, claudeRegistrationFromHook, claudeRegistrationRefused},
	{ClaudeRegistrationHelperArguments, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHelperAckMissing, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHelperAckNotPipe, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHelperInput, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationProducerMismatch, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationBootstrapInvalid, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationHelperIdentity, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationLeaseUnavailable, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationCoordinationListener, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationLeaseOwnerUnavailable, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationProviderProcessGone, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationClaimRefusedActivation, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationClaimRefusedCompeting, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationClaimRefusedNewer, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationLockTimeout, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationLockAcquireFailed, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationRegistryDegraded, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationRegistryWriteFailed, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationRouteMismatch, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationDialogueBroker, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationReplyToolGate, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationStaleBeforeAck, claudeRegistrationFromHelper, claudeRegistrationRefused},
	{ClaudeRegistrationReady, claudeRegistrationFromHelper, claudeRegistrationAdmitted},
	{ClaudeRegistrationEndedContextDone, claudeRegistrationFromHelper, claudeRegistrationEnded},
	{ClaudeRegistrationEndedNotCurrent, claudeRegistrationFromHelper, claudeRegistrationEnded},
	{ClaudeRegistrationEndedAcceptFailed, claudeRegistrationFromHelper, claudeRegistrationEnded},
}

func claudeRegistrationSpec(reason ClaudeRegistrationReason) (claudeRegistrationReasonSpec, bool) {
	for _, spec := range claudeRegistrationReasonTable {
		if spec.reason == reason {
			return spec, true
		}
	}
	return claudeRegistrationReasonSpec{}, false
}

// Refusal reports a reason in the table that stops an attempt before Ready.
func (r ClaudeRegistrationReason) Refusal() bool {
	spec, ok := claudeRegistrationSpec(r)
	return ok && spec.stage == claudeRegistrationRefused
}

// Recorded reports a reason in the table that is ever journaled.
func (r ClaudeRegistrationReason) Recorded() bool {
	spec, ok := claudeRegistrationSpec(r)
	return ok && spec.stage != claudeRegistrationOutside
}

// Code is the record's `code` spelling of the reason.
func (r ClaudeRegistrationReason) Code() string {
	return claudeRegistrationCodePrefix + string(r)
}

func (s claudeRegistrationSources) allows(source ClaudeRegistrationSource) bool {
	switch source {
	case ClaudeRegistrationSourceHook:
		return s&claudeRegistrationFromHook != 0
	case ClaudeRegistrationSourceHelper:
		return s&claudeRegistrationFromHelper != 0
	default:
		return false
	}
}

// ClaudeRegistrationRecord is one attempt's outcome as its process states it.
// Duration runs from that process's route entry to the append. The UIDs are the
// Registry-matched Agent and Pane; the caller passes them only once its
// bootstrap has matched both against the Registry.
type ClaudeRegistrationRecord struct {
	Source   ClaudeRegistrationSource
	Reason   ClaudeRegistrationReason
	Duration time.Duration
	AgentUID string
	PaneUID  string
}

// ClaudeRegistrationRecorder appends `agent.claude.registration` records under
// the invocation run ID. It owns no top-level outcome: both routes return nil
// whatever happened. Appends are best-effort and never flow back into the hook
// or the helper.
type ClaudeRegistrationRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) ClaudeRegistration() *ClaudeRegistrationRecorder {
	if r == nil {
		return nil
	}
	return &ClaudeRegistrationRecorder{lifecycle: r}
}

// Record appends one record. A source or reason outside the closed set, or a
// reason that source may not record, drops it. A UID that is not strictly
// shaped is omitted rather than projected. The provider session id, the
// registration nonce, the messaging token and socket, lease paths, process
// identities, argv, and error text have no field here and are never recorded.
func (r *ClaudeRegistrationRecorder) Record(record ClaudeRegistrationRecord) {
	if r == nil || r.lifecycle == nil {
		return
	}
	spec, ok := claudeRegistrationSpec(record.Reason)
	if !ok || !spec.sources.allows(record.Source) || spec.stage == claudeRegistrationOutside {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "agent",
		Event: claudeRegistrationEvent, Result: "success", DurationMS: max(record.Duration, 0).Milliseconds(),
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Source: string(record.Source), Code: record.Reason.Code(),
	}
	if spec.stage == claudeRegistrationRefused {
		event.Level, event.Result, event.Kind = "error", "error", "runtime"
	}
	if validTeardownUID(record.AgentUID, "agent-") {
		event.AgentUID = record.AgentUID
	}
	if validTeardownUID(record.PaneUID, "pane-") {
		event.PaneUID = record.PaneUID
	}
	if validateClaudeRegistrationEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

// validateClaudeRegistrationEvent admits exactly the shape Record writes.
func validateClaudeRegistrationEvent(event Event) error {
	if event.Component != "agent" || event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Operation != "" ||
		event.LockHeldMS != nil || event.WaitMS != nil || event.hasCreatePhaseFields() || event.Decision != "" || event.Classification != "" ||
		event.WindowUID != "" || event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() {
		return fmt.Errorf("invalid claude registration shape")
	}
	reason, ok := strings.CutPrefix(event.Code, claudeRegistrationCodePrefix)
	if !ok {
		return fmt.Errorf("invalid claude registration code")
	}
	spec, ok := claudeRegistrationSpec(ClaudeRegistrationReason(reason))
	if !ok || spec.stage == claudeRegistrationOutside {
		return fmt.Errorf("invalid claude registration code")
	}
	if !spec.sources.allows(ClaudeRegistrationSource(event.Source)) {
		return fmt.Errorf("invalid claude registration source")
	}
	if spec.stage == claudeRegistrationRefused {
		if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" {
			return fmt.Errorf("invalid claude registration refusal shape")
		}
	} else if event.Level != "info" || event.Result != "success" || event.Kind != "" {
		return fmt.Errorf("invalid claude registration success shape")
	}
	if event.AgentUID != "" && !validTeardownUID(event.AgentUID, "agent-") {
		return fmt.Errorf("invalid claude registration Agent uid")
	}
	if event.PaneUID != "" && !validTeardownUID(event.PaneUID, "pane-") {
		return fmt.Errorf("invalid claude registration Pane uid")
	}
	return nil
}
