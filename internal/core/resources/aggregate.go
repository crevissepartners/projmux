package resources

import (
	"cmp"
	"slices"
	"sort"
	"strings"
)

type paneKey struct {
	socket string
	paneID string
}

type windowKey struct {
	socket   string
	windowID string
}

type paneTopology struct {
	key        paneKey
	window     windowKey
	pid        int
	tty        string
	sessionIDs map[string]struct{}
	sessions   map[string]struct{}
	anchors    map[string]struct{}
}

type usageAccumulator struct {
	processCount int
	rssBytes     uint64
	cpuShare     float64
	cpuKnown     bool
}

// BuildSnapshot performs deterministic, I/O-free SID attribution and
// pane->window->project aggregation. previous may be nil for the first sample.
func BuildSnapshot(inventory []PaneInventory, previous *Sample, current Sample) Snapshot {
	snapshot := Snapshot{
		At:           current.At,
		Host:         current.Host,
		Diagnostics:  Diagnostics{Scan: current.Diagnostics},
		Status:       StatusReady,
		StatusReason: "ready",
	}
	if !current.Available {
		snapshot.Status = StatusUnavailable
		snapshot.StatusReason = nonEmpty(current.UnavailableReason, "procfs unavailable")
		return snapshot
	}

	topology, sidPanes := buildTopology(inventory)
	currentByPID := make(map[int]ProcessSample, len(current.Processes))
	for _, process := range current.Processes {
		currentByPID[process.Identity.PID] = process
	}

	previousByIdentity := make(map[ProcessIdentity]ProcessSample)
	previousByPID := make(map[int]ProcessSample)
	if previous != nil && previous.Available {
		previousByIdentity = make(map[ProcessIdentity]ProcessSample, len(previous.Processes))
		previousByPID = make(map[int]ProcessSample, len(previous.Processes))
		for _, process := range previous.Processes {
			previousByIdentity[process.Identity] = process
			previousByPID[process.Identity.PID] = process
		}
	}

	cpuReady, hostBusy := validateCPUInterval(previous, current, &snapshot.Diagnostics)
	acc := make(map[paneKey]*usageAccumulator, len(topology))
	for key := range topology {
		acc[key] = &usageAccumulator{}
	}

	for _, process := range current.Processes {
		keys := sidPanes[process.SessionID]
		if len(keys) != 1 {
			if len(keys) > 1 {
				snapshot.Diagnostics.AmbiguousSIDCount++
			} else if escapedFromPane(process, currentByPID, sidPanes) {
				snapshot.Diagnostics.EscapedProcessCount++
			}
			continue
		}

		paneAcc := acc[keys[0]]
		paneAcc.processCount++
		paneAcc.rssBytes += process.RSSBytes
		if !cpuReady {
			continue
		}
		old, ok := previousByIdentity[process.Identity]
		if !ok {
			if reused, exists := previousByPID[process.Identity.PID]; exists && reused.Identity != process.Identity {
				snapshot.Diagnostics.PIDReuseCount++
			}
			snapshot.Diagnostics.CPUUnknownProcesses++
			continue
		}
		if process.CPUTimeTicks < old.CPUTimeTicks {
			snapshot.Diagnostics.InvalidCPUCount++
			snapshot.Diagnostics.CPUUnknownProcesses++
			continue
		}
		delta := process.CPUTimeTicks - old.CPUTimeTicks
		paneAcc.cpuShare += float64(delta) * 100 / float64(current.Host.CPUTotalTicks-previous.Host.CPUTotalTicks)
		paneAcc.cpuKnown = true
	}
	for _, pane := range topology {
		if _, ok := currentByPID[pane.pid]; !ok {
			snapshot.Diagnostics.MissingPanePIDCount++
		}
	}
	if previous != nil && previous.Available {
		currentByIdentity := make(map[ProcessIdentity]struct{}, len(current.Processes))
		for _, process := range current.Processes {
			currentByIdentity[process.Identity] = struct{}{}
		}
		for _, process := range previous.Processes {
			if len(sidPanes[process.SessionID]) != 1 {
				continue
			}
			if _, stillPresent := currentByIdentity[process.Identity]; stillPresent {
				continue
			}
			if replacement, reused := currentByPID[process.Identity.PID]; reused && replacement.Identity != process.Identity {
				continue
			}
			snapshot.Diagnostics.FastExitCount++
		}
	}

	snapshot.Panes = buildPaneUsages(topology, acc, current.Host)
	snapshot.Windows = buildWindowUsages(snapshot.Panes, current.Host)
	snapshot.Projects = buildProjectUsages(snapshot.Panes, current.Host)
	snapshot.CPU = reconcileCPU(hostBusy, snapshot.Panes)
	snapshot.Memory = reconcileMemory(current.Host, snapshot.Panes)
	snapshot.Other = OtherUsage{
		Key:                 OtherUnattributed,
		CPUHostSharePercent: cloneFloat(snapshot.CPU.OtherHostSharePercent),
		MemoryBytes:         cloneUint64(snapshot.Memory.OtherBytes),
		CPUOveragePercent:   snapshot.CPU.OveragePercent,
		MemoryOverageBytes:  snapshot.Memory.OverageBytes,
	}

	partial := current.Diagnostics.SkippedProcesses > 0 ||
		current.Diagnostics.RaceCount > 0 ||
		current.Diagnostics.PermissionCount > 0 ||
		snapshot.Diagnostics.PIDReuseCount > 0 ||
		snapshot.Diagnostics.FastExitCount > 0 ||
		snapshot.Diagnostics.MissingPanePIDCount > 0 ||
		snapshot.Diagnostics.InvalidCPUCount > 0 ||
		snapshot.Diagnostics.CPUUnknownProcesses > 0 ||
		snapshot.Diagnostics.AmbiguousSIDCount > 0 ||
		snapshot.Diagnostics.LogicalCPUChanged || snapshot.Diagnostics.HostCPUInvalid ||
		!current.Host.MemoryAvailable
	switch {
	case partial:
		snapshot.Status = StatusPartial
		snapshot.StatusReason = "incomplete process or host sample"
	case previous == nil || !previous.Available:
		snapshot.Status = StatusWarming
		snapshot.StatusReason = "first CPU sample"
	default:
		snapshot.Status = StatusReady
		snapshot.StatusReason = "ready"
	}
	return snapshot
}

func buildTopology(inventory []PaneInventory) (map[paneKey]*paneTopology, map[int][]paneKey) {
	topology := make(map[paneKey]*paneTopology)
	for _, row := range inventory {
		key := paneKey{socket: row.Socket, paneID: row.PaneID}
		pane, ok := topology[key]
		if !ok {
			pane = &paneTopology{
				key:        key,
				window:     windowKey{socket: row.Socket, windowID: row.WindowID},
				pid:        row.PanePID,
				tty:        row.PaneTTY,
				sessionIDs: make(map[string]struct{}),
				sessions:   make(map[string]struct{}),
				anchors:    make(map[string]struct{}),
			}
			topology[key] = pane
		}
		if row.SessionName != "" {
			pane.sessions[row.SessionName] = struct{}{}
		}
		if row.SessionID != "" {
			pane.sessionIDs[row.SessionID] = struct{}{}
		}
		if anchor := strings.TrimSpace(row.ProjectAnchor); anchor != "" {
			pane.anchors[anchor] = struct{}{}
		}
	}

	sidPanes := make(map[int][]paneKey)
	for key, pane := range topology {
		if pane.pid > 0 {
			sidPanes[pane.pid] = append(sidPanes[pane.pid], key)
		}
	}
	for sid := range sidPanes {
		slices.SortFunc(sidPanes[sid], func(a, b paneKey) int {
			return cmp.Or(strings.Compare(a.socket, b.socket), strings.Compare(a.paneID, b.paneID))
		})
	}
	return topology, sidPanes
}

func validateCPUInterval(previous *Sample, current Sample, diagnostics *Diagnostics) (bool, *float64) {
	if previous == nil || !previous.Available {
		return false, nil
	}
	if !previous.Host.CPUAvailable || !current.Host.CPUAvailable || previous.Host.LogicalCPUs <= 0 || current.Host.LogicalCPUs <= 0 || previous.Host.LogicalCPUs != current.Host.LogicalCPUs {
		diagnostics.LogicalCPUChanged = previous.Host.LogicalCPUs != current.Host.LogicalCPUs
		diagnostics.HostCPUInvalid = true
		return false, nil
	}
	if current.Host.CPUTotalTicks <= previous.Host.CPUTotalTicks || current.Host.CPUIdleTicks < previous.Host.CPUIdleTicks {
		diagnostics.HostCPUInvalid = true
		return false, nil
	}
	totalDelta := current.Host.CPUTotalTicks - previous.Host.CPUTotalTicks
	idleDelta := current.Host.CPUIdleTicks - previous.Host.CPUIdleTicks
	if idleDelta > totalDelta {
		diagnostics.HostCPUInvalid = true
		return false, nil
	}
	busy := float64(totalDelta-idleDelta) * 100 / float64(totalDelta)
	return true, &busy
}

func escapedFromPane(process ProcessSample, byPID map[int]ProcessSample, sidPanes map[int][]paneKey) bool {
	seen := make(map[int]struct{})
	parent := process.ParentPID
	for parent > 0 {
		if _, duplicate := seen[parent]; duplicate {
			return false
		}
		seen[parent] = struct{}{}
		ancestor, ok := byPID[parent]
		if !ok {
			return false
		}
		if len(sidPanes[ancestor.SessionID]) == 1 || len(sidPanes[ancestor.Identity.PID]) == 1 {
			return true
		}
		parent = ancestor.ParentPID
	}
	return false
}

func buildPaneUsages(topology map[paneKey]*paneTopology, acc map[paneKey]*usageAccumulator, host HostSample) []PaneUsage {
	panes := make([]PaneUsage, 0, len(topology))
	for key, pane := range topology {
		projectKey, anchor := projectIdentity(pane.anchors)
		usage := acc[key]
		row := PaneUsage{
			Socket:        pane.key.socket,
			PaneID:        pane.key.paneID,
			WindowID:      pane.window.windowID,
			SessionIDs:    sortedKeys(pane.sessionIDs),
			Sessions:      sortedKeys(pane.sessions),
			PanePID:       pane.pid,
			PaneTTY:       pane.tty,
			ProjectKey:    projectKey,
			ProjectAnchor: anchor,
			ProcessCount:  usage.processCount,
			Memory:        memoryUsage(usage.rssBytes, host.MemoryTotalBytes),
		}
		if usage.cpuKnown {
			row.CPU = cpuUsage(usage.cpuShare, host.LogicalCPUs)
		}
		panes = append(panes, row)
	}
	sort.Slice(panes, func(i, j int) bool {
		return panes[i].Socket < panes[j].Socket || panes[i].Socket == panes[j].Socket && panes[i].PaneID < panes[j].PaneID
	})
	return panes
}

func buildWindowUsages(panes []PaneUsage, host HostSample) []WindowUsage {
	type windowAcc struct {
		usageAccumulator
		sessions   map[string]struct{}
		sessionIDs map[string]struct{}
		projects   map[string]struct{}
		panes      int
	}
	values := make(map[windowKey]*windowAcc)
	for _, pane := range panes {
		key := windowKey{socket: pane.Socket, windowID: pane.WindowID}
		value := values[key]
		if value == nil {
			value = &windowAcc{sessions: make(map[string]struct{}), sessionIDs: make(map[string]struct{}), projects: make(map[string]struct{})}
			values[key] = value
		}
		value.panes++
		value.processCount += pane.ProcessCount
		value.rssBytes += pane.Memory.RSSBytes
		value.projects[pane.ProjectKey] = struct{}{}
		for _, session := range pane.Sessions {
			value.sessions[session] = struct{}{}
		}
		for _, sessionID := range pane.SessionIDs {
			value.sessionIDs[sessionID] = struct{}{}
		}
		if pane.CPU != nil {
			value.cpuKnown = true
			value.cpuShare += pane.CPU.HostSharePercent
		}
	}
	rows := make([]WindowUsage, 0, len(values))
	for key, value := range values {
		project := ProjectShared
		if len(value.projects) == 1 {
			for candidate := range value.projects {
				project = candidate
			}
		}
		row := WindowUsage{Socket: key.socket, WindowID: key.windowID, SessionIDs: sortedKeys(value.sessionIDs), Sessions: sortedKeys(value.sessions), ProjectKey: project, PaneCount: value.panes, ProcessCount: value.processCount, Memory: memoryUsage(value.rssBytes, host.MemoryTotalBytes)}
		if value.cpuKnown {
			row.CPU = cpuUsage(value.cpuShare, host.LogicalCPUs)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Socket < rows[j].Socket || rows[i].Socket == rows[j].Socket && rows[i].WindowID < rows[j].WindowID
	})
	return rows
}

func buildProjectUsages(panes []PaneUsage, host HostSample) []ProjectUsage {
	type projectAcc struct {
		usageAccumulator
		panes   int
		windows map[windowKey]struct{}
	}
	values := make(map[string]*projectAcc)
	for _, pane := range panes {
		value := values[pane.ProjectKey]
		if value == nil {
			value = &projectAcc{windows: make(map[windowKey]struct{})}
			values[pane.ProjectKey] = value
		}
		value.panes++
		value.windows[windowKey{socket: pane.Socket, windowID: pane.WindowID}] = struct{}{}
		value.processCount += pane.ProcessCount
		value.rssBytes += pane.Memory.RSSBytes
		if pane.CPU != nil {
			value.cpuKnown = true
			value.cpuShare += pane.CPU.HostSharePercent
		}
	}
	rows := make([]ProjectUsage, 0, len(values))
	for key, value := range values {
		row := ProjectUsage{Key: key, PaneCount: value.panes, WindowCount: len(value.windows), ProcessCount: value.processCount, Memory: memoryUsage(value.rssBytes, host.MemoryTotalBytes)}
		if value.cpuKnown {
			row.CPU = cpuUsage(value.cpuShare, host.LogicalCPUs)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

func reconcileCPU(hostBusy *float64, panes []PaneUsage) CPUReconciliation {
	result := CPUReconciliation{HostBusyPercent: cloneFloat(hostBusy)}
	if hostBusy == nil {
		return result
	}
	attributed := 0.0
	known := false
	for _, pane := range panes {
		if pane.CPU != nil {
			known = true
			attributed += pane.CPU.HostSharePercent
		}
	}
	if !known {
		return result
	}
	result.AttributedPercent = cloneFloat(&attributed)
	if attributed <= *hostBusy {
		other := *hostBusy - attributed
		result.OtherHostSharePercent = &other
	} else {
		result.OveragePercent = attributed - *hostBusy
	}
	return result
}

func reconcileMemory(host HostSample, panes []PaneUsage) MemoryReconciliation {
	attributed := uint64(0)
	for _, pane := range panes {
		attributed += pane.Memory.RSSBytes
	}
	result := MemoryReconciliation{AttributedRSSBytes: attributed}
	if !host.MemoryAvailable || host.MemoryTotalBytes == 0 || host.MemoryAvailableBytes > host.MemoryTotalBytes {
		return result
	}
	result.HostUsedBytes = host.MemoryTotalBytes - host.MemoryAvailableBytes
	if attributed <= result.HostUsedBytes {
		other := result.HostUsedBytes - attributed
		result.OtherBytes = &other
	} else {
		result.OverageBytes = attributed - result.HostUsedBytes
	}
	return result
}

func projectIdentity(anchors map[string]struct{}) (string, string) {
	switch len(anchors) {
	case 0:
		return ProjectUnassigned, ""
	case 1:
		for anchor := range anchors {
			return anchor, anchor
		}
	}
	return ProjectShared, ""
}

func cpuUsage(hostShare float64, logicalCPUs int) *CPUUsage {
	cores := 0.0
	if logicalCPUs > 0 {
		cores = hostShare * float64(logicalCPUs) / 100
	}
	return &CPUUsage{HostSharePercent: hostShare, CoreEquivalent: cores}
}

func memoryUsage(rss, hostTotal uint64) MemoryUsage {
	usage := MemoryUsage{RSSBytes: rss}
	if hostTotal > 0 {
		percent := float64(rss) * 100 / float64(hostTotal)
		usage.HostPercent = &percent
	}
	return usage
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
