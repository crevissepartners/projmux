package diagnostics

import (
	"fmt"
	"strings"
	"time"
)

// claudeHandoffRouteEvent is the record a Claude target helper writes when it
// refuses a push before its durable handoff because one end of the envelope
// route could not be proved again. The receipt keeps its existing
// `broker-handoff-persist-failed` reason, which every sender version reads as
// a known zero-write failure; this record names the cause the receipt cannot.
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

var (
	claudeHandoffSides = stringSet(string(ClaudeHandoffSourceUnproven), string(ClaudeHandoffTargetUnproven))
	claudeHandoffPeers = stringSet(string(ClaudeHandoffPeerTmuxClaude), string(ClaudeHandoffPeerTmuxCodex),
		string(ClaudeHandoffPeerProcessClaude), string(ClaudeHandoffPeerProcessCodex), string(ClaudeHandoffPeerUnknown))
)

// ClaudeHandoffRouteRecord is one refused handoff. AgentUID is the unproved
// Agent, omitted when not strictly shaped.
type ClaudeHandoffRouteRecord struct {
	Side     ClaudeHandoffRouteSide
	Peer     ClaudeHandoffPeer
	AgentUID string
}

// RecordHandoffRoute appends one record under the helper's run ID. A side or
// peer outside the closed sets drops it. Message refs, payloads, provider
// sessions, process identities, sockets, and error text are never recorded.
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
		event.LockHeldMS != nil || event.WaitMS != nil || event.hasCreatePhaseFields() || event.Decision != "" || event.Classification != "" ||
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
	return nil
}
