//go:build linux

package systemstatus

import (
	"math"
	"math/bits"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultProcStatPath    = "/proc/stat"
	DefaultProcMemInfoPath = "/proc/meminfo"
)

func (s Sampler) Sample() Metrics { return s.sampleProc() }

func (s Sampler) sampleProc() Metrics {
	readFile := s.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	statPath := strings.TrimSpace(s.StatPath)
	if statPath == "" {
		statPath = DefaultProcStatPath
	}
	memInfoPath := strings.TrimSpace(s.MemInfoPath)
	if memInfoPath == "" {
		memInfoPath = DefaultProcMemInfoPath
	}

	metrics := Metrics{}
	if content, err := readFile(memInfoPath); err == nil {
		metrics.MemoryPercent = parseMemoryPercent(content)
	}
	if content, err := readFile(statPath); err == nil {
		if current, ok := parseCPUSample(content); ok {
			metrics.CPUPercent = s.sampleCPU(current)
		}
	}
	return metrics
}

func parseCPUSample(content []byte) (cpuSample, bool) {
	line, _, _ := strings.Cut(string(content), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}, false
	}

	values := make([]uint64, 0, 8)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuSample{}, false
		}
		values = append(values, value)
		if len(values) == 8 {
			break
		}
	}
	if len(values) < 4 {
		return cpuSample{}, false
	}

	var total uint64
	for _, value := range values {
		var carry uint64
		total, carry = bits.Add64(total, value, 0)
		if carry != 0 {
			return cpuSample{}, false
		}
	}
	idle := values[3]
	if len(values) > 4 {
		var carry uint64
		idle, carry = bits.Add64(idle, values[4], 0)
		if carry != 0 {
			return cpuSample{}, false
		}
	}
	if idle > total {
		return cpuSample{}, false
	}
	return cpuSample{Total: total, Idle: idle}, true
}

func parseMemoryPercent(content []byte) *int {
	var total, available uint64
	var hasTotal, hasAvailable bool
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if name != "MemTotal" && name != "MemAvailable" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil
		}
		switch name {
		case "MemTotal":
			total, hasTotal = value, true
		case "MemAvailable":
			available, hasAvailable = value, true
		}
	}
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return nil
	}
	percent := int(math.Round(float64(total-available) * 100 / float64(total)))
	percent = min(max(percent, 0), 100)
	return &percent
}
