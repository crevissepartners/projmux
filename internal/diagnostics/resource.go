package diagnostics

import (
	"sync"
	"time"
)

// ResourceSource is the closed ownership boundary for one attribution anomaly.
// It never contains a path, tmux identifier, PID, process value, or metric.
type ResourceSource string

const (
	ResourceSourceSampler          ResourceSource = "sampler"
	ResourceSourceInventory        ResourceSource = "tmux-inventory"
	ResourceSourceProjectDiscovery ResourceSource = "project-discovery"
	ResourceSourceRefresh          ResourceSource = "refresh"
)

// ResourceResult classifies only anomalous Resource Inspector outcomes.
// Healthy periodic samples and refreshes are deliberately absent.
type ResourceResult string

const (
	ResourceResultUnavailable        ResourceResult = "unavailable"
	ResourceResultPartial            ResourceResult = "partial"
	ResourceResultStale              ResourceResult = "stale"
	ResourceResultError              ResourceResult = "error"
	ResourceResultScanBudgetExceeded ResourceResult = "scan-budget-exceeded"
)

// ResourceFailure is a safe stage enum, never an error string or sampler data.
type ResourceFailure string

const (
	ResourceFailureSampleUnavailable ResourceFailure = "sample-unavailable"
	ResourceFailureSamplePartial     ResourceFailure = "sample-partial"
	ResourceFailureSampleStale       ResourceFailure = "sample-stale"
	ResourceFailureInventory         ResourceFailure = "inventory-failed"
	ResourceFailureProjectDiscovery  ResourceFailure = "project-discovery-failed"
	ResourceFailureCollection        ResourceFailure = "collection-failed"
	ResourceFailureScanBudget        ResourceFailure = "scan-budget-exceeded"
)

type resourceAnomaly struct {
	result  ResourceResult
	failure ResourceFailure
}

// ResourceRecorder emits only transitions into an anomaly. A persistent safe
// tuple is coalesced until Healthy observes recovery; a later re-entry is then
// observable. Appends remain best-effort and have no control-flow effect.
type ResourceRecorder struct {
	writer     EventWriter
	runID      string
	version    string
	muxBackend string
	now        func() time.Time

	mu     sync.Mutex
	active map[ResourceSource]resourceAnomaly
}

func (r *ResourceRecorder) Record(source ResourceSource, result ResourceResult, failure ResourceFailure, started time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = make(map[ResourceSource]resourceAnomaly)
	}
	anomaly := resourceAnomaly{result: result, failure: failure}
	if r.active[source] == anomaly {
		return
	}
	r.active[source] = anomaly
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	event := Event{
		At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "resource", Event: "resource.sampler.outcome",
		Result: "success", DurationMS: max(now.Sub(started).Milliseconds(), 0), RunID: r.runID, Version: r.version,
		MuxBackend: r.muxBackend, Source: string(source), ResourceResult: string(result), Failure: string(failure),
	}
	if result == ResourceResultUnavailable || result == ResourceResultError || result == ResourceResultScanBudgetExceeded {
		event.Level, event.Result, event.Kind = "error", "error", "runtime"
	}
	if r.writer != nil {
		_ = r.writer.Append(event)
	}
}

// Healthy silently clears prior anomalies. Normal samples therefore emit no
// event, while the same anomaly can be recorded again after recovery.
func (r *ResourceRecorder) Healthy() {
	if r == nil {
		return
	}
	r.mu.Lock()
	clear(r.active)
	r.mu.Unlock()
}

// Recover clears only the named ownership surfaces after a successful pass.
func (r *ResourceRecorder) Recover(sources ...ResourceSource) {
	if r == nil {
		return
	}
	r.mu.Lock()
	for _, source := range sources {
		delete(r.active, source)
	}
	r.mu.Unlock()
}

func resourceTupleMatches(event Event) bool {
	result, failure := ResourceResult(event.ResourceResult), ResourceFailure(event.Failure)
	wantFailure := map[ResourceResult]ResourceFailure{
		ResourceResultUnavailable:        ResourceFailureSampleUnavailable,
		ResourceResultPartial:            ResourceFailureSamplePartial,
		ResourceResultStale:              ResourceFailureSampleStale,
		ResourceResultScanBudgetExceeded: ResourceFailureScanBudget,
	}
	if result == ResourceResultError {
		if event.Level != "error" || event.Result != "error" || event.Kind != "runtime" {
			return false
		}
		switch event.Source {
		case string(ResourceSourceInventory):
			return failure == ResourceFailureInventory
		case string(ResourceSourceProjectDiscovery):
			return failure == ResourceFailureProjectDiscovery
		case string(ResourceSourceSampler):
			return failure == ResourceFailureCollection
		default:
			return false
		}
	}
	if failure != wantFailure[result] {
		return false
	}
	switch result {
	case ResourceResultUnavailable:
		return event.Source == string(ResourceSourceSampler) && event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
	case ResourceResultPartial:
		return event.Source == string(ResourceSourceSampler) && event.Level == "info" && event.Result == "success" && event.Kind == ""
	case ResourceResultStale:
		return event.Source == string(ResourceSourceRefresh) && event.Level == "info" && event.Result == "success" && event.Kind == ""
	case ResourceResultScanBudgetExceeded:
		return event.Source == string(ResourceSourceSampler) && event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
	default:
		return false
	}
}

// Resource returns the Phase 6 recorder bound to this process run.
func (r *LifecycleRecorder) Resource() *ResourceRecorder {
	if r == nil {
		return nil
	}
	return &ResourceRecorder{writer: r.writer, runID: r.runID, version: r.version, muxBackend: r.muxBackend, now: time.Now}
}
