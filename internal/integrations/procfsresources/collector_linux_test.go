//go:build linux

package procfsresources

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/resources"
)

func TestCollectorSampleReadsOnePassFixture(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "basic")
	readDirCalls := 0
	collector := Collector{
		Root: root,
		ReadDir: func(path string) ([]os.DirEntry, error) {
			readDirCalls++
			return os.ReadDir(path)
		},
		PageSize: 4096,
	}
	got := collector.Sample(context.Background())
	if !got.Available || !got.Host.CPUAvailable || !got.Host.MemoryAvailable {
		t.Fatalf("Sample() availability = %#v", got)
	}
	if readDirCalls != 1 {
		t.Fatalf("ReadDir calls = %d, want one procfs pass", readDirCalls)
	}
	if got.Host.LogicalCPUs != 2 || got.Host.CPUTotalTicks != 1000 || got.Host.CPUIdleTicks != 850 || got.Host.MemoryTotalBytes != 1000*1024 || got.Host.MemoryAvailableBytes != 400*1024 {
		t.Fatalf("host sample = %#v", got.Host)
	}
	want := []resources.ProcessSample{
		{Identity: resources.ProcessIdentity{PID: 100, StartTimeTicks: 1000}, ParentPID: 1, SessionID: 100, CPUTimeTicks: 15, RSSBytes: 25 * 4096},
		{Identity: resources.ProcessIdentity{PID: 101, StartTimeTicks: 1001}, ParentPID: 100, SessionID: 100, CPUTimeTicks: 27, RSSBytes: 50 * 4096},
	}
	if !reflect.DeepEqual(got.Processes, want) {
		t.Fatalf("process samples = %#v, want %#v", got.Processes, want)
	}
	if got.Diagnostics.SampledProcesses != 2 || got.Diagnostics.SkippedProcesses != 0 || got.Diagnostics.Duration <= 0 {
		t.Fatalf("diagnostics = %#v", got.Diagnostics)
	}
}

func TestCollectorClassifiesFastExitPermissionAndMalformedStat(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, pid := range []string{"102", "103", "104"} {
		if err := os.Mkdir(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "stat"), []byte("malformed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 0 0 9\ncpu0 1 0 0 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte("MemTotal: 10 kB\nMemAvailable: 5 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collector := Collector{Root: root, ReadFile: func(path string) ([]byte, error) {
		switch filepath.Base(filepath.Dir(path)) {
		case "102":
			return nil, fs.ErrNotExist
		case "103":
			return nil, fs.ErrPermission
		default:
			return os.ReadFile(path)
		}
	}}
	got := collector.Sample(context.Background())
	if got.Diagnostics.SampledProcesses != 0 || got.Diagnostics.SkippedProcesses != 3 || got.Diagnostics.RaceCount != 1 || got.Diagnostics.PermissionCount != 1 {
		t.Fatalf("diagnostics = %#v", got.Diagnostics)
	}
	snapshot := resources.BuildSnapshot(nil, nil, got)
	if snapshot.Status != resources.StatusPartial {
		t.Fatalf("first partial scan status = %q, want partial with warming CPU retained as unknown", snapshot.Status)
	}
	second := resources.BuildSnapshot(nil, &got, got)
	if second.Status != resources.StatusPartial {
		t.Fatalf("second partial scan status = %q", second.Status)
	}
}

func TestCollectorUnavailableAndCanceledAreExplicit(t *testing.T) {
	t.Parallel()

	collector := Collector{Root: "/missing", ReadDir: func(string) ([]os.DirEntry, error) { return nil, fs.ErrPermission }}
	got := collector.Sample(context.Background())
	if got.Available || got.UnavailableReason == "" {
		t.Fatalf("unavailable sample = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collector = Collector{Root: filepath.Join("testdata", "basic")}
	got = collector.Sample(ctx)
	if got.Available || !strings.Contains(got.UnavailableReason, "canceled") {
		t.Fatalf("canceled sample = %#v", got)
	}
}

func TestParseProcessStatRejectsPIDReuseShapedMismatchAndOverflow(t *testing.T) {
	t.Parallel()

	valid, err := os.ReadFile(filepath.Join("testdata", "basic", "100", "stat"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseProcessStat(999, valid, 4096); ok {
		t.Fatal("directory PID/stat PID mismatch accepted")
	}
	negativeRSS := strings.TrimSuffix(string(valid), "25\n") + "-1\n"
	if got, ok := parseProcessStat(100, []byte(negativeRSS), 4096); !ok || got.RSSBytes != 0 {
		t.Fatalf("negative RSS parse = %#v, %v, want valid zero RSS", got, ok)
	}
	for _, input := range []string{"", "100 no-parens", "100 (x) S 1", "100 (x) S 1 1 1 1 1 0 0 0 0 0 bad 0 0 0 20 0 1 0 10 10 1"} {
		if got, ok := parseProcessStat(100, []byte(input), 4096); ok {
			t.Fatalf("parseProcessStat(%q) = %#v, want rejected", input, got)
		}
	}
}

func TestParseHostFilesRejectInvalidCapacity(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "cpu0 1 2 3 4", "cpu 1 2 bad 4\ncpu0 1 2 3 4", "cpu 18446744073709551615 1 0 0\ncpu0 1 0 0 0"} {
		if got, ok := parseHostCPU([]byte(raw)); ok {
			t.Fatalf("parseHostCPU(%q) = %#v", raw, got)
		}
	}
	for _, raw := range []string{"", "MemTotal: 10 kB", "MemTotal: 10 kB\nMemAvailable: 11 kB", "MemTotal: bad kB\nMemAvailable: 1 kB"} {
		if total, available, ok := parseHostMemory([]byte(raw)); ok {
			t.Fatalf("parseHostMemory(%q) = %d %d", raw, total, available)
		}
	}
}

func BenchmarkCollectorScan(b *testing.B) {
	for _, processCount := range []int{50, 200, 1000} {
		b.Run("processes="+strconv.Itoa(processCount), func(b *testing.B) {
			root := b.TempDir()
			if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 100 0 50 850 0 0 0 0\ncpu0 50 0 25 425\ncpu1 50 0 25 425\n"), 0o644); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte("MemTotal: 1000 kB\nMemAvailable: 400 kB\n"), 0o644); err != nil {
				b.Fatal(err)
			}
			for i := 1; i <= processCount; i++ {
				dir := filepath.Join(root, strconv.Itoa(i))
				if err := os.Mkdir(dir, 0o755); err != nil {
					b.Fatal(err)
				}
				stat := fmt.Sprintf("%d (fixture) S 0 %d %d 0 0 0 0 0 0 0 10 5 0 0 20 0 1 0 %d 1000 10\n", i, i, i, i+100)
				if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			collector := Collector{Root: root, PageSize: 4096}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got := collector.Sample(context.Background())
				if got.Diagnostics.SampledProcesses != processCount {
					b.Fatalf("sampled %d processes", got.Diagnostics.SampledProcesses)
				}
			}
		})
	}
}

func TestCollectorDoesNotExposeSensitiveProcessFields(t *testing.T) {
	t.Parallel()

	typeOf := reflect.TypeFor[resources.ProcessSample]()
	for _, forbidden := range []string{"Command", "CommandLine", "Prompt", "Content", "Transcript"} {
		if _, ok := typeOf.FieldByName(forbidden); ok {
			t.Fatalf("ProcessSample unexpectedly exposes %s", forbidden)
		}
	}
}

// TestPSSReadCostMeasurement is measurement-only. It deliberately stays out
// of Collector so continuous sampling cannot start reading smaps_rollup.
func TestPSSReadCostMeasurement(t *testing.T) {
	if os.Getenv("PROJMUX_RESOURCE_PSS_MEASURE") != "1" {
		t.Skip("set PROJMUX_RESOURCE_PSS_MEASURE=1 for count-only live PSS measurement")
	}
	sample := (Collector{}).Sample(context.Background())
	if !sample.Available || len(sample.Processes) == 0 {
		t.Fatal("procfs process sample unavailable")
	}
	limit := min(200, len(sample.Processes))
	started := time.Now()
	read := 0
	skipped := 0
	for _, process := range sample.Processes[:limit] {
		_, err := os.ReadFile(filepath.Join(DefaultRoot, strconv.Itoa(process.Identity.PID), "smaps_rollup"))
		if err != nil {
			skipped++
			continue
		}
		read++
	}
	t.Logf("sanitized PSS measurement: attempted=%d read=%d skipped=%d duration=%s rss_stat_scan=%s", limit, read, skipped, time.Since(started), sample.Diagnostics.Duration)
}
