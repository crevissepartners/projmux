package diagnostics

import (
	"fmt"
	"time"
)

// codexObserverFallbackEvent is the record of one native Codex observer that
// the SDK creator started and did not find serving: failed, exited, still
// pending, or reported a fallback by the observer. The Agent keeps the provider
// hook as its authority and the create still succeeds; this record lets an
// operator find out why without reading the Pane.
const codexObserverFallbackEvent = "codex.observer.fallback"

const (
	codexObserverSource    = "sdk"
	codexObserverOperation = "observer-start"
)

// codexObserverReasons is the closed set of reasons a creator reports for a
// start that is not serving. The first three are the creator's own receipts;
// observer-reported stands for everything the observer child committed itself
// (a fallback or a retry), whose own wording is never repeated here.
var codexObserverReasons = stringSet("observer-start-failed", "observer-exited", "observer-timeout", "observer-reported")

// CodexObserverRecorder appends the observer fallback record under the
// invocation run ID. It owns no top-level outcome, and appends are best-effort:
// they never flow back into the create.
type CodexObserverRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) CodexObserver() *CodexObserverRecorder {
	if r == nil {
		return nil
	}
	return &CodexObserverRecorder{lifecycle: r}
}

// RecordFallback appends one record naming the Agent and its Pane by opaque
// Registry UID and the closed reason. The observer's stderr and environment,
// socket paths, the thread id and the wrapped cause have no field here. A
// reason outside the closed set or a UID that is not strictly shaped drops
// the record.
func (r *CodexObserverRecorder) RecordFallback(agentUID, paneUID, reason string) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: "agent",
		Event: codexObserverFallbackEvent, Result: "success",
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Source: codexObserverSource, Operation: codexObserverOperation, Code: reason,
		AgentUID: agentUID, PaneUID: paneUID,
	}
	if validateCodexObserverFallbackEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

func validateCodexObserverFallbackEvent(event Event) error {
	rest := event
	rest.At, rest.Level, rest.Component, rest.Event, rest.Result = "", "", "", "", ""
	rest.DurationMS, rest.RunID, rest.Version, rest.MuxBackend = 0, "", "", ""
	rest.Source, rest.Operation, rest.Code, rest.AgentUID, rest.PaneUID = "", "", "", "", ""
	if rest != (Event{}) || event.Component != "agent" || event.Level != "info" || event.Result != "success" ||
		event.Source != codexObserverSource || event.Operation != codexObserverOperation {
		return fmt.Errorf("invalid codex observer fallback shape")
	}
	if _, ok := codexObserverReasons[event.Code]; !ok {
		return fmt.Errorf("invalid codex observer fallback reason")
	}
	if !validTeardownUID(event.AgentUID, "agent-") || !validTeardownUID(event.PaneUID, "pane-") {
		return fmt.Errorf("invalid codex observer fallback uid")
	}
	return nil
}
