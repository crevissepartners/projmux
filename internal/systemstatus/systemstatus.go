package systemstatus

import (
	"encoding/json"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultProcStatPath    = "/proc/stat"
	DefaultProcMemInfoPath = "/proc/meminfo"
	DefaultMaxSampleAge    = 30 * time.Second
)

type Metrics struct {
	CPUPercent    *int
	MemoryPercent *int
}

func (m Metrics) Available() bool {
	return m.CPUPercent != nil || m.MemoryPercent != nil
}

type Sampler struct {
	StatPath     string
	MemInfoPath  string
	CachePath    string
	ReadFile     func(string) ([]byte, error)
	Now          func() time.Time
	MaxSampleAge time.Duration
}

type cpuSample struct {
	Total uint64 `json:"total"`
	Idle  uint64 `json:"idle"`
	At    int64  `json:"at_unix_nano"`
}

func (s Sampler) Sample() Metrics {
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
			now := time.Now()
			if s.Now != nil {
				now = s.Now()
			}
			current.At = now.UnixNano()
			maxAge := s.MaxSampleAge
			if maxAge <= 0 {
				maxAge = DefaultMaxSampleAge
			}
			if previous, ok := loadCPUSample(s.CachePath); ok && cpuSampleFresh(previous, current, maxAge) {
				metrics.CPUPercent = cpuPercent(previous, current)
			}
			_ = saveCPUSample(s.CachePath, current)
		}
	}
	return metrics
}

func cpuSampleFresh(previous, current cpuSample, maxAge time.Duration) bool {
	if previous.At <= 0 || current.At <= previous.At || maxAge <= 0 {
		return false
	}
	return time.Duration(current.At-previous.At) <= maxAge
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

func cpuPercent(previous, current cpuSample) *int {
	if current.Total <= previous.Total || current.Idle < previous.Idle {
		return nil
	}
	totalDelta := current.Total - previous.Total
	idleDelta := current.Idle - previous.Idle
	if idleDelta > totalDelta {
		return nil
	}
	percent := int(math.Round(float64(totalDelta-idleDelta) * 100 / float64(totalDelta)))
	percent = min(max(percent, 0), 100)
	return &percent
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

func loadCPUSample(path string) (cpuSample, bool) {
	if strings.TrimSpace(path) == "" {
		return cpuSample{}, false
	}
	// #nosec G304 -- path is the resolved projmux state cache supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		return cpuSample{}, false
	}
	var sample cpuSample
	if err := json.Unmarshal(content, &sample); err != nil || sample.Total == 0 || sample.Idle > sample.Total || sample.At <= 0 {
		return cpuSample{}, false
	}
	return sample, true
}

func saveCPUSample(path string, sample cpuSample) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
