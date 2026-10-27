package diagnostics

import (
	"fmt"
	"strings"
	"time"
)

// claudeHandoffRouteEvent is the record a Claude target helper writes when it
// refuses a push before its durable handoff because one end of the envelope
// route could not be proved again. The receipt keeps its existing
// `broker-handoff-persist-failed` reason, or `provider-prewrite-refused` when a
// process target's proof before its reservation fails, which every sender
// version reads as a known zero-write failure; this record names the cause the
// receipt cannot.
const claudeHandoffRouteEvent = "agent.message.claude-handoff-route"

// claudeHandoffCodePrefix prefixes every side in the record's `code`.
const claudeHandoffCodePrefix = "claude.handoff."

// ClaudeHandoffRouteSide is the closed name of the envelope end that failed.
type ClaudeHandoffRouteSide string

const (
	ClaudeHandoffSourceUnproven ClaudeHandoffRouteSide = "source-route-unproven"
	ClaudeHandoffTargetUnproven ClaudeHandoffRouteSide = "target-route-unproven"
)

// ClaudeHandoffPeer is the closed host and provider of the unproved Agent as
// the Registry records it, carried in the `source` field.
type ClaudeHandoffPeer string

const (
	ClaudeHandoffPeerTmuxClaude    ClaudeHandoffPeer = "tmux-claude"
	ClaudeHandoffPeerTmuxCodex     ClaudeHandoffPeer = "tmux-codex"
	ClaudeHandoffPeerProcessClaude ClaudeHandoffPeer = "process-claude"
	ClaudeHandoffPeerProcessCodex  ClaudeHandoffPeer = "process-codex"
	ClaudeHandoffPeerUnknown       ClaudeHandoffPeer = "unknown"
)

// ClaudeHandoffRouteStage is the closed name of the proof step that refused
// the unproved end, carried in the `classification` field. Each names a check
// the proof already ran; none adds a read, probe, or wait. A record written
// before stages existed carries none.
type ClaudeHandoffRouteStage string

const (
	// The Registry resolved no current route for the Agent.
	ClaudeHandoffStageRouteUnresolved ClaudeHandoffRouteStage = "route-unresolved"
	// The resolved route does not accept the envelope's stored route fence.
	ClaudeHandoffStageRouteFenceMismatch ClaudeHandoffRouteStage = "route-fence-mismatch"
	// The Codex broker did not prove the route's authority.
	ClaudeHandoffStageCodexAuthorityUnproven ClaudeHandoffRouteStage = "codex-authority-unproven"
	// The Claude provider or lease helper process is not the recorded birth.
	ClaudeHandoffStageProviderProcessMismatch    ClaudeHandoffRouteStage = "provider-process-mismatch"
	ClaudeHandoffStageLeaseHelperProcessMismatch ClaudeHandoffRouteStage = "lease-helper-process-mismatch"
	// The source registration lease probe refused at the named step; `stale`
	// and `unanswered` split a refusal from an expired client bound.
	ClaudeHandoffStageLeaseAuthorityInvalid    ClaudeHandoffRouteStage = "lease-authority-invalid"
	ClaudeHandoffStageLeaseProcessMismatch     ClaudeHandoffRouteStage = "lease-process-mismatch"
	ClaudeHandoffStageLeaseSocketUnavailable   ClaudeHandoffRouteStage = "lease-socket-unavailable"
	ClaudeHandoffStageLeaseDialStale           ClaudeHandoffRouteStage = "lease-dial-stale"
	ClaudeHandoffStageLeaseDialUnanswered      ClaudeHandoffRouteStage = "lease-dial-unanswered"
	ClaudeHandoffStageLeasePeerMismatch        ClaudeHandoffRouteStage = "lease-peer-mismatch"
	ClaudeHandoffStageLeaseReadinessStale      ClaudeHandoffRouteStage = "lease-readiness-stale"
	ClaudeHandoffStageLeaseReadinessUnanswered ClaudeHandoffRouteStage = "lease-readiness-unanswered"
	ClaudeHandoffStageLeaseNotReady            ClaudeHandoffRouteStage = "lease-not-ready"
	ClaudeHandoffStageLeaseTargetInvalid       ClaudeHandoffRouteStage = "lease-target-invalid"
	ClaudeHandoffStageLeaseProbeStale          ClaudeHandoffRouteStage = "lease-probe-stale"
	ClaudeHandoffStageLeaseProbeUnanswered     ClaudeHandoffRouteStage = "lease-probe-unanswered"
	ClaudeHandoffStageLeaseProbeNotReady       ClaudeHandoffRouteStage = "lease-probe-not-ready"
	// The lease probe refused at a step this vocabulary does not name.
	ClaudeHandoffStageLeaseUnclassified ClaudeHandoffRouteStage = "lease-unclassified"
)

var (
	claudeHandoffSides = stringSet(string(ClaudeHandoffSourceUnproven), string(ClaudeHandoffTargetUnproven))
	claudeHandoffPeers = stringSet(string(ClaudeHandoffPeerTmuxClaude), string(ClaudeHandoffPeerTmuxCodex),
		string(ClaudeHandoffPeerProcessClaude), string(ClaudeHandoffPeerProcessCodex), string(ClaudeHandoffPeerUnknown))
	claudeHandoffStages = stringSet(string(ClaudeHandoffStageRouteUnresolved), string(ClaudeHandoffStageRouteFenceMismatch),
		string(ClaudeHandoffStageCodexAuthorityUnproven), string(ClaudeHandoffStageProviderProcessMismatch),
		string(ClaudeHandoffStageLeaseHelperProcessMismatch), string(ClaudeHandoffStageLeaseAuthorityInvalid),
		string(ClaudeHandoffStageLeaseProcessMismatch), string(ClaudeHandoffStageLeaseSocketUnavailable),
		string(ClaudeHandoffStageLeaseDialStale), string(ClaudeHandoffStageLeaseDialUnanswered),
		string(ClaudeHandoffStageLeasePeerMismatch), string(ClaudeHandoffStageLeaseReadinessStale),
		string(ClaudeHandoffStageLeaseReadinessUnanswered), string(ClaudeHandoffStageLeaseNotReady),
		string(ClaudeHandoffStageLeaseTargetInvalid), string(ClaudeHandoffStageLeaseProbeStale),
		string(ClaudeHandoffStageLeaseProbeUnanswered), string(ClaudeHandoffStageLeaseProbeNotReady),
		string(ClaudeHandoffStageLeaseUnclassified))
)

// ClaudeHandoffRouteRecord is one refused handoff. AgentUID is the unproved
// Agent, omitted when not strictly shaped. Stage is the refusing proof step,
// omitted when empty.
type ClaudeHandoffRouteRecord struct {
	Side     ClaudeHandoffRouteSide
	Peer     ClaudeHandoffPeer
	AgentUID string
	Stage    ClaudeHandoffRouteStage
}

// RecordHandoffRoute appends one record under the helper's run ID. A side,
// peer, or non-empty stage outside the closed sets drops it. Message refs,
// payloads, provider sessions, process identities, sockets, and error text
// are never recorded.
func (r *ClaudeRegistrationRecorder) RecordHandoffRoute(record ClaudeHandoffRouteRecord) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "error", Component: "agent",
		Event: claudeHandoffRouteEvent, Result: "error", Kind: "runtime",
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Code: claudeHandoffCodePrefix + string(record.Side), Source: string(record.Peer),
		Classification: string(record.Stage),
	}
	if validTeardownUID(record.AgentUID, "agent-") {
		event.AgentUID = record.AgentUID
	}
	if validateClaudeHandoffRouteEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

// validateClaudeHandoffRouteEvent admits exactly the shape RecordHandoffRoute
// writes.
func validateClaudeHandoffRouteEvent(event Event) error {
	if event.Component != "agent" || event.Level != "error" || event.Result != "error" || event.Kind != "runtime" ||
		event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Operation != "" || event.DurationMS != 0 ||
		event.LockHeldMS != nil || event.WaitMS != nil || event.hasCreatePhaseFields() || event.Decision != "" ||
		event.WindowUID != "" || event.PaneUID != "" || event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() {
		return fmt.Errorf("invalid claude handoff route shape")
	}
	side, ok := strings.CutPrefix(event.Code, claudeHandoffCodePrefix)
	if _, known := claudeHandoffSides[side]; !ok || !known {
		return fmt.Errorf("invalid claude handoff route code")
	}
	if _, known := claudeHandoffPeers[event.Source]; !known {
		return fmt.Errorf("invalid claude handoff route peer")
	}
	if event.AgentUID != "" && !validTeardownUID(event.AgentUID, "agent-") {
		return fmt.Errorf("invalid claude handoff route Agent uid")
	}
	if _, known := claudeHandoffStages[event.Classification]; event.Classification != "" && !known {
		return fmt.Errorf("invalid claude handoff route stage")
	}
	return nil
}
