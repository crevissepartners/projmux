package systemstatus

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultMaxSampleAge = 30 * time.Second

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

func (s Sampler) sampleCPU(current cpuSample) *int {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	current.At = now.UnixNano()
	maxAge := s.MaxSampleAge
	if maxAge <= 0 {
		maxAge = DefaultMaxSampleAge
	}
	var percent *int
	if previous, ok := loadCPUSample(s.CachePath); ok && cpuSampleFresh(previous, current, maxAge) {
		percent = cpuPercent(previous, current)
	}
	_ = saveCPUSample(s.CachePath, current)
	return percent
}

func cpuSampleFresh(previous, current cpuSample, maxAge time.Duration) bool {
	if previous.At <= 0 || current.At <= previous.At || maxAge <= 0 {
		return false
	}
	return time.Duration(current.At-previous.At) <= maxAge
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
