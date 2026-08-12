// Package resources defines the read-only resource-attribution model shared
// by the tmux inventory adapter, procfs collector, and a future UI consumer.
// The model intentionally contains no process command lines or persistent
// state representation.
package resources

import "time"

const (
	ProjectUnassigned = "Unassigned"
	ProjectShared     = "Shared / ambiguous"
	OtherUnattributed = "Other / unattributed"
)

// Status describes whether a snapshot can be presented to a user.
type Status string

const (
	StatusWarming     Status = "warming"
	StatusReady       Status = "ready"
	StatusPartial     Status = "partial"
	StatusUnavailable Status = "unavailable"
)

// PaneInventory is a typed tmux topology row. Repeated pane/window ids are
// retained here because linked windows can expose the same pane under more
// than one session/project anchor.
type PaneInventory struct {
	Socket        string
	SessionID     string
	SessionName   string
	WindowID      string
	WindowName    string
	PaneID        string
	PanePID       int
	PaneTTY       string
	ProjectAnchor string
	PaneLabel     string
	AIAgent       string
	AITopic       string
	PaneCommand   string
	PaneTitle     string
}

// ProcessIdentity prevents a recycled PID from inheriting an earlier sample.
type ProcessIdentity struct {
	PID            int
	StartTimeTicks uint64
}

// ProcessSample contains only the fields required for attribution. It must
// never grow command-line, prompt, pane-content, or transcript fields.
type ProcessSample struct {
	Identity     ProcessIdentity
	ParentPID    int
	SessionID    int
	CPUTimeTicks uint64
	RSSBytes     uint64
}

// HostSample is the procfs host-capacity input for a scan.
type HostSample struct {
	CPUAvailable         bool
	MemoryAvailable      bool
	LogicalCPUs          int
	CPUTotalTicks        uint64
	CPUIdleTicks         uint64
	MemoryTotalBytes     uint64
	MemoryAvailableBytes uint64
}

// ScanDiagnostics reports bounded counts only. It contains no process names
// or other identifying payloads.
type ScanDiagnostics struct {
	Duration         time.Duration
	SampledProcesses int
	SkippedProcesses int
	RaceCount        int
	PermissionCount  int
}

// Sample is one immutable-by-convention procfs observation. Callers replace
// samples rather than mutating them and never persist them as session state.
type Sample struct {
	At                time.Time
	Host              HostSample
	Processes         []ProcessSample
	Diagnostics       ScanDiagnostics
	Available         bool
	UnavailableReason string
}

// CPUUsage carries both views computed from the same tick interval.
type CPUUsage struct {
	HostSharePercent float64
	CoreEquivalent   float64
}

// MemoryUsage is an RSS sum, not unique resident memory.
type MemoryUsage struct {
	RSSBytes    uint64
	HostPercent *float64
}

// PaneUsage is the finest UI-facing resource row. Sessions contains every
// linked-session view of the unique pane.
type PaneUsage struct {
	Socket        string
	PaneID        string
	WindowID      string
	WindowName    string
	SessionIDs    []string
	Sessions      []string
	PanePID       int
	PaneTTY       string
	ProjectKey    string
	ProjectAnchor string
	PaneLabel     string
	AIAgent       string
	AITopic       string
	PaneCommand   string
	PaneTitle     string
	ProcessCount  int
	CPU           *CPUUsage
	Memory        MemoryUsage
}

// WindowUsage sums unique panes belonging to one tmux window id.
type WindowUsage struct {
	Socket       string
	WindowID     string
	WindowName   string
	SessionIDs   []string
	Sessions     []string
	ProjectKey   string
	PaneCount    int
	ProcessCount int
	CPU          *CPUUsage
	Memory       MemoryUsage
}

// ProjectUsage sums unique pane rows by stable project anchor or one of the
// explicit Unassigned/Shared buckets.
type ProjectUsage struct {
	Key          string
	PaneCount    int
	WindowCount  int
	ProcessCount int
	CPU          *CPUUsage
	Memory       MemoryUsage
}

// CPUReconciliation distinguishes a normal host remainder from sampling
// overage. OtherHostSharePercent is nil when attributed sampling exceeds the
// host busy sample; the excess is preserved rather than clamped.
type CPUReconciliation struct {
	HostBusyPercent       *float64
	AttributedPercent     *float64
	OtherHostSharePercent *float64
	OveragePercent        float64
}

// MemoryReconciliation compares RSS sums with host used memory. Shared RSS
// can make AttributedRSSBytes exceed HostUsedBytes, which is diagnostic rather
// than an arithmetic error.
type MemoryReconciliation struct {
	HostUsedBytes      uint64
	AttributedRSSBytes uint64
	OtherBytes         *uint64
	OverageBytes       uint64
}

// OtherUsage is the non-drillable host remainder row. Nil remainder fields
// mean the corresponding metric is unknown or exceeded its host comparison;
// overage remains available on the reconciliation diagnostics.
type OtherUsage struct {
	Key                 string
	CPUHostSharePercent *float64
	MemoryBytes         *uint64
	CPUOveragePercent   float64
	MemoryOverageBytes  uint64
}

// Diagnostics augments scan counts with attribution and delta quality.
type Diagnostics struct {
	Scan                ScanDiagnostics
	PIDReuseCount       int
	FastExitCount       int
	MissingPanePIDCount int
	InvalidCPUCount     int
	CPUUnknownProcesses int
	AmbiguousSIDCount   int
	EscapedProcessCount int
	LogicalCPUChanged   bool
	HostCPUInvalid      bool
}

// Snapshot is a detached, popup-ready read model. It has no persistence
// contract; a consumer should discard it when its sampling lifetime ends.
type Snapshot struct {
	At           time.Time
	Status       Status
	StatusReason string
	Host         HostSample
	Panes        []PaneUsage
	Windows      []WindowUsage
	Projects     []ProjectUsage
	CPU          CPUReconciliation
	Memory       MemoryReconciliation
	Other        OtherUsage
	Diagnostics  Diagnostics
}
