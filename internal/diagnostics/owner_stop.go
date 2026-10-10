package diagnostics

import (
	"fmt"
	"time"
)

const ownerStopEvent = "agent.owner.stop"

// OwnerStopReason is a closed, content-free explanation of an owner's first
// shutdown path. It describes the trigger, not the provider's exit class.
type OwnerStopReason string

const (
	OwnerStopSIGINT       OwnerStopReason = "signal-sigint"
	OwnerStopSIGTERM      OwnerStopReason = "signal-sigterm"
	OwnerStopStdinEOF     OwnerStopReason = "stdin-eof"
	OwnerStopControl      OwnerStopReason = "control-stop"
	OwnerStopGeneration   OwnerStopReason = "generation-abandoned"
	OwnerStopProviderExit OwnerStopReason = "provider-exit"
	OwnerStopOther        OwnerStopReason = "other"
)

func validOwnerStopReason(reason OwnerStopReason) bool {
	switch reason {
	case OwnerStopSIGINT, OwnerStopSIGTERM, OwnerStopStdinEOF, OwnerStopControl, OwnerStopGeneration, OwnerStopProviderExit, OwnerStopOther:
		return true
	}
	return false
}

// OwnerStopRecord contains only opaque binding IDs and owner process identity.
// ParentComm is a bounded process name, never argv or provider content.
type OwnerStopRecord struct {
	Reason                        OwnerStopReason
	AgentUID, PaneUID, Generation string
	OwnerPID, OwnerPPID           int
	ParentComm                    string
}

// RecordOwnerStop appends best-effort diagnostics without changing shutdown.
func (r *LifecycleRecorder) RecordOwnerStop(record OwnerStopRecord) {
	if r == nil {
		return
	}
	event := Event{At: r.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "agent", Event: ownerStopEvent, Result: "success", RunID: r.runID, Version: r.version, MuxBackend: r.muxBackend,
		Code: "owner.stop." + string(record.Reason), AgentUID: record.AgentUID, PaneUID: record.PaneUID, Generation: record.Generation, OwnerPID: record.OwnerPID, OwnerPPID: record.OwnerPPID, ParentComm: record.ParentComm}
	if validateOwnerStopEvent(event) != nil {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.append(event)
}

func validateOwnerStopEvent(event Event) error {
	reason := OwnerStopReason("")
	const prefix = "owner.stop."
	if len(event.Code) >= len(prefix) && event.Code[:len(prefix)] == prefix {
		reason = OwnerStopReason(event.Code[len(prefix):])
	}
	if !validOwnerStopReason(reason) || !validTeardownUID(event.AgentUID, "agent-") || !validTeardownUID(event.PaneUID, "pane-") || !validTeardownUID(event.Generation, "gen-") || event.OwnerPID <= 0 || event.OwnerPPID < 0 || !ValidOwnerParentComm(event.ParentComm) {
		return fmt.Errorf("invalid owner stop identity or reason")
	}
	// Comparing the complete shape keeps new fields from leaking into this family.
	expected := Event{At: event.At, Level: "info", Component: "agent", Event: ownerStopEvent, Result: "success", RunID: event.RunID, Version: event.Version, MuxBackend: event.MuxBackend, Code: event.Code, AgentUID: event.AgentUID, PaneUID: event.PaneUID, Generation: event.Generation, OwnerPID: event.OwnerPID, OwnerPPID: event.OwnerPPID, ParentComm: event.ParentComm}
	if event != expected {
		return fmt.Errorf("invalid owner stop shape")
	}
	return nil
}

// ValidOwnerParentComm accepts a short process basename or an unavailable value.
func ValidOwnerParentComm(value string) bool {
	if len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
