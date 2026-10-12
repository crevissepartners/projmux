package diagnostics

import (
	"fmt"
	"time"
)

const attentionSyncEvent = "agent.attention.sync"

// RecordAttentionSync records the first failure of an episode and its recovery.
// The owner supplies a closed classification; raw errors never enter the log.
func (r *LifecycleRecorder) RecordAttentionSync(agentUID, kind string, recovered bool) {
	if r == nil {
		return
	}
	event := Event{At: r.now().UTC().Format(time.RFC3339Nano), Level: "error", Component: "agent", Event: attentionSyncEvent, Result: "error", RunID: r.runID, Version: r.version, MuxBackend: r.muxBackend, AgentUID: agentUID, Code: "attention.sync." + kind}
	if recovered {
		event.Level, event.Result, event.Code = "info", "success", "attention.sync.recovered"
	}
	if validateAttentionSyncEvent(event) != nil {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.append(event)
}

func validateAttentionSyncEvent(event Event) error {
	switch event.Code {
	case "attention.sync.lock-timeout", "attention.sync.notify-write", "attention.sync.io", "attention.sync.projection":
		if event.Level != "error" || event.Result != "error" {
			return fmt.Errorf("invalid attention sync failure")
		}
	case "attention.sync.recovered":
		if event.Level != "info" || event.Result != "success" {
			return fmt.Errorf("invalid attention sync recovery")
		}
	default:
		return fmt.Errorf("invalid attention sync classification")
	}
	if !validTeardownUID(event.AgentUID, "agent-") {
		return fmt.Errorf("invalid attention sync identity")
	}
	expected := Event{At: event.At, Level: event.Level, Component: "agent", Event: attentionSyncEvent, Result: event.Result, RunID: event.RunID, Version: event.Version, MuxBackend: event.MuxBackend, AgentUID: event.AgentUID, Code: event.Code}
	if event != expected {
		return fmt.Errorf("invalid attention sync shape")
	}
	return nil
}
