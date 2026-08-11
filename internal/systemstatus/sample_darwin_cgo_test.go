//go:build darwin && cgo

package systemstatus

import "testing"
import "time"

func intValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func TestDarwinMemoryPercent(t *testing.T) {
	t.Parallel()

	if got := intValue(darwinMemoryPercent(1000, 10, 20, 30)); got != 50 {
		t.Fatalf("darwinMemoryPercent() = %d, want 50", got)
	}
	for _, sample := range []struct {
		total, pageSize, free, inactive uint64
	}{
		{},
		{total: 1000},
		{total: 1000, pageSize: 10, free: 101},
		{total: 1000, pageSize: ^uint64(0), free: 2},
		{total: 1000, pageSize: 1, free: ^uint64(0), inactive: 1},
	} {
		if got := darwinMemoryPercent(sample.total, sample.pageSize, sample.free, sample.inactive); got != nil {
			t.Fatalf("darwinMemoryPercent(%#v) = %d, want unavailable", sample, *got)
		}
	}
}

func TestDarwinNativeSampleIsAvailable(t *testing.T) {
	t.Parallel()
	if !Supported() {
		t.Fatal("Supported() = false, want true for darwin+cgo")
	}

	cpu, ok := darwinCPUSample()
	if !ok || cpu.Total == 0 || cpu.Idle > cpu.Total {
		t.Fatalf("darwin CPU sample = %#v, %v, want valid aggregate ticks", cpu, ok)
	}
	memory := darwinMemorySample()
	if memory == nil || *memory < 0 || *memory > 100 {
		t.Fatalf("darwin memory sample = %#v, want 0..100", memory)
	}
}

func TestDarwinSamplerUsesSharedCPUDeltaCache(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	sampler := Sampler{
		CachePath: t.TempDir() + "/cpu-sample.json",
		Now:       func() time.Time { return now },
	}
	if got := sampler.sampleCPU(cpuSample{Total: 1000, Idle: 600}); got != nil {
		t.Fatalf("first CPU sample = %d, want unavailable", *got)
	}
	now = now.Add(time.Second)
	if got := intValue(sampler.sampleCPU(cpuSample{Total: 1100, Idle: 650})); got != 50 {
		t.Fatalf("second CPU sample = %d, want 50", got)
	}
}
