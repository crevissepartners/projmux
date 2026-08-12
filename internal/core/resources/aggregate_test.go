package resources

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestBuildSnapshotAggregatesUniquePanesAndExplicitProjectBuckets(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{
		{Socket: "/s", SessionID: "$1", SessionName: "alpha", WindowID: "@1", PaneID: "%1", PanePID: 100, PaneTTY: "/dev/pts/1", ProjectAnchor: "/repo/a"},
		{Socket: "/s", SessionID: "$2", SessionName: "linked", WindowID: "@1", PaneID: "%1", PanePID: 100, PaneTTY: "/dev/pts/1", ProjectAnchor: "/repo/b"},
		{Socket: "/s", SessionID: "$3", SessionName: "loose", WindowID: "@2", PaneID: "%2", PanePID: 200, PaneTTY: "/dev/pts/2"},
		{Socket: "/s", SessionID: "$4", SessionName: "beta", WindowID: "@3", PaneID: "%3", PanePID: 300, PaneTTY: "/dev/pts/3", ProjectAnchor: "/repo/b"},
	}
	previous := sampleWithHost(1000, 600, 4, 10_000, 4_000,
		process(100, 10, 100, 10, 100), process(101, 11, 100, 20, 200),
		process(200, 20, 200, 30, 300), process(300, 30, 300, 40, 400))
	current := sampleWithHost(1400, 800, 4, 10_000, 4_000,
		process(100, 10, 100, 30, 110), process(101, 11, 100, 40, 220),
		process(200, 20, 200, 50, 330), process(300, 30, 300, 60, 440))

	got := BuildSnapshot(inventory, &previous, current)
	if got.Status != StatusReady {
		t.Fatalf("Status = %q, want ready (%+v)", got.Status, got.Diagnostics)
	}
	if len(got.Panes) != 3 || len(got.Windows) != 3 || len(got.Projects) != 3 {
		t.Fatalf("snapshot topology = %d panes, %d windows, %d projects", len(got.Panes), len(got.Windows), len(got.Projects))
	}
	if got.Panes[0].ProjectKey != ProjectShared || got.Panes[0].ProcessCount != 2 || got.Panes[0].Memory.RSSBytes != 330 {
		t.Fatalf("linked pane = %#v", got.Panes[0])
	}
	if len(got.Panes[0].SessionIDs) != 2 || got.Panes[0].SessionIDs[0] != "$1" || got.Panes[0].SessionIDs[1] != "$2" {
		t.Fatalf("linked pane session IDs = %#v", got.Panes[0].SessionIDs)
	}
	if got.Panes[1].ProjectKey != ProjectUnassigned {
		t.Fatalf("unanchored pane project = %q", got.Panes[1].ProjectKey)
	}
	if got.Panes[2].ProjectKey != "/repo/b" {
		t.Fatalf("anchored pane project = %q", got.Panes[2].ProjectKey)
	}
	assertFloat(t, got.Panes[0].CPU.HostSharePercent, 10)
	assertFloat(t, got.Panes[0].CPU.CoreEquivalent, 0.4)
	if err := validateConservation(got); err != nil {
		t.Fatal(err)
	}
	if got.Memory.AttributedRSSBytes != 1100 || got.Memory.OtherBytes == nil || *got.Memory.OtherBytes != 4900 {
		t.Fatalf("memory reconciliation = %#v", got.Memory)
	}
	if got.CPU.HostBusyPercent == nil || got.CPU.AttributedPercent == nil || got.CPU.OtherHostSharePercent == nil {
		t.Fatalf("CPU reconciliation unavailable: %#v", got.CPU)
	}
	assertFloat(t, *got.CPU.HostBusyPercent, 50)
	assertFloat(t, *got.CPU.AttributedPercent, 20)
	assertFloat(t, *got.CPU.OtherHostSharePercent, 30)
	if got.Other.Key != OtherUnattributed || got.Other.CPUHostSharePercent == nil || got.Other.MemoryBytes == nil {
		t.Fatalf("Other row = %#v", got.Other)
	}
}

func TestBuildSnapshotFirstInvalidAndLogicalCPUChangeStayUnknown(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{{Socket: "/s", WindowID: "@1", PaneID: "%1", PanePID: 100, ProjectAnchor: "/repo"}}
	first := sampleWithHost(1000, 600, 4, 1000, 500, process(100, 1, 100, 20, 100))
	warming := BuildSnapshot(inventory, nil, first)
	if warming.Status != StatusWarming || warming.Panes[0].CPU != nil || warming.CPU.HostBusyPercent != nil {
		t.Fatalf("first snapshot = %#v", warming)
	}

	tests := []struct {
		name     string
		current  Sample
		logical  bool
		invalids int
	}{
		{"host reset", sampleWithHost(900, 500, 4, 1000, 500, process(100, 1, 100, 30, 100)), false, 0},
		{"logical CPUs changed", sampleWithHost(1400, 800, 8, 1000, 500, process(100, 1, 100, 30, 100)), true, 0},
		{"process counter reset", sampleWithHost(1400, 800, 4, 1000, 500, process(100, 1, 100, 10, 100)), false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSnapshot(inventory, &first, tc.current)
			if got.Status != StatusPartial || got.Panes[0].CPU != nil {
				t.Fatalf("snapshot = status %q pane CPU %#v", got.Status, got.Panes[0].CPU)
			}
			if got.Diagnostics.LogicalCPUChanged != tc.logical || got.Diagnostics.InvalidCPUCount != tc.invalids {
				t.Fatalf("diagnostics = %#v", got.Diagnostics)
			}
		})
	}
}

func TestBuildSnapshotPIDReuseRacePermissionAndSetsidAreExplicit(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{{Socket: "/s", WindowID: "@1", PaneID: "%1", PanePID: 100, ProjectAnchor: "/repo"}}
	previous := sampleWithHost(1000, 600, 2, 1000, 500,
		process(100, 1, 100, 10, 10), process(101, 10, 100, 20, 20), process(150, 15, 150, 5, 5))
	current := sampleWithHost(1200, 700, 2, 1000, 500,
		process(100, 1, 100, 20, 10), process(101, 99, 100, 2, 20),
		ProcessSample{Identity: ProcessIdentity{PID: 150, StartTimeTicks: 15}, ParentPID: 100, SessionID: 150, CPUTimeTicks: 10, RSSBytes: 5})
	current.Diagnostics = ScanDiagnostics{SampledProcesses: 3, SkippedProcesses: 2, RaceCount: 1, PermissionCount: 1}

	got := BuildSnapshot(inventory, &previous, current)
	if got.Status != StatusPartial || got.Diagnostics.PIDReuseCount != 1 || got.Diagnostics.EscapedProcessCount != 1 || got.Diagnostics.CPUUnknownProcesses != 1 {
		t.Fatalf("diagnostics = %#v", got.Diagnostics)
	}
	if got.Panes[0].ProcessCount != 2 || got.Panes[0].Memory.RSSBytes != 30 {
		t.Fatalf("pane attribution = %#v", got.Panes[0])
	}
	assertFloat(t, got.Panes[0].CPU.HostSharePercent, 5)
}

func TestBuildSnapshotOneProcessNeverChoosesAmbiguousSID(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{
		{Socket: "/s", WindowID: "@1", PaneID: "%1", PanePID: 100, ProjectAnchor: "/a"},
		{Socket: "/s", WindowID: "@2", PaneID: "%2", PanePID: 100, ProjectAnchor: "/b"},
	}
	previous := sampleWithHost(1000, 600, 2, 1000, 500, process(101, 1, 100, 10, 20))
	current := sampleWithHost(1200, 700, 2, 1000, 500, process(101, 1, 100, 20, 20))
	got := BuildSnapshot(inventory, &previous, current)
	if got.Diagnostics.AmbiguousSIDCount != 1 || got.Panes[0].ProcessCount != 0 || got.Panes[1].ProcessCount != 0 {
		t.Fatalf("ambiguous SID snapshot = %#v", got)
	}
}

func TestBuildSnapshotFastExitIsPartialNotZero(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{{Socket: "/s", WindowID: "@1", PaneID: "%1", PanePID: 100, ProjectAnchor: "/repo"}}
	previous := sampleWithHost(1000, 600, 2, 1000, 500, process(100, 1, 100, 10, 20), process(101, 2, 100, 10, 20))
	current := sampleWithHost(1200, 700, 2, 1000, 500, process(100, 1, 100, 20, 20))
	got := BuildSnapshot(inventory, &previous, current)
	if got.Status != StatusPartial || got.Diagnostics.FastExitCount != 1 {
		t.Fatalf("fast-exit snapshot = status %q diagnostics %#v", got.Status, got.Diagnostics)
	}
	if got.Panes[0].CPU == nil {
		t.Fatal("surviving process delta should remain visible")
	}
}

func TestBuildSnapshotPreservesHostOverageInsteadOfClamping(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{{Socket: "/s", WindowID: "@1", PaneID: "%1", PanePID: 100, ProjectAnchor: "/repo"}}
	previous := sampleWithHost(1000, 500, 2, 1000, 900, process(100, 1, 100, 10, 120))
	current := sampleWithHost(1100, 595, 2, 1000, 900, process(100, 1, 100, 20, 120))
	got := BuildSnapshot(inventory, &previous, current)
	if got.CPU.OtherHostSharePercent != nil || got.CPU.OveragePercent <= 0 {
		t.Fatalf("CPU overage = %#v", got.CPU)
	}
	if got.Memory.OtherBytes != nil || got.Memory.OverageBytes != 20 {
		t.Fatalf("memory overage = %#v", got.Memory)
	}
	assertFloat(t, got.CPU.OveragePercent, 5)
	if got.Other.CPUHostSharePercent != nil || got.Other.MemoryBytes != nil || got.Other.CPUOveragePercent != 5 || got.Other.MemoryOverageBytes != 20 {
		t.Fatalf("Other overage row = %#v", got.Other)
	}
}

func TestBuildSnapshotUnavailableIsNotZeroMetrics(t *testing.T) {
	t.Parallel()

	got := BuildSnapshot(nil, nil, Sample{UnavailableReason: "unsupported platform"})
	if got.Status != StatusUnavailable || got.StatusReason != "unsupported platform" || got.CPU.HostBusyPercent != nil {
		t.Fatalf("unavailable snapshot = %#v", got)
	}
}

func sampleWithHost(total, idle uint64, cpus int, memoryTotal, memoryAvailable uint64, processes ...ProcessSample) Sample {
	return Sample{
		At: time.Unix(100, 0),
		Host: HostSample{
			CPUAvailable:         true,
			MemoryAvailable:      true,
			LogicalCPUs:          cpus,
			CPUTotalTicks:        total,
			CPUIdleTicks:         idle,
			MemoryTotalBytes:     memoryTotal,
			MemoryAvailableBytes: memoryAvailable,
		},
		Processes: processes,
		Available: true,
	}
}

func process(pid int, start uint64, sid int, cpu, rss uint64) ProcessSample {
	return ProcessSample{Identity: ProcessIdentity{PID: pid, StartTimeTicks: start}, ParentPID: 1, SessionID: sid, CPUTimeTicks: cpu, RSSBytes: rss}
}

func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %.9f, want %.9f", got, want)
	}
}

func validateConservation(snapshot Snapshot) error {
	paneRSS := uint64(0)
	paneCPU := 0.0
	for _, pane := range snapshot.Panes {
		paneRSS += pane.Memory.RSSBytes
		if pane.CPU != nil {
			paneCPU += pane.CPU.HostSharePercent
		}
	}
	windowRSS := uint64(0)
	windowCPU := 0.0
	for _, window := range snapshot.Windows {
		windowRSS += window.Memory.RSSBytes
		if window.CPU != nil {
			windowCPU += window.CPU.HostSharePercent
		}
	}
	projectRSS := uint64(0)
	projectCPU := 0.0
	for _, project := range snapshot.Projects {
		projectRSS += project.Memory.RSSBytes
		if project.CPU != nil {
			projectCPU += project.CPU.HostSharePercent
		}
	}
	if paneRSS != windowRSS || paneRSS != projectRSS {
		return fmt.Errorf("resource totals diverged: panes=%d windows=%d projects=%d", paneRSS, windowRSS, projectRSS)
	}
	if math.Abs(paneCPU-windowCPU) > 1e-9 || math.Abs(paneCPU-projectCPU) > 1e-9 {
		return fmt.Errorf("CPU totals diverged: panes=%f windows=%f projects=%f", paneCPU, windowCPU, projectCPU)
	}
	return nil
}

func BenchmarkBuildSnapshot(b *testing.B) {
	for _, panes := range []int{10, 50} {
		for _, processes := range []int{50, 200, 1000} {
			b.Run(benchmarkName(panes, processes), func(b *testing.B) {
				inventory := make([]PaneInventory, panes)
				previousProcesses := make([]ProcessSample, processes)
				currentProcesses := make([]ProcessSample, processes)
				for i := range inventory {
					inventory[i] = PaneInventory{Socket: "/s", SessionName: "s", WindowID: "@" + benchmarkInt(i/5), PaneID: "%" + benchmarkInt(i), PanePID: 1000 + i, ProjectAnchor: "/repo/" + benchmarkInt(i%4)}
				}
				for i := range processes {
					pid := 10_000 + i
					sid := 1000 + i%panes
					previousProcesses[i] = process(pid, uint64(i+1), sid, 10, 4096)
					currentProcesses[i] = process(pid, uint64(i+1), sid, 12, 4096)
				}
				previous := sampleWithHost(1_000_000, 600_000, 16, 64<<30, 32<<30, previousProcesses...)
				current := sampleWithHost(1_100_000, 650_000, 16, 64<<30, 32<<30, currentProcesses...)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_ = BuildSnapshot(inventory, &previous, current)
				}
			})
		}
	}
}

func benchmarkName(panes, processes int) string {
	return "panes=" + benchmarkInt(panes) + "/processes=" + benchmarkInt(processes)
}

func benchmarkInt(value int) string {
	if value == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
