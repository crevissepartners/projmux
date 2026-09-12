package diagnostics

import (
	"fmt"
	"sync"
	"time"
)

// TopologyAgentReason is the closed, content-free first refusal of a retained
// Agent. Decision sites supply it directly; prose and provider errors never do.
type TopologyAgentReason string

const (
	TopologyAgentTerminationExcluded  TopologyAgentReason = "topology.agent.termination-excluded"
	TopologyAgentPhaseIneligible      TopologyAgentReason = "topology.agent.phase-ineligible"
	TopologyAgentActivationUnproven   TopologyAgentReason = "topology.agent.activation-unproven"
	TopologyAgentTerminationInvalid   TopologyAgentReason = "topology.agent.termination-invalid"
	TopologyAgentSessionRefMissing    TopologyAgentReason = "topology.agent.session-ref-missing"
	TopologyAgentSessionRefInvalid    TopologyAgentReason = "topology.agent.session-ref-invalid"
	TopologyAgentSessionRefMismatch   TopologyAgentReason = "topology.agent.session-ref-mismatch"
	TopologyAgentProviderUnavailable  TopologyAgentReason = "topology.agent.provider-unavailable"
	TopologyAgentWorkspaceUnavailable TopologyAgentReason = "topology.agent.workspace-unavailable"
	TopologyAgentResumePrepareFailed  TopologyAgentReason = "topology.agent.resume-prepare-failed"
)

var topologyAgentReasons = [...]TopologyAgentReason{
	TopologyAgentTerminationExcluded, TopologyAgentPhaseIneligible,
	TopologyAgentActivationUnproven, TopologyAgentTerminationInvalid,
	TopologyAgentSessionRefMissing, TopologyAgentSessionRefInvalid,
	TopologyAgentSessionRefMismatch, TopologyAgentProviderUnavailable,
	TopologyAgentWorkspaceUnavailable, TopologyAgentResumePrepareFailed,
}

func validTopologyAgentReason(reason TopologyAgentReason) bool {
	for _, candidate := range topologyAgentReasons {
		if reason == candidate {
			return true
		}
	}
	return false
}

// TopologyCounts describes only a committed Continue/materialize pass. Existing
// live Agents are absent. An error has no committed counts or reason rows.
type TopologyCounts struct {
	Resumed int                         `json:"resumed"`
	Skipped int                         `json:"skipped"`
	Reasons map[TopologyAgentReason]int `json:"reasons,omitempty"`
}

func (c TopologyCounts) validate(result LifecycleResult) error {
	if c.Resumed < 0 || c.Skipped < 0 {
		return fmt.Errorf("negative topology count")
	}
	if result != LifecycleSuccess && result != LifecycleError {
		return fmt.Errorf("invalid topology result")
	}
	if result == LifecycleError && (c.Resumed != 0 || c.Skipped != 0 || len(c.Reasons) != 0) {
		return fmt.Errorf("uncommitted topology counts")
	}
	remaining := c.Skipped
	for code, count := range c.Reasons {
		if !validTopologyAgentReason(code) || count <= 0 || count > remaining {
			return fmt.Errorf("invalid topology reason count")
		}
		remaining -= count
	}
	if remaining != 0 {
		return fmt.Errorf("topology reason counts differ from skipped total")
	}
	return nil
}

// TopologyRecorder owns exactly one execution, including its failure. Creating
// it has no journal effect. Preview/replanning never call Record. Logical
// ownership is sealed before best-effort I/O, even when the writer fails.
type TopologyRecorder struct {
	lifecycle *LifecycleRecorder
	once      sync.Once
}

func (r *LifecycleRecorder) Topology() *TopologyRecorder {
	if r == nil {
		return nil
	}
	return &TopologyRecorder{lifecycle: r}
}

func (r *TopologyRecorder) Record(started time.Time, result LifecycleResult, counts TopologyCounts) {
	if r == nil || counts.validate(result) != nil {
		return
	}
	r.once.Do(func() {
		owner := r.lifecycle
		now := owner.now()
		event := Event{
			At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "topology",
			Event: "topology.outcome", Result: string(result), DurationMS: max(now.Sub(started).Milliseconds(), 0),
			RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
			ResumedCount: intPointer(counts.Resumed), SkippedCount: intPointer(counts.Skipped),
		}
		if result == LifecycleError {
			event.Level, event.Kind = "error", "runtime"
		}
		owner.outcomes.Add(1)
		owner.writeMu.Lock()
		defer owner.writeMu.Unlock()
		owner.append(event)
		for _, code := range topologyAgentReasons {
			if count := counts.Reasons[code]; count > 0 {
				reason := event
				reason.Event = "topology.agent.skipped"
				reason.ResumedCount, reason.SkippedCount = nil, nil
				reason.Code, reason.ItemCount = string(code), intPointer(count)
				owner.append(reason)
			}
		}
	})
}

func validateTopologyEvent(event Event) error {
	if event.Component != "topology" || event.Message != "" || event.Command != "" || event.Subcommand != "" || event.Operation != "" || event.Source != "" || event.hasSnapshotCounts() || event.hasNotifyFocusFields() || event.hasAIFields() || event.hasResourceFields() || !event.nonNegativeCounts() {
		return fmt.Errorf("invalid topology event shape")
	}
	if event.Event == "topology.agent.skipped" {
		if event.Result != "success" || event.Level != "info" || event.Kind != "" || !validTopologyAgentReason(TopologyAgentReason(event.Code)) || event.ItemCount == nil || *event.ItemCount <= 0 || event.hasTopologyCounts() {
			return fmt.Errorf("invalid topology reason shape")
		}
		return nil
	}
	if event.Code != "" || event.ItemCount != nil || event.ResumedCount == nil || event.SkippedCount == nil {
		return fmt.Errorf("invalid topology outcome counts")
	}
	switch event.Result {
	case "success":
		if event.Level != "info" || event.Kind != "" {
			return fmt.Errorf("invalid topology success shape")
		}
	case "error":
		if event.Level != "error" || event.Kind != "runtime" || *event.ResumedCount != 0 || *event.SkippedCount != 0 {
			return fmt.Errorf("invalid topology error shape")
		}
	default:
		return fmt.Errorf("invalid topology result")
	}
	return nil
}
