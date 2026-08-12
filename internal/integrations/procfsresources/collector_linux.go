//go:build linux

// Package procfsresources implements the Linux read-only resource collector.
package procfsresources

import (
	"context"
	"errors"
	"io/fs"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/resources"
)

const DefaultRoot = "/proc"

// Collector scans procfs once per Sample call. It reads only stat and meminfo
// data and never reads cmdline, environ, fd contents, or process memory maps.
type Collector struct {
	Root     string
	ReadDir  func(string) ([]os.DirEntry, error)
	ReadFile func(string) ([]byte, error)
	Now      func() time.Time
	PageSize uint64
}

// Sample collects a detached observation. A failed procfs directory read is
// unavailable; individual process and host-file failures remain diagnosable
// partial inputs for BuildSnapshot.
func (c Collector) Sample(ctx context.Context) resources.Sample {
	started := time.Now()
	root := strings.TrimSpace(c.Root)
	if root == "" {
		root = DefaultRoot
	}
	readDir := c.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	readFile := c.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	pageSize := c.PageSize
	if pageSize == 0 {
		pageSize, _ = strconv.ParseUint(strconv.Itoa(os.Getpagesize()), 10, 64)
	}

	sample := resources.Sample{At: now(), Available: true}
	entries, err := readDir(root)
	if err != nil {
		sample.Available = false
		sample.UnavailableReason = "cannot enumerate procfs"
		sample.Diagnostics.Duration = time.Since(started)
		return sample
	}

	if content, readErr := readFile(filepath.Join(root, "stat")); readErr == nil {
		if host, ok := parseHostCPU(content); ok {
			sample.Host.CPUAvailable = true
			sample.Host.LogicalCPUs = host.logicalCPUs
			sample.Host.CPUTotalTicks = host.totalTicks
			sample.Host.CPUIdleTicks = host.idleTicks
		}
	}
	if content, readErr := readFile(filepath.Join(root, "meminfo")); readErr == nil {
		if total, available, ok := parseHostMemory(content); ok {
			sample.Host.MemoryAvailable = true
			sample.Host.MemoryTotalBytes = total
			sample.Host.MemoryAvailableBytes = available
		}
	}

	sample.Processes = make([]resources.ProcessSample, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			sample.Available = false
			sample.UnavailableReason = "procfs scan canceled"
			break
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		content, readErr := readFile(filepath.Join(root, entry.Name(), "stat"))
		if readErr != nil {
			sample.Diagnostics.SkippedProcesses++
			switch {
			case errors.Is(readErr, fs.ErrNotExist):
				sample.Diagnostics.RaceCount++
			case errors.Is(readErr, fs.ErrPermission):
				sample.Diagnostics.PermissionCount++
			}
			continue
		}
		process, ok := parseProcessStat(pid, content, pageSize)
		if !ok {
			sample.Diagnostics.SkippedProcesses++
			continue
		}
		sample.Processes = append(sample.Processes, process)
		sample.Diagnostics.SampledProcesses++
	}
	sample.Diagnostics.Duration = time.Since(started)
	return sample
}

type hostCPU struct {
	logicalCPUs int
	totalTicks  uint64
	idleTicks   uint64
}

func parseHostCPU(content []byte) (hostCPU, bool) {
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 {
		return hostCPU{}, false
	}
	total, idle, ok := parseCPULine(lines[0])
	if !ok {
		return hostCPU{}, false
	}
	logical := 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") || fields[0] == "cpu" {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu")); err == nil {
			logical++
		}
	}
	if logical == 0 {
		return hostCPU{}, false
	}
	return hostCPU{logicalCPUs: logical, totalTicks: total, idleTicks: idle}, true
}

func parseCPULine(line string) (uint64, uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	values := make([]uint64, 0, 8)
	for _, raw := range fields[1:] {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		values = append(values, value)
		if len(values) == 8 {
			break
		}
	}
	var total uint64
	for _, value := range values {
		var carry uint64
		total, carry = bits.Add64(total, value, 0)
		if carry != 0 {
			return 0, 0, false
		}
	}
	idle := values[3]
	if len(values) > 4 {
		var carry uint64
		idle, carry = bits.Add64(idle, values[4], 0)
		if carry != 0 {
			return 0, 0, false
		}
	}
	if idle > total {
		return 0, 0, false
	}
	return total, idle, true
}

func parseHostMemory(content []byte) (uint64, uint64, bool) {
	var total, available uint64
	var hasTotal, hasAvailable bool
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != "kB" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			return 0, 0, false
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total, hasTotal = value*1024, true
		case "MemAvailable":
			available, hasAvailable = value*1024, true
		}
	}
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return 0, 0, false
	}
	return total, available, true
}

func parseProcessStat(expectedPID int, content []byte, pageSize uint64) (resources.ProcessSample, bool) {
	raw := strings.TrimSpace(string(content))
	open := strings.IndexByte(raw, '(')
	close := strings.LastIndexByte(raw, ')')
	if open <= 0 || close <= open || close+2 > len(raw) {
		return resources.ProcessSample{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(raw[:open]))
	if err != nil || pid != expectedPID {
		return resources.ProcessSample{}, false
	}
	fields := strings.Fields(raw[close+1:])
	// fields[0] is stat field 3 (state); rss is field 24.
	if len(fields) < 22 {
		return resources.ProcessSample{}, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return resources.ProcessSample{}, false
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil || sid < 0 {
		return resources.ProcessSample{}, false
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return resources.ProcessSample{}, false
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return resources.ProcessSample{}, false
	}
	cpu, carry := bits.Add64(utime, stime, 0)
	if carry != 0 {
		return resources.ProcessSample{}, false
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return resources.ProcessSample{}, false
	}
	var rssPages uint64
	if strings.HasPrefix(fields[21], "-") {
		if _, err := strconv.ParseInt(fields[21], 10, 64); err != nil {
			return resources.ProcessSample{}, false
		}
	} else {
		rssPages, err = strconv.ParseUint(fields[21], 10, 64)
		if err != nil {
			return resources.ProcessSample{}, false
		}
	}
	if pageSize == 0 || rssPages > ^uint64(0)/pageSize {
		return resources.ProcessSample{}, false
	}
	return resources.ProcessSample{
		Identity:     resources.ProcessIdentity{PID: pid, StartTimeTicks: start},
		ParentPID:    parent,
		SessionID:    sid,
		CPUTimeTicks: cpu,
		RSSBytes:     rssPages * pageSize,
	}, true
}

// Collect builds the UI read model from the current scan and optional prior
// scan. It is a convenience for the future popup sampling loop.
func (c Collector) Collect(ctx context.Context, inventory []resources.PaneInventory, previous *resources.Sample) (resources.Snapshot, resources.Sample) {
	current := c.Sample(ctx)
	return resources.BuildSnapshot(inventory, previous, current), current
}
