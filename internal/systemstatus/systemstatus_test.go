//go:build linux

package systemstatus

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func intValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func TestParseCPUSampleAndPercent(t *testing.T) {
	t.Parallel()

	sample, ok := parseCPUSample([]byte("cpu  100 10 20 70 5 2 3 4 9 1\ncpu0 1 2 3 4\n"))
	if !ok || sample.Total != 214 || sample.Idle != 75 {
		t.Fatalf("parseCPUSample() = %#v, %v, want total=214 idle=75", sample, ok)
	}
	if got := intValue(cpuPercent(cpuSample{Total: 1000, Idle: 600}, cpuSample{Total: 1100, Idle: 650})); got != 50 {
		t.Fatalf("cpuPercent() = %d, want 50", got)
	}
	for _, current := range []cpuSample{
		{Total: 999, Idle: 650},
		{Total: 1100, Idle: 599},
		{Total: 1100, Idle: 750},
	} {
		if got := cpuPercent(cpuSample{Total: 1000, Idle: 600}, current); got != nil {
			t.Fatalf("cpuPercent(reset=%#v) = %d, want unavailable", current, *got)
		}
	}
}

func TestParseCPUSampleRejectsMalformedAndOverflow(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		"",
		"cpu0 1 2 3 4\n",
		"cpu 1 2 bad 4\n",
		"cpu 18446744073709551615 1 0 0\n",
	} {
		if sample, ok := parseCPUSample([]byte(content)); ok {
			t.Fatalf("parseCPUSample(%q) = %#v, true, want rejected", content, sample)
		}
	}
}

func TestParseMemoryPercent(t *testing.T) {
	t.Parallel()

	content := []byte("MemTotal:       1000 kB\nMemFree:         100 kB\nNoise:           not-a-number\nMemAvailable:    600 kB\n")
	if got := intValue(parseMemoryPercent(content)); got != 40 {
		t.Fatalf("parseMemoryPercent() = %d, want 40", got)
	}
	for _, malformed := range []string{
		"MemTotal: 1000 kB\nMemFree: 600 kB\n",
		"MemTotal: 1000 kB\nMemAvailable: 1001 kB\n",
		"MemTotal: bad kB\nMemAvailable: 600 kB\n",
	} {
		if got := parseMemoryPercent([]byte(malformed)); got != nil {
			t.Fatalf("parseMemoryPercent(%q) = %d, want unavailable", malformed, *got)
		}
	}
}

func TestSamplerFirstAndSecondCPURefresh(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statPath := filepath.Join(dir, "stat")
	memPath := filepath.Join(dir, "meminfo")
	cachePath := filepath.Join(dir, "state", "sample.json")
	if err := os.WriteFile(statPath, []byte("cpu 100 0 0 100 0 0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte("MemTotal: 1000 kB\nMemAvailable: 600 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sampler := Sampler{StatPath: statPath, MemInfoPath: memPath, CachePath: cachePath}
	first := sampler.sampleProc()
	if first.CPUPercent != nil || intValue(first.MemoryPercent) != 40 {
		t.Fatalf("first Sample() = %#v, want CPU unavailable and MEM 40", first)
	}
	if err := os.WriteFile(statPath, []byte("cpu 150 0 0 150 0 0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := sampler.sampleProc()
	if intValue(second.CPUPercent) != 50 || intValue(second.MemoryPercent) != 40 {
		t.Fatalf("second Sample() = %#v, want CPU 50 and MEM 40", second)
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("sample cache mode = %o, want 600", got)
	}
}

func TestSamplerRejectsStaleCPUReference(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statPath := filepath.Join(dir, "stat")
	memPath := filepath.Join(dir, "meminfo")
	cachePath := filepath.Join(dir, "sample.json")
	if err := os.WriteFile(statPath, []byte("cpu 150 0 0 150 0 0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte("MemTotal: 1000 kB\nMemAvailable: 600 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveCPUSample(cachePath, cpuSample{Total: 200, Idle: 100, At: time.Unix(100, 0).UnixNano()}); err != nil {
		t.Fatal(err)
	}
	metrics := (Sampler{
		StatPath:    statPath,
		MemInfoPath: memPath,
		CachePath:   cachePath,
		Now:         func() time.Time { return time.Unix(200, 0) },
	}).sampleProc()
	if metrics.CPUPercent != nil || intValue(metrics.MemoryPercent) != 40 {
		t.Fatalf("stale Sample() = %#v, want CPU unavailable and MEM 40", metrics)
	}
}

func TestConcurrentSampleWritesRemainReadable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "sample.json")
	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(value uint64) {
			defer wg.Done()
			_ = saveCPUSample(path, cpuSample{Total: value + 100, Idle: value, At: int64(value)})
		}(uint64(i))
	}
	wg.Wait()
	if sample, ok := loadCPUSample(path); !ok || sample.Total < sample.Idle {
		t.Fatalf("loadCPUSample() = %#v, %v after concurrent writes", sample, ok)
	}
}
