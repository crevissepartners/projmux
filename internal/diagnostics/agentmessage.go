package diagnostics

import (
	"fmt"
	"time"
)

// agentMessageForeignSourceEvent is the one record an accepted `agent message
// send` writes when it names a Claude source Agent whose registered Claude
// process the caller does not descend from. The send itself is unchanged; the
// record keeps that caller mismatch findable after the stderr warning scrolls
// away.
const agentMessageForeignSourceEvent = "agent.message.foreign-source"

// AgentMessageRecorder appends typed agent-message records under the
// invocation run ID. It owns no top-level outcome: the send keeps its own
// `command.outcome`. Appends are best-effort and never flow back into the send.
type AgentMessageRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) AgentMessage() *AgentMessageRecorder {
	if r == nil {
		return nil
	}
	return &AgentMessageRecorder{lifecycle: r}
}

// RecordForeignSource appends one record naming the source Agent and its
// Pane by opaque Registry UID. The provider session, process ids, and
// environment the stderr warning shows are never recorded. A UID that is not
// strictly shaped drops the record.
func (r *AgentMessageRecorder) RecordForeignSource(agentUID, paneUID string) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "agent",
		Event: agentMessageForeignSourceEvent, Result: "success",
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		AgentUID: agentUID, PaneUID: paneUID,
	}
	if validateAgentMessageForeignSourceEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

func validateAgentMessageForeignSourceEvent(event Event) error {
	if event.Component != "agent" || event.Level != "info" || event.Result != "success" || event.Kind != "" ||
		event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Operation != "" || event.Source != "" ||
		event.Code != "" || event.LockHeldMS != nil || event.Decision != "" || event.Classification != "" || event.WindowUID != "" ||
		event.hasCounts() || event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() {
		return fmt.Errorf("invalid agent message foreign source shape")
	}
	// Agent and Pane UIDs share the teardown record's strict opaque shape.
	if !validTeardownUID(event.AgentUID, "agent-") || !validTeardownUID(event.PaneUID, "pane-") {
		return fmt.Errorf("invalid agent message foreign source uid")
	}
	return nil
}
